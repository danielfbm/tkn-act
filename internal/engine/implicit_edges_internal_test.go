package engine

import (
	"reflect"
	"testing"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

func TestImplicitParamEdgesScansWhenAndIndexedRefs(t *testing.T) {
	str := func(s string) tektontypes.ParamValue {
		return tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: s}
	}
	pt := tektontypes.PipelineTask{
		Name: "x",
		Params: []tektontypes.Param{
			{Name: "all", Value: str("$(tasks.arr.results.list[*])")},
			{Name: "one", Value: str("pre-$(tasks.idx.results.list[3])-post")},
			{Name: "plain", Value: str("$(tasks.plain.results.v)")},
			{Name: "notaref", Value: str("$(tasks.status)")},
		},
		When: []tektontypes.WhenExpression{
			{Input: "$(tasks.wi.results.v)", Operator: "in", Values: []string{"a"}},
			{Input: "lit", Operator: "notin", Values: []string{"b", "$(tasks.wv.results.v)"}},
		},
	}
	got := implicitParamEdges(pt)
	want := []string{"arr", "idx", "plain", "wi", "wv"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("implicitParamEdges = %v, want %v", got, want)
	}
}

func TestExpandRefNamesMapsMatrixParent(t *testing.T) {
	pts := []tektontypes.PipelineTask{
		{Name: "build-0", MatrixInfo: &tektontypes.MatrixInfo{Parent: "build", Index: 0, Of: 2}},
		{Name: "build-1", MatrixInfo: &tektontypes.MatrixInfo{Parent: "build", Index: 1, Of: 2}},
		{Name: "plain"},
	}
	exp := matrixExpansions(pts)
	if got := expandRefNames("build", exp); !reflect.DeepEqual(got, []string{"build-0", "build-1"}) {
		t.Errorf("expandRefNames(build) = %v", got)
	}
	if got := expandRefNames("plain", exp); !reflect.DeepEqual(got, []string{"plain"}) {
		t.Errorf("expandRefNames(plain) = %v", got)
	}
}

func TestMissingResultRefs(t *testing.T) {
	pt := tektontypes.PipelineTask{
		Name:   "c",
		Params: []tektontypes.Param{{Name: "p", Value: tektontypes.ParamValue{StringVal: "$(tasks.m.results.v[*])"}}},
	}
	exp := map[string][]string{"m": {"m-0", "m-1"}}
	cases := []struct {
		name     string
		outcomes map[string]TaskOutcome
		missing  bool
	}{
		{"not yet run", map[string]TaskOutcome{}, false},
		{"one row ok", map[string]TaskOutcome{"m-0": {Status: "skipped", skip: skipWhen}, "m-1": {Status: "succeeded"}}, false},
		{"all skipped", map[string]TaskOutcome{"m-0": {Status: "skipped", skip: skipWhen}, "m-1": {Status: "skipped", skip: skipWhen}}, true},
		{"failed", map[string]TaskOutcome{"m-0": {Status: "failed"}, "m-1": {Status: "failed"}}, true},
	}
	for _, tc := range cases {
		if _, got := missingResultRefs(pt, tc.outcomes, exp); got != tc.missing {
			t.Errorf("%s: missing = %v, want %v", tc.name, got, tc.missing)
		}
	}
}
