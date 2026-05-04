package engine

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// matrixMaxRows caps the total expansions per matrix (cross-product
// rows + include rows). Hardcoded to prevent foot-guns; if a real
// pipeline genuinely needs more, split the pipeline or run with
// --cluster.
const matrixMaxRows = 256

// MatrixInfo is re-exported from tektontypes so callers in the
// engine package can refer to it as engine.MatrixInfo. The single
// type lives in tektontypes to avoid an engine→tektontypes import
// cycle when the cluster backend reconstructs it from a TaskRun.
type MatrixInfo = tektontypes.MatrixInfo

// expandMatrix replaces every PipelineTask in pl.Spec.Tasks and
// pl.Spec.Finally that has Matrix != nil with N expansion children,
// rewriting downstream RunAfter edges so anything that referenced
// the original name now waits on every expansion. Returns pl
// unchanged when no task has a matrix.
//
// The unused `params` argument reserves a hook for future support
// of $(params.X) in matrix.params[].value entries (Tekton allows
// this); v1 takes literal string lists only.
func expandMatrix(pl tektontypes.Pipeline, _ map[string]tektontypes.ParamValue) (tektontypes.Pipeline, error) {
	if !hasAnyMatrix(pl) {
		return pl, nil
	}

	rewriteMap := map[string][]string{} // original name → expansion names
	newTasks, err := expandList(pl.Spec.Tasks, rewriteMap)
	if err != nil {
		return tektontypes.Pipeline{}, err
	}
	newFinally, err := expandList(pl.Spec.Finally, rewriteMap)
	if err != nil {
		return tektontypes.Pipeline{}, err
	}

	for i := range newTasks {
		newTasks[i].RunAfter = rewriteRunAfter(newTasks[i].RunAfter, rewriteMap)
	}
	for i := range newFinally {
		newFinally[i].RunAfter = rewriteRunAfter(newFinally[i].RunAfter, rewriteMap)
	}

	out := pl
	out.Spec.Tasks = newTasks
	out.Spec.Finally = newFinally
	return out, nil
}

func hasAnyMatrix(pl tektontypes.Pipeline) bool {
	for _, pt := range pl.Spec.Tasks {
		if pt.Matrix != nil {
			return true
		}
	}
	for _, pt := range pl.Spec.Finally {
		if pt.Matrix != nil {
			return true
		}
	}
	return false
}

func expandList(in []tektontypes.PipelineTask, rewrite map[string][]string) ([]tektontypes.PipelineTask, error) {
	var out []tektontypes.PipelineTask
	for _, pt := range in {
		if pt.Matrix == nil {
			out = append(out, pt)
			continue
		}
		rows, err := materializeRows(pt)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(rows))
		for i, row := range rows {
			child := pt
			child.Matrix = nil
			child.Name = rowName(pt.Name, i, row.includeName)
			child.Params = mergeParams(pt.Params, row.params)
			child.MatrixInfo = &tektontypes.MatrixInfo{
				Parent:      pt.Name,
				Index:       i,
				Of:          len(rows),
				Params:      rowParamMap(row.params),
				IncludeName: row.includeName,
			}
			out = append(out, child)
			names = append(names, child.Name)
		}
		rewrite[pt.Name] = names
	}
	return out, nil
}

// matrixRow is one materialized expansion: the params it contributes
// (a list of tektontypes.Param so we can preserve declaration order)
// and an optional include-row name (empty for cross-product rows).
type matrixRow struct {
	params      []tektontypes.Param
	includeName string
}

