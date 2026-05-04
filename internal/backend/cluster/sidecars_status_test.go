package cluster

import (
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// recordingSidecarSink captures EmitSidecarStart / EmitSidecarEnd
// calls so the diff-driven transition emitter can be unit-tested
// without spinning up a Tekton controller.
type recordingSidecarSink struct {
	starts []string
	ends   []endRec
}

type endRec struct {
	Name     string
	ExitCode int
	Status   string
}

func (s *recordingSidecarSink) StepLog(_, _, _, _, _ string) {}
func (s *recordingSidecarSink) SidecarLog(_, _, _, _ string) {}
func (s *recordingSidecarSink) EmitSidecarStart(_, name string) {
	s.starts = append(s.starts, name)
}
func (s *recordingSidecarSink) EmitSidecarEnd(_, name string, exitCode int, status, _ string) {
	s.ends = append(s.ends, endRec{Name: name, ExitCode: exitCode, Status: status})
}

// TestEmitSidecarTransitionsRunningThenTerminated verifies the
// diff-driven emission: a sidecar going Running gets one
// sidecar-start; later going Terminated gets one sidecar-end. The
// per-(taskRun, sidecar) state map prevents duplicate emission
// even if the watch loop re-observes the same status repeatedly.
func TestEmitSidecarTransitionsRunningThenTerminated(t *testing.T) {
	sink := &recordingSidecarSink{}
	be := &Backend{}
	in := backend.PipelineRunInvocation{LogSink: sink}
	seen := map[string]map[string]sidecarSeenState{}

	// First poll: Running.
	tr1 := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"sidecars": []any{
				map[string]any{"name": "redis", "running": map[string]any{}},
			},
		},
	}}
	be.emitSidecarTransitions(in, "t", "tr1", tr1, seen)

	// Same status re-observed → no duplicate emission.
	be.emitSidecarTransitions(in, "t", "tr1", tr1, seen)

	if len(sink.starts) != 1 || sink.starts[0] != "redis" {
		t.Errorf("starts = %v, want [redis] (one entry, no duplicates)", sink.starts)
	}
	if len(sink.ends) != 0 {
		t.Errorf("unexpected ends before terminated: %v", sink.ends)
	}

	// Second poll: Terminated (exit 0 → succeeded).
	tr2 := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"sidecars": []any{
				map[string]any{
					"name":       "redis",
					"terminated": map[string]any{"exitCode": int64(0)},
				},
			},
		},
	}}
	be.emitSidecarTransitions(in, "t", "tr1", tr2, seen)

	if len(sink.ends) != 1 || sink.ends[0].Name != "redis" || sink.ends[0].Status != "succeeded" {
		t.Errorf("ends = %+v, want one redis succeeded", sink.ends)
	}

	// Re-observe terminated → no duplicate.
	be.emitSidecarTransitions(in, "t", "tr1", tr2, seen)
	if len(sink.ends) != 1 {
		t.Errorf("duplicate sidecar-end emitted: %+v", sink.ends)
	}
}

// TestEmitSidecarTransitionsNonzeroExit verifies the failed-status
// mapping: terminated exit != 0 → status "failed".
func TestEmitSidecarTransitionsNonzeroExit(t *testing.T) {
	sink := &recordingSidecarSink{}
	be := &Backend{}
	in := backend.PipelineRunInvocation{LogSink: sink}
	seen := map[string]map[string]sidecarSeenState{}

	tr := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"sidecars": []any{
				map[string]any{
					"name":       "broken",
					"terminated": map[string]any{"exitCode": int64(137)},
				},
			},
		},
	}}
	be.emitSidecarTransitions(in, "t", "tr1", tr, seen)
	if len(sink.ends) != 1 || sink.ends[0].Status != "failed" || sink.ends[0].ExitCode != 137 {
		t.Errorf("ends = %+v, want broken failed exit 137", sink.ends)
	}
}

// TestEmitSidecarTransitionsNoLogSink is a defensive check: the
// helper must not panic when the invocation has no LogSink (the
// engine guarantees one in production but unit tests may pass
// PipelineRunInvocation{} fixtures).
func TestEmitSidecarTransitionsNoLogSink(t *testing.T) {
	be := &Backend{}
	tr := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"sidecars": []any{map[string]any{"name": "x", "running": map[string]any{}}},
		},
	}}
	// Should be a no-op — does not panic.
	be.emitSidecarTransitions(backend.PipelineRunInvocation{}, "t", "tr1", tr, map[string]map[string]sidecarSeenState{})
}

func TestParsePodSidecarStatusesRunningAndTerminated(t *testing.T) {
	// Synthetic TaskRun with two sidecar entries: one still running,
	// one terminated with exit code 0.
	tr := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"sidecars": []any{
				map[string]any{
					"name":      "redis",
					"container": "sidecar-redis",
					"running":   map[string]any{"startedAt": "2026-05-04T00:00:00Z"},
				},
				map[string]any{
					"name":      "mock",
					"container": "sidecar-mock",
					"terminated": map[string]any{
						"exitCode":   int64(0),
						"finishedAt": "2026-05-04T00:00:01Z",
					},
				},
			},
		},
	}}
	got := parsePodSidecarStatuses(tr)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Name != "redis" || !got[0].Running {
		t.Errorf("got[0] = %+v, want redis running", got[0])
	}
	if got[1].Name != "mock" || !got[1].Terminated || got[1].ExitCode != 0 {
		t.Errorf("got[1] = %+v, want mock terminated exit 0", got[1])
	}
}

func TestParsePodSidecarStatusesTerminatedFloatExitCode(t *testing.T) {
	// JSON unmarshal often produces float64 for numbers; the helper
	// must coerce both int64 and float64 to int32.
	tr := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"sidecars": []any{
				map[string]any{
					"name": "mock",
					"terminated": map[string]any{
						"exitCode": float64(137),
					},
				},
			},
		},
	}}
	got := parsePodSidecarStatuses(tr)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ExitCode != 137 {
		t.Errorf("ExitCode = %d, want 137", got[0].ExitCode)
	}
}

func TestParsePodSidecarStatusesEmpty(t *testing.T) {
	tr := &unstructured.Unstructured{Object: map[string]any{}}
	if got := parsePodSidecarStatuses(tr); len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestParsePodSidecarStatusesNil(t *testing.T) {
	if got := parsePodSidecarStatuses(nil); got != nil {
		t.Errorf("nil input should return nil, got %+v", got)
	}
}

func TestParsePodSidecarStatusesSkipsMalformed(t *testing.T) {
	// Non-map entry should be silently skipped (defensive — Tekton
	// won't produce it but the unstructured tree allows anything).
	tr := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"sidecars": []any{
				"not-a-map",
				map[string]any{"name": "ok"},
			},
		},
	}}
	got := parsePodSidecarStatuses(tr)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (malformed entry skipped)", len(got))
	}
	if got[0].Name != "ok" {
		t.Errorf("got[0].Name = %q, want ok", got[0].Name)
	}
}
