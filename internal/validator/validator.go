// Package validator runs semantic checks on a loaded Bundle: refs resolve,
// the pipeline DAG has no cycles, workspaces are bound, params are present.
package validator

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/danielfbm/tkn-act/internal/engine/dag"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// Validate checks the named pipeline against the bundle. providedParams names
// only — values are checked elsewhere. Returns all errors found, not just the
// first.
func Validate(b *loader.Bundle, pipelineName string, providedParams map[string]bool) []error {
	var errs []error

	pl, ok := b.Pipelines[pipelineName]
	if !ok {
		return []error{fmt.Errorf("pipeline %q not found in loaded files", pipelineName)}
	}

	// 1. Validate task refs and inline taskSpecs.
	all := append([]tektontypes.PipelineTask{}, pl.Spec.Tasks...)
	all = append(all, pl.Spec.Finally...)
	resolvedTasks := map[string]tektontypes.TaskSpec{} // pipelineTaskName → resolved Task
	for _, pt := range all {
		switch {
		case pt.TaskRef != nil && pt.TaskSpec != nil:
			errs = append(errs, fmt.Errorf("pipeline task %q sets both taskRef and taskSpec", pt.Name))
		case pt.TaskRef != nil:
			t, ok := b.Tasks[pt.TaskRef.Name]
			if !ok {
				errs = append(errs, fmt.Errorf("pipeline task %q references unknown Task %q", pt.Name, pt.TaskRef.Name))
				continue
			}
			resolvedTasks[pt.Name] = t.Spec
		case pt.TaskSpec != nil:
			resolvedTasks[pt.Name] = *pt.TaskSpec
		default:
			errs = append(errs, fmt.Errorf("pipeline task %q has no taskRef or taskSpec", pt.Name))
		}
	}

	// 2. Required params declared by tasks must be bound.
	for _, pt := range all {
		spec, ok := resolvedTasks[pt.Name]
		if !ok {
			continue
		}
		bound := map[string]bool{}
		for _, p := range pt.Params {
			bound[p.Name] = true
		}
		for _, decl := range spec.Params {
			if decl.Default == nil && !bound[decl.Name] {
				errs = append(errs, fmt.Errorf("pipeline task %q missing required param %q", pt.Name, decl.Name))
			}
		}
	}

	// 3. Workspaces declared by the task must be bound by the pipeline task.
	pipelineWS := map[string]bool{}
	for _, w := range pl.Spec.Workspaces {
		pipelineWS[w.Name] = true
	}
	for _, pt := range all {
		spec, ok := resolvedTasks[pt.Name]
		if !ok {
			continue
		}
		bound := map[string]string{}
		for _, b := range pt.Workspaces {
			bound[b.Name] = b.Workspace
		}
		for _, decl := range spec.Workspaces {
			if decl.Optional {
				continue
			}
			plws, ok := bound[decl.Name]
			if !ok {
				errs = append(errs, fmt.Errorf("pipeline task %q missing workspace binding %q", pt.Name, decl.Name))
				continue
			}
			if !pipelineWS[plws] {
				errs = append(errs, fmt.Errorf("pipeline task %q binds workspace %q to undeclared pipeline workspace %q", pt.Name, decl.Name, plws))
			}
		}
	}

	// 4. DAG: cycle + unknown runAfter.
	g := dag.New()
	main := map[string]bool{}
	for _, pt := range pl.Spec.Tasks {
		g.AddNode(pt.Name)
		main[pt.Name] = true
	}
	for _, pt := range pl.Spec.Tasks {
		for _, dep := range pt.RunAfter {
			if !main[dep] {
				errs = append(errs, fmt.Errorf("pipeline task %q runAfter references unknown task %q", pt.Name, dep))
				continue
			}
			g.AddEdge(dep, pt.Name)
		}
	}
	if _, err := g.Levels(); err != nil {
		errs = append(errs, fmt.Errorf("pipeline DAG: %w", err))
	}

	// 5. Finally task names must not collide with main DAG.
	finally := map[string]bool{}
	for _, pt := range pl.Spec.Finally {
		if main[pt.Name] {
			errs = append(errs, fmt.Errorf("finally task %q collides with main task name", pt.Name))
		}
		if finally[pt.Name] {
			errs = append(errs, fmt.Errorf("duplicate finally task %q", pt.Name))
		}
		finally[pt.Name] = true
	}

	// 6. When-expression operator sanity.
	for _, pt := range all {
		for _, w := range pt.When {
			op := strings.ToLower(w.Operator)
			if op != "in" && op != "notin" {
				errs = append(errs, fmt.Errorf("pipeline task %q: unsupported when operator %q (only 'in' and 'notin')", pt.Name, w.Operator))
			}
		}
	}

	// 7. Retries must be non-negative.
	for _, pt := range all {
		if pt.Retries < 0 {
			errs = append(errs, fmt.Errorf("pipeline task %q: retries must be non-negative, got %d", pt.Name, pt.Retries))
		}
	}

	// 8. Task timeout must parse as a Go duration.
	for name, spec := range resolvedTasks {
		if spec.Timeout == "" {
			continue
		}
		if _, err := time.ParseDuration(spec.Timeout); err != nil {
			errs = append(errs, fmt.Errorf("pipeline task %q: invalid timeout %q: %v", name, spec.Timeout, err))
		}
	}

	// 8b. Pipeline-level timeouts: parseable, positive, and tasks+finally ≤ pipeline.
	if t := pl.Spec.Timeouts; t != nil {
		var pdur, tdur, fdur time.Duration
		var perr, terr, ferr error
		if t.Pipeline != "" {
			pdur, perr = parseTimeout("timeouts.pipeline", t.Pipeline)
			if perr != nil {
				errs = append(errs, perr)
			}
		}
		if t.Tasks != "" {
			tdur, terr = parseTimeout("timeouts.tasks", t.Tasks)
			if terr != nil {
				errs = append(errs, terr)
			}
		}
		if t.Finally != "" {
			fdur, ferr = parseTimeout("timeouts.finally", t.Finally)
			if ferr != nil {
				errs = append(errs, ferr)
			}
		}
		if perr == nil && terr == nil && ferr == nil &&
			pdur > 0 && tdur > 0 && fdur > 0 && tdur+fdur > pdur {
			errs = append(errs, fmt.Errorf(
				"timeouts.tasks (%s) + timeouts.finally (%s) > timeouts.pipeline (%s)",
				tdur, fdur, pdur))
		}
	}

	// 8c. Pipeline.spec.results: every $(tasks.X.results.Y) reference
	// must name a task that exists in spec.tasks ∪ spec.finally. Result-
	// name existence isn't checked here (some Tasks compute results
	// dynamically; resolution-time error handling drops unknown names
	// non-fatally).
	if len(pl.Spec.Results) > 0 {
		known := map[string]bool{}
		for _, pt := range pl.Spec.Tasks {
			known[pt.Name] = true
		}
		for _, pt := range pl.Spec.Finally {
			known[pt.Name] = true
		}
		for _, r := range pl.Spec.Results {
			collectStrings(r.Value, func(s string) {
				for _, ref := range extractTaskRefs(s) {
					if !known[ref] {
						errs = append(errs, fmt.Errorf("pipeline result %q references unknown task %q (must be in spec.tasks or spec.finally)", r.Name, ref))
					}
				}
			})
		}
	}

	// 9. Step.OnError values must be empty, "continue", or "stopAndFail".
	for taskName, spec := range resolvedTasks {
		for _, st := range spec.Steps {
			switch st.OnError {
			case "", "continue", "stopAndFail":
			default:
				errs = append(errs, fmt.Errorf("pipeline task %q step %q: unsupported onError %q (allowed: continue | stopAndFail)", taskName, st.Name, st.OnError))
			}
		}
	}

	// 10. Volume kinds: must be exactly one of emptyDir/hostPath/configMap/secret.
	for taskName, spec := range resolvedTasks {
		volNames := map[string]bool{}
		for _, v := range spec.Volumes {
			volNames[v.Name] = true
			n := 0
			if v.EmptyDir != nil {
				n++
			}
			if v.HostPath != nil {
				n++
			}
			if v.ConfigMap != nil {
				n++
			}
			if v.Secret != nil {
				n++
			}
			switch n {
			case 0:
				errs = append(errs, fmt.Errorf("pipeline task %q volume %q: unsupported volume kind (only emptyDir, hostPath, configMap, secret)", taskName, v.Name))
			case 1:
				// ok
			default:
				errs = append(errs, fmt.Errorf("pipeline task %q volume %q: multiple sources set on a single volume", taskName, v.Name))
			}
			if v.HostPath != nil && v.HostPath.Path == "" {
				errs = append(errs, fmt.Errorf("pipeline task %q volume %q: hostPath.path is required", taskName, v.Name))
			}
		}
		// 11. Every volumeMount must reference a declared volume.
		for _, st := range spec.Steps {
			for _, vm := range st.VolumeMounts {
				if !volNames[vm.Name] {
					errs = append(errs, fmt.Errorf("pipeline task %q step %q: volumeMount %q references undeclared volume", taskName, st.Name, vm.Name))
				}
				if vm.MountPath == "" {
					errs = append(errs, fmt.Errorf("pipeline task %q step %q: volumeMount %q has empty mountPath", taskName, st.Name, vm.Name))
				}
			}
		}
	}

	return errs
}

