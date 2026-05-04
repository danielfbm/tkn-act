package cluster

import (
	"reflect"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
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
