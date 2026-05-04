package engine

import (
	"reflect"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

func newBundleWithStepActions(actions ...tektontypes.StepAction) *loader.Bundle {
	b := &loader.Bundle{
		Tasks:       map[string]tektontypes.Task{},
		Pipelines:   map[string]tektontypes.Pipeline{},
		StepActions: map[string]tektontypes.StepAction{},
	}
	for _, a := range actions {
		b.StepActions[a.Metadata.Name] = a
	}
	return b
}

func mkAction(name, image, script string, params []tektontypes.ParamSpec, results []tektontypes.ResultSpec) tektontypes.StepAction {
	a := tektontypes.StepAction{
		Spec: tektontypes.StepActionSpec{Image: image, Script: script, Params: params, Results: results},
	}
	a.Metadata.Name = name
	a.APIVersion = "tekton.dev/v1beta1"
	a.Kind = "StepAction"
	return a
}

func TestResolveStepActionsNoOpWhenNoRefs(t *testing.T) {
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "a", Image: "alpine:3", Script: "echo hi"}}}
	got, err := resolveStepActions(spec, newBundleWithStepActions())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, spec) {
		t.Errorf("no-op got %+v", got)
	}
}

func TestResolveStepActionsInlinesBody(t *testing.T) {
	action := mkAction("greet", "alpine:3", "echo hello $(params.who)",
		[]tektontypes.ParamSpec{{Name: "who", Default: &tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "world"}}},
		[]tektontypes.ResultSpec{{Name: "greeting"}})
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name: "g",
		Ref:  &tektontypes.StepActionRef{Name: "greet"},
	}}}
	got, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err != nil {
		t.Fatal(err)
	}
	st := got.Steps[0]
	if st.Name != "g" {
		t.Errorf("name = %q (must be from caller)", st.Name)
	}
	if st.Image != "alpine:3" {
		t.Errorf("image = %q (must be from action)", st.Image)
	}
	if st.Script != "echo hello world" {
		t.Errorf("script = %q (default `who=world` should be applied)", st.Script)
	}
	if len(st.Results) != 1 || st.Results[0].Name != "greeting" {
		t.Errorf("results = %+v (must be carried from action)", st.Results)
	}
	if st.Ref != nil {
		t.Errorf("ref must be cleared after expansion")
	}
}

func TestResolveStepActionsAppliesCallerParams(t *testing.T) {
	action := mkAction("greet", "alpine:3", "echo hello $(params.who)",
		[]tektontypes.ParamSpec{{Name: "who", Default: &tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "world"}}},
		nil)
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name:   "g",
		Ref:    &tektontypes.StepActionRef{Name: "greet"},
		Params: []tektontypes.Param{{Name: "who", Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "tekton"}}},
	}}}
	got, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err != nil {
		t.Fatal(err)
	}
	if got.Steps[0].Script != "echo hello tekton" {
		t.Errorf("script = %q (caller param should have overridden default)", got.Steps[0].Script)
	}
}

// resolveStepActions itself only enforces ref-vs-inline + ref-exists;
// missing required params is the validator's job (rule 14). With the
// widened SubstituteAllowStepRefs, an unbound $(params.who) is DEFERRED
// (left intact) rather than erroring at the inner pass.
func TestResolveStepActionsRequiredParamMissingDefersToOuter(t *testing.T) {
	action := mkAction("greet", "alpine:3", "echo $(params.who)",
		[]tektontypes.ParamSpec{{Name: "who"}}, // no default
		nil)
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name: "g",
		Ref:  &tektontypes.StepActionRef{Name: "greet"},
	}}}
	got, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err != nil {
		t.Fatalf("inner pass should defer, not error: %v", err)
	}
	if got.Steps[0].Script != "echo $(params.who)" {
		t.Errorf("script = %q, want literal-deferred `echo $(params.who)`", got.Steps[0].Script)
	}
}

