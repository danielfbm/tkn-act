package engine

import (
	"reflect"
	"testing"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// paramByName is a test helper that returns the StringVal of the
// first param matching `name`, or empty string when not found.
func paramByName(ps []tektontypes.Param, name string) string {
	for _, p := range ps {
		if p.Name == name {
			return p.Value.StringVal
		}
	}
	return ""
}

func TestExpandMatrixNoMatrixIsNoOp(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	got, err := expandMatrix(pl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, pl) {
		t.Errorf("nil-matrix should be no-op, got %+v", got)
	}
}

func TestExpandMatrixCrossProduct(t *testing.T) {
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
	got, err := expandMatrix(pl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.Tasks) != 4 {
		t.Fatalf("tasks = %d, want 4", len(got.Spec.Tasks))
	}
	wantNames := []string{"build-0", "build-1", "build-2", "build-3"}
	for i, pt := range got.Spec.Tasks {
		if pt.Name != wantNames[i] {
			t.Errorf("tasks[%d].Name = %q, want %q", i, pt.Name, wantNames[i])
		}
		if pt.Matrix != nil {
			t.Errorf("tasks[%d].Matrix = %+v, want nil after expansion", i, pt.Matrix)
		}
		if pt.MatrixInfo == nil {
			t.Errorf("tasks[%d].MatrixInfo nil; want populated", i)
			continue
		}
		if pt.MatrixInfo.Parent != "build" || pt.MatrixInfo.Index != i || pt.MatrixInfo.Of != 4 {
			t.Errorf("tasks[%d].MatrixInfo = %+v", i, *pt.MatrixInfo)
		}
	}
	pt0 := got.Spec.Tasks[0]
	osVal, goVal := paramByName(pt0.Params, "os"), paramByName(pt0.Params, "goversion")
	if osVal != "linux" || goVal != "1.21" {
		t.Errorf("row 0 = (os=%q goversion=%q), want (linux, 1.21)", osVal, goVal)
	}
	pt3 := got.Spec.Tasks[3]
	if paramByName(pt3.Params, "os") != "darwin" || paramByName(pt3.Params, "goversion") != "1.22" {
		t.Errorf("row 3 = (os=%q goversion=%q), want (darwin, 1.22)", paramByName(pt3.Params, "os"), paramByName(pt3.Params, "goversion"))
	}
}

func TestExpandMatrixIncludeAppendsRows(t *testing.T) {
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
					{Params: []tektontypes.Param{
						{Name: "arch", Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "armv7"}},
					}},
				},
			},
		}},
	}}
	got, err := expandMatrix(pl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.Tasks) != 3 {
		t.Fatalf("tasks = %d, want 3", len(got.Spec.Tasks))
	}
	names := []string{got.Spec.Tasks[0].Name, got.Spec.Tasks[1].Name, got.Spec.Tasks[2].Name}
	want := []string{"build-0", "arm-extra", "build-2"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	// Named include row carries IncludeName in MatrixInfo.
	if got.Spec.Tasks[1].MatrixInfo == nil || got.Spec.Tasks[1].MatrixInfo.IncludeName != "arm-extra" {
		t.Errorf("Tasks[1].MatrixInfo = %+v, want IncludeName=arm-extra", got.Spec.Tasks[1].MatrixInfo)
	}
}

func TestExpandMatrixRunAfterRewrite(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{
			{
				Name:    "build",
				TaskRef: &tektontypes.TaskRef{Name: "t"},
				Matrix: &tektontypes.Matrix{Params: []tektontypes.MatrixParam{
					{Name: "os", Value: []string{"linux", "darwin"}},
				}},
			},
			{Name: "publish", TaskRef: &tektontypes.TaskRef{Name: "t2"}, RunAfter: []string{"build"}},
		},
	}}
	got, err := expandMatrix(pl, nil)
	if err != nil {
		t.Fatal(err)
	}
	var pub tektontypes.PipelineTask
	for _, pt := range got.Spec.Tasks {
		if pt.Name == "publish" {
			pub = pt
			break
		}
	}
	if !reflect.DeepEqual(pub.RunAfter, []string{"build-0", "build-1"}) {
		t.Errorf("publish.RunAfter = %v, want [build-0 build-1]", pub.RunAfter)
	}
}

