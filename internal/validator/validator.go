// Package validator runs semantic checks on a loaded Bundle: refs resolve,
// the pipeline DAG has no cycles, workspaces are bound, params are present.
package validator

import (
	"fmt"
	"strings"

	"github.com/dfbmorinigo/tkn-act/internal/engine/dag"
	"github.com/dfbmorinigo/tkn-act/internal/loader"
	"github.com/dfbmorinigo/tkn-act/internal/tektontypes"
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

	return errs
}
