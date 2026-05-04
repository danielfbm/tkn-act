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
			inlineTaskSpec(m, in.Tasks)
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
			inlineTaskSpec(m, in.Tasks)
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

// inlineTaskSpec resolves a PipelineTask map's taskRef into an inlined
// taskSpec (Tekton's EmbeddedTask). It also moves any per-task
// `timeout` from TaskSpec onto PipelineTask.timeout, because Tekton's
// EmbeddedTask schema has no `timeout` field — leaving it on taskSpec
// gets the PipelineRun rejected by the admission webhook with
// `unknown field "timeout"`. PipelineTask.timeout is the supported
// place for per-task wall clocks under inlined taskSpec.
func inlineTaskSpec(pt map[string]any, tasks map[string]tektontypes.Task) {
	ref, hasRef := pt["taskRef"].(map[string]any)
	if !hasRef {
		return
	}
	name, _ := ref["name"].(string)
	tk, ok := tasks[name]
	if !ok {
		return
	}
	tsm := taskSpecToMap(tk.Spec)
	// Hoist taskSpec.timeout → pipelineSpec.tasks[].timeout. The
	// PipelineTask.Timeout field is what Tekton's reconciler uses to
	// drive per-TaskRun wall clocks; the inlined taskSpec must not
	// carry a timeout. Only hoist when PipelineTask doesn't already
	// have its own (the on-PipelineTask one wins).
	if to, present := tsm["timeout"]; present {
		if _, already := pt["timeout"]; !already {
			pt["timeout"] = to
		}
		delete(tsm, "timeout")
	}
	pt["taskSpec"] = tsm
	delete(pt, "taskRef")
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
			message, _ := cm["message"].(string)
			res.Status = mapPipelineRunStatus(status, reason)
			res.Reason = reason
			res.Message = message
			res.Ended = time.Now()
			res.Tasks = b.collectTaskOutcomes(ctx, in, ns)
			res.Results = extractPipelineResults(un)
			// `spec.timeouts.{pipeline,tasks,finally}` exhaustion does
			// NOT always set the PipelineRun condition reason to a
			// timeout — depending on which budget fired and how Tekton
			// observed it, the PR can end with reason `Failed` while
			// the underlying cause was a timeout. Two distinct fallback
			// signals exist on the PR object:
			//
			//   1. TaskRuns that were running when the budget fired are
			//      cancelled with `TaskRunCancelledByPipelineTimeoutMsg`
			//      on `spec.statusMessage`. taskRunToOutcome surfaces
			//      that as per-task `timeout`.
			//   2. Tasks that hadn't launched yet are recorded in
			//      `status.skippedTasks` with a SkippingReason of
			//      `PipelineRun timeout has been reached` /
			//      `PipelineRun Tasks timeout has been reached` /
			//      `PipelineRun Finally timeout has been reached`
			//      (no TaskRun is ever created for them, so #1 misses).
			//
			// Re-classify to `timeout` if either signal fires. Without
			// #2, the cluster-CI `pipeline-timeout` fixture intermittently
			// reports `failed` in well under the budget when the
			// reconciler skips the only task before launching it.
			if res.Status == "failed" {
				if anyTaskTimedOut(res.Tasks) || anySkippedDueToTimeout(un) {
					res.Status = "timeout"
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

// pipelineRunSkippedDueToTimeoutReasons mirrors Tekton's
// v1.PipelineTimedOutSkip / v1.TasksTimedOutSkip / v1.FinallyTimedOutSkip
// constants. A skipped task with any of these reasons is the unmistakable
// signal that the PipelineRun ran out of budget — even when the run-
// level condition reason ends up as the generic `Failed`. (Tekton's
// `getPipelineTasksCount` increments SkippedDueToTimeout for these,
// and the condition reason then collapses to `Failed` because at
// least one task "failed or was skipped due to timeout".)
var pipelineRunSkippedDueToTimeoutReasons = map[string]struct{}{
	"PipelineRun timeout has been reached":         {},
	"PipelineRun Tasks timeout has been reached":   {},
	"PipelineRun Finally timeout has been reached": {},
}

// anyTaskTimedOut returns true if any per-task outcome is already
// classified as `timeout` (via the per-TaskRun `spec.statusMessage`
// check in taskRunToOutcome).
func anyTaskTimedOut(tasks map[string]backend.TaskOutcomeOnCluster) bool {
	for _, oc := range tasks {
		if oc.Status == "timeout" {
			return true
		}
	}
	return false
}

// anySkippedDueToTimeout returns true if any entry in
// `status.skippedTasks` carries a timeout-related SkippingReason. This
// is the only signal we get for tasks that the budget killed before
// they could launch — those tasks have no TaskRun, so the per-TaskRun
// statusMessage check (anyTaskTimedOut) misses them.
func anySkippedDueToTimeout(pr *unstructured.Unstructured) bool {
	skipped, _, _ := unstructured.NestedSlice(pr.Object, "status", "skippedTasks")
	for _, s := range skipped {
		sm, ok := s.(map[string]any)
		if !ok {
			continue
		}
		reason, _ := sm["reason"].(string)
		if _, hit := pipelineRunSkippedDueToTimeoutReasons[reason]; hit {
			return true
		}
	}
	return false
}

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

// extractPipelineResults reads `pr.status.results` (Tekton v1) into a
// generic map. Each entry has shape {name, value}; value may be a
// string, a []any (array of strings), or a map[string]any (object).
// We preserve the JSON-decoded shape: ParamTypeString → string,
// ParamTypeArray → []string, ParamTypeObject → map[string]string,
// matching how the docker-path resolvePipelineResults populates
// RunResult.Results.
//
// Tekton v1 is the only schema the cluster integration installs, so
// we read v1's `status.results` only. Earlier Tekton releases used
// `status.pipelineResults`; tkn-act doesn't support those.
func extractPipelineResults(pr *unstructured.Unstructured) map[string]any {
	results, found, _ := unstructured.NestedSlice(pr.Object, "status", "results")
	if !found || len(results) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, r := range results {
		rm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name, _ := rm["name"].(string)
		if name == "" {
			continue
		}
		switch v := rm["value"].(type) {
		case string:
			out[name] = v
		case []any:
			arr := make([]string, 0, len(v))
			for _, item := range v {
				if s, ok := item.(string); ok {
					arr = append(arr, s)
				}
			}
			out[name] = arr
		case map[string]any:
			obj := make(map[string]string, len(v))
			for k, item := range v {
				if s, ok := item.(string); ok {
					obj[k] = s
				}
			}
			out[name] = obj
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
