package cluster_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/backend/cluster"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/danielfbm/tkn-act/internal/volumes"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

// gvrFor* mirror the production constants in run.go but live in the
// _test package so the runpipeline tests can pre-populate / mutate
// objects on the fake dynamic client.
var (
	gvrPipelineRunTest = schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "pipelineruns"}
	gvrTaskRunTest     = schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "taskruns"}
)

// fakeBackend wires together the kube + dynamic fakes plus the volume
// stores in a way that exercises RunPipeline as a unit.
func fakeBackend(t *testing.T, prObjs ...runtime.Object) (*cluster.Backend, *dynamicfake.FakeDynamicClient, *kubefake.Clientset, *volumes.Store, *volumes.Store) {
	t.Helper()
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(gvrPipelineRunTest.GroupVersion().WithKind("PipelineRun"), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gvrPipelineRunTest.GroupVersion().WithKind("PipelineRunList"), &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(gvrTaskRunTest.GroupVersion().WithKind("TaskRun"), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gvrTaskRunTest.GroupVersion().WithKind("TaskRunList"), &unstructured.UnstructuredList{})

	gvrToList := map[schema.GroupVersionResource]string{
		gvrPipelineRunTest: "PipelineRunList",
		gvrTaskRunTest:     "TaskRunList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToList, prObjs...)
	kube := kubefake.NewSimpleClientset()
	cm := volumes.NewStore("")
	sec := volumes.NewStore("")
	be := cluster.NewWithClientsAndStores(cluster.ClientBundle{Dynamic: dyn, Kube: kube}, cm, sec)
	return be, dyn, kube, cm, sec
}

// TestRunPipelineSucceedsThroughWatch exercises RunPipeline end-to-end
// against fakes: namespace create, volume apply (no-op), PR submit, watch
// to terminal Succeeded=True.
//
// The fake dynamic client's watcher only sees Updates that happen after
// the watch is established, so we keep flipping the PR status in a loop
// until either the Update is observed or the test times out.
func TestRunPipelineSucceedsThroughWatch(t *testing.T) {
	be, dyn, kube, _, _ := fakeBackend(t)

	pl := tektontypes.Pipeline{
		Object: tektontypes.Object{APIVersion: "tekton.dev/v1", Kind: "Pipeline"},
		Spec: tektontypes.PipelineSpec{
			Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
		},
	}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		Steps: []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}},
	}}
	tk.Metadata.Name = "t"

	prName := "p-12345678"
	ns := "tkn-act-12345678"

	stopUpdater := flipStatusUntilStop(t, dyn, ns, prName, "True", "Succeeded")
	defer close(stopUpdater)

	res, err := be.RunPipeline(context.Background(), backend.PipelineRunInvocation{
		RunID: "12345678", PipelineRunName: prName,
		Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if res.Status != "succeeded" {
		t.Errorf("status = %q, want succeeded", res.Status)
	}
	if _, err := kube.CoreV1().Namespaces().Get(context.Background(), ns, metav1.GetOptions{}); err != nil {
		t.Errorf("namespace not created: %v", err)
	}
}

// TestRunPipelineMapsTimeoutReason proves the timeout-reason mapping
// reaches the engine: status=False with reason=PipelineRunTimeout must
// surface as RunResult.Status="timeout".
func TestRunPipelineMapsTimeoutReason(t *testing.T) {
	be, dyn, _, _, _ := fakeBackend(t)
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "sleep 30"}}}}
	tk.Metadata.Name = "t"

	prName := "p-87654321"
	ns := "tkn-act-87654321"

	stopUpdater := flipStatusUntilStop(t, dyn, ns, prName, "False", "PipelineRunTimeout")
	defer close(stopUpdater)

	res, err := be.RunPipeline(context.Background(), backend.PipelineRunInvocation{
		RunID: "87654321", PipelineRunName: prName,
		Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if res.Status != "timeout" {
		t.Errorf("status = %q, want timeout", res.Status)
	}
}

// flipStatusUntilStop keeps Get-and-Updating the PR with the given
// Succeeded condition every 20ms until the returned channel is closed
// (the test should defer close(...)). The fake dynamic watcher misses
// Updates that happen before Watch is established, so the loop ensures
// at least one Update fires after the watch is up.
func flipStatusUntilStop(t *testing.T, dyn *dynamicfake.FakeDynamicClient, ns, prName, status, reason string) chan struct{} {
	t.Helper()
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-stop:
				return
			case <-deadline.C:
				return
			case <-ticker.C:
				obj, err := dyn.Resource(gvrPipelineRunTest).Namespace(ns).Get(context.Background(), prName, metav1.GetOptions{})
				if err != nil {
					continue
				}
				_ = unstructured.SetNestedSlice(obj.Object, []any{
					map[string]any{"type": "Succeeded", "status": status, "reason": reason},
				}, "status", "conditions")
				_, _ = dyn.Resource(gvrPipelineRunTest).Namespace(ns).Update(context.Background(), obj, metav1.UpdateOptions{})
			}
		}
	}()
	return stop
}

