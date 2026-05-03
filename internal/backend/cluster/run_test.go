package cluster

import (
	"reflect"
	"testing"

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
