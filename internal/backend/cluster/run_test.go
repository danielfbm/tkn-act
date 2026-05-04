package cluster

import (
	"reflect"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestMapPipelineRunStatus locks in the cross-backend status contract: the
// cluster watch must produce the same engine.RunResult.Status string the
// docker engine would for the same outcome.
func TestMapPipelineRunStatus(t *testing.T) {
	cases := []struct {
		name    string
		status  string
		reason  string
		want    string
	}{
		{"succeeded", "True", "Succeeded", "succeeded"},
		{"true ignores reason", "True", "PipelineRunTimeout", "succeeded"},
		{"plain failed", "False", "Failed", "failed"},
		{"empty reason still failed", "False", "", "failed"},
		{"pipelinerun timeout", "False", "PipelineRunTimeout", "timeout"},
		{"taskrun timeout", "False", "TaskRunTimeout", "timeout"},
		{"unknown reason still failed", "False", "InternalError", "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapPipelineRunStatus(tc.status, tc.reason); got != tc.want {
				t.Errorf("mapPipelineRunStatus(%q,%q) = %q, want %q", tc.status, tc.reason, got, tc.want)
			}
		})
	}
}

// TestExtractPipelineResults covers the JSON-shape decoding for each
// ParamValue kind plus the empty / missing edge cases. The
// happy-path string case is already exercised by
// TestRunPipelineSurfacesResults; this fills in coverage for the
// array, object, malformed, and empty branches.
func TestExtractPipelineResults(t *testing.T) {
	t.Run("nil when status.results missing", func(t *testing.T) {
		un := &unstructured.Unstructured{Object: map[string]any{
			"status": map[string]any{},
		}}
		if got := extractPipelineResults(un); got != nil {
			t.Errorf("got = %v, want nil", got)
		}
	})

	t.Run("nil when status.results empty slice", func(t *testing.T) {
		un := &unstructured.Unstructured{Object: map[string]any{
			"status": map[string]any{"results": []any{}},
		}}
		if got := extractPipelineResults(un); got != nil {
			t.Errorf("got = %v, want nil", got)
		}
	})

	t.Run("array value decoded as []string", func(t *testing.T) {
		un := &unstructured.Unstructured{Object: map[string]any{
			"status": map[string]any{
				"results": []any{
					map[string]any{"name": "files", "value": []any{"a.txt", "b.txt"}},
				},
			},
		}}
		got := extractPipelineResults(un)
		want := map[string]any{"files": []string{"a.txt", "b.txt"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got = %v, want %v", got, want)
		}
	})

	t.Run("object value decoded as map[string]string", func(t *testing.T) {
		un := &unstructured.Unstructured{Object: map[string]any{
			"status": map[string]any{
				"results": []any{
					map[string]any{"name": "meta", "value": map[string]any{
						"owner": "team-a", "sha": "abc",
					}},
				},
			},
		}}
		got := extractPipelineResults(un)
		gotMeta, ok := got["meta"].(map[string]string)
		if !ok {
			t.Fatalf("meta not a map[string]string: %T", got["meta"])
		}
		want := map[string]string{"owner": "team-a", "sha": "abc"}
		if !reflect.DeepEqual(gotMeta, want) {
			t.Errorf("meta = %v, want %v", gotMeta, want)
		}
	})

	t.Run("entries with empty name are skipped", func(t *testing.T) {
		un := &unstructured.Unstructured{Object: map[string]any{
			"status": map[string]any{
				"results": []any{
					map[string]any{"name": "", "value": "ignored"},
					map[string]any{"name": "kept", "value": "x"},
				},
			},
		}}
		got := extractPipelineResults(un)
		if got["kept"] != "x" {
			t.Errorf("kept = %v, want x", got["kept"])
		}
		if _, present := got[""]; present {
			t.Errorf("empty-name entry should be dropped, got %v", got[""])
		}
	})

	t.Run("non-map entries skipped; nil returned when nothing usable", func(t *testing.T) {
		un := &unstructured.Unstructured{Object: map[string]any{
			"status": map[string]any{
				// All non-map slice entries; NestedSlice deep-copies through
				// the JSON value graph so we keep the values JSON-shaped
				// (strings) rather than a Go-only int.
				"results": []any{"not-a-map", "still-not-a-map"},
			},
		}}
		if got := extractPipelineResults(un); got != nil {
			t.Errorf("got = %v, want nil (no map-shaped entries to decode)", got)
		}
	})

	t.Run("array entries that are not strings are skipped", func(t *testing.T) {
		un := &unstructured.Unstructured{Object: map[string]any{
			"status": map[string]any{
				"results": []any{
					// Strings only; non-string array members would force
					// a value-type panic in the apimachinery deep-copy.
					// Mixed-type validation lives in the helper itself,
					// which silently drops a non-string array element.
					map[string]any{"name": "files", "value": []any{"a", "b"}},
				},
			},
		}}
		got := extractPipelineResults(un)
		if files, ok := got["files"].([]string); !ok || len(files) != 2 {
			t.Errorf("files = %v (%T), want []string len 2", got["files"], got["files"])
		}
	})
}

// TestStepDisplayNameLookup covers the per-task step-name -> displayName
// lookup that powers cross-backend display_name parity on step-log
// events in cluster mode. Source-of-truth is the input bundle (NOT the
// controller verdict), so the test feeds a synthetic
// PipelineRunInvocation and asserts the four code paths:
//   - main task with taskRef → resolved Task.Spec.Steps populates the map
//   - finally task with taskRef → same
//   - inline taskSpec → reads Steps directly
//   - unknown task name → empty map (graceful fallback)
func TestStepDisplayNameLookup(t *testing.T) {
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		Steps: []tektontypes.Step{
			{Name: "compile", DisplayName: "Compile"},
			{Name: "no-display"},
		},
	}}
	tk.Metadata.Name = "tk"
	inlineSpec := tektontypes.TaskSpec{Steps: []tektontypes.Step{
		{Name: "inline-step", DisplayName: "Inline"},
	}}
	in := backend.PipelineRunInvocation{
		Pipeline: tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
			Tasks: []tektontypes.PipelineTask{
				{Name: "main", TaskRef: &tektontypes.TaskRef{Name: "tk"}},
				{Name: "main-inline", TaskSpec: &inlineSpec},
			},
			Finally: []tektontypes.PipelineTask{
				{Name: "fin", TaskRef: &tektontypes.TaskRef{Name: "tk"}},
			},
		}},
		Tasks: map[string]tektontypes.Task{"tk": tk},
	}

	t.Run("main task taskRef", func(t *testing.T) {
		got := stepDisplayNameLookup(in, "main")
		if got["compile"] != "Compile" {
			t.Errorf("compile -> %q, want Compile", got["compile"])
		}
		if _, ok := got["no-display"]; ok {
			t.Errorf("no-display should be omitted (empty displayName)")
		}
	})
	t.Run("finally task taskRef", func(t *testing.T) {
		got := stepDisplayNameLookup(in, "fin")
		if got["compile"] != "Compile" {
			t.Errorf("compile -> %q, want Compile", got["compile"])
		}
	})
	t.Run("inline taskSpec", func(t *testing.T) {
		got := stepDisplayNameLookup(in, "main-inline")
		if got["inline-step"] != "Inline" {
			t.Errorf("inline-step -> %q, want Inline", got["inline-step"])
		}
	})
	t.Run("unknown task", func(t *testing.T) {
		got := stepDisplayNameLookup(in, "missing")
		if len(got) != 0 {
			t.Errorf("unknown task should return empty map; got %v", got)
		}
	})
	t.Run("missing taskRef target", func(t *testing.T) {
		in2 := in
		in2.Pipeline = tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
			Tasks: []tektontypes.PipelineTask{
				{Name: "orphan", TaskRef: &tektontypes.TaskRef{Name: "no-such-task"}},
			},
		}}
		got := stepDisplayNameLookup(in2, "orphan")
		if len(got) != 0 {
			t.Errorf("missing taskRef target should return empty map; got %v", got)
		}
	})
}