// TestTaskRunToOutcomeReadsTimeoutCancelMessage: when Tekton's
// tasks/finally budget cancels a TaskRun, the condition reason is just
// `TaskRunCancelled`; the timeout signal lives in
// `spec.statusMessage`. taskRunToOutcome must read the message and
// surface the per-task status as `timeout` so the engine emits the
// correct task-end event.
func TestTaskRunToOutcomeReadsTimeoutCancelMessage(t *testing.T) {
	ns := "tkn-act-12345678"
	prName := "p-12345678"
	tr := taskRunObj("p-12345678-t-pod", ns, prName, "t", "False", "TaskRunCancelled", 0)
	_ = unstructured.SetNestedField(tr.Object, "TaskRun cancelled as the PipelineRun it belongs to has timed out.", "spec", "statusMessage")
	be, _, _, _, _ := fakeBackend(t, tr)

	got := be.CollectTaskOutcomesForTest(context.Background(), backend.PipelineRunInvocation{
		PipelineRunName: prName,
	}, ns)

	if got["t"].Status != "timeout" {
		t.Errorf("status = %q, want timeout", got["t"].Status)
	}
}

// TestRunPipelineMapsTasksTimeoutViaTaskRunMessage: when the
// PipelineRun condition is Failed (no `PipelineRunTimeout` reason)
// but at least one TaskRun was cancelled by the pipeline timeout,
// the run-level status must be re-classified to `timeout`.
func TestRunPipelineMapsTasksTimeoutViaTaskRunMessage(t *testing.T) {
	prName := "p-aabbccdd"
	ns := "tkn-act-aabbccdd"

	// Pre-seed a TaskRun cancelled by pipeline timeout. The watcher
	// will Get this object after the PR transitions to Failed.
	tr := taskRunObj(prName+"-t-pod", ns, prName, "t", "False", "TaskRunCancelled", 0)
	_ = unstructured.SetNestedField(tr.Object, "TaskRun cancelled as the PipelineRun it belongs to has timed out.", "spec", "statusMessage")

	be, dyn, _, _, _ := fakeBackend(t, tr)

	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Timeouts: &tektontypes.Timeouts{Tasks: "2s"},
		Tasks:    []tektontypes.PipelineTask{{Name: "t", TaskRef: &tektontypes.TaskRef{Name: "x"}}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "sleep 30"}}}}
	tk.Metadata.Name = "x"

	stopUpdater := flipStatusUntilStop(t, dyn, ns, prName, "False", "Failed")
	defer close(stopUpdater)

	res, err := be.RunPipeline(context.Background(), backend.PipelineRunInvocation{
		RunID: "aabbccdd", PipelineRunName: prName,
		Pipeline: pl, Tasks: map[string]tektontypes.Task{"x": tk},
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if res.Status != "timeout" {
		t.Errorf("status = %q, want timeout (cancelled-by-pipeline-timeout TaskRun must be surfaced)", res.Status)
	}
}

// TestRunPipelineMapsTimeoutViaSkippedTasks reproduces the cluster-CI flake
// where pipeline-timeout returns status=failed within 0.11s. When Tekton's
// reconciler observes that a task was skipped because the budget elapsed
// before it could launch, it records the skip in `status.skippedTasks` with
// SkippingReason `PipelineRun timeout has been reached` (or the tasks/
// finally analogues). Because at least one task was "skipped due to
// timeout", `getPipelineTasksCount` increments `SkippedDueToTimeout`,
// which makes `GetPipelineConditionStatus` return reason `Failed` (not
// `PipelineRunTimeout`) — and no TaskRun exists for the skipped task to
// fall back on via the per-TaskRun statusMessage check.
//
// The fix: when the PR ends `Failed` and `status.skippedTasks` contains
// any task with a timeout-related SkippingReason, classify the run as
// `timeout`.
func TestRunPipelineMapsTimeoutViaSkippedTasks(t *testing.T) {
	be, dyn, _, _, _ := fakeBackend(t)

	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Timeouts: &tektontypes.Timeouts{Pipeline: "2s"},
		Tasks:    []tektontypes.PipelineTask{{Name: "t", TaskRef: &tektontypes.TaskRef{Name: "x"}}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "sleep 30"}}}}
	tk.Metadata.Name = "x"

	prName := "p-deadbe01"
	ns := "tkn-act-deadbe01"

	// Drive the PR to status=False/Failed *with* a skippedTasks entry whose
	// reason indicates the PipelineRun timeout. No TaskRun exists for the
	// skipped task — that's the whole point: tasks skipped due to budget
	// exhaustion never get a TaskRun.
	stopUpdater := flipStatusWithSkippedTasksUntilStop(t, dyn, ns, prName, "False", "Failed",
		[]any{
			map[string]any{
				"name":   "t",
				"reason": "PipelineRun timeout has been reached",
			},
		})
	defer close(stopUpdater)

	res, err := be.RunPipeline(context.Background(), backend.PipelineRunInvocation{
		RunID: "deadbe01", PipelineRunName: prName,
		Pipeline: pl, Tasks: map[string]tektontypes.Task{"x": tk},
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if res.Status != "timeout" {
		t.Errorf("status = %q, want timeout (skippedTasks with PipelineTimedOutSkip must be surfaced)", res.Status)
	}
}

// TestRunPipelineMapsTasksAndFinallyTimeoutViaSkippedTasks: same as above
// but for `tasks` and `finally` budget skip reasons. Both should map to
// run-level status `timeout`.
func TestRunPipelineMapsTasksAndFinallyTimeoutViaSkippedTasks(t *testing.T) {
	cases := []struct {
		name          string
		runID         string
		skippedReason string
	}{
		{"tasks-budget", "deadbe02", "PipelineRun Tasks timeout has been reached"},
		{"finally-budget", "deadbe03", "PipelineRun Finally timeout has been reached"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			be, dyn, _, _, _ := fakeBackend(t)
			pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
				Timeouts: &tektontypes.Timeouts{Tasks: "2s"},
				Tasks:    []tektontypes.PipelineTask{{Name: "t", TaskRef: &tektontypes.TaskRef{Name: "x"}}},
			}}
			pl.Metadata.Name = "p"
			tk := tektontypes.Task{Spec: tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "sleep 30"}}}}
			tk.Metadata.Name = "x"

			prName := "p-" + c.runID
			ns := "tkn-act-" + c.runID

			stopUpdater := flipStatusWithSkippedTasksUntilStop(t, dyn, ns, prName, "False", "Failed",
				[]any{
					map[string]any{
						"name":   "t",
						"reason": c.skippedReason,
					},
				})
			defer close(stopUpdater)

			res, err := be.RunPipeline(context.Background(), backend.PipelineRunInvocation{
				RunID: c.runID, PipelineRunName: prName,
				Pipeline: pl, Tasks: map[string]tektontypes.Task{"x": tk},
			})
			if err != nil {
				t.Fatalf("RunPipeline: %v", err)
			}
			if res.Status != "timeout" {
				t.Errorf("status = %q, want timeout (skippedTasks reason %q must be surfaced)", res.Status, c.skippedReason)
			}
		})
	}
}

