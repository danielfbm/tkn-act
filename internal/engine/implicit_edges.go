package engine

import (
	"regexp"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// taskResultRefPat matches `$(tasks.<name>.results.<...>)` — same shape
// internal/validator and internal/resolver use. Duplicated here to keep
// engine free of a validator import; the regex is small and the source
// of truth lives in the comment.
var taskResultRefPat = regexp.MustCompile(`\$\(tasks\.([a-zA-Z0-9][\w-]*)\.results\.[\w.-]+\)`)

// implicitParamEdges returns the set of upstream task names referenced
// by `$(tasks.X.results.Y)` substrings in pt's Params and (when set)
// the resolver.params on pt.TaskRef. A task that references X this way
// must run strictly after X — upstream Tekton's controller adds these
// implicit edges automatically, and tkn-act now does too.
//
// The walker scans:
//   - pt.Params: scalar StringVal, every element of ArrayVal, every
//     value of ObjectVal.
//   - pt.TaskRef.ResolverParams: same shape (scalar / array / object).
//
// Edges to nonexistent tasks are NOT silently dropped — the validator
// catches those at validate-time. This walker only emits the names; the
// caller intersects against the known-task set before calling
// g.AddEdge.
func implicitParamEdges(pt tektontypes.PipelineTask) []string {
	seen := map[string]struct{}{}
	scan := func(s string) {
		for _, m := range taskResultRefPat.FindAllStringSubmatch(s, -1) {
			seen[m[1]] = struct{}{}
		}
	}
	scanValue := func(v tektontypes.ParamValue) {
		switch v.Type {
		case tektontypes.ParamTypeArray:
			for _, item := range v.ArrayVal {
				scan(item)
			}
		case tektontypes.ParamTypeObject:
			for _, item := range v.ObjectVal {
				scan(item)
			}
		default:
			scan(v.StringVal)
		}
	}
	for _, p := range pt.Params {
		scanValue(p.Value)
	}
	if pt.TaskRef != nil {
		for _, p := range pt.TaskRef.ResolverParams {
			scanValue(p.Value)
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	return out
}