func TestResolveStepActionsUnknownRefError(t *testing.T) {
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "g", Ref: &tektontypes.StepActionRef{Name: "nope"}}}}
	_, err := resolveStepActions(spec, newBundleWithStepActions())
	if err == nil {
		t.Fatal("want unknown-ref error, got nil")
	}
}

func TestResolveStepActionsRefAndInlineRejected(t *testing.T) {
	action := mkAction("greet", "alpine:3", "echo hi", nil, nil)
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name:   "g",
		Ref:    &tektontypes.StepActionRef{Name: "greet"},
		Image:  "busybox", // illegal: can't set image alongside ref
		Script: "echo override",
	}}}
	_, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err == nil {
		t.Fatal("want ref+inline-rejected error, got nil")
	}
}

func TestResolveStepActionsPreservesIdentityFields(t *testing.T) {
	action := mkAction("greet", "alpine:3", "echo hi", nil, nil)
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name:    "g",
		Ref:     &tektontypes.StepActionRef{Name: "greet"},
		OnError: "continue",
	}}}
	got, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err != nil {
		t.Fatal(err)
	}
	if got.Steps[0].OnError != "continue" {
		t.Errorf("onError = %q (must be preserved from caller)", got.Steps[0].OnError)
	}
}

func TestResolveStepActionsDoesNotMutateInput(t *testing.T) {
	action := mkAction("greet", "alpine:3", "echo hi", nil, nil)
	original := tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "g", Ref: &tektontypes.StepActionRef{Name: "greet"}}}}
	_, err := resolveStepActions(original, newBundleWithStepActions(action))
	if err != nil {
		t.Fatal(err)
	}
	if original.Steps[0].Ref == nil || original.Steps[0].Image != "" {
		t.Errorf("input mutated: %+v", original.Steps[0])
	}
}

// TestResolveStepActionsLeavesOuterRefsIntact: Critical-1 fix.
// The inner pass MUST leave every outer-scope token verbatim so the
// outer substituteSpec can resolve them. If this test ever goes red,
// the production code regressed away from SubstituteAllowStepRefs to
// plain Substitute (which errors on unknown $(...) refs).
func TestResolveStepActionsLeavesOuterRefsIntact(t *testing.T) {
	body := strings.Join([]string{
		"step=$(step.results.greeting.path)",
		"prev=$(steps.prev.results.foo)",
		"trun=$(context.taskRun.name)",
		"chk=$(tasks.checkout.results.commit)",
		"outer=$(params.outerOnly)",
		"inner=$(params.who)",
	}, " ")
	action := mkAction("greet", "alpine:3", body,
		[]tektontypes.ParamSpec{{Name: "who", Default: &tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "world"}}},
		[]tektontypes.ResultSpec{{Name: "greeting"}})
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name: "g", Ref: &tektontypes.StepActionRef{Name: "greet"},
	}}}
	got, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err != nil {
		t.Fatalf("inner pass errored on outer-scope tokens: %v", err)
	}
	out := got.Steps[0].Script
	for _, must := range []string{
		"$(step.results.greeting.path)",
		"$(steps.prev.results.foo)",
		"$(context.taskRun.name)",
		"$(tasks.checkout.results.commit)",
		"$(params.outerOnly)",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("outer token %q was rewritten or dropped; got script: %q", must, out)
		}
	}
	// And the inner-scope $(params.who) was resolved:
	if !strings.Contains(out, "inner=world") {
		t.Errorf("inner $(params.who) not resolved; got script: %q", out)
	}
}