// TestRunPipelineFailedWithNonTimeoutSkippedTasksStaysFailed: a PR that
// ends Failed where the only skipped tasks were skipped for non-timeout
// reasons (When expressions, parent skip, …) must stay `failed`, not get
// re-classified to `timeout`. Guards against over-eager fallback.
func TestRunPipelineFailedWithNonTimeoutSkippedTasksStaysFailed(t *testing.T) {
	be, dyn, _, _, _ := fakeBackend(t)
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "t", TaskRef: &tektontypes.TaskRef{Name: "x"}}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "false"}}}}
	tk.Metadata.Name = "x"

	prName := "p-deadbe04"
	ns := "tkn-act-deadbe04"

	stopUpdater := flipStatusWithSkippedTasksUntilStop(t, dyn, ns, prName, "False", "Failed",
		[]any{
			map[string]any{
				"name":   "t",
				"reason": "When Expressions evaluated to false",
			},
		})
	defer close(stopUpdater)

	res, err := be.RunPipeline(context.Background(), backend.PipelineRunInvocation{
		RunID: "deadbe04", PipelineRunName: prName,
		Pipeline: pl, Tasks: map[string]tektontypes.Task{"x": tk},
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if res.Status != "failed" {
		t.Errorf("status = %q, want failed (non-timeout skipped reasons must NOT trigger timeout fallback)", res.Status)
	}
}

// TestRunPipelineSurfacesPRReasonAndMessage: when the PR ends terminal,
// the cluster backend must surface the Tekton condition `reason` and
// `message` on PipelineRunResult so the engine (and failing CI logs)
// can attribute the outcome to a specific Tekton path. Without this,
// every classification flake shows up as `status = failed, want X ()`
// with no diagnostic context.
func TestRunPipelineSurfacesPRReasonAndMessage(t *testing.T) {
	be, dyn, _, _, _ := fakeBackend(t)
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "t", TaskRef: &tektontypes.TaskRef{Name: "x"}}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}}}}
	tk.Metadata.Name = "x"

	prName := "p-deadbe05"
	ns := "tkn-act-deadbe05"

	stopUpdater := flipStatusWithMessageUntilStop(t, dyn, ns, prName, "False", "PipelineValidationFailed",
		"Pipeline p/p can't be Run; it has an invalid spec: bogus")
	defer close(stopUpdater)

	res, err := be.RunPipeline(context.Background(), backend.PipelineRunInvocation{
		RunID: "deadbe05", PipelineRunName: prName,
		Pipeline: pl, Tasks: map[string]tektontypes.Task{"x": tk},
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if res.Reason != "PipelineValidationFailed" {
		t.Errorf("reason = %q, want PipelineValidationFailed", res.Reason)
	}
	if res.Message == "" || !strings.Contains(res.Message, "invalid spec") {
		t.Errorf("message = %q, want substring 'invalid spec'", res.Message)
	}
}

// flipStatusWithSkippedTasksUntilStop is flipStatusUntilStop plus a
// `status.skippedTasks` slice. Used to drive the
// timeout-via-skipped-tasks fallback under test.
func flipStatusWithSkippedTasksUntilStop(t *testing.T, dyn *dynamicfake.FakeDynamicClient, ns, prName, status, reason string, skipped []any) chan struct{} {
	t.Helper()
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-stop:
				return
			case <-deadline.C:
				return
			case <-ticker.C:
				obj, err := dyn.Resource(gvrPipelineRunTest).Namespace(ns).Get(context.Background(), prName, metav1.GetOptions{})
				if err != nil {
					continue
				}
				_ = unstructured.SetNestedSlice(obj.Object, []any{
					map[string]any{"type": "Succeeded", "status": status, "reason": reason},
				}, "status", "conditions")
				_ = unstructured.SetNestedSlice(obj.Object, skipped, "status", "skippedTasks")
				_, _ = dyn.Resource(gvrPipelineRunTest).Namespace(ns).Update(context.Background(), obj, metav1.UpdateOptions{})
			}
		}
	}()
	return stop
}

