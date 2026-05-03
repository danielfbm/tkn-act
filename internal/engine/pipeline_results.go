package engine

import (
	"fmt"

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
			obj := make(map[string]string, len(spec.Value.ObjectVal))
			dropped := false
			for k, v := range spec.Value.ObjectVal {
				s, err := resolver.Substitute(v, ctx)
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
