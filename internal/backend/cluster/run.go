package cluster

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
)

var (
	gvrPipelineRun = schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "pipelineruns"}
	gvrTaskRun     = schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "taskruns"}
)

func (b *Backend) RunPipeline(ctx context.Context, in backend.PipelineRunInvocation) (backend.PipelineRunResult, error) {
	if b.client.Dynamic == nil || b.client.Kube == nil {
		return backend.PipelineRunResult{}, fmt.Errorf("cluster backend not Prepared")
	}
	ns := "tkn-act-" + shortRunID(in.RunID)
	if err := b.ensureNamespace(ctx, ns); err != nil {
		return backend.PipelineRunResult{}, err
	}
	if err := b.applyVolumeSources(ctx, in, ns); err != nil {
		return backend.PipelineRunResult{}, err
	}
	pr := buildPipelineRun(in, ns)
	created, err := b.client.Dynamic.Resource(gvrPipelineRun).Namespace(ns).Create(ctx, pr, metav1.CreateOptions{})
	if err != nil {
		return backend.PipelineRunResult{}, fmt.Errorf("create PipelineRun: %w", err)
	}
	return b.watchPipelineRun(ctx, in, ns, created.GetName())
}

func (b *Backend) ensureNamespace(ctx context.Context, name string) error {
	_, err := b.client.Kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}, metav1.CreateOptions{})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("create namespace %q: %w", name, err)
	}
	return nil
}

// buildPipelineRun returns a fully populated unstructured PipelineRun with
// pipelineSpec inlined and workspaces backed by volumeClaimTemplate.
func buildPipelineRun(in backend.PipelineRunInvocation, namespace string) *unstructured.Unstructured {
	pipelineSpec := pipelineSpecToMap(in.Pipeline)
	// Tekton's v1 PipelineSpec does not carry a `timeouts` field —
	// timeouts live on PipelineRun.spec.timeouts. tkn-act's tektontypes
	// puts the field on PipelineSpec (so authors can write it once on
	// the Pipeline), but when serialized into pipelineSpec for the
	// cluster backend it would be rejected by Tekton's webhook
	// ("unknown field"). Drop it here; we re-attach it onto the
	// PipelineRun's spec below.
	delete(pipelineSpec, "timeouts")

	// Inline embedded Tasks under each PipelineTask.taskSpec.
	if tasks, ok := pipelineSpec["tasks"].([]any); ok {
		for i, t := range tasks {
			m, ok := t.(map[string]any)
			if !ok {
				continue
			}
			ref, hasRef := m["taskRef"].(map[string]any)
			if hasRef {
				name, _ := ref["name"].(string)
				if tk, ok := in.Tasks[name]; ok {
					m["taskSpec"] = taskSpecToMap(tk.Spec)
					delete(m, "taskRef")
				}
			}
			tasks[i] = m
		}
		pipelineSpec["tasks"] = tasks
	}
	if fin, ok := pipelineSpec["finally"].([]any); ok {
		for i, t := range fin {
			m, ok := t.(map[string]any)
			if !ok {
				continue
			}
			ref, hasRef := m["taskRef"].(map[string]any)
			if hasRef {
				name, _ := ref["name"].(string)
				if tk, ok := in.Tasks[name]; ok {
					m["taskSpec"] = taskSpecToMap(tk.Spec)
					delete(m, "taskRef")
				}
			}
			fin[i] = m
		}
		pipelineSpec["finally"] = fin
	}

	spec := map[string]any{
		"pipelineSpec": pipelineSpec,
	}
	if t := in.Pipeline.Spec.Timeouts; t != nil {
		out := map[string]any{}
		if t.Pipeline != "" {
			out["pipeline"] = t.Pipeline
		}
		if t.Tasks != "" {
			out["tasks"] = t.Tasks
		}
		if t.Finally != "" {
			out["finally"] = t.Finally
		}
		if len(out) > 0 {
			spec["timeouts"] = out
		}
	}
	if len(in.Params) > 0 {
		var ps []any
		for _, p := range in.Params {
			ps = append(ps, paramToMap(p))
		}
		spec["params"] = ps
	}
	// Workspaces: volumeClaimTemplate per declared pipeline workspace.
	if pwss, ok := pipelineSpec["workspaces"].([]any); ok && len(pwss) > 0 {
		var wsBindings []any
		for _, w := range pwss {
			m, ok := w.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["name"].(string)
			wsBindings = append(wsBindings, map[string]any{
				"name": name,
				"volumeClaimTemplate": map[string]any{
					"spec": map[string]any{
						"accessModes": []any{"ReadWriteOnce"},
						"resources": map[string]any{
							"requests": map[string]any{"storage": "1Gi"},
						},
					},
				},
			})
		}
		spec["workspaces"] = wsBindings
	}

	pr := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "tekton.dev/v1",
			"kind":       "PipelineRun",
			"metadata": map[string]any{
				"name":      in.PipelineRunName,
				"namespace": namespace,
			},
			"spec": spec,
		},
	}
	return pr
}

