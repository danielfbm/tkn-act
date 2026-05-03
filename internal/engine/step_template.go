package engine

import "github.com/danielfbm/tkn-act/internal/tektontypes"

// applyStepTemplate returns a new TaskSpec where each Step has fields
// inherited from spec.StepTemplate when the Step itself didn't set
// them. Returns spec unchanged when StepTemplate is nil. Never
// mutates the input — copies slices before merging.
//
// Merge rules (mirror Tekton v1):
//   - Scalars (image, workingDir, imagePullPolicy): Step value wins if non-empty
//   - Slices (command, args): Step value wins as a whole if non-empty;
//     no element-wise merge
//   - Env: union by Name; Step entry wins; order is template-then-step-only
//   - Resources: Step value wins (replace); no deep merge of limits/requests
//
// Fields that are intrinsically per-Step (Name, Script, VolumeMounts,
// Results, OnError) are not touched.
func applyStepTemplate(spec tektontypes.TaskSpec) tektontypes.TaskSpec {
	if spec.StepTemplate == nil {
		return spec
	}
	t := spec.StepTemplate
	out := spec
	out.Steps = make([]tektontypes.Step, len(spec.Steps))
	for i, st := range spec.Steps {
		ns := st
		if ns.Image == "" {
			ns.Image = t.Image
		}
		if ns.WorkingDir == "" {
			ns.WorkingDir = t.WorkingDir
		}
		if ns.ImagePullPolicy == "" {
			ns.ImagePullPolicy = t.ImagePullPolicy
		}
		if len(ns.Command) == 0 && len(t.Command) > 0 {
			ns.Command = append([]string(nil), t.Command...)
		}
		if len(ns.Args) == 0 && len(t.Args) > 0 {
			ns.Args = append([]string(nil), t.Args...)
		}
		if ns.Resources == nil && t.Resources != nil {
			r := *t.Resources
			ns.Resources = &r
		}
		ns.Env = mergeEnv(t.Env, st.Env)
		out.Steps[i] = ns
	}
	return out
}

// mergeEnv unions tmpl and step envs by Name. Step entries override
// template entries with the same name. Order: every template entry in
// template order (with the value swapped if the step overrode it),
// followed by step-only entries in step order.
func mergeEnv(tmpl, step []tektontypes.EnvVar) []tektontypes.EnvVar {
	if len(tmpl) == 0 && len(step) == 0 {
		return nil
	}
	stepIdx := map[string]int{}
	for i, e := range step {
		stepIdx[e.Name] = i
	}
	out := make([]tektontypes.EnvVar, 0, len(tmpl)+len(step))
	emitted := map[string]bool{}
	for _, e := range tmpl {
		if i, ok := stepIdx[e.Name]; ok {
			out = append(out, step[i])
		} else {
			out = append(out, e)
		}
		emitted[e.Name] = true
	}
	for _, e := range step {
		if !emitted[e.Name] {
			out = append(out, e)
		}
	}
	return out
}