// flipStatusWithMessageUntilStop is flipStatusUntilStop plus a `message`
// field on the Succeeded condition. Used to verify the PR's terminal
// message is surfaced on PipelineRunResult.
func flipStatusWithMessageUntilStop(t *testing.T, dyn *dynamicfake.FakeDynamicClient, ns, prName, status, reason, message string) chan struct{} {
	t.Helper()
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-stop:
				return
			case <-deadline.C:
				return
			case <-ticker.C:
				obj, err := dyn.Resource(gvrPipelineRunTest).Namespace(ns).Get(context.Background(), prName, metav1.GetOptions{})
				if err != nil {
					continue
				}
				_ = unstructured.SetNestedSlice(obj.Object, []any{
					map[string]any{
						"type":    "Succeeded",
						"status":  status,
						"reason":  reason,
						"message": message,
					},
				}, "status", "conditions")
				_, _ = dyn.Resource(gvrPipelineRunTest).Namespace(ns).Update(context.Background(), obj, metav1.UpdateOptions{})
			}
		}
	}()
	return stop
}

// TestRunPipelineNotPrepared: missing dynamic/kube clients must yield a
// clear error, not a panic.
func TestRunPipelineNotPrepared(t *testing.T) {
	be := cluster.NewWithClients(cluster.ClientBundle{}) // no clients
	_, err := be.RunPipeline(context.Background(), backend.PipelineRunInvocation{
		RunID: "abcdef12", PipelineRunName: "p-abcdef12",
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

// TestCollectTaskOutcomesMultipleTaskRuns: the post-terminal walk must
// turn each TaskRun owned by the PipelineRun into one entry in
// res.Tasks, keyed by the tekton.dev/pipelineTask label, with the right
// per-task status. Drives collectTaskOutcomes directly against
// pre-seeded TaskRun objects on the fake dynamic client.
func TestCollectTaskOutcomesMultipleTaskRuns(t *testing.T) {
	ns := "tkn-act-12345678"
	prName := "p-12345678"
	tr1 := taskRunObj("p-12345678-task1-pod", ns, prName, "task1", "True", "Succeeded", 0)
	tr2 := taskRunObj("p-12345678-task2-pod", ns, prName, "task2", "False", "Failed", 0)
	tr3 := taskRunObj("p-12345678-task3-pod", ns, prName, "task3", "True", "Succeeded", 2)
	be, _, _, _, _ := fakeBackend(t, tr1, tr2, tr3)

	got := be.CollectTaskOutcomesForTest(context.Background(), backend.PipelineRunInvocation{
		RunID: "12345678", PipelineRunName: prName,
	}, ns)

	if len(got) != 3 {
		t.Fatalf("got %d outcomes, want 3 (keys: %v)", len(got), keysFromMap(got))
	}
	if s := got["task1"].Status; s != "succeeded" {
		t.Errorf("task1 status = %q, want succeeded", s)
	}
	if s := got["task2"].Status; s != "failed" {
		t.Errorf("task2 status = %q, want failed", s)
	}
	if a := got["task3"].Attempts; a != 3 {
		t.Errorf("task3 attempts = %d, want 3 (2 retries + final)", a)
	}
	if a := len(got["task3"].RetryAttempts); a != 2 {
		t.Errorf("task3 retry-attempts = %d, want 2", a)
	}
}

// TestBuildPipelineRunPromotesTaskTimeout: when a referenced Task has
// `spec.timeout`, the cluster backend must (a) strip `timeout` from
// the inlined `taskSpec` (Tekton's EmbeddedTask has no `timeout`
// field — webhook rejects it as `unknown field "timeout"`), and
// (b) hoist it onto `pipelineSpec.tasks[].timeout` (PipelineTask.Timeout)
// so Tekton still enforces the per-task wall clock.
func TestBuildPipelineRunPromotesTaskTimeout(t *testing.T) {
	be, _, _, _, _ := fakeBackend(t)

	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		Timeout: "1s",
		Steps:   []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "sleep 30"}},
	}}
	tk.Metadata.Name = "t"

	prObj, err := be.BuildPipelineRunObject(backend.PipelineRunInvocation{
		RunID: "12345678", PipelineRunName: "p-12345678",
		Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
	}, "tkn-act-12345678")
	if err != nil {
		t.Fatal(err)
	}
	un := prObj.(*unstructured.Unstructured)

	tasks, found, err := unstructured.NestedSlice(un.Object, "spec", "pipelineSpec", "tasks")
	if err != nil || !found || len(tasks) != 1 {
		t.Fatalf("pipelineSpec.tasks missing or malformed: found=%v err=%v", found, err)
	}
	pt := tasks[0].(map[string]any)

	if got, ok := pt["timeout"].(string); !ok || got != "1s" {
		t.Errorf("pipelineSpec.tasks[0].timeout = %v, want %q (must be promoted from taskSpec.timeout)", pt["timeout"], "1s")
	}
	taskSpec, ok := pt["taskSpec"].(map[string]any)
	if !ok {
		t.Fatalf("pipelineSpec.tasks[0].taskSpec missing")
	}
	if _, present := taskSpec["timeout"]; present {
		t.Errorf("taskSpec.timeout MUST be stripped (Tekton EmbeddedTask has no `timeout`); got %v", taskSpec["timeout"])
	}
}