func pipelineSpecToMap(pl tektontypes.Pipeline) map[string]any {
	b, _ := json.Marshal(pl.Spec)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func taskSpecToMap(ts tektontypes.TaskSpec) map[string]any {
	b, _ := json.Marshal(ts)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func paramToMap(p tektontypes.Param) map[string]any {
	m := map[string]any{"name": p.Name}
	switch p.Value.Type {
	case tektontypes.ParamTypeArray:
		m["value"] = strSliceToAny(p.Value.ArrayVal)
	case tektontypes.ParamTypeObject:
		obj := map[string]any{}
		for k, v := range p.Value.ObjectVal {
			obj[k] = v
		}
		m["value"] = obj
	default:
		m["value"] = p.Value.StringVal
	}
	return m
}

func strSliceToAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func shortRunID(rid string) string {
	if len(rid) >= 8 {
		return rid[:8]
	}
	return rid
}

// watchPipelineRun watches the PipelineRun to terminal status, streaming
// TaskRun pod logs as they appear.
func (b *Backend) watchPipelineRun(ctx context.Context, in backend.PipelineRunInvocation, ns, name string) (backend.PipelineRunResult, error) {
	res := backend.PipelineRunResult{Started: time.Now(), Tasks: map[string]backend.TaskOutcomeOnCluster{}}

	w, err := b.client.Dynamic.Resource(gvrPipelineRun).Namespace(ns).Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + name,
	})
	if err != nil {
		return res, fmt.Errorf("watch PipelineRun: %w", err)
	}
	defer w.Stop()

	// Stream logs for each TaskRun pod as it appears.
	streamed := map[string]bool{}
	go b.streamAllTaskRunLogs(ctx, in, ns, streamed)

	for ev := range w.ResultChan() {
		if ev.Type == watch.Deleted || ev.Object == nil {
			continue
		}
		un, ok := ev.Object.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		conds, _, _ := unstructured.NestedSlice(un.Object, "status", "conditions")
		for _, c := range conds {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if cm["type"] != "Succeeded" {
				continue
			}
			status, _ := cm["status"].(string)
			if status != "True" && status != "False" {
				continue
			}
			reason, _ := cm["reason"].(string)
			res.Status = mapPipelineRunStatus(status, reason)
			res.Ended = time.Now()
			res.Tasks = b.collectTaskOutcomes(ctx, in, ns)
			// `spec.timeouts.{tasks,finally}` exhaustion does NOT set the
			// PipelineRun condition reason to a timeout — Tekton only
			// cancels the affected TaskRuns with the
			// `TaskRunCancelledByPipelineTimeoutMsg` spec.statusMessage,
			// then the PipelineRun ends Failed because a TaskRun failed.
			// Re-classify the run-level status to `timeout` when any
			// per-task outcome already mapped to `timeout` (taskRunToOutcome
			// reads spec.statusMessage to surface that).
			if res.Status == "failed" {
				for _, oc := range res.Tasks {
					if oc.Status == "timeout" {
						res.Status = "timeout"
						break
					}
				}
			}
			return res, nil
		}
	}
	return res, fmt.Errorf("PipelineRun watch closed before terminal status")
}

// collectTaskOutcomes walks every TaskRun owned by this PipelineRun and
// produces a per-pipeline-task summary the engine turns into task-end /
// task-retry events. Best-effort: a list error returns an empty map (the
// PipelineRun status alone still drives the run-level outcome).
func (b *Backend) collectTaskOutcomes(ctx context.Context, in backend.PipelineRunInvocation, ns string) map[string]backend.TaskOutcomeOnCluster {
	out := map[string]backend.TaskOutcomeOnCluster{}
	list, err := b.client.Dynamic.Resource(gvrTaskRun).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "tekton.dev/pipelineRun=" + in.PipelineRunName,
	})
	if err != nil {
		return out
	}
	for i := range list.Items {
		tr := &list.Items[i]
		ptName, _, _ := unstructured.NestedString(tr.Object, "metadata", "labels", "tekton.dev/pipelineTask")
		if ptName == "" {
			continue
		}
		out[ptName] = taskRunToOutcome(tr)
	}
	return out
}

// taskRunCancelledByPipelineTimeoutMsg matches Tekton's
// v1.TaskRunCancelledByPipelineTimeoutMsg constant. Tekton cancels
// TaskRuns affected by `spec.timeouts.{tasks,finally}` exhaustion by
// patching their `spec.statusMessage` to this exact string; the
// resulting condition reason is just `TaskRunCancelled`, so we have
// to read the message to disambiguate cancel-vs-timeout.
const taskRunCancelledByPipelineTimeoutMsg = "TaskRun cancelled as the PipelineRun it belongs to has timed out."