// TestMatchMatrixRowFromTaskRunPrimaryParamHash: a faked TaskRun
// whose spec.params record the matrix-row params is matched by
// canonical-hash to the correct row index, regardless of the
// TaskRun's order in the list. Specifically, simulate a TaskRun
// for the (os=darwin, goversion=1.22) row of a 2×2 matrix and
// assert MatrixInfo.Index == 3.
func TestMatchMatrixRowFromTaskRunPrimaryParamHash(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{
			Name:    "build",
			TaskRef: &tektontypes.TaskRef{Name: "t"},
			Matrix: &tektontypes.Matrix{Params: []tektontypes.MatrixParam{
				{Name: "os", Value: []string{"linux", "darwin"}},
				{Name: "goversion", Value: []string{"1.21", "1.22"}},
			}},
		}},
	}}
	// Row 3 of expandMatrix's output is (os=darwin, goversion=1.22).
	tr := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name": "p-build-row3",
		},
		"spec": map[string]any{
			"params": []any{
				map[string]any{"name": "os", "value": "darwin"},
				map[string]any{"name": "goversion", "value": "1.22"},
			},
		},
	}}
	fallbackSeq := map[string]int{}
	mi := matchMatrixRowFromTaskRun(pl, "build", tr, -1, fallbackSeq, nil)
	if mi == nil {
		t.Fatalf("matchMatrixRowFromTaskRun returned nil; want index 3")
	}
	if mi.Parent != "build" || mi.Index != 3 || mi.Of != 4 {
		t.Errorf("matrix info = %+v, want {Parent:build Index:3 Of:4}", *mi)
	}
	if mi.Params["os"] != "darwin" || mi.Params["goversion"] != "1.22" {
		t.Errorf("matrix.Params = %+v", mi.Params)
	}
	// A second TaskRun for row 0 also matches by hash.
	tr0 := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "p-build-row0"},
		"spec": map[string]any{
			"params": []any{
				map[string]any{"name": "os", "value": "linux"},
				map[string]any{"name": "goversion", "value": "1.21"},
			},
		},
	}}
	mi0 := matchMatrixRowFromTaskRun(pl, "build", tr0, -1, fallbackSeq, nil)
	if mi0 == nil || mi0.Index != 0 {
		t.Fatalf("row 0 reconstruction = %+v, want Index=0", mi0)
	}
}