// parseTimeout parses a Tekton-style duration string and returns a
// non-zero positive duration. Empty strings should not reach this
// function. The error message includes the field name so users see
// "timeouts.pipeline: invalid duration" rather than just "invalid".
func parseTimeout(field, s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q: %w", field, s, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: must be positive (use omission to mean no budget), got %q", field, s)
	}
	return d, nil
}

// taskResultRefPat matches $(tasks.<name>.results.<anything>) — we
// only need to extract the <name> for ref validation.
var taskResultRefPat = regexp.MustCompile(`\$\(tasks\.([a-zA-Z][\w-]*)\.results\.[\w.-]+\)`)

// extractTaskRefs returns every task name referenced via
// $(tasks.X.results.Y) in s (in source order; duplicates allowed —
// the caller's known-set check is set-based anyway).
func extractTaskRefs(s string) []string {
	matches := taskResultRefPat.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// collectStrings calls fn once per string atom in v. For string-typed
// values that's the single StringVal; for array-typed, each element;
// for object-typed, each map value.
func collectStrings(v tektontypes.ParamValue, fn func(string)) {
	switch v.Type {
	case tektontypes.ParamTypeArray:
		for _, item := range v.ArrayVal {
			fn(item)
		}
	case tektontypes.ParamTypeObject:
		for _, item := range v.ObjectVal {
			fn(item)
		}
	default:
		fn(v.StringVal)
	}
}