// materializeRows builds the cross-product followed by include rows.
// Cross-product order: outermost param (matrix.params[0]) iterates
// slowest, so for params [os=[linux,darwin], goversion=[1.21,1.22]]
// the order is (linux,1.21), (linux,1.22), (darwin,1.21), (darwin,1.22).
func materializeRows(pt tektontypes.PipelineTask) ([]matrixRow, error) {
	m := pt.Matrix
	for _, mp := range m.Params {
		if len(mp.Value) == 0 {
			return nil, fmt.Errorf("pipeline task %q matrix param %q must be a non-empty string list", pt.Name, mp.Name)
		}
	}
	cross := 0
	if len(m.Params) > 0 {
		cross = 1
		for _, mp := range m.Params {
			cross *= len(mp.Value)
		}
	}
	total := cross + len(m.Include)
	if total > matrixMaxRows {
		return nil, fmt.Errorf("pipeline task %q matrix would produce %d rows, exceeding the cap of %d", pt.Name, total, matrixMaxRows)
	}

	var rows []matrixRow
	if cross > 0 {
		idxs := make([]int, len(m.Params))
		for {
			ps := make([]tektontypes.Param, 0, len(m.Params))
			for i, mp := range m.Params {
				ps = append(ps, tektontypes.Param{
					Name:  mp.Name,
					Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: mp.Value[idxs[i]]},
				})
			}
			rows = append(rows, matrixRow{params: ps})
			done := true
			for i := len(idxs) - 1; i >= 0; i-- {
				idxs[i]++
				if idxs[i] < len(m.Params[i].Value) {
					done = false
					break
				}
				idxs[i] = 0
			}
			if done {
				break
			}
		}
	}
	for _, inc := range m.Include {
		rows = append(rows, matrixRow{params: inc.Params, includeName: inc.Name})
	}
	return rows, nil
}

// rowName returns parent-i for cross-product rows and the include
// row's declared name when set. Unnamed include rows use parent-i
// where i continues past the cross-product.
func rowName(parent string, idx int, includeName string) string {
	if includeName != "" {
		return includeName
	}
	return parent + "-" + strconv.Itoa(idx)
}

// mergeParams returns base ∪ row, where row entries override base
// entries with the same Name. Order: base entries first (preserving
// order, with values overridden in place), then row-only entries.
func mergeParams(base, row []tektontypes.Param) []tektontypes.Param {
	rowIdx := map[string]int{}
	for i, p := range row {
		rowIdx[p.Name] = i
	}
	out := make([]tektontypes.Param, 0, len(base)+len(row))
	emitted := map[string]bool{}
	for _, p := range base {
		if i, ok := rowIdx[p.Name]; ok {
			out = append(out, row[i])
		} else {
			out = append(out, p)
		}
		emitted[p.Name] = true
	}
	for _, p := range row {
		if !emitted[p.Name] {
			out = append(out, p)
		}
	}
	return out
}

func rewriteRunAfter(in []string, rewrite map[string][]string) []string {
	if len(in) == 0 {
		return in
	}
	out := make([]string, 0, len(in))
	for _, dep := range in {
		if expansions, ok := rewrite[dep]; ok {
			out = append(out, expansions...)
		} else {
			out = append(out, dep)
		}
	}
	return out
}

// rowParamMap converts a row's params (declaration-ordered) into a
// string-keyed string map for MatrixInfo.Params and the cluster
// backend's param-hash matcher.
func rowParamMap(ps []tektontypes.Param) map[string]string {
	out := make(map[string]string, len(ps))
	for _, p := range ps {
		out[p.Name] = p.Value.StringVal
	}
	return out
}

// matrixEventFor lifts a PipelineTask's Go-only MatrixInfo (set by
// expandMatrix) into the wire-shape reporter.MatrixEvent. Returns
// nil for non-matrix tasks so the field stays omitempty on the
// emitted event.
func matrixEventFor(pt tektontypes.PipelineTask) *reporter.MatrixEvent {
	if pt.MatrixInfo == nil {
		return nil
	}
	return &reporter.MatrixEvent{
		Parent: pt.MatrixInfo.Parent,
		Index:  pt.MatrixInfo.Index,
		Of:     pt.MatrixInfo.Of,
		Params: pt.MatrixInfo.Params,
	}
}

// matrixEventFromInfo builds the same wire-shape from a *MatrixInfo
// directly (used by the cluster backend, which carries MatrixInfo on
// TaskOutcomeOnCluster instead of on a synthetic PipelineTask).
func matrixEventFromInfo(mi *tektontypes.MatrixInfo) *reporter.MatrixEvent {
	if mi == nil {
		return nil
	}
	return &reporter.MatrixEvent{
		Parent: mi.Parent,
		Index:  mi.Index,
		Of:     mi.Of,
		Params: mi.Params,
	}
}

