package engine

import (
	"context"

	"github.com/danielfbm/tkn-act/internal/refresolver"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/resolver"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// inlineResolverBackedTasks walks pl.Spec.Tasks ∪ pl.Spec.Finally and
// replaces each resolver-backed TaskRef with an inlined TaskSpec. Used
// only by the cluster backend path (runViaPipelineBackend) — the
// docker path uses lookupTaskSpecLazy directly per-task instead.
//
// Phase 1 limitation: this helper handles resolver.params that
// reference run-scope only ($(params.X), $(context.*)). Pipelines
// whose resolver.params reference upstream task results
// ($(tasks.X.results.Y)) need per-level submission (each level's
// substitution sees prior levels' results). That extension is tracked
// for Phase 2+; the helper does NOT silently drop upstream-result
// references — it lets resolver.Substitute fail with the usual
// "no results for task X" error, and the failure short-circuits the
// run. The validator (Task 8) catches this case at validate-time.
func inlineResolverBackedTasks(
	ctx context.Context,
	pl *tektontypes.Pipeline,
	rctx resolver.Context,
	registry *refresolver.Registry,
	rep reporter.Reporter,
) error {
	if pl == nil {
		return nil
	}
	for i := range pl.Spec.Tasks {
		if err := inlineOnePipelineTask(ctx, &pl.Spec.Tasks[i], rctx, registry, rep); err != nil {
			return err
		}
	}
	for i := range pl.Spec.Finally {
		if err := inlineOnePipelineTask(ctx, &pl.Spec.Finally[i], rctx, registry, rep); err != nil {
			return err
		}
	}
	return nil
}

func inlineOnePipelineTask(
	ctx context.Context,
	pt *tektontypes.PipelineTask,
	rctx resolver.Context,
	registry *refresolver.Registry,
	rep reporter.Reporter,
) error {
	if pt.TaskRef == nil || pt.TaskRef.Resolver == "" {
		return nil
	}
	spec, _, err := lookupTaskSpecLazy(ctx, *pt, rctx, registry, rep)
	if err != nil {
		return err
	}
	specCopy := spec
	pt.TaskSpec = &specCopy
	pt.TaskRef = nil
	return nil
}
