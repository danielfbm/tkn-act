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

// AllowStepRefs hard-fails on syntactically-malformed refs (no known
// scope prefix). Known-scope-but-unbound names are deferred (see
// TestSubstituteAllowStepRefsLeavesEveryOuterScope).
func TestSubstituteAllowStepRefsFailsOnUnknown(t *testing.T) {
	_, err := resolver.SubstituteAllowStepRefs("$(notascope.foo)", resolver.Context{})
	if err == nil {
		t.Fatal("expected error for malformed reference")
	}
}

// TestSubstituteAllowStepRefsLeavesEveryOuterScope pins the spec's widening
// of SubstituteAllowStepRefs: every scope the engine.resolveStepActions
// inner pass might encounter for a name it doesn't own (params from outer
// task scope, context vars, cross-task results) must be LEFT INTACT
// (deferred) so the outer substituteSpec pass sees the literal $(...) token
// and resolves it under the full Task scope. Known inner params still
// resolve.
//
// Note workspaces.<name>.path is NOT deferred — it resolves to the same
// literal path (/workspace/<name>) at every layer, so leaving it for the
// outer pass would be a no-op.
func TestSubstituteAllowStepRefsLeavesEveryOuterScope(t *testing.T) {
	ctx := resolver.Context{Params: map[string]string{"who": "tekton"}}
	cases := []string{
		"$(params.repo)",                   // outer task-scope param
		"$(context.taskRun.name)",          // outer context var
		"$(tasks.checkout.results.commit)", // cross-task result
		"$(step.results.greeting.path)",    // per-step results path (existing)
		"$(steps.prev.results.foo)",        // earlier-step result (existing)
	}
	for _, in := range cases {
		got, err := resolver.SubstituteAllowStepRefs(in, ctx)
		if err != nil {
			t.Errorf("%s: err = %v (want deferred, no error)", in, err)
			continue
		}
		if got != in {
			t.Errorf("%s: rewrote to %q (must survive verbatim)", in, got)
		}
	}
	// And a known inner param is still resolved.
	got, err := resolver.SubstituteAllowStepRefs("hi $(params.who)", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hi tekton" {
		t.Errorf("got %q, want %q", got, "hi tekton")
	}
}

// TestSubstituteAllowStepRefsResolvesWorkspaceImmediately pins the
// non-deferral of $(workspaces.X.path): both the inner StepAction pass
// and the outer engine pass synthesize /workspace/<name> the same way,
// so the inner pass resolves it eagerly. (If a future workspace
// rebinding mechanism makes the path layer-dependent, switch this to
// deferral and update the spec.)
func TestSubstituteAllowStepRefsResolvesWorkspaceImmediately(t *testing.T) {
	got, err := resolver.SubstituteAllowStepRefs("$(workspaces.source.path)", resolver.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "/workspace/source" {
		t.Errorf("got %q, want /workspace/source", got)
	}
}

// Plain Substitute keeps strict error-on-unknown semantics — only the
// AllowStepRefs path was widened. Catches outer-pass typos.
func TestSubstituteStrictUnknownParamErrors(t *testing.T) {
	_, err := resolver.Substitute("$(params.missing)", resolver.Context{})
	if err == nil {
		t.Fatal("Substitute (strict) must still error on unknown param")
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