// TestMatchMatrixRowFromTaskRunFallbackChildRefOrder: when the
// TaskRun's spec.params don't hold matrix-row params (a
// hypothetical future Tekton version), the reconstruction falls
// back to per-parent ordering. The fallback path also emits one
// EvtError warning per TaskRun.
func TestMatchMatrixRowFromTaskRunFallbackChildRefOrder(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{
			Name:    "build",
			TaskRef: &tektontypes.TaskRef{Name: "t"},
			Matrix: &tektontypes.Matrix{Params: []tektontypes.MatrixParam{
				{Name: "os", Value: []string{"linux", "darwin"}},
			}},
		}},
	}}
	type capture struct{ events []reporter.Event }
	cap := &capture{}
	// Stub Reporter capturing emitted events.
	r := reporterFunc(func(e reporter.Event) { cap.events = append(cap.events, e) })
	tr := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "p-build-mystery"},
		"spec":     map[string]any{}, // no params
	}}
	fallback := map[string]int{}
	mi := matchMatrixRowFromTaskRun(pl, "build", tr, 0, fallback, r)
	if mi == nil || mi.Index != 0 {
		t.Fatalf("fallback row 0 = %+v, want Index=0", mi)
	}
	if len(cap.events) != 1 || cap.events[0].Kind != reporter.EvtError {
		t.Errorf("expected one EvtError warning; got %v", cap.events)
	}
	tr2 := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "p-build-mystery2"},
		"spec":     map[string]any{},
	}}
	mi2 := matchMatrixRowFromTaskRun(pl, "build", tr2, 1, fallback, r)
	if mi2 == nil || mi2.Index != 1 {
		t.Fatalf("fallback row 1 = %+v, want Index=1", mi2)
	}
	if len(cap.events) != 2 {
		t.Errorf("expected 2 EvtError warnings; got %d", len(cap.events))
	}
}

// TestMatchMatrixRowFromTaskRunIncludeRow: the include row's
// IncludeName is preserved on the reconstructed MatrixInfo so the
// outcomes-map key uses the declared name (e.g. "arm-extra")
// instead of "<parent>-<index>".
func TestMatchMatrixRowFromTaskRunIncludeRow(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{
			Name:    "build",
			TaskRef: &tektontypes.TaskRef{Name: "t"},
			Matrix: &tektontypes.Matrix{
				Params: []tektontypes.MatrixParam{
					{Name: "os", Value: []string{"linux"}},
				},
				Include: []tektontypes.MatrixInclude{
					{Name: "arm-extra", Params: []tektontypes.Param{
						{Name: "arch", Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "arm64"}},
					}},
				},
			},
		}},
	}}
	tr := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "p-build-arm"},
		"spec": map[string]any{
			"params": []any{
				map[string]any{"name": "arch", "value": "arm64"},
			},
		},
	}}
	mi := matchMatrixRowFromTaskRun(pl, "build", tr, -1, map[string]int{}, nil)
	if mi == nil {
		t.Fatal("nil reconstruction")
	}
	if mi.IncludeName != "arm-extra" {
		t.Errorf("IncludeName = %q, want arm-extra", mi.IncludeName)
	}
	if mi.Index != 1 || mi.Of != 2 {
		t.Errorf("Index=%d Of=%d, want 1/2", mi.Index, mi.Of)
	}
}

// reporterFunc adapts a function literal to the reporter.Reporter
// interface for tests that just need to capture events.
type reporterFunc func(e reporter.Event)

func (f reporterFunc) Emit(e reporter.Event) { f(e) }
func (f reporterFunc) Close() error          { return nil }
