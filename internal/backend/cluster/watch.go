package cluster

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/debug"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
)

// Tunables for the watch / log plumbing. Variables (not constants) so
// unit tests can shrink them.
var (
	// logJoinTimeout bounds how long RunPipeline waits for in-flight
	// pod log streams to drain after the PipelineRun is terminal, so
	// the last lines are reported before run-end without risking a
	// hang on a stream the API server never closes.
	logJoinTimeout = 5 * time.Second
	// logRetryMax caps the backoff between attempts to attach to a
	// container's log stream while the container is still starting.
	logRetryMax = 2 * time.Second
	// watchRetryDelay is the pause before re-establishing a watch the
	// API server closed.
	watchRetryDelay = 200 * time.Millisecond
	// cancelPatchTimeout bounds the best-effort PipelineRun cancel
	// patch issued after the run context is cancelled.
	cancelPatchTimeout = 10 * time.Second
)

// maxQuickWatchFailures is how many consecutive watch attempts may
// fail to open, or close within a second without delivering a single
// event, before watchLoop gives up.
const maxQuickWatchFailures = 10

// watchLoop drives handle over every object event of a watch opened by
// open, re-establishing the watch when the API server closes it. The
// API server ends every watch after --min-request-timeout (30-60m), so
// a long PipelineRun would otherwise fail with "watch closed before
// terminal status". Re-watches resume from the last seen
// resourceVersion; a 410 Gone (expired version) restarts from the
// current state, whose synthetic ADDED events re-deliver it.
//
// handle returns true to stop. watchLoop returns nil when handle
// stopped it, ctx.Err() when ctx ended, or the last error once the
// watch keeps failing.
func watchLoop(ctx context.Context, open func(ctx context.Context, rv string) (watch.Interface, error), handle func(watch.EventType, *unstructured.Unstructured) bool) error {
	rv := ""
	failures := 0
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		opened := time.Now()
		w, err := open(ctx, rv)
		gotEvent := false
		stopped := false
		if err != nil {
			lastErr = err
		} else {
			stopped, gotEvent = consumeWatch(ctx, w, &rv, handle)
			lastErr = fmt.Errorf("watch closed by the API server")
		}
		if stopped {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil || (!gotEvent && time.Since(opened) < time.Second) {
			failures++
		} else {
			failures = 0
		}
		if failures >= maxQuickWatchFailures {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(watchRetryDelay * time.Duration(failures+1)):
		}
	}
}

// consumeWatch reads one watch until it closes, ctx ends, or handle
// asks to stop. rv tracks the last seen resourceVersion.
func consumeWatch(ctx context.Context, w watch.Interface, rv *string, handle func(watch.EventType, *unstructured.Unstructured) bool) (stopped, gotEvent bool) {
	defer w.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, gotEvent
		case ev, ok := <-w.ResultChan():
			if !ok {
				return false, gotEvent
			}
			if ev.Type == watch.Error {
				// Typically 410 Gone: our resourceVersion is too old.
				// Restart from the current state.
				*rv = ""
				return false, gotEvent
			}
			un, ok := ev.Object.(*unstructured.Unstructured)
			if !ok || un == nil {
				continue
			}
			gotEvent = true
			if v := un.GetResourceVersion(); v != "" {
				*rv = v
			}
			if ev.Type == watch.Bookmark {
				continue
			}
			if handle(ev.Type, un) {
				return true, gotEvent
			}
		}
	}
}

// cancelPipelineRun best-effort patches spec.status=Cancelled on the
// PipelineRun so Tekton stops its pods after the user interrupts the
// run. It runs on a fresh context because the run context is already
// cancelled.
func (b *Backend) cancelPipelineRun(ns, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), cancelPatchTimeout)
	defer cancel()
	_, err := b.client.Dynamic.Resource(gvrPipelineRun).Namespace(ns).Patch(ctx, name, types.MergePatchType,
		[]byte(`{"spec":{"status":"Cancelled"}}`), metav1.PatchOptions{})
	b.dbg().Emit(debug.Backend, func() (string, map[string]any) {
		f := map[string]any{"name": name, "namespace": ns}
		if err != nil {
			f["error"] = err.Error()
		}
		return "pipelinerun cancel requested", f
	})
}

// taskRunLogTracker attaches log streams to TaskRun pods as they
// appear and emits sidecar lifecycle transitions. observe is called
// from one goroutine at a time (the TaskRun watch, then the final
// sweep), so its maps need no lock; the WaitGroup tracks the
// per-container log goroutines it spawns.
type taskRunLogTracker struct {
	b         *Backend
	in        backend.PipelineRunInvocation
	ns        string
	streamCtx context.Context
	streams   sync.WaitGroup

	streamed     map[string]bool                        // pod name
	taskRunsSeen map[string]bool                        // TaskRun name
	sidecarSeen  map[string]map[string]sidecarSeenState // taskRun → sidecar → state
}

func newTaskRunLogTracker(b *Backend, in backend.PipelineRunInvocation, ns string, streamCtx context.Context) *taskRunLogTracker {
	return &taskRunLogTracker{
		b: b, in: in, ns: ns, streamCtx: streamCtx,
		streamed:     map[string]bool{},
		taskRunsSeen: map[string]bool{},
		sidecarSeen:  map[string]map[string]sidecarSeenState{},
	}
}