// TestCollectTaskOutcomesIgnoresUnlabelled: a TaskRun without the
// tekton.dev/pipelineTask label must be skipped — we don't know which
// pipeline task it belongs to, so it can't appear in res.Tasks.
func TestCollectTaskOutcomesIgnoresUnlabelled(t *testing.T) {
	ns := "tkn-act-12345678"
	prName := "p-12345678"
	bad := &unstructured.Unstructured{}
	bad.SetAPIVersion("tekton.dev/v1")
	bad.SetKind("TaskRun")
	bad.SetName("orphan")
	bad.SetNamespace(ns)
	bad.SetLabels(map[string]string{"tekton.dev/pipelineRun": prName}) // no pipelineTask
	be, _, _, _, _ := fakeBackend(t, bad)
	got := be.CollectTaskOutcomesForTest(context.Background(), backend.PipelineRunInvocation{
		PipelineRunName: prName,
	}, ns)
	if len(got) != 0 {
		t.Errorf("got %d outcomes from unlabeled TaskRun, want 0", len(got))
	}
}

func keysFromMap(m map[string]backend.TaskOutcomeOnCluster) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// taskRunObj builds an unstructured TaskRun with the labels the cluster
// backend uses to associate TaskRuns with their PipelineRun + per-task
// name, optionally including N retriesStatus entries.
func taskRunObj(name, ns, prName, ptName, condStatus, reason string, retries int) *unstructured.Unstructured {
	tr := &unstructured.Unstructured{}
	tr.SetAPIVersion("tekton.dev/v1")
	tr.SetKind("TaskRun")
	tr.SetName(name)
	tr.SetNamespace(ns)
	tr.SetLabels(map[string]string{
		"tekton.dev/pipelineRun":  prName,
		"tekton.dev/pipelineTask": ptName,
	})
	_ = unstructured.SetNestedSlice(tr.Object, []any{
		map[string]any{"type": "Succeeded", "status": condStatus, "reason": reason},
	}, "status", "conditions")
	if retries > 0 {
		rs := make([]any, 0, retries)
		for i := 0; i < retries; i++ {
			rs = append(rs, map[string]any{
				"conditions": []any{
					map[string]any{"type": "Succeeded", "status": "False", "reason": "Failed", "message": "attempt failed"},
				},
			})
		}
		_ = unstructured.SetNestedSlice(tr.Object, rs, "status", "retriesStatus")
	}
	return tr
}