// TestResolveStepActionsForwardsOuterParamRefAsLiteral: Critical-2 fix.
// A caller writes `params: [{name: who, value: $(params.repo)}]`. The
// inner Context's Params["who"] must contain the LITERAL string
// $(params.repo) (not pre-resolved). The inner pass rewrites
// $(params.who) in the body to $(params.repo). The outer pass (not
// exercised here) then resolves $(params.repo) from the Task scope.
func TestResolveStepActionsForwardsOuterParamRefAsLiteral(t *testing.T) {
	action := mkAction("greet", "alpine:3", "echo $(params.who)",
		[]tektontypes.ParamSpec{{Name: "who"}}, // required, no default
		nil)
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name: "g",
		Ref:  &tektontypes.StepActionRef{Name: "greet"},
		Params: []tektontypes.Param{{
			Name:  "who",
			Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "$(params.repo)"},
		}},
	}}}
	got, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err != nil {
		t.Fatal(err)
	}
	if got.Steps[0].Script != "echo $(params.repo)" {
		t.Errorf("script = %q, want literal `echo $(params.repo)` (outer ref must be forwarded as literal)", got.Steps[0].Script)
	}
}

// TestResolveStepActionsTwoStepsSameAction: Important-6 fix.
// Two Steps in the same Task referencing the same StepAction with
// different param values produce two distinct inlined Steps, each
// keyed on the calling Step's name (not the StepAction's name) so
// per-step results dirs don't collide.
func TestResolveStepActionsTwoStepsSameAction(t *testing.T) {
	action := mkAction("git-clone", "alpine/git", "echo cloning $(params.url)",
		[]tektontypes.ParamSpec{{Name: "url"}},
		[]tektontypes.ResultSpec{{Name: "commit"}})
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{
		{
			Name: "clone1", Ref: &tektontypes.StepActionRef{Name: "git-clone"},
			Params: []tektontypes.Param{{Name: "url", Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "https://example.com/a"}}},
		},
		{
			Name: "clone2", Ref: &tektontypes.StepActionRef{Name: "git-clone"},
			Params: []tektontypes.Param{{Name: "url", Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "https://example.com/b"}}},
		},
	}}
	got, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(got.Steps))
	}
	if got.Steps[0].Name != "clone1" || got.Steps[1].Name != "clone2" {
		t.Errorf("step names = %q,%q (must be from caller)", got.Steps[0].Name, got.Steps[1].Name)
	}
	if got.Steps[0].Script != "echo cloning https://example.com/a" {
		t.Errorf("step[0].script = %q (param a should win)", got.Steps[0].Script)
	}
	if got.Steps[1].Script != "echo cloning https://example.com/b" {
		t.Errorf("step[1].script = %q (param b should win)", got.Steps[1].Script)
	}
	if len(got.Steps[0].Results) != 1 || got.Steps[0].Results[0].Name != "commit" {
		t.Errorf("step[0].Results = %+v", got.Steps[0].Results)
	}
	if len(got.Steps[1].Results) != 1 || got.Steps[1].Results[0].Name != "commit" {
		t.Errorf("step[1].Results = %+v", got.Steps[1].Results)
	}
	if &got.Steps[0].Results[0] == &got.Steps[1].Results[0] {
		t.Errorf("Results slices are aliased; must be independent copies")
	}
}

// TestResolveStepActionsVolumeMountsUnion: Important-7 decision.
// StepAction body's volumeMounts come first, caller's appended (matches
// Tekton).
func TestResolveStepActionsVolumeMountsUnion(t *testing.T) {
	action := tektontypes.StepAction{
		Spec: tektontypes.StepActionSpec{
			Image:        "alpine:3",
			Script:       "echo hi",
			VolumeMounts: []tektontypes.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
		},
	}
	action.Metadata.Name = "greet"
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name:         "g",
		Ref:          &tektontypes.StepActionRef{Name: "greet"},
		VolumeMounts: []tektontypes.VolumeMount{{Name: "cache", MountPath: "/cache"}},
	}}}
	got, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Steps[0].VolumeMounts) != 2 {
		t.Fatalf("mounts = %+v, want 2", got.Steps[0].VolumeMounts)
	}
	if got.Steps[0].VolumeMounts[0].Name != "tmp" || got.Steps[0].VolumeMounts[1].Name != "cache" {
		t.Errorf("mount order = %+v, want [tmp, cache] (action then caller)", got.Steps[0].VolumeMounts)
	}
}
