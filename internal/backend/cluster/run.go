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
			if cm["type"] == "Succeeded" {
				switch cm["status"] {
				case "True":
					res.Status = "succeeded"
					res.Ended = time.Now()
					return res, nil
				case "False":
					res.Status = "failed"
					res.Ended = time.Now()
					return res, nil
				}
			}
		}
	}
	return res, fmt.Errorf("PipelineRun watch closed before terminal status")
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
