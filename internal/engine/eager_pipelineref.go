package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/refresolver"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// maybeResolveTopLevelPipelineRef handles spec §7's top-level
// pipelineRef.resolver case: when a PipelineRun in the input bundle
// carries `spec.pipelineRef.resolver`, we resolve the Pipeline
// synchronously at load time (before any task runs), validate the
// returned bytes, and substitute the resulting Pipeline into the
// bundle.
//
// Top-level resolution emits resolver-start / resolver-end events with
// an empty Task field, per spec §12; the consumer disambiguates
// "top-level pipelineRef resolution" from "per-task resolution" via
// the absence of the task field.
//
// Returns (resolvedPipeline, true) if a resolver-backed PipelineRun was
// found AND resolution succeeded. Returns ({}, false) if no top-level
// pipelineRef.resolver was present, OR if resolution failed (in which
// case the failure has already been emitted as a run-start / run-end
// pair on the reporter).
func maybeResolveTopLevelPipelineRef(
	ctx context.Context,
	in PipelineInput,
	registry *refresolver.Registry,
	rep reporter.Reporter,
) (tektontypes.Pipeline, bool) {
	if in.Bundle == nil {
		return tektontypes.Pipeline{}, false
	}
	// Find the first PipelineRun with a resolver-backed pipelineRef.
	// Multiple PipelineRuns with resolver-backed refs in one bundle is
	// unusual; we only handle the first match.
	var pr *tektontypes.PipelineRun
	for i := range in.Bundle.PipelineRuns {
		p := &in.Bundle.PipelineRuns[i]
		if p.Spec.PipelineRef != nil && p.Spec.PipelineRef.Resolver != "" {
			pr = p
			break
		}
	}
	if pr == nil {
		return tektontypes.Pipeline{}, false
	}

	// Top-level resolver.params can only reference run-scope: $(params.X),
	// $(context.*). Result-refs are not legal at this layer (no DAG yet).
	// We hand the substituted-or-literal params straight through.
	params := map[string]string{}
	for _, p := range pr.Spec.PipelineRef.ResolverParams {
		// Phase 1 doesn't yet substitute top-level params here (the
		// run-input scope is sparse — for the inline-stub harness used
		// by tests, params come through as literals). Phase 5 / 6 plug
		// $(params.X) substitution against pr.Spec.Params; for now we
		// emit literal strings.
		switch p.Value.Type {
		case tektontypes.ParamTypeString, "":
			params[p.Name] = p.Value.StringVal
		case tektontypes.ParamTypeArray:
			// Join with comma — same convention as lookupTaskSpecLazy.
			s := ""
			for i, item := range p.Value.ArrayVal {
				if i > 0 {
					s += ","
				}
				s += item
			}
			params[p.Name] = s
		case tektontypes.ParamTypeObject:
			// Stable key=v,k=v serialization for reproducible cache keys.
			parts := []string{}
			for k, v := range p.Value.ObjectVal {
				parts = append(parts, k+"="+v)
			}
			sortStrings(parts)
			joined := ""
			for i, s := range parts {
				if i > 0 {
					joined += ","
				}
				joined += s
			}
			params[p.Name] = joined
		}
	}

	startTime := time.Now()
	if rep != nil {
		rep.Emit(reporter.Event{
			Kind:     reporter.EvtResolverStart,
			Time:     startTime,
			Resolver: pr.Spec.PipelineRef.Resolver,
		})
	}

	if registry == nil {
		emitTopLevelResolverFailure(rep, pr.Spec.PipelineRef.Resolver, startTime,
			"resolver: no resolver registry configured (--resolver-allow / engine.Options.Refresolver)",
			refresolver.Resolved{})
		return tektontypes.Pipeline{}, false
	}

	out, err := registry.Resolve(ctx, refresolver.Request{
		Kind:     refresolver.KindPipeline,
		Resolver: pr.Spec.PipelineRef.Resolver,
		Params:   params,
	})
	if err != nil {
		emitTopLevelResolverFailure(rep, pr.Spec.PipelineRef.Resolver, startTime,
			"resolver: "+err.Error(), out)
		return tektontypes.Pipeline{}, false
	}

	bundle, err := loader.LoadBytes(out.Bytes)
	if err != nil {
		emitTopLevelResolverFailure(rep, pr.Spec.PipelineRef.Resolver, startTime,
			"resolver: decoding bytes: "+err.Error(), out)
		return tektontypes.Pipeline{}, false
	}
	if len(bundle.Pipelines) != 1 {
		emitTopLevelResolverFailure(rep, pr.Spec.PipelineRef.Resolver, startTime,
			fmt.Sprintf("resolver: expected exactly one Pipeline in resolved bytes, got %d", len(bundle.Pipelines)),
			out)
		return tektontypes.Pipeline{}, false
	}
	var pipeline tektontypes.Pipeline
	for _, p := range bundle.Pipelines {
		pipeline = p
	}

	// Merge any Tasks from the resolved bundle into the input bundle so
	// inline-task references inside the resolved Pipeline still find
	// their byNames.
	if in.Bundle.Tasks == nil {
		in.Bundle.Tasks = map[string]tektontypes.Task{}
	}
	for n, t := range bundle.Tasks {
		if _, exists := in.Bundle.Tasks[n]; !exists {
			in.Bundle.Tasks[n] = t
		}
	}

	if rep != nil {
		rep.Emit(reporter.Event{
			Kind:     reporter.EvtResolverEnd,
			Time:     time.Now(),
			Resolver: pr.Spec.PipelineRef.Resolver,
			Status:   reporter.StatusSucceeded,
			Duration: time.Since(startTime),
			SHA256:   out.SHA256,
			Source:   out.Source,
			Cached:   out.Cached,
		})
	}
	return pipeline, true
}

func emitTopLevelResolverFailure(rep reporter.Reporter, name string, started time.Time, msg string, out refresolver.Resolved) {
	if rep == nil {
		return
	}
	now := time.Now()
	rep.Emit(reporter.Event{
		Kind:     reporter.EvtResolverEnd,
		Time:     now,
		Resolver: name,
		Status:   reporter.StatusFailed,
		Duration: now.Sub(started),
		Message:  msg,
		SHA256:   out.SHA256,
		Source:   out.Source,
		Cached:   out.Cached,
	})
	rep.Emit(reporter.Event{
		Kind:    reporter.EvtRunEnd,
		Time:    time.Now(),
		Status:  "failed",
		Message: msg,
	})
}

// hasTopLevelPipelineRefResolver returns true if the bundle carries any
// PipelineRun with a resolver-backed pipelineRef. Used by RunPipeline
// to recognize the "resolution failed pre-DAG" return path: the
// run-end event was already emitted by the resolution failure handler.
func hasTopLevelPipelineRefResolver(b *loader.Bundle) bool {
	if b == nil {
		return false
	}
	for _, pr := range b.PipelineRuns {
		if pr.Spec.PipelineRef != nil && pr.Spec.PipelineRef.Resolver != "" {
			return true
		}
	}
	return false
}