// taskRunToOutcome reads the parts of a Tekton TaskRun status the engine
// needs to reconstruct task-end / task-retry events: terminal status,
// retriesStatus list, and (when present) per-result values.
func taskRunToOutcome(tr *unstructured.Unstructured) backend.TaskOutcomeOnCluster {
	oc := backend.TaskOutcomeOnCluster{Attempts: 1}
	conds, _, _ := unstructured.NestedSlice(tr.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok || cm["type"] != "Succeeded" {
			continue
		}
		st, _ := cm["status"].(string)
		reason, _ := cm["reason"].(string)
		oc.Status = mapTaskRunStatus(st, reason)
		oc.Message, _ = cm["message"].(string)
		break
	}
	// Detect cancel-by-pipeline-timeout: spec.statusMessage carries the
	// signal even when the condition reason is just TaskRunCancelled.
	if msg, _, _ := unstructured.NestedString(tr.Object, "spec", "statusMessage"); msg == taskRunCancelledByPipelineTimeoutMsg {
		oc.Status = "timeout"
		if oc.Message == "" {
			oc.Message = msg
		}
	}
	if oc.Status == "" {
		oc.Status = "infrafailed"
	}
	// retriesStatus: one entry per *failed* attempt that preceded the
	// terminal one. Total attempts = len(retriesStatus) + 1.
	retries, _, _ := unstructured.NestedSlice(tr.Object, "status", "retriesStatus")
	if n := len(retries); n > 0 {
		oc.Attempts = n + 1
		oc.RetryAttempts = make([]backend.RetryAttempt, 0, n)
		for i, r := range retries {
			rm, _ := r.(map[string]any)
			cs, _, _ := unstructured.NestedSlice(rm, "conditions")
			ra := backend.RetryAttempt{Attempt: i + 1, Status: "failed"}
			for _, c := range cs {
				cmap, ok := c.(map[string]any)
				if !ok || cmap["type"] != "Succeeded" {
					continue
				}
				st, _ := cmap["status"].(string)
				reason, _ := cmap["reason"].(string)
				ra.Status = mapTaskRunStatus(st, reason)
				ra.Message, _ = cmap["message"].(string)
				break
			}
			oc.RetryAttempts = append(oc.RetryAttempts, ra)
		}
	}
	// Surface task results (best-effort).
	if results, found, _ := unstructured.NestedSlice(tr.Object, "status", "results"); found {
		oc.Results = map[string]string{}
		for _, r := range results {
			rm, _ := r.(map[string]any)
			name, _ := rm["name"].(string)
			value, _ := rm["value"].(string)
			if name != "" {
				oc.Results[name] = value
			}
		}
	}
	return oc
}

// mapTaskRunStatus is the per-TaskRun analogue of mapPipelineRunStatus.
func mapTaskRunStatus(condStatus, reason string) string {
	if condStatus == "True" {
		return "succeeded"
	}
	switch reason {
	case "TaskRunTimeout", "PipelineRunTimeout":
		return "timeout"
	}
	return "failed"
}

// mapPipelineRunStatus translates the (Succeeded condition status, reason)
// pair on a Tekton PipelineRun into one of our user-visible statuses. Today
// only timeout needs disambiguation; everything else collapses to
// succeeded/failed. Keep the table here next to the cluster watch so docker
// engine.RunResult.Status and cluster engine.RunResult.Status emit the
// same value for the same outcome.
func mapPipelineRunStatus(condStatus, reason string) string {
	if condStatus == "True" {
		return "succeeded"
	}
	switch reason {
	case "PipelineRunTimeout", "TaskRunTimeout":
		return "timeout"
	}
	return "failed"
}

func (b *Backend) streamAllTaskRunLogs(ctx context.Context, in backend.PipelineRunInvocation, ns string, streamed map[string]bool) {
	w, err := b.client.Dynamic.Resource(gvrTaskRun).Namespace(ns).Watch(ctx, metav1.ListOptions{
		LabelSelector: "tekton.dev/pipelineRun=" + in.PipelineRunName,
	})
	if err != nil {
		return
	}
	defer w.Stop()
	for ev := range w.ResultChan() {
		un, ok := ev.Object.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		podName, found, _ := unstructured.NestedString(un.Object, "status", "podName")
		if !found || streamed[podName] {
			continue
		}
		streamed[podName] = true
		taskName, _, _ := unstructured.NestedString(un.Object, "metadata", "labels", "tekton.dev/pipelineTask")
		go b.streamPodLogs(ctx, in, ns, podName, taskName)
	}
}

func (b *Backend) streamPodLogs(ctx context.Context, in backend.PipelineRunInvocation, ns, pod, taskName string) {
	if in.LogSink == nil {
		return
	}
	// Wait briefly for the pod to have containers.
	time.Sleep(500 * time.Millisecond)
	p, err := b.client.Kube.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return
	}
	for _, c := range p.Spec.Containers {
		if !strings.HasPrefix(c.Name, "step-") {
			continue
		}
		stepName := strings.TrimPrefix(c.Name, "step-")
		req := b.client.Kube.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{Container: c.Name, Follow: true})
		rc, err := req.Stream(ctx)
		if err != nil {
			continue
		}
		go func(stepName string, rc io.ReadCloser) {
			defer rc.Close()
			s := bufio.NewScanner(rc)
			s.Buffer(make([]byte, 64*1024), 1024*1024)
			for s.Scan() {
				in.LogSink.StepLog(taskName, stepName, "stdout", s.Text())
			}
		}(stepName, rc)
	}
}