func TestExpandMatrixParamPrecedence(t *testing.T) {
	// PipelineTask.params says os=linux; matrix says os ∈ {linux, darwin}.
	// After expansion, each row's os MUST come from matrix.
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{
			Name:    "build",
			TaskRef: &tektontypes.TaskRef{Name: "t"},
			Params: []tektontypes.Param{
				{Name: "os", Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "linux"}},
			},
			Matrix: &tektontypes.Matrix{Params: []tektontypes.MatrixParam{
				{Name: "os", Value: []string{"linux", "darwin"}},
			}},
		}},
	}}
	got, err := expandMatrix(pl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if paramByName(got.Spec.Tasks[1].Params, "os") != "darwin" {
		t.Errorf("expansion 1 os = %q, want darwin (matrix wins)", paramByName(got.Spec.Tasks[1].Params, "os"))
	}
}

func TestExpandMatrixCardinalityCap(t *testing.T) {
	// 17 × 17 = 289 > 256.
	big := func() []string {
		out := make([]string, 17)
		for i := range out {
			out[i] = "v"
		}
		return out
	}
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{
			Name:    "build",
			TaskRef: &tektontypes.TaskRef{Name: "t"},
			Matrix: &tektontypes.Matrix{Params: []tektontypes.MatrixParam{
				{Name: "a", Value: big()},
				{Name: "b", Value: big()},
			}},
		}},
	}}
	if _, err := expandMatrix(pl, nil); err == nil {
		t.Fatal("expected cardinality-cap error, got nil")
	}
}

func TestExpandMatrixEmptyValueListErrors(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{{
			Name:    "build",
			TaskRef: &tektontypes.TaskRef{Name: "t"},
			Matrix: &tektontypes.Matrix{Params: []tektontypes.MatrixParam{
				{Name: "os", Value: []string{}},
			}},
		}},
	}}
	if _, err := expandMatrix(pl, nil); err == nil {
		t.Fatal("expected empty-value error, got nil")
	}
}

func TestExpandMatrixFinallyRewrite(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Finally: []tektontypes.PipelineTask{{
			Name:    "notify",
			TaskRef: &tektontypes.TaskRef{Name: "t"},
			Matrix: &tektontypes.Matrix{Params: []tektontypes.MatrixParam{
				{Name: "channel", Value: []string{"slack", "email"}},
			}},
		}},
	}}
	got, err := expandMatrix(pl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.Finally) != 2 {
		t.Errorf("finally tasks = %d, want 2", len(got.Spec.Finally))
	}
}

func TestAggregateMatrixResults(t *testing.T) {
	results := map[string]map[string]string{
		"build-0": {"tag": "linux-1.21"},
		"build-1": {"tag": "linux-1.22"},
		"build-2": {"tag": "darwin-1.21"},
		"build-3": {"tag": "darwin-1.22"},
	}
	aggregateMatrixResults("build", []string{"build-0", "build-1", "build-2", "build-3"}, results)
	got, ok := results["build"]["tag"]
	if !ok {
		t.Fatalf("results[build][tag] missing; got %+v", results["build"])
	}
	want := `["linux-1.21","linux-1.22","darwin-1.21","darwin-1.22"]`
	if got != want {
		t.Errorf("results[build][tag] = %q, want %q", got, want)
	}
}

func TestAggregateMatrixResultsMissingExpansionEmptyString(t *testing.T) {
	results := map[string]map[string]string{
		"build-0": {"tag": "linux"},
		"build-1": {}, // no tag produced
	}
	aggregateMatrixResults("build", []string{"build-0", "build-1"}, results)
	got := results["build"]["tag"]
	if got != `["linux",""]` {
		t.Errorf("aggregated = %q, want %q", got, `["linux",""]`)
	}
}

func TestMaterializeMatrixRowsRoundTrip(t *testing.T) {
	pt := tektontypes.PipelineTask{
		Name: "build",
		Matrix: &tektontypes.Matrix{
			Params: []tektontypes.MatrixParam{
				{Name: "os", Value: []string{"linux", "darwin"}},
			},
			Include: []tektontypes.MatrixInclude{
				{Name: "arm-extra", Params: []tektontypes.Param{
					{Name: "arch", Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "arm64"}},
				}},
			},
		},
	}
	rows := MaterializeMatrixRows(pt)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (2 cross + 1 include)", len(rows))
	}
	if rows[2].IncludeName != "arm-extra" || rows[2].Params["arch"] != "arm64" {
		t.Errorf("rows[2] = %+v", rows[2])
	}
}