// aggregateMatrixResults folds per-expansion string results into
// the parent name. After every expansion of a matrix-fanned parent
// has produced its results map, this writes one entry per result
// name into results[parent], where the value is a JSON-array
// literal of the per-expansion strings in row order. That is the
// shape Tekton itself writes to /tekton/results/<name> for
// matrix-fanned tasks, so the existing resolver handles
// $(tasks.parent.results.Y) (the JSON-literal string flows through)
// and $(tasks.parent.results.Y[*]) (after the resolver task-result
// [*] extension; see internal/resolver/resolver.go).
func aggregateMatrixResults(parent string, expansionNames []string, results map[string]map[string]string) {
	if len(expansionNames) == 0 {
		return
	}
	nameSet := map[string]bool{}
	for _, n := range expansionNames {
		for k := range results[n] {
			nameSet[k] = true
		}
	}
	if len(nameSet) == 0 {
		return
	}
	if results[parent] == nil {
		results[parent] = map[string]string{}
	}
	for k := range nameSet {
		arr := make([]string, 0, len(expansionNames))
		for _, n := range expansionNames {
			arr = append(arr, results[n][k])
		}
		b, _ := json.Marshal(arr)
		results[parent][k] = string(b)
	}
}

// expansionNamesOf collects every PipelineTask name in `pts` that
// belongs to the same matrix parent as `mi`. Order matches `pts`,
// which (after expandMatrix) is row order — that's the order
// aggregateMatrixResults uses to compose the output array.
func expansionNamesOf(mi *tektontypes.MatrixInfo, pts []tektontypes.PipelineTask) []string {
	if mi == nil {
		return nil
	}
	var out []string
	for _, pt := range pts {
		if pt.MatrixInfo != nil && pt.MatrixInfo.Parent == mi.Parent {
			out = append(out, pt.Name)
		}
	}
	return out
}

// siblingsAllTerminal reports whether every expansion of mi.Parent
// in `pts` already has an outcome recorded. Caller holds the engine's
// outcomes mutex (RunPipeline writes outcomes only inside the lock).
func siblingsAllTerminal(mi *tektontypes.MatrixInfo, pts []tektontypes.PipelineTask, outcomes map[string]TaskOutcome) bool {
	if mi == nil {
		return false
	}
	for _, pt := range pts {
		if pt.MatrixInfo == nil || pt.MatrixInfo.Parent != mi.Parent {
			continue
		}
		if _, ok := outcomes[pt.Name]; !ok {
			return false
		}
	}
	return true
}

// maybeAggregateMatrix is called from the engine's per-task terminal
// block (caller holds outcomes/results mutex). When pt is the LAST
// expansion of a matrix-fanned parent to reach a terminal state,
// aggregateMatrixResults folds the per-expansion string results into
// the parent name. Concurrent expansions are serialised by the engine's
// outcomes/results mutex; the aggregation is therefore single-fire per
// parent.
func maybeAggregateMatrix(pt tektontypes.PipelineTask, pl tektontypes.Pipeline, outcomes map[string]TaskOutcome, results map[string]map[string]string) {
	if pt.MatrixInfo == nil {
		return
	}
	// Use the union of main + finally; a matrix-fanned parent lives
	// in exactly one of these lists, so the union is safe.
	all := append([]tektontypes.PipelineTask{}, pl.Spec.Tasks...)
	all = append(all, pl.Spec.Finally...)
	if !siblingsAllTerminal(pt.MatrixInfo, all, outcomes) {
		return
	}
	// Already aggregated? aggregate is a no-op on subsequent calls
	// only if the parent map already contains every name; skip the
	// re-marshal cost when results[parent] is already populated.
	if _, ok := results[pt.MatrixInfo.Parent]; ok {
		return
	}
	aggregateMatrixResults(pt.MatrixInfo.Parent, expansionNamesOf(pt.MatrixInfo, all), results)
}

// MaterializeMatrixRows is the exported helper the cluster backend's
// param-hash matcher uses to compute the same row order the engine
// uses internally. Returns the per-row params as an
// engine-package-private map[string]string slice the backend can
// canonical-hash. Order matches expandMatrix's row iteration.
func MaterializeMatrixRows(pt tektontypes.PipelineTask) []MatrixRow {
	if pt.Matrix == nil {
		return nil
	}
	rows, err := materializeRows(pt)
	if err != nil {
		return nil
	}
	out := make([]MatrixRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, MatrixRow{
			Params:      rowParamMap(r.params),
			IncludeName: r.includeName,
		})
	}
	return out
}

// MatrixRow is the public shape of one matrix expansion's params,
// used by the cluster backend to reconstruct MatrixInfo from a
// TaskRun's spec.params. Mirrors the unexported matrixRow with
// the Params type folded into a flat map for hashing.
type MatrixRow struct {
	Params      map[string]string
	IncludeName string
}