// TestBuildPipelineRunInlinesTimeouts: when the Pipeline declares
// spec.timeouts, the cluster backend must copy that block onto the
// submitted PipelineRun's spec so the controller enforces it.
func TestBuildPipelineRunInlinesTimeouts(t *testing.T) {
	be, _, _, _, _ := fakeBackend(t)

	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Timeouts: &tektontypes.Timeouts{Pipeline: "10m", Tasks: "8m", Finally: "2m"},
		Tasks:    []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}}}}
	tk.Metadata.Name = "t"

	prObj, err := be.BuildPipelineRunObject(backend.PipelineRunInvocation{
		RunID: "12345678", PipelineRunName: "p-12345678",
		Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
	}, "tkn-act-12345678")
	if err != nil {
		t.Fatal(err)
	}
	un := prObj.(*unstructured.Unstructured)
	got, found, err := unstructured.NestedMap(un.Object, "spec", "timeouts")
	if err != nil || !found {
		t.Fatalf("spec.timeouts missing on submitted PipelineRun")
	}
	if got["pipeline"] != "10m" || got["tasks"] != "8m" || got["finally"] != "2m" {
		t.Errorf("spec.timeouts = %v, want pipeline=10m tasks=8m finally=2m", got)
	}
	// Tekton v1 PipelineSpec does NOT have a timeouts field. Leaving it
	// under pipelineSpec.timeouts gets rejected by the admission webhook
	// ("unknown field"). Verify it's stripped.
	if _, found, _ := unstructured.NestedMap(un.Object, "spec", "pipelineSpec", "timeouts"); found {
		t.Errorf("pipelineSpec.timeouts must NOT be set on the submitted PipelineRun (Tekton rejects it)")
	}
}

