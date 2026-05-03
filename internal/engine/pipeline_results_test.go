package engine

import (
	"reflect"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

func TestResolvePipelineResultsNil(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{}}
	got, errs := resolvePipelineResults(pl, map[string]map[string]string{})
	if got != nil {
		t.Errorf("got = %v, want nil for pipeline without spec.results", got)
	}
	if len(errs) != 0 {
		t.Errorf("errs = %v, want none", errs)
	}
}

func TestResolvePipelineResultsString(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Results: []tektontypes.PipelineResultSpec{
			{Name: "revision", Value: tektontypes.ParamValue{
				Type: tektontypes.ParamTypeString, StringVal: "$(tasks.checkout.results.commit)",
			}},
		},
	}}
	results := map[string]map[string]string{"checkout": {"commit": "abc123"}}
	got, errs := resolvePipelineResults(pl, results)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	want := map[string]any{"revision": "abc123"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got = %v, want %v", got, want)
	}
}

func TestResolvePipelineResultsArray(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Results: []tektontypes.PipelineResultSpec{
			{Name: "files", Value: tektontypes.ParamValue{
				Type: tektontypes.ParamTypeArray,
				ArrayVal: []string{
					"$(tasks.scan.results.first)",
					"static",
					"$(tasks.scan.results.second)",
				},
			}},
		},
	}}
	results := map[string]map[string]string{"scan": {"first": "a.txt", "second": "b.txt"}}
	got, errs := resolvePipelineResults(pl, results)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	want := map[string]any{"files": []string{"a.txt", "static", "b.txt"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got = %v, want %v", got, want)
	}
}

func TestResolvePipelineResultsObject(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Results: []tektontypes.PipelineResultSpec{
			{Name: "meta", Value: tektontypes.ParamValue{
				Type: tektontypes.ParamTypeObject,
				ObjectVal: map[string]string{
					"owner": "team-a",
					"sha":   "$(tasks.checkout.results.commit)",
				},
			}},
		},
	}}
	results := map[string]map[string]string{"checkout": {"commit": "abc123"}}
	got, errs := resolvePipelineResults(pl, results)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	wantMeta := map[string]string{"owner": "team-a", "sha": "abc123"}
	gotMeta, ok := got["meta"].(map[string]string)
	if !ok {
		t.Fatalf("meta not a map[string]string: %T", got["meta"])
	}
	if !reflect.DeepEqual(gotMeta, wantMeta) {
		t.Errorf("meta = %v, want %v", gotMeta, wantMeta)
	}
}

func TestResolvePipelineResultsDropsMissingTask(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Results: []tektontypes.PipelineResultSpec{
			{Name: "good", Value: tektontypes.ParamValue{
				Type: tektontypes.ParamTypeString, StringVal: "$(tasks.ok.results.v)",
			}},
			{Name: "bad", Value: tektontypes.ParamValue{
				Type: tektontypes.ParamTypeString, StringVal: "$(tasks.failed.results.v)",
			}},
		},
	}}
	results := map[string]map[string]string{"ok": {"v": "yes"}}
	got, errs := resolvePipelineResults(pl, results)
	if got["good"] != "yes" {
		t.Errorf("good = %v, want yes", got["good"])
	}
	if _, present := got["bad"]; present {
		t.Errorf("bad should be dropped, got = %v", got["bad"])
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly 1 drop error", errs)
	}
	if !strings.Contains(errs[0].Error(), `"bad"`) {
		t.Errorf("err message = %q, want it to mention the dropped result name", errs[0].Error())
	}
}

func TestResolvePipelineResultsDropsMissingResultName(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Results: []tektontypes.PipelineResultSpec{
			{Name: "x", Value: tektontypes.ParamValue{
				Type: tektontypes.ParamTypeString, StringVal: "$(tasks.t.results.absent)",
			}},
		},
	}}
	results := map[string]map[string]string{"t": {"present": "v"}}
	got, errs := resolvePipelineResults(pl, results)
	if _, present := got["x"]; present {
		t.Errorf("x should be dropped (referenced result name not produced)")
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want 1", errs)
	}
}
