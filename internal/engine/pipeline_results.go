package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/danielfbm/tkn-act/internal/resolver"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// resolvePipelineResults evaluates Pipeline.spec.results once every task
// (including finally) is terminal. It returns the map of resolved
// values keyed by result name, plus a slice of per-result errors for
// entries that had to be dropped (referenced task didn't produce the
// result, expression couldn't resolve, etc.). Drops are not fatal —
// the caller emits each as an EvtError but does not change run status
// or exit code.
//
// Value shape mirrors ParamValue:
//   - ParamTypeString → string
//   - ParamTypeArray  → []string
//   - ParamTypeObject → map[string]string
//
// Returns (nil, nil) when the pipeline declared no results.
func resolvePipelineResults(pl tektontypes.Pipeline, results map[string]map[string]string) (map[string]any, []error) {
	if len(pl.Spec.Results) == 0 {
		return nil, nil
	}
	ctx := resolver.Context{
		// Pipeline results only ever reference task results — no params,
		// no context vars, no workspaces. Keeping the context narrow
		// guarantees a cleaner failure message if someone tries to use
		// other refs ("unknown reference $(params.x)" rather than a
		// silent miss).
		Results: results,
	}
	out := map[string]any{}
	var errs []error
	for _, spec := range pl.Spec.Results {
		switch spec.Value.Type {
		case tektontypes.ParamTypeString, "":
			s, err := resolver.Substitute(spec.Value.StringVal, ctx)
			if err != nil {
				errs = append(errs, fmt.Errorf("pipeline result %q dropped: %w", spec.Name, err))
				continue
			}
			out[spec.Name] = s
		case tektontypes.ParamTypeArray:
			items := make([]string, 0, len(spec.Value.ArrayVal))
			dropped := false
			for _, item := range spec.Value.ArrayVal {
				// Sole $(...[*]) reference for the entire element →
				// splice via the array-aware path so the result lands
				// as []string (matrix-fanned task results, array
				// params). Mixed text + [*] would be an upstream
				// error; fall through to scalar substitute and let
				// it surface.
				if isSoleArrayStarRef(item) {
					arr, err := resolver.SubstituteArgs([]string{item}, ctx)
					if err != nil {
						errs = append(errs, fmt.Errorf("pipeline result %q dropped: %w", spec.Name, err))
						dropped = true
						break
					}
					items = append(items, arr...)
					continue
				}
				s, err := resolver.Substitute(item, ctx)
				if err != nil {
					errs = append(errs, fmt.Errorf("pipeline result %q dropped: %w", spec.Name, err))
					dropped = true
					break
				}
				items = append(items, s)
			}
			if !dropped {
				out[spec.Name] = items
			}
		case tektontypes.ParamTypeObject:
			// Sort the object's keys before iterating: Go map iteration
			// is randomised, and we surface drops via EvtError — drop
			// order must be deterministic across runs so log diffs and
			// test assertions stay stable.
			keys := make([]string, 0, len(spec.Value.ObjectVal))
			for k := range spec.Value.ObjectVal {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			obj := make(map[string]string, len(spec.Value.ObjectVal))
			dropped := false
			for _, k := range keys {
				s, err := resolver.Substitute(spec.Value.ObjectVal[k], ctx)
				if err != nil {
					errs = append(errs, fmt.Errorf("pipeline result %q dropped: %w", spec.Name, err))
					dropped = true
					break
				}
				obj[k] = s
			}
			if !dropped {
				out[spec.Name] = obj
			}
		default:
			errs = append(errs, fmt.Errorf("pipeline result %q dropped: unknown value type %q", spec.Name, spec.Value.Type))
		}
	}
	return out, errs
}

// isSoleArrayStarRef reports whether s is exactly a single `$(...[*])`
// reference — no surrounding text. The resolver's array-splice path
// only runs in this case; mixed `$(x[*])-suffix` falls through to
// scalar substitution.
func isSoleArrayStarRef(s string) bool {
	if !strings.HasPrefix(s, "$(") || !strings.HasSuffix(s, "[*])") {
		return false
	}
	// Reject `$(a)$(b[*])` and similar by ensuring there's only one
	// closing paren (the one at the end).
	if strings.Count(s, ")") != 1 {
		return false
	}
	return true
}

// droppedClusterResultNames returns the names declared in
// pl.Spec.Results that are NOT present in produced. The cluster
// backend reads pr.status.results from the Tekton controller's
// verdict; any declared result Tekton didn't emit is "dropped" from
// the engine's perspective. The slice is sorted for stable EvtError
// ordering — log diffs and test assertions stay deterministic.
//
// Returns nil when nothing was declared, or when produced covers every
// declared name.
func droppedClusterResultNames(pl tektontypes.Pipeline, produced map[string]any) []string {
	if len(pl.Spec.Results) == 0 {
		return nil
	}
	var dropped []string
	for _, spec := range pl.Spec.Results {
		if _, ok := produced[spec.Name]; !ok {
			dropped = append(dropped, spec.Name)
		}
	}
	if len(dropped) == 0 {
		return nil
	}
	sort.Strings(dropped)
	return dropped
}
