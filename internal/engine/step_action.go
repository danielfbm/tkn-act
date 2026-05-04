package engine

import (
	"fmt"

	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/resolver"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// resolveStepActions returns a new TaskSpec where every Step that
// carries a Ref has its body replaced by the referenced StepAction's
// inlined body, with Step.Params bound against the StepAction's
// declared params (StepAction defaults applied when the caller did
// not supply a value).
//
// Identity fields (Name, OnError, VolumeMounts) are kept from the
// calling Step. Body fields (Image, Command, Args, Script, Env,
// WorkingDir, ImagePullPolicy, Resources, Results) come from the
// StepAction. The calling Step must not set any body field alongside
// Ref — that's a hard error here as well as in the validator (defense
// in depth: validator catches it pre-run; engine catches it if a
// future loader path skips validation).
//
// $(params.X) inside the StepAction body is resolved here against a
// scoped param view (StepAction declarations + caller bindings).
// Outer-scope substitutions ($(params.<task-param>),
// $(tasks.X.results.Y), $(workspaces.X.path), $(step.results.X.path),
// $(steps.X.results.Y)) are left to the existing substituteSpec /
// per-step backend passes.
func resolveStepActions(spec tektontypes.TaskSpec, b *loader.Bundle) (tektontypes.TaskSpec, error) {
	hasRef := false
	for _, s := range spec.Steps {
		if s.Ref != nil {
			hasRef = true
			break
		}
	}
	if !hasRef {
		return spec, nil
	}
	out := spec
	out.Steps = make([]tektontypes.Step, len(spec.Steps))
	for i, st := range spec.Steps {
		if st.Ref == nil {
			out.Steps[i] = st
			continue
		}
		if err := assertNoInlineBody(st); err != nil {
			return tektontypes.TaskSpec{}, err
		}
		var (
			action tektontypes.StepAction
			ok     bool
		)
		if b != nil {
			action, ok = b.StepActions[st.Ref.Name]
		}
		if !ok {
			return tektontypes.TaskSpec{}, fmt.Errorf("step %q: references unknown StepAction %q", st.Name, st.Ref.Name)
		}
		resolved, err := inlineStepAction(st, action)
		if err != nil {
			return tektontypes.TaskSpec{}, err
		}
		out.Steps[i] = resolved
	}
	return out, nil
}

// assertNoInlineBody returns an error if the Step (with Ref set) also
// carries any body field. Mirrors validator rule 13.
func assertNoInlineBody(st tektontypes.Step) error {
	if st.Image != "" || len(st.Command) > 0 || len(st.Args) > 0 ||
		st.Script != "" || len(st.Env) > 0 || st.WorkingDir != "" ||
		st.ImagePullPolicy != "" || st.Resources != nil ||
		len(st.Results) > 0 {
		return fmt.Errorf("step %q: ref and inline body are mutually exclusive", st.Name)
	}
	return nil
}

// expandBundleStepActions returns a copy of bundle.Tasks and pl with every
// Step.Ref expanded into its inlined StepAction body. Used by the
// pipeline-backend dispatch path (cluster mode) so the controller never
// sees a Ref-bearing Step. The docker per-task path doesn't need this
// because runOne already calls resolveStepActions on the per-task
// TaskSpec.
//
// Both Tasks (referenced by PipelineTask.taskRef.name) and inline
// taskSpec on PipelineTask entries (in pl.Spec.Tasks ∪ pl.Spec.Finally)
// are expanded. The original bundle and Pipeline are not mutated.
func expandBundleStepActions(b *loader.Bundle, pl tektontypes.Pipeline) (map[string]tektontypes.Task, tektontypes.Pipeline, error) {
	tasksOut := make(map[string]tektontypes.Task, len(b.Tasks))
	for name, tk := range b.Tasks {
		expanded, err := resolveStepActions(tk.Spec, b)
		if err != nil {
			return nil, tektontypes.Pipeline{}, fmt.Errorf("Task %q: %w", name, err)
		}
		tk.Spec = expanded
		tasksOut[name] = tk
	}
	// Defensive copy of the pipeline-task slices: rewriting pts[i].TaskSpec
	// would otherwise mutate the bundle's Pipeline via the shared backing
	// array.
	plOut := pl
	tasksCopy, err := expandPipelineTaskSpecs(b, append([]tektontypes.PipelineTask(nil), pl.Spec.Tasks...))
	if err != nil {
		return nil, tektontypes.Pipeline{}, err
	}
	plOut.Spec.Tasks = tasksCopy
	finallyCopy, err := expandPipelineTaskSpecs(b, append([]tektontypes.PipelineTask(nil), pl.Spec.Finally...))
	if err != nil {
		return nil, tektontypes.Pipeline{}, err
	}
	plOut.Spec.Finally = finallyCopy
	return tasksOut, plOut, nil
}

// expandPipelineTaskSpecs returns the slice with each entry's inline
// TaskSpec expanded (when set). The slice itself is a fresh copy made
// by the caller; this function mutates pts[i].TaskSpec safely.
func expandPipelineTaskSpecs(b *loader.Bundle, pts []tektontypes.PipelineTask) ([]tektontypes.PipelineTask, error) {
	for i := range pts {
		if pts[i].TaskSpec == nil {
			continue
		}
		expanded, err := resolveStepActions(*pts[i].TaskSpec, b)
		if err != nil {
			return nil, fmt.Errorf("PipelineTask %q: %w", pts[i].Name, err)
		}
		copy := expanded
		pts[i].TaskSpec = &copy
	}
	return pts, nil
}

func inlineStepAction(st tektontypes.Step, action tektontypes.StepAction) (tektontypes.Step, error) {
	// Build the scoped param view: StepAction defaults first, then
	// caller overrides. Caller values are forwarded as LITERAL
	// strings — if the caller wrote `value: $(params.repo)`, the
	// inner Context's Params["<inner>"] is the literal string
	// `$(params.repo)` (not pre-resolved). The inner pass rewrites
	// `$(params.<inner>)` → `$(params.repo)`; the OUTER substituteSpec
	// pass that runs immediately after this function (and after
	// applyStepTemplate) resolves $(params.repo) from the Task scope.
	// Pre-resolving caller values here would lose outer-scope tokens
	// like $(tasks.X.results.Y) that aren't bound at this site.
	params := map[string]string{}
	for _, decl := range action.Spec.Params {
		if decl.Default == nil {
			continue
		}
		// v1: only string defaults are honored. Array/object defaults
		// are rejected by validator rule 18; this guard is
		// defense-in-depth in case the engine is invoked without
		// validation.
		if decl.Default.Type != "" && decl.Default.Type != tektontypes.ParamTypeString {
			return tektontypes.Step{}, fmt.Errorf("step %q (StepAction %q): param %q default type %q is not supported (only string defaults)", st.Name, action.Metadata.Name, decl.Name, decl.Default.Type)
		}
		params[decl.Name] = decl.Default.StringVal
	}
	for _, p := range st.Params {
		// Forward as a literal string. Outer-scope refs survive.
		params[p.Name] = p.Value.StringVal
	}
	rctx := resolver.Context{Params: params}

	// Substitute the StepAction body against the scoped context.
	// CRITICAL: Use the AllowStepRefs variants so outer-scope tokens
	// ($(step.results.X.path), $(steps.X.results.Y), $(context.X),
	// $(tasks.X.results.Y), and outer $(params.<task-param>))
	// survive the inner pass and are resolved by the outer
	// substituteSpec pass that runs immediately after. Plain
	// resolver.Substitute would error on every one of these
	// tokens — see spec §3.3 "AllowStepRefs widening note".
	image, err := resolver.SubstituteAllowStepRefs(action.Spec.Image, rctx)
	if err != nil {
		return tektontypes.Step{}, fmt.Errorf("step %q (StepAction %q): %w", st.Name, action.Metadata.Name, err)
	}
	script, err := resolver.SubstituteAllowStepRefs(action.Spec.Script, rctx)
	if err != nil {
		return tektontypes.Step{}, fmt.Errorf("step %q (StepAction %q): %w", st.Name, action.Metadata.Name, err)
	}
	workdir, err := resolver.SubstituteAllowStepRefs(action.Spec.WorkingDir, rctx)
	if err != nil {
		return tektontypes.Step{}, fmt.Errorf("step %q (StepAction %q): %w", st.Name, action.Metadata.Name, err)
	}
	args, err := resolver.SubstituteArgsAllowStepRefs(action.Spec.Args, rctx)
	if err != nil {
		return tektontypes.Step{}, fmt.Errorf("step %q (StepAction %q): %w", st.Name, action.Metadata.Name, err)
	}
	cmd, err := resolver.SubstituteArgsAllowStepRefs(action.Spec.Command, rctx)
	if err != nil {
		return tektontypes.Step{}, fmt.Errorf("step %q (StepAction %q): %w", st.Name, action.Metadata.Name, err)
	}
	var env []tektontypes.EnvVar
	if len(action.Spec.Env) > 0 {
		env = make([]tektontypes.EnvVar, len(action.Spec.Env))
		for i, e := range action.Spec.Env {
			v, err := resolver.SubstituteAllowStepRefs(e.Value, rctx)
			if err != nil {
				return tektontypes.Step{}, fmt.Errorf("step %q (StepAction %q): %w", st.Name, action.Metadata.Name, err)
			}
			env[i] = tektontypes.EnvVar{Name: e.Name, Value: v}
		}
	}

	// VolumeMounts: union — StepAction body's mounts first, caller's
	// appended (matches Tekton; see spec §9 open-question 3).
	var mounts []tektontypes.VolumeMount
	if len(action.Spec.VolumeMounts) > 0 {
		mounts = append(mounts, action.Spec.VolumeMounts...)
	}
	if len(st.VolumeMounts) > 0 {
		mounts = append(mounts, st.VolumeMounts...)
	}

	resolved := tektontypes.Step{
		Name:            st.Name,
		OnError:         st.OnError,
		VolumeMounts:    mounts,
		Image:           image,
		Command:         cmd,
		Args:            args,
		Script:          script,
		Env:             env,
		WorkingDir:      workdir,
		ImagePullPolicy: action.Spec.ImagePullPolicy,
		Resources:       action.Spec.Resources,
		Results:         append([]tektontypes.ResultSpec(nil), action.Spec.Results...),
	}
	return resolved, nil
}
