package engine

import (
	"regexp"
	"sort"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// taskResultRefPat matches `$(tasks.<name>.results.<...>)` — same shape
// internal/validator and internal/resolver use — including the
// array-index (`[N]`) and whole-array (`[*]`) suffixes. Only the task
// name is captured; the engine needs nothing else for scheduling.
// Duplicated here to keep engine free of a validator import; the regex
// is small and the source of truth lives in the comment.
var taskResultRefPat = regexp.MustCompile(`\$\(tasks\.([a-zA-Z0-9][\w-]*)\.results\.[\w.-]+(?:\[(?:\*|\d+)\])?\)`)

// implicitParamEdges returns the set of upstream task names referenced
// by `$(tasks.X.results.Y)` substrings in pt's Params, `when`
// expressions, and (when set) the resolver.params on pt.TaskRef. A task
// that references X this way must run strictly after X — upstream
// Tekton's controller adds these implicit edges automatically, and
// tkn-act does too.
//
// The walker scans:
//   - pt.Params: scalar StringVal, every element of ArrayVal, every
//     value of ObjectVal.
//   - pt.When: every expression's Input and each of its Values.
//   - pt.TaskRef.ResolverParams: same shape (scalar / array / object).
//
// The returned names are the names *as written* — a reference to a
// matrix-fanned parent returns the parent name; callers map it onto the
// expansions with expandRefNames. Edges to nonexistent tasks are NOT
// silently dropped — the validator catches those at validate-time.
// This walker only emits the names (sorted, for determinism); the
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
	for _, w := range pt.When {
		scan(w.Input)
		for _, v := range w.Values {
			scan(v)
		}
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
	sort.Strings(out)
	return out
}

// matrixExpansions maps every matrix-fanned parent name in pts to its
// expansion names (row order). Non-matrix tasks are absent.
func matrixExpansions(pts ...[]tektontypes.PipelineTask) map[string][]string {
	out := map[string][]string{}
	for _, list := range pts {
		for _, pt := range list {
			if pt.MatrixInfo != nil {
				out[pt.MatrixInfo.Parent] = append(out[pt.MatrixInfo.Parent], pt.Name)
			}
		}
	}
	return out
}

// expandRefNames maps a referenced task name onto the concrete
// PipelineTask names that produce it: the expansions for a
// matrix-fanned parent, otherwise the name itself.
func expandRefNames(name string, exp map[string][]string) []string {
	if names, ok := exp[name]; ok {
		return names
	}
	return []string{name}
}