// TestEnsureNamespaceIdempotent: a second RunPipeline call against the
// same RunID prefix (same namespace) must not error on the duplicate
// Namespace create.
func TestEnsureNamespaceIdempotent(t *testing.T) {
	be, _, kube, _, _ := fakeBackend(t)
	ns := "tkn-act-deadbeef"
	if _, err := kube.CoreV1().Namespaces().Create(context.Background(),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// applyVolumeSources is the cleanest hook to re-trigger the namespace
	// path indirectly; volume-less invocation simply no-ops.
	if err := be.ApplyVolumeSourcesForTest(context.Background(), backend.PipelineRunInvocation{
		RunID: "deadbeef", PipelineRunName: "p-deadbeef",
		Pipeline: tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{}},
	}, ns); err != nil {
		t.Errorf("apply on existing ns: %v", err)
	}
}

// TestBuildPipelineRunInlinesStepTemplate: when a referenced Task has
// stepTemplate, the cluster backend must inline it under
// pipelineSpec.tasks[].taskSpec.stepTemplate intact (Tekton's
// EmbeddedTask schema accepts stepTemplate natively).
func TestBuildPipelineRunInlinesStepTemplate(t *testing.T) {
	be, _, _, _, _ := fakeBackend(t)

	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		StepTemplate: &tektontypes.StepTemplate{
			Image: "alpine:3",
			Env:   []tektontypes.EnvVar{{Name: "SHARED", Value: "hello"}},
		},
		Steps: []tektontypes.Step{{Name: "s", Script: "true"}},
	}}
	tk.Metadata.Name = "t"

	prObj, err := be.BuildPipelineRunObject(backend.PipelineRunInvocation{
		RunID: "12345678", PipelineRunName: "p-12345678",
		Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
	}, "tkn-act-12345678")
	if err != nil {
		t.Fatal(err)
	}
	un := prObj.(*unstructured.Unstructured)

	tasks, _, _ := unstructured.NestedSlice(un.Object, "spec", "pipelineSpec", "tasks")
	if len(tasks) != 1 {
		t.Fatalf("tasks slice = %d, want 1", len(tasks))
	}
	taskMap, ok := tasks[0].(map[string]any)
	if !ok {
		t.Fatalf("tasks[0] not a map: %T", tasks[0])
	}
	taskSpec, ok := taskMap["taskSpec"].(map[string]any)
	if !ok {
		t.Fatalf("taskSpec missing under inlined task")
	}
	st, ok := taskSpec["stepTemplate"].(map[string]any)
	if !ok {
		t.Fatalf("stepTemplate missing on inlined taskSpec; got: %v", taskSpec)
	}
	if got := st["image"]; got != "alpine:3" {
		t.Errorf("stepTemplate.image = %v, want alpine:3", got)
	}
}

// TestRunPipelineSurfacesResults: when the Tekton controller writes
// `status.results` on the PipelineRun, the cluster backend must
// forward those into PipelineRunResult.Results so the engine can
// emit them on the run-end event.
func TestRunPipelineSurfacesResults(t *testing.T) {
	be, dyn, _, _, _ := fakeBackend(t)

	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Results: []tektontypes.PipelineResultSpec{
			{Name: "revision", Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "$(tasks.t.results.commit)"}},
		},
		Tasks: []tektontypes.PipelineTask{{Name: "t", TaskRef: &tektontypes.TaskRef{Name: "x"}}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		Results: []tektontypes.ResultSpec{{Name: "commit"}},
		Steps:   []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}},
	}}
	tk.Metadata.Name = "x"

	prName := "p-resabcde"
	ns := "tkn-act-resabcde"

	// Driver writes Succeeded=True AND status.results = [{name:revision,value:abc}].
	stop := flipStatusWithResultsUntilStop(t, dyn, ns, prName, "True", "Succeeded",
		[]any{map[string]any{"name": "revision", "value": "abc"}})
	defer close(stop)

	res, err := be.RunPipeline(context.Background(), backend.PipelineRunInvocation{
		RunID: "resabcde", PipelineRunName: prName,
		Pipeline: pl, Tasks: map[string]tektontypes.Task{"x": tk},
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("status = %q, want succeeded", res.Status)
	}
	if got := res.Results["revision"]; got != "abc" {
		t.Errorf("Results[revision] = %v, want abc", got)
	}
}