func (t *taskRunLogTracker) observe(un *unstructured.Unstructured) {
	b := t.b
	taskName, _, _ := unstructured.NestedString(un.Object, "metadata", "labels", "tekton.dev/pipelineTask")
	trName := un.GetName()
	if !t.taskRunsSeen[trName] {
		t.taskRunsSeen[trName] = true
		b.dbg().Emit(debug.Backend, func() (string, map[string]any) {
			return "taskrun seen", map[string]any{
				"name":          trName,
				"pipeline_task": taskName,
			}
		})
	}
	// Diff sidecar statuses and emit transition events.
	b.emitSidecarTransitions(t.in, taskName, trName, un, t.sidecarSeen)

	podName, found, _ := unstructured.NestedString(un.Object, "status", "podName")
	if !found || podName == "" || t.streamed[podName] {
		return
	}
	t.streamed[podName] = true
	b.dbg().Emit(debug.Backend, func() (string, map[string]any) {
		return "pod attached", map[string]any{
			"pod":           podName,
			"taskrun":       trName,
			"pipeline_task": taskName,
		}
	})
	t.streams.Add(1)
	go func() {
		defer t.streams.Done()
		b.streamPodLogs(t.streamCtx, &t.streams, t.in, t.ns, podName, taskName)
	}()
}

// watch follows TaskRuns of this PipelineRun until ctx ends.
func (t *taskRunLogTracker) watch(ctx context.Context) {
	open := func(ctx context.Context, rv string) (watch.Interface, error) {
		return t.b.client.Dynamic.Resource(gvrTaskRun).Namespace(t.ns).Watch(ctx, metav1.ListOptions{
			LabelSelector:   "tekton.dev/pipelineRun=" + t.in.PipelineRunName,
			ResourceVersion: rv,
		})
	}
	_ = watchLoop(ctx, open, func(_ watch.EventType, un *unstructured.Unstructured) bool {
		t.observe(un)
		return false
	})
}

// sweep lists every TaskRun once so pods (and sidecar transitions)
// the watch had not yet observed when the PipelineRun went terminal
// still get their logs / events before run-end.
func (t *taskRunLogTracker) sweep(ctx context.Context) {
	list, err := t.b.client.Dynamic.Resource(gvrTaskRun).Namespace(t.ns).List(ctx, metav1.ListOptions{
		LabelSelector: "tekton.dev/pipelineRun=" + t.in.PipelineRunName,
	})
	if err != nil {
		return
	}
	for i := range list.Items {
		t.observe(&list.Items[i])
	}
}

// join waits (bounded by logJoinTimeout) for every attached log stream
// to finish.
func (t *taskRunLogTracker) join() {
	done := make(chan struct{})
	go func() {
		t.streams.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(logJoinTimeout):
	}
}

// streamPodLogs starts one goroutine per step / sidecar container of
// pod that follows its logs into the LogSink.
func (b *Backend) streamPodLogs(ctx context.Context, wg *sync.WaitGroup, in backend.PipelineRunInvocation, ns, pod, taskName string) {
	if in.LogSink == nil {
		return
	}
	// Build a step-name -> displayName lookup from the input bundle so
	// log events can carry the same display_name docker mode does.
	// Source-of-truth is the input YAML, NOT the controller verdict.
	stepDisplayByName := stepDisplayNameLookup(in, taskName)
	p, err := b.waitPodContainers(ctx, ns, pod)
	if err != nil {
		return
	}
	for _, c := range p.Spec.Containers {
		var emit func(string)
		switch {
		case strings.HasPrefix(c.Name, "step-"):
			stepName := strings.TrimPrefix(c.Name, "step-")
			display := stepDisplayByName[stepName]
			emit = func(l string) { in.LogSink.StepLog(taskName, stepName, display, "stdout", l) }
		case strings.HasPrefix(c.Name, "sidecar-"):
			sidecarName := strings.TrimPrefix(c.Name, "sidecar-")
			emit = func(l string) { in.LogSink.SidecarLog(taskName, sidecarName, "sidecar-stdout", l) }
		default:
			continue
		}
		wg.Add(1)
		go func(container string) {
			defer wg.Done()
			b.followContainerLogs(ctx, ns, pod, container, emit)
		}(c.Name)
	}
}

// waitPodContainers fetches the pod, retrying while it is not yet
// visible (the TaskRun can report podName a moment before the pod is
// readable).
func (b *Backend) waitPodContainers(ctx context.Context, ns, pod string) (*corev1.Pod, error) {
	backoff := 100 * time.Millisecond
	for {
		p, err := b.client.Kube.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
		if err == nil && len(p.Spec.Containers) > 0 {
			return p, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, logRetryMax)
	}
}

// followContainerLogs waits until container has started (running or
// terminated) and then follows its log stream to EOF. Opening the
// stream earlier fails with 400 "waiting to start: PodInitializing /
// ContainerCreating"; the previous single attempt dropped every log
// line of a slow-to-start pod. Gives up when the pod is gone, when the
// pod finished without the container ever starting, or when ctx ends.
func (b *Backend) followContainerLogs(ctx context.Context, ns, pod, container string, emit func(string)) {
	backoff := 100 * time.Millisecond
	for {
		ready, gone := b.containerLogReady(ctx, ns, pod, container)
		if gone {
			return
		}
		if ready {
			rc, err := b.client.Kube.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{Container: container, Follow: true}).Stream(ctx)
			if err == nil {
				backend.ScanLogLines(rc, emit)
				_ = rc.Close()
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, logRetryMax)
	}
}

// containerLogReady reports whether container has a running or
// terminated state (logs can be streamed), and whether waiting is
// pointless (pod deleted, or pod finished without the container ever
// starting).
func (b *Backend) containerLogReady(ctx context.Context, ns, pod, container string) (ready, gone bool) {
	p, err := b.client.Kube.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return false, apierrors.IsNotFound(err)
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Name != container {
			continue
		}
		if cs.State.Running != nil || cs.State.Terminated != nil {
			return true, false
		}
	}
	if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
		return false, true
	}
	return false, false
}
