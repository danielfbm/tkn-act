package resolver_test

import (
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/resolver"
)

func TestStepResultsPath(t *testing.T) {
	got, err := resolver.Substitute("write $(step.results.version.path)", resolver.Context{
		CurrentStep: "compile",
	})
	if err != nil {
		t.Fatalf("sub: %v", err)
	}
	if got != "write /tekton/steps/compile/results/version" {
		t.Errorf("got %q", got)
	}
}

func TestStepsResultLookup(t *testing.T) {
	got, err := resolver.Substitute("v=$(steps.compile.results.version)", resolver.Context{
		StepResults: map[string]map[string]string{"compile": {"version": "1.2.3"}},
	})
	if err != nil {
		t.Fatalf("sub: %v", err)
	}
	if got != "v=1.2.3" {
		t.Errorf("got %q", got)
	}
}

func TestStepsRefMissingStep(t *testing.T) {
	_, err := resolver.Substitute("$(steps.nope.results.x)", resolver.Context{
		StepResults: map[string]map[string]string{},
	})
	if err == nil || !strings.Contains(err.Error(), "no results for step") {
		t.Errorf("err = %v", err)
	}
}

// AllowStepRefs leaves the step placeholder for the docker backend.
func TestSubstituteAllowStepRefsLeavesPlaceholder(t *testing.T) {
	got, err := resolver.SubstituteAllowStepRefs("a=$(params.x) b=$(steps.s1.results.r1)", resolver.Context{
		Params: map[string]string{"x": "X"},
	})
	if err != nil {
		t.Fatalf("sub: %v", err)
	}
	if !strings.Contains(got, "a=X") {
		t.Errorf("param not substituted: %q", got)
	}
	if !strings.Contains(got, "$(steps.s1.results.r1)") {
		t.Errorf("step ref not preserved: %q", got)
	}
}

// AllowStepRefs still hard-fails on truly unknown refs.
func TestSubstituteAllowStepRefsFailsOnUnknown(t *testing.T) {
	_, err := resolver.SubstituteAllowStepRefs("$(params.missing)", resolver.Context{})
	if err == nil {
		t.Fatal("expected error for unknown param")
	}
}

// AllowStepRefs leaves $(step.results.X.path) intact when CurrentStep is empty.
func TestSubstituteAllowStepRefsLeavesStepResultsPath(t *testing.T) {
	got, err := resolver.SubstituteAllowStepRefs("$(step.results.foo.path)", resolver.Context{})
	if err != nil {
		t.Fatalf("sub: %v", err)
	}
	if got != "$(step.results.foo.path)" {
		t.Errorf("got %q", got)
	}
}

func TestSubstituteArgsAllowStepRefs(t *testing.T) {
	got, err := resolver.SubstituteArgsAllowStepRefs(
		[]string{"$(params.x)", "$(steps.s1.results.r1)"},
		resolver.Context{Params: map[string]string{"x": "X"}},
	)
	if err != nil {
		t.Fatalf("sub: %v", err)
	}
	if got[0] != "X" || got[1] != "$(steps.s1.results.r1)" {
		t.Errorf("got %v", got)
	}
}