// TestBuildPipelineRunRoundTripsDisplayName: every displayName /
// description on the input Pipeline + Task must appear under the
// inlined spec.pipelineSpec.* on the resulting PipelineRun object.
// Locks in that a future hand-written conversion can't silently drop
// these fields.
func TestBuildPipelineRunRoundTripsDisplayName(t *testing.T) {
	be, _, _, _, _ := fakeBackend(t)

	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		DisplayName: "Build & test",
		Description: "Build then test.",
		Tasks: []tektontypes.PipelineTask{{
			Name:        "t1",
			DisplayName: "Compile binary",
			TaskRef:     &tektontypes.TaskRef{Name: "tk"},
		}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		DisplayName: "Unit-test runner",
		Description: "Runs go test.",
		Steps: []tektontypes.Step{{
			Name:        "s",
			DisplayName: "Compile",
			Description: "Compile the binary.",
			Image:       "alpine:3",
			Script:      "true",
		}},
	}}
	tk.Metadata.Name = "tk"

	prObj, err := be.BuildPipelineRunObject(backend.PipelineRunInvocation{
		RunID: "12345678", PipelineRunName: "p-12345678",
		Pipeline: pl, Tasks: map[string]tektontypes.Task{"tk": tk},
	}, "tkn-act-12345678")
	if err != nil {
		t.Fatal(err)
	}
	un := prObj.(*unstructured.Unstructured)

	plSpec, _, _ := unstructured.NestedMap(un.Object, "spec", "pipelineSpec")
	if plSpec["displayName"] != "Build & test" {
		t.Errorf("pipelineSpec.displayName = %v", plSpec["displayName"])
	}
	if plSpec["description"] != "Build then test." {
		t.Errorf("pipelineSpec.description = %v", plSpec["description"])
	}
	tasks, _, _ := unstructured.NestedSlice(un.Object, "spec", "pipelineSpec", "tasks")
	taskMap := tasks[0].(map[string]any)
	if taskMap["displayName"] != "Compile binary" {
		t.Errorf("tasks[0].displayName = %v", taskMap["displayName"])
	}
	taskSpec := taskMap["taskSpec"].(map[string]any)
	if taskSpec["displayName"] != "Unit-test runner" {
		t.Errorf("taskSpec.displayName = %v", taskSpec["displayName"])
	}
	if taskSpec["description"] != "Runs go test." {
		t.Errorf("taskSpec.description = %v", taskSpec["description"])
	}
	steps := taskSpec["steps"].([]any)
	stepMap := steps[0].(map[string]any)
	if stepMap["displayName"] != "Compile" {
		t.Errorf("step.displayName = %v", stepMap["displayName"])
	}
	if stepMap["description"] != "Compile the binary." {
		t.Errorf("step.description = %v", stepMap["description"])
	}
}

// flipStatusWithResultsUntilStop is flipStatusUntilStop but also writes
// `status.results` to the PR.
func flipStatusWithResultsUntilStop(t *testing.T, dyn *dynamicfake.FakeDynamicClient, ns, prName, status, reason string, results []any) chan struct{} {
	t.Helper()
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-stop:
				return
			case <-deadline.C:
				return
			case <-ticker.C:
				obj, err := dyn.Resource(gvrPipelineRunTest).Namespace(ns).Get(context.Background(), prName, metav1.GetOptions{})
				if err != nil {
					continue
				}
				_ = unstructured.SetNestedSlice(obj.Object, []any{
					map[string]any{"type": "Succeeded", "status": status, "reason": reason},
				}, "status", "conditions")
				_ = unstructured.SetNestedSlice(obj.Object, results, "status", "results")
				_, _ = dyn.Resource(gvrPipelineRunTest).Namespace(ns).Update(context.Background(), obj, metav1.UpdateOptions{})
			}
		}
	}()
	return stop
}
