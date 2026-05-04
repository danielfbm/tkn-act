package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/refresolver"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/resolver"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/danielfbm/tkn-act/internal/validator"
)

// lookupTaskSpecLazy is the lazy-dispatch counterpart of lookupTaskSpec.
// When pt.TaskRef carries a Resolver, this function:
//
//  1. Substitutes pt.TaskRef.ResolverParams against the dispatch-time
//     resolver.Context (so `$(tasks.X.results.Y)` references the
//     accumulated upstream-task results).
//  2. Calls registry.Resolve(ctx, Request{...}) — which itself layers
//     the per-run cache (and, in later phases, the on-disk cache).
//  3. Decodes the returned bytes via loader.LoadBytes and asserts
//     exactly one Task is loaded.
//  4. Validates the resolved TaskSpec via validator.ValidateTaskSpec.
//  5. Returns the inlined TaskSpec for the rest of runOne.
//
// Spec §3 cache-key invariant: the Resolve call's Request carries the
// post-substitution params; the registry hashes those to produce the
// per-run cache key, so two PipelineTasks via the same resolver with
// resolver.params that substitute to different values yield different
// keys.
func lookupTaskSpecLazy(
	ctx context.Context,
	pt tektontypes.PipelineTask,
	rctx resolver.Context,
	registry *refresolver.Registry,
	rep reporter.Reporter,
) (tektontypes.TaskSpec, refresolver.Resolved, error) {
	if pt.TaskRef == nil || pt.TaskRef.Resolver == "" {
		return tektontypes.TaskSpec{}, refresolver.Resolved{}, fmt.Errorf("lookupTaskSpecLazy: pt %q has no resolver", pt.Name)
	}
	if registry == nil {
		return tektontypes.TaskSpec{}, refresolver.Resolved{},
			fmt.Errorf("resolver: no resolver registry configured (--resolver-allow / engine.Options.Refresolver)")
	}
	// Substitute resolver.params against the dispatch-time context.
	params := map[string]string{}
	for _, p := range pt.TaskRef.ResolverParams {
		switch p.Value.Type {
		case tektontypes.ParamTypeString, "":
			s, err := resolver.Substitute(p.Value.StringVal, rctx)
			if err != nil {
				return tektontypes.TaskSpec{}, refresolver.Resolved{},
					fmt.Errorf("resolver: substituting param %q: %w", p.Name, err)
			}
			params[p.Name] = s
		case tektontypes.ParamTypeArray:
			parts := make([]string, 0, len(p.Value.ArrayVal))
			for _, item := range p.Value.ArrayVal {
				s, err := resolver.Substitute(item, rctx)
				if err != nil {
					return tektontypes.TaskSpec{}, refresolver.Resolved{},
						fmt.Errorf("resolver: substituting array param %q: %w", p.Name, err)
				}
				parts = append(parts, s)
			}
			// Resolver params are presented to resolvers as strings.
			// We join arrays with the canonical "," separator; the
			// only resolvers that consume arrays (none in v1.6's
			// in-scope set) can split. The substituted-form is what
			// CacheKey hashes, so the join must be deterministic.
			params[p.Name] = strings.Join(parts, ",")
		case tektontypes.ParamTypeObject:
			// Same rationale: resolver-protocol params are scalar.
			// We serialize objects as sorted "k=v,k=v" so cache keys
			// are stable.
			parts := make([]string, 0, len(p.Value.ObjectVal))
			for k, item := range p.Value.ObjectVal {
				s, err := resolver.Substitute(item, rctx)
				if err != nil {
					return tektontypes.TaskSpec{}, refresolver.Resolved{},
						fmt.Errorf("resolver: substituting object param %q[%q]: %w", p.Name, k, err)
				}
				parts = append(parts, k+"="+s)
			}
			// Sort for stability.
			sortStrings(parts)
			params[p.Name] = strings.Join(parts, ",")
		}
	}

	req := refresolver.Request{
		Kind:     refresolver.KindTask,
		Resolver: pt.TaskRef.Resolver,
		Params:   params,
	}

	// Emit resolver-start.
	startTime := time.Now()
	if rep != nil {
		rep.Emit(reporter.Event{
			Kind:     reporter.EvtResolverStart,
			Time:     startTime,
			Task:     pt.Name,
			Resolver: pt.TaskRef.Resolver,
		})
	}

	out, err := registry.Resolve(ctx, req)
	dur := time.Since(startTime)
	if err != nil {
		if rep != nil {
			rep.Emit(reporter.Event{
				Kind:     reporter.EvtResolverEnd,
				Time:     time.Now(),
				Task:     pt.Name,
				Resolver: pt.TaskRef.Resolver,
				Status:   reporter.StatusFailed,
				Duration: dur,
				Message:  err.Error(),
			})
		}
		return tektontypes.TaskSpec{}, refresolver.Resolved{}, fmt.Errorf("resolver: %w", err)
	}

	// Decode the returned YAML; expect exactly one Task.
	bundle, err := loader.LoadBytes(out.Bytes)
	if err != nil {
		emitResolverEndFailure(rep, pt.Name, pt.TaskRef.Resolver, dur, "resolver: decoding bytes: "+err.Error(), out)
		return tektontypes.TaskSpec{}, refresolver.Resolved{},
			fmt.Errorf("resolver: decoding bytes: %w", err)
	}
	if len(bundle.Tasks) != 1 {
		msg := fmt.Sprintf("resolver: expected exactly one Task in resolved bytes, got %d", len(bundle.Tasks))
		emitResolverEndFailure(rep, pt.Name, pt.TaskRef.Resolver, dur, msg, out)
		return tektontypes.TaskSpec{}, refresolver.Resolved{}, fmt.Errorf("%s", msg)
	}
	var spec tektontypes.TaskSpec
	for _, t := range bundle.Tasks {
		spec = t.Spec
	}

	// Validate the resolved TaskSpec.
	if errs := validator.ValidateTaskSpec(pt.Name, spec); len(errs) > 0 {
		// Combine errors into one message.
		msgs := make([]string, 0, len(errs))
		for _, e := range errs {
			msgs = append(msgs, e.Error())
		}
		full := "resolver: validate: " + strings.Join(msgs, "; ")
		emitResolverEndFailure(rep, pt.Name, pt.TaskRef.Resolver, dur, full, out)
		return tektontypes.TaskSpec{}, refresolver.Resolved{}, fmt.Errorf("%s", full)
	}

	if rep != nil {
		rep.Emit(reporter.Event{
			Kind:     reporter.EvtResolverEnd,
			Time:     time.Now(),
			Task:     pt.Name,
			Resolver: pt.TaskRef.Resolver,
			Status:   reporter.StatusSucceeded,
			Duration: dur,
			SHA256:   out.SHA256,
			Source:   out.Source,
			Cached:   out.Cached,
		})
	}
	return spec, out, nil
}

func emitResolverEndFailure(rep reporter.Reporter, task, name string, dur time.Duration, msg string, out refresolver.Resolved) {
	if rep == nil {
		return
	}
	rep.Emit(reporter.Event{
		Kind:     reporter.EvtResolverEnd,
		Time:     time.Now(),
		Task:     task,
		Resolver: name,
		Status:   reporter.StatusFailed,
		Duration: dur,
		Message:  msg,
		SHA256:   out.SHA256,
		Source:   out.Source,
		Cached:   out.Cached,
	})
}

// sortStrings is a tiny in-place sort used by the object-param
// serialization above. Avoids pulling in sort just for one site.
func sortStrings(s []string) {
	// insertion sort is fine: object params are typically <10 keys.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
