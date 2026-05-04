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

// TestExpandBundleStepActionsRefBearingTasksAndInlineSpecs covers the
// cluster-dispatch path: every Task in bundle.Tasks AND every inline
// PipelineTask.taskSpec must come out without a `Ref:` field. Without
// this, the cluster backend submits a PipelineRun whose taskSpec.steps
// still has `ref:` set, the Tekton controller tries to look up
// `stepactions.tekton.dev/<name>` (we never apply it), and the run fails.
func TestExpandBundleStepActionsRefBearingTasksAndInlineSpecs(t *testing.T) {
	action := mkAction("greet", "alpine:3", "echo hi", nil, nil)
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		Steps: []tektontypes.Step{{Name: "g", Ref: &tektontypes.StepActionRef{Name: "greet"}}},
	}}
	tk.Metadata.Name = "t"
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Tasks: []tektontypes.PipelineTask{
			{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}},
			{Name: "b", TaskSpec: &tektontypes.TaskSpec{
				Steps: []tektontypes.Step{{Name: "g2", Ref: &tektontypes.StepActionRef{Name: "greet"}}},
			}},
		},
		Finally: []tektontypes.PipelineTask{
			{Name: "f", TaskSpec: &tektontypes.TaskSpec{
				Steps: []tektontypes.Step{{Name: "g3", Ref: &tektontypes.StepActionRef{Name: "greet"}}},
			}},
		},
	}}
	pl.Metadata.Name = "p"
	b := newBundleWithStepActions(action)
	b.Tasks["t"] = tk
	b.Pipelines["p"] = pl

	tasks, plOut, err := expandBundleStepActions(b, pl)
	if err != nil {
		t.Fatal(err)
	}
	// Bundled Task expanded.
	if got := tasks["t"].Spec.Steps[0]; got.Ref != nil || got.Image != "alpine:3" {
		t.Errorf("Tasks[t][0] = %+v (want Ref=nil, Image=alpine:3)", got)
	}
	// Inline Pipeline.spec.tasks[1].taskSpec expanded.
	if plOut.Spec.Tasks[1].TaskSpec == nil || plOut.Spec.Tasks[1].TaskSpec.Steps[0].Ref != nil {
		t.Errorf("inline taskSpec on Pipeline.spec.tasks[1] still has ref: %+v", plOut.Spec.Tasks[1].TaskSpec)
	}
	// Inline Pipeline.spec.finally[0].taskSpec expanded.
	if plOut.Spec.Finally[0].TaskSpec == nil || plOut.Spec.Finally[0].TaskSpec.Steps[0].Ref != nil {
		t.Errorf("inline taskSpec on Pipeline.spec.finally[0] still has ref: %+v", plOut.Spec.Finally[0].TaskSpec)
	}
	// Original bundle's Tasks must NOT be mutated.
	if b.Tasks["t"].Spec.Steps[0].Ref == nil {
		t.Errorf("expandBundleStepActions mutated bundle.Tasks[t]")
	}
	// Original Pipeline.spec.tasks[1].TaskSpec must NOT be mutated either —
	// the cluster path mutates a defensive copy.
	if b.Pipelines["p"].Spec.Tasks[1].TaskSpec.Steps[0].Ref == nil {
		t.Errorf("expandBundleStepActions mutated bundle.Pipelines[p].Spec.Tasks[1].TaskSpec")
	}
}

// TestResolveStepActionsRejectsArrayDefault: defense-in-depth path
// for validator rule 18 — if the engine is invoked without validation
// and a StepAction declares an array/object default, inlineStepAction
// errors out rather than silently dropping the typed value.
func TestResolveStepActionsRejectsArrayDefault(t *testing.T) {
	action := tektontypes.StepAction{
		Spec: tektontypes.StepActionSpec{
			Image:  "alpine:3",
			Script: "echo $(params.who)",
			Params: []tektontypes.ParamSpec{{
				Name:    "who",
				Default: &tektontypes.ParamValue{Type: tektontypes.ParamTypeArray, ArrayVal: []string{"a", "b"}},
			}},
		},
	}
	action.Metadata.Name = "greet"
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name: "g", Ref: &tektontypes.StepActionRef{Name: "greet"},
	}}}
	_, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err == nil || !strings.Contains(err.Error(), "default type") {
		t.Errorf("want default-type error from inlineStepAction, got %v", err)
	}
}

// TestResolveStepActionsCommandArgsAndEnvInlined: cover the full body-
// field substitution surface (Command, Args, Env) with caller-bound
// params, plus WorkingDir + ImagePullPolicy + Resources passthrough.
func TestResolveStepActionsCommandArgsAndEnvInlined(t *testing.T) {
	action := tektontypes.StepAction{
		Spec: tektontypes.StepActionSpec{
			Image:           "alpine:3",
			Command:         []string{"sh", "-c"},
			Args:            []string{"echo $(params.who)"},
			Script:          "",
			Env:             []tektontypes.EnvVar{{Name: "WHO", Value: "$(params.who)"}},
			WorkingDir:      "/work/$(params.who)",
			ImagePullPolicy: "IfNotPresent",
			Resources: &tektontypes.StepResources{
				Requests: tektontypes.ResourceList{CPU: "10m"},
			},
			Params: []tektontypes.ParamSpec{{
				Name:    "who",
				Default: &tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "world"},
			}},
		},
	}
	action.Metadata.Name = "greet"
	spec := tektontypes.TaskSpec{Steps: []tektontypes.Step{{
		Name: "g",
		Ref:  &tektontypes.StepActionRef{Name: "greet"},
		Params: []tektontypes.Param{{
			Name:  "who",
			Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "tekton"},
		}},
	}}}
	got, err := resolveStepActions(spec, newBundleWithStepActions(action))
	if err != nil {
		t.Fatal(err)
	}
	st := got.Steps[0]
	if len(st.Command) != 2 || st.Command[0] != "sh" || st.Command[1] != "-c" {
		t.Errorf("command = %v", st.Command)
	}
	if len(st.Args) != 1 || st.Args[0] != "echo tekton" {
		t.Errorf("args = %v (want [echo tekton])", st.Args)
	}
	if len(st.Env) != 1 || st.Env[0].Name != "WHO" || st.Env[0].Value != "tekton" {
		t.Errorf("env = %v", st.Env)
	}
	if st.WorkingDir != "/work/tekton" {
		t.Errorf("workingDir = %q", st.WorkingDir)
	}
	if st.ImagePullPolicy != "IfNotPresent" {
		t.Errorf("imagePullPolicy = %q", st.ImagePullPolicy)
	}
	if st.Resources == nil || st.Resources.Requests.CPU != "10m" {
		t.Errorf("resources = %+v", st.Resources)
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
