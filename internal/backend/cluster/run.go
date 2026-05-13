package cluster

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
)

var (
	gvrPipelineRun = schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "pipelineruns"}
	gvrTaskRun     = schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "taskruns"}
)

func (b *Backend) RunPipeline(ctx context.Context, in backend.PipelineRunInvocation) (backend.PipelineRunResult, error) {
	if b.client.Dynamic == nil || b.client.Kube == nil {
		return backend.PipelineRunResult{}, fmt.Errorf("cluster backend not Prepared")
	}
	ns := "tkn-act-" + shortRunID(in.RunID)
	if err := b.ensureNamespace(ctx, ns); err != nil {
		return backend.PipelineRunResult{}, err
	}
	if err := b.applyVolumeSources(ctx, in, ns); err != nil {
		return backend.PipelineRunResult{}, err
	}
	pr := buildPipelineRun(in, ns)
	created, err := b.client.Dynamic.Resource(gvrPipelineRun).Namespace(ns).Create(ctx, pr, metav1.CreateOptions{})
	if err != nil {
		return backend.PipelineRunResult{}, fmt.Errorf("create PipelineRun: %w", err)
	}
	return b.watchPipelineRun(ctx, in, ns, created.GetName())
}

func (b *Backend) ensureNamespace(ctx context.Context, name string) error {
	_, err := b.client.Kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}, metav1.CreateOptions{})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("create namespace %q: %w", name, err)
	}
	// Wait for the default ServiceAccount to be provisioned by the
	// service-account controller before submitting the PipelineRun.
	// Without this, Tekton v1.12+ races the SA controller and the
	// TaskRun pod creation fails with `serviceaccounts "default" not
	// found. Maybe invalid TaskSpec`. v0.65 and v1.3.0 happened to be
	// slow enough to never hit the race; v1.12.0 exposed it on
	// cluster-CI. 30s matches Tekton's own integration tests.
	return b.waitForDefaultServiceAccount(ctx, name, 30*time.Second)
}

// waitForDefaultServiceAccount polls the namespace until the `default`
// ServiceAccount exists (created by Kubernetes' SA controller) or the
// timeout elapses. 100ms poll interval matches the cadence used by
// Tekton's e2e tests; raising it would slow per-fixture turn-around
// in the common case where the SA appears within a few hundred ms.
func (b *Backend) waitForDefaultServiceAccount(ctx context.Context, ns string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		_, err := b.client.Kube.CoreV1().ServiceAccounts(ns).Get(ctx, "default", metav1.GetOptions{})
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("default ServiceAccount not provisioned in namespace %q within %v: %w", ns, timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// buildPipelineRun returns a fully populated unstructured PipelineRun with
// pipelineSpec inlined and workspaces backed by volumeClaimTemplate.
func buildPipelineRun(in backend.PipelineRunInvocation, namespace string) *unstructured.Unstructured {
	pipelineSpec := pipelineSpecToMap(in.Pipeline)
	// Tekton's v1 PipelineSpec does not carry a `timeouts` field —
	// timeouts live on PipelineRun.spec.timeouts. tkn-act's tektontypes
	// puts the field on PipelineSpec (so authors can write it once on
	// the Pipeline), but when serialized into pipelineSpec for the
	// cluster backend it would be rejected by Tekton's webhook
	// ("unknown field"). Drop it here; we re-attach it onto the
	// PipelineRun's spec below.
	delete(pipelineSpec, "timeouts")

	// Inline embedded Tasks under each PipelineTask.taskSpec.
	if tasks, ok := pipelineSpec["tasks"].([]any); ok {
		for i, t := range tasks {
			m, ok := t.(map[string]any)
			if !ok {
				continue
			}
			inlineTaskSpec(m, in.Tasks)
			tasks[i] = m
		}
		pipelineSpec["tasks"] = tasks
	}
	if fin, ok := pipelineSpec["finally"].([]any); ok {
		for i, t := range fin {
			m, ok := t.(map[string]any)
			if !ok {
				continue
			}
			inlineTaskSpec(m, in.Tasks)
			fin[i] = m
		}
		pipelineSpec["finally"] = fin
	}

	spec := map[string]any{
		"pipelineSpec": pipelineSpec,
	}
	if t := in.Pipeline.Spec.Timeouts; t != nil {
		out := map[string]any{}
		if t.Pipeline != "" {
			out["pipeline"] = t.Pipeline
		}
		if t.Tasks != "" {
			out["tasks"] = t.Tasks
		}
		if t.Finally != "" {
			out["finally"] = t.Finally
		}
		if len(out) > 0 {
			spec["timeouts"] = out
		}
	}
	if len(in.Params) > 0 {
		var ps []any
		for _, p := range in.Params {
			ps = append(ps, paramToMap(p))
		}
		spec["params"] = ps
	}
	// Workspaces: volumeClaimTemplate per declared pipeline workspace.
	if pwss, ok := pipelineSpec["workspaces"].([]any); ok && len(pwss) > 0 {
		var wsBindings []any
		for _, w := range pwss {
			m, ok := w.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["name"].(string)
			wsBindings = append(wsBindings, map[string]any{
				"name": name,
				"volumeClaimTemplate": map[string]any{
					"spec": map[string]any{
						"accessModes": []any{"ReadWriteOnce"},
						"resources": map[string]any{
							"requests": map[string]any{"storage": "1Gi"},
						},
					},
				},
			})
		}
		spec["workspaces"] = wsBindings
	}

	pr := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "tekton.dev/v1",
			"kind":       "PipelineRun",
			"metadata": map[string]any{
				"name":      in.PipelineRunName,
				"namespace": namespace,
			},
			"spec": spec,
		},
	}
	return pr
}

func pipelineSpecToMap(pl tektontypes.Pipeline) map[string]any {
	b, _ := json.Marshal(pl.Spec)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

// inlineTaskSpec resolves a PipelineTask map's taskRef into an inlined
// taskSpec (Tekton's EmbeddedTask). It also moves any per-task
// `timeout` from TaskSpec onto PipelineTask.timeout, because Tekton's
// EmbeddedTask schema has no `timeout` field — leaving it on taskSpec
// gets the PipelineRun rejected by the admission webhook with
// `unknown field "timeout"`. PipelineTask.timeout is the supported
// place for per-task wall clocks under inlined taskSpec.
func inlineTaskSpec(pt map[string]any, tasks map[string]tektontypes.Task) {
	ref, hasRef := pt["taskRef"].(map[string]any)
	if !hasRef {
		return
	}
	name, _ := ref["name"].(string)
	tk, ok := tasks[name]
	if !ok {
		return
	}
	tsm := taskSpecToMap(tk.Spec)
	// Hoist taskSpec.timeout → pipelineSpec.tasks[].timeout. The
	// PipelineTask.Timeout field is what Tekton's reconciler uses to
	// drive per-TaskRun wall clocks; the inlined taskSpec must not
	// carry a timeout. Only hoist when PipelineTask doesn't already
	// have its own (the on-PipelineTask one wins).
	if to, present := tsm["timeout"]; present {
		if _, already := pt["timeout"]; !already {
			pt["timeout"] = to
		}
		delete(tsm, "timeout")
	}
	pt["taskSpec"] = tsm
	delete(pt, "taskRef")
}

func taskSpecToMap(ts tektontypes.TaskSpec) map[string]any {
	b, _ := json.Marshal(ts)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m == nil {
		m = map[string]any{}
	}
	// Strip Step.displayName / Step.description before submission.
	// History: Tekton v0.65 (tkn-act's first cluster pin) had neither
	// field on Step — only Pipeline.spec, PipelineTask, and TaskSpec
	// did. As of v1.12 (current LTS pin) Step.displayName exists but
	// Step.description still doesn't, so a wholesale strip is still
	// defensive. The admission webhook strict-decodes the inlined
	// PipelineRun and rejects unknown fields. tkn-act's docker backend
	// is the source of truth for both: it consumes Step.DisplayName /
	// Description locally, so cluster mode doesn't need to round-trip
	// them through the Tekton controller.
	if steps, ok := m["steps"].([]any); ok {
		for i, s := range steps {
			sm, ok := s.(map[string]any)
			if !ok {
				continue
			}
			delete(sm, "displayName")
			delete(sm, "description")
			steps[i] = sm
		}
		m["steps"] = steps
	}
	return m
}

func paramToMap(p tektontypes.Param) map[string]any {
	m := map[string]any{"name": p.Name}
	switch p.Value.Type {
	case tektontypes.ParamTypeArray:
		m["value"] = strSliceToAny(p.Value.ArrayVal)
	case tektontypes.ParamTypeObject:
		obj := map[string]any{}
		for k, v := range p.Value.ObjectVal {
			obj[k] = v
		}
		m["value"] = obj
	default:
		m["value"] = p.Value.StringVal
	}
	return m
}

func strSliceToAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func shortRunID(rid string) string {
	if len(rid) >= 8 {
		return rid[:8]
	}
	return rid
}

// watchPipelineRun watches the PipelineRun to terminal status, streaming
// TaskRun pod logs as they appear.
func (b *Backend) watchPipelineRun(ctx context.Context, in backend.PipelineRunInvocation, ns, name string) (backend.PipelineRunResult, error) {
	res := backend.PipelineRunResult{Started: time.Now(), Tasks: map[string]backend.TaskOutcomeOnCluster{}}

	w, err := b.client.Dynamic.Resource(gvrPipelineRun).Namespace(ns).Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + name,
	})
	if err != nil {
		return res, fmt.Errorf("watch PipelineRun: %w", err)
	}
	defer w.Stop()

	// Stream logs for each TaskRun pod as it appears.
	streamed := map[string]bool{}
	go b.streamAllTaskRunLogs(ctx, in, ns, streamed)

	for ev := range w.ResultChan() {
		if ev.Type == watch.Deleted || ev.Object == nil {
			continue
		}
		un, ok := ev.Object.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		conds, _, _ := unstructured.NestedSlice(un.Object, "status", "conditions")
		for _, c := range conds {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if cm["type"] != "Succeeded" {
				continue
			}
			status, _ := cm["status"].(string)
			if status != "True" && status != "False" {
				continue
			}
			reason, _ := cm["reason"].(string)
			message, _ := cm["message"].(string)
			res.Status = mapPipelineRunStatus(status, reason)
			res.Reason = reason
			res.Message = message
			res.Ended = time.Now()
			res.Tasks = b.collectTaskOutcomesWithSink(ctx, in, ns, matrixWarnSinkFor(in))
			res.Results = extractPipelineResults(un)
			// `spec.timeouts.{pipeline,tasks,finally}` exhaustion does
			// NOT always set the PipelineRun condition reason to a
			// timeout — depending on which budget fired and how Tekton
			// observed it, the PR can end with reason `Failed` while
			// the underlying cause was a timeout. Two distinct fallback
			// signals exist on the PR object:
			//
			//   1. TaskRuns that were running when the budget fired are
			//      cancelled with `TaskRunCancelledByPipelineTimeoutMsg`
			//      on `spec.statusMessage`. taskRunToOutcome surfaces
			//      that as per-task `timeout`.
			//   2. Tasks that hadn't launched yet are recorded in
			//      `status.skippedTasks` with a SkippingReason of
			//      `PipelineRun timeout has been reached` /
			//      `PipelineRun Tasks timeout has been reached` /
			//      `PipelineRun Finally timeout has been reached`
			//      (no TaskRun is ever created for them, so #1 misses).
			//
			// Re-classify to `timeout` if either signal fires. Without
			// #2, the cluster-CI `pipeline-timeout` fixture intermittently
			// reports `failed` in well under the budget when the
			// reconciler skips the only task before launching it.
			if res.Status == "failed" {
				if anyTaskTimedOut(res.Tasks) || anySkippedDueToTimeout(un) {
					res.Status = "timeout"
				}
			}
			return res, nil
		}
	}
	return res, fmt.Errorf("PipelineRun watch closed before terminal status")
}

// collectTaskOutcomes walks every TaskRun owned by this PipelineRun and
// produces a per-pipeline-task summary the engine turns into task-end /
// task-retry events. Best-effort: a list error returns an empty map (the
// PipelineRun status alone still drives the run-level outcome).
//
// Matrix-fanned PipelineTasks produce N TaskRuns sharing the same
// pipelineTask label; for each such TaskRun we reconstruct the
// MatrixInfo triple (Parent, Index, Params) via param-hash matching
// PRIMARY and childReferences ordering FALLBACK. The map key changes
// from the parent name to <parent>-<index> (or the include row's
// declared name) so the engine sees one outcome per expansion in the
// same shape the docker backend produces.
func (b *Backend) collectTaskOutcomes(ctx context.Context, in backend.PipelineRunInvocation, ns string) map[string]backend.TaskOutcomeOnCluster {
	return b.collectTaskOutcomesWithSink(ctx, in, ns, nil)
}

// collectTaskOutcomesWithSink is the fallback-warning-aware variant.
// `repSink` (when non-nil) receives one EvtError event per TaskRun
// that fell through to the childReferences-ordering fallback. The
// sink-less variant logs nothing — production callers always pass a
// reporter.Sink.
//
// Read the PipelineRun once up-front so the FALLBACK strategy can
// consult `pr.status.childReferences` for ordering when the
// param-hash match misses.
func (b *Backend) collectTaskOutcomesWithSink(ctx context.Context, in backend.PipelineRunInvocation, ns string, repSink reporter.Reporter) map[string]backend.TaskOutcomeOnCluster {
	out := map[string]backend.TaskOutcomeOnCluster{}
	list, err := b.client.Dynamic.Resource(gvrTaskRun).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "tekton.dev/pipelineRun=" + in.PipelineRunName,
	})
	if err != nil {
		return out
	}
	// childReferences map: TaskRun name → declaration index. Built once
	// from the parent PR object so the fallback path is O(N) total, not
	// O(N²) when N expansions are present.
	childRefIdx := map[string]int{}
	if pr, prerr := b.client.Dynamic.Resource(gvrPipelineRun).Namespace(ns).Get(ctx, in.PipelineRunName, metav1.GetOptions{}); prerr == nil {
		refs, _, _ := unstructured.NestedSlice(pr.Object, "status", "childReferences")
		for i, r := range refs {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			if name, _ := rm["name"].(string); name != "" {
				childRefIdx[name] = i
			}
		}
	}
	// Per-parent counter for the FALLBACK childReferences ordering.
	// Walking TaskRuns in childRefIdx order gives stable indices even
	// when the List() returns them in arbitrary order.
	type trWithIndex struct {
		tr        *unstructured.Unstructured
		childRef  int // -1 if absent
		ptName    string
		hasIndex  bool
	}
	scored := make([]trWithIndex, 0, len(list.Items))
	for i := range list.Items {
		tr := &list.Items[i]
		ptName, _, _ := unstructured.NestedString(tr.Object, "metadata", "labels", "tekton.dev/pipelineTask")
		if ptName == "" {
			continue
		}
		idx, hasIdx := childRefIdx[tr.GetName()]
		if !hasIdx {
			idx = -1
		}
		scored = append(scored, trWithIndex{tr: tr, childRef: idx, ptName: ptName, hasIndex: hasIdx})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		// Place items with childRef indices first (sorted ascending);
		// items without childRef indices keep their listing order.
		switch {
		case scored[i].hasIndex && !scored[j].hasIndex:
			return true
		case !scored[i].hasIndex && scored[j].hasIndex:
			return false
		case scored[i].hasIndex && scored[j].hasIndex:
			return scored[i].childRef < scored[j].childRef
		default:
			return false
		}
	})
	// Per-parent counter for FALLBACK ordering.
	fallbackSeq := map[string]int{}
	for _, sc := range scored {
		oc := taskRunToOutcome(sc.tr)
		mi := matchMatrixRowFromTaskRun(in.Pipeline, sc.ptName, sc.tr, sc.childRef, fallbackSeq, repSink)
		if mi != nil {
			oc.Matrix = mi
			key := sc.ptName + "-" + strconv.Itoa(mi.Index)
			if mi.IncludeName != "" {
				key = mi.IncludeName
			}
			out[key] = oc
		} else {
			out[sc.ptName] = oc
		}
	}
	return out
}

// matchMatrixRowFromTaskRun reconstructs MatrixInfo for a TaskRun
// belonging to a matrix-fanned parent. PRIMARY: hash the
// matrix-row params extracted from TaskRun.spec.params and look up
// against the parent's pre-computed row hashes. FALLBACK:
// `pr.status.childReferences` ordering, with an EvtError warning.
//
// Returns nil when the parent isn't matrix-fanned (caller falls back
// to the parent name as the outcomes-map key).
func matchMatrixRowFromTaskRun(pl tektontypes.Pipeline, parent string, tr *unstructured.Unstructured, childRefIdx int, fallbackSeq map[string]int, rep reporter.Reporter) *tektontypes.MatrixInfo {
	pt := findMatrixParent(pl, parent)
	if pt == nil || pt.Matrix == nil {
		return nil
	}
	rows := tektontypes.MaterializeMatrixRows(*pt)
	if len(rows) == 0 {
		return nil
	}
	matrixNames := matrixParamNamesFor(pt)
	trParams := extractTaskRunParams(tr)
	matrixOnly := filterToNames(trParams, matrixNames)

	// PRIMARY: param-hash match.
	if len(matrixOnly) > 0 {
		target := canonicalMatrixHash(matrixOnly)
		for i, row := range rows {
			if canonicalMatrixHash(row.Params) == target {
				return &tektontypes.MatrixInfo{
					Parent:      parent,
					Index:       i,
					Of:          len(rows),
					Params:      copyStringMap(row.Params),
					IncludeName: row.IncludeName,
				}
			}
		}
	}
	// FALLBACK: childReferences order. Use a per-parent monotonic
	// counter (childReferences may interleave parents). When the
	// TaskRun isn't in childReferences at all, use the per-parent
	// fallback sequence directly; either way, log one EvtError so a
	// silent regression on a future Tekton minor surfaces in CI.
	idx := fallbackSeq[parent]
	fallbackSeq[parent] = idx + 1
	if idx >= len(rows) {
		// More TaskRuns than expected rows — give up rather than
		// alias multiple TaskRuns onto the same row.
		return nil
	}
	if rep != nil {
		rep.Emit(reporter.Event{
			Kind:    reporter.EvtError,
			Time:    time.Now(),
			Message: fmt.Sprintf("matrix index reconstruction fell back to childReferences ordering for TaskRun %q (param-hash matching produced no hit; please file an issue with your Tekton version)", tr.GetName()),
		})
	}
	row := rows[idx]
	return &tektontypes.MatrixInfo{
		Parent:      parent,
		Index:       idx,
		Of:          len(rows),
		Params:      copyStringMap(row.Params),
		IncludeName: row.IncludeName,
	}
}

// findMatrixParent returns the PipelineTask in pl.Spec.Tasks ∪
// pl.Spec.Finally whose Name matches `parent` AND whose Matrix !=
// nil. Returns nil when not found or not matrix-fanned.
func findMatrixParent(pl tektontypes.Pipeline, parent string) *tektontypes.PipelineTask {
	for i := range pl.Spec.Tasks {
		if pl.Spec.Tasks[i].Name == parent && pl.Spec.Tasks[i].Matrix != nil {
			return &pl.Spec.Tasks[i]
		}
	}
	for i := range pl.Spec.Finally {
		if pl.Spec.Finally[i].Name == parent && pl.Spec.Finally[i].Matrix != nil {
			return &pl.Spec.Finally[i]
		}
	}
	return nil
}

// matrixParamNamesFor collects every param name that contributes to a
// row of pt's matrix: every matrix.params[*].name plus every
// matrix.include[*].params[*].name. The cluster backend filters
// TaskRun.spec.params down to this set before hashing so non-matrix
// PipelineTask.params don't perturb the match.
func matrixParamNamesFor(pt *tektontypes.PipelineTask) map[string]bool {
	names := map[string]bool{}
	if pt.Matrix == nil {
		return names
	}
	for _, mp := range pt.Matrix.Params {
		names[mp.Name] = true
	}
	for _, inc := range pt.Matrix.Include {
		for _, p := range inc.Params {
			names[p.Name] = true
		}
	}
	return names
}

// extractTaskRunParams reads spec.params off an unstructured TaskRun
// into a flat string map. Tekton's controller copies the
// PipelineTask's resolved params (post matrix-row substitution) onto
// the TaskRun, so this is the version-stable carrier for the
// matrix-row identity.
func extractTaskRunParams(tr *unstructured.Unstructured) map[string]string {
	out := map[string]string{}
	params, _, _ := unstructured.NestedSlice(tr.Object, "spec", "params")
	for _, p := range params {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		name, _ := pm["name"].(string)
		if name == "" {
			continue
		}
		switch v := pm["value"].(type) {
		case string:
			out[name] = v
		}
	}
	return out
}

func filterToNames(in map[string]string, names map[string]bool) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if names[k] {
			out[k] = v
		}
	}
	return out
}

// canonicalMatrixHash JSON-encodes the input as a sorted [{K,V}]
// list and hashes it. Stable regardless of map iteration order;
// insensitive to whitespace.
func canonicalMatrixHash(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	type kv struct{ K, V string }
	pairs := make([]kv, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, kv{K: k, V: m[k]})
	}
	b, _ := json.Marshal(pairs)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// matrixWarnSinkFor returns a reporter.Reporter to receive the
// matrix-fallback EvtError warning, or nil when the LogSink doesn't
// expose one. The production reporter.LogSink wraps a Reporter and
// implements reporterAccessor — tests typically pass a LogSink that
// doesn't, in which case the fallback warning is silently dropped.
func matrixWarnSinkFor(in backend.PipelineRunInvocation) reporter.Reporter {
	type reporterAccessor interface{ Reporter() reporter.Reporter }
	if in.LogSink == nil {
		return nil
	}
	if ra, ok := in.LogSink.(reporterAccessor); ok {
		return ra.Reporter()
	}
	return nil
}

// taskRunCancelledByPipelineTimeoutMsg matches Tekton's
// v1.TaskRunCancelledByPipelineTimeoutMsg constant. Tekton cancels
// TaskRuns affected by `spec.timeouts.{tasks,finally}` exhaustion by
// patching their `spec.statusMessage` to this exact string; the
// resulting condition reason is just `TaskRunCancelled`, so we have
// to read the message to disambiguate cancel-vs-timeout.
const taskRunCancelledByPipelineTimeoutMsg = "TaskRun cancelled as the PipelineRun it belongs to has timed out."

// pipelineRunSkippedDueToTimeoutReasons mirrors Tekton's
// v1.PipelineTimedOutSkip / v1.TasksTimedOutSkip / v1.FinallyTimedOutSkip
// constants. A skipped task with any of these reasons is the unmistakable
// signal that the PipelineRun ran out of budget — even when the run-
// level condition reason ends up as the generic `Failed`. (Tekton's
// `getPipelineTasksCount` increments SkippedDueToTimeout for these,
// and the condition reason then collapses to `Failed` because at
// least one task "failed or was skipped due to timeout".)
var pipelineRunSkippedDueToTimeoutReasons = map[string]struct{}{
	"PipelineRun timeout has been reached":         {},
	"PipelineRun Tasks timeout has been reached":   {},
	"PipelineRun Finally timeout has been reached": {},
}

// anyTaskTimedOut returns true if any per-task outcome is already
// classified as `timeout` (via the per-TaskRun `spec.statusMessage`
// check in taskRunToOutcome).
func anyTaskTimedOut(tasks map[string]backend.TaskOutcomeOnCluster) bool {
	for _, oc := range tasks {
		if oc.Status == "timeout" {
			return true
		}
	}
	return false
}

// anySkippedDueToTimeout returns true if any entry in
// `status.skippedTasks` carries a timeout-related SkippingReason. This
// is the only signal we get for tasks that the budget killed before
// they could launch — those tasks have no TaskRun, so the per-TaskRun
// statusMessage check (anyTaskTimedOut) misses them.
func anySkippedDueToTimeout(pr *unstructured.Unstructured) bool {
	skipped, _, _ := unstructured.NestedSlice(pr.Object, "status", "skippedTasks")
	for _, s := range skipped {
		sm, ok := s.(map[string]any)
		if !ok {
			continue
		}
		reason, _ := sm["reason"].(string)
		if _, hit := pipelineRunSkippedDueToTimeoutReasons[reason]; hit {
			return true
		}
	}
	return false
}

// taskRunToOutcome reads the parts of a Tekton TaskRun status the engine
// needs to reconstruct task-end / task-retry events: terminal status,
// retriesStatus list, and (when present) per-result values.
func taskRunToOutcome(tr *unstructured.Unstructured) backend.TaskOutcomeOnCluster {
	oc := backend.TaskOutcomeOnCluster{Attempts: 1}
	conds, _, _ := unstructured.NestedSlice(tr.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok || cm["type"] != "Succeeded" {
			continue
		}
		st, _ := cm["status"].(string)
		reason, _ := cm["reason"].(string)
		oc.Status = mapTaskRunStatus(st, reason)
		oc.Message, _ = cm["message"].(string)
		break
	}
	// Detect cancel-by-pipeline-timeout: spec.statusMessage carries the
	// signal even when the condition reason is just TaskRunCancelled.
	if msg, _, _ := unstructured.NestedString(tr.Object, "spec", "statusMessage"); msg == taskRunCancelledByPipelineTimeoutMsg {
		oc.Status = "timeout"
		if oc.Message == "" {
			oc.Message = msg
		}
	}
	if oc.Status == "" {
		oc.Status = "infrafailed"
	}
	// retriesStatus: one entry per *failed* attempt that preceded the
	// terminal one. Total attempts = len(retriesStatus) + 1.
	retries, _, _ := unstructured.NestedSlice(tr.Object, "status", "retriesStatus")
	if n := len(retries); n > 0 {
		oc.Attempts = n + 1
		oc.RetryAttempts = make([]backend.RetryAttempt, 0, n)
		for i, r := range retries {
			rm, _ := r.(map[string]any)
			cs, _, _ := unstructured.NestedSlice(rm, "conditions")
			ra := backend.RetryAttempt{Attempt: i + 1, Status: "failed"}
			for _, c := range cs {
				cmap, ok := c.(map[string]any)
				if !ok || cmap["type"] != "Succeeded" {
					continue
				}
				st, _ := cmap["status"].(string)
				reason, _ := cmap["reason"].(string)
				ra.Status = mapTaskRunStatus(st, reason)
				ra.Message, _ = cmap["message"].(string)
				break
			}
			oc.RetryAttempts = append(oc.RetryAttempts, ra)
		}
	}
	// Surface task results (best-effort).
	if results, found, _ := unstructured.NestedSlice(tr.Object, "status", "results"); found {
		oc.Results = map[string]string{}
		for _, r := range results {
			rm, _ := r.(map[string]any)
			name, _ := rm["name"].(string)
			value, _ := rm["value"].(string)
			if name != "" {
				oc.Results[name] = value
			}
		}
	}
	return oc
}

// mapTaskRunStatus is the per-TaskRun analogue of mapPipelineRunStatus.
func mapTaskRunStatus(condStatus, reason string) string {
	if condStatus == "True" {
		return "succeeded"
	}
	switch reason {
	case "TaskRunTimeout", "PipelineRunTimeout":
		return "timeout"
	}
	return "failed"
}

// mapPipelineRunStatus translates the (Succeeded condition status, reason)
// pair on a Tekton PipelineRun into one of our user-visible statuses. Today
// only timeout needs disambiguation; everything else collapses to
// succeeded/failed. Keep the table here next to the cluster watch so docker
// engine.RunResult.Status and cluster engine.RunResult.Status emit the
// same value for the same outcome.
func mapPipelineRunStatus(condStatus, reason string) string {
	if condStatus == "True" {
		return "succeeded"
	}
	switch reason {
	case "PipelineRunTimeout", "TaskRunTimeout":
		return "timeout"
	}
	return "failed"
}

func (b *Backend) streamAllTaskRunLogs(ctx context.Context, in backend.PipelineRunInvocation, ns string, streamed map[string]bool) {
	w, err := b.client.Dynamic.Resource(gvrTaskRun).Namespace(ns).Watch(ctx, metav1.ListOptions{
		LabelSelector: "tekton.dev/pipelineRun=" + in.PipelineRunName,
	})
	if err != nil {
		return
	}
	defer w.Stop()
	// Per-TaskRun sidecar lifecycle tracker. We diff the
	// status.sidecars[] slice between events and emit
	// EvtSidecarStart / EvtSidecarEnd as states transition.
	// Cross-backend fidelity is a hard project rule — the same
	// `sidecars` e2e fixture must produce equivalent JSON event
	// shapes on docker and cluster.
	sidecarSeen := map[string]map[string]sidecarSeenState{} // taskRunName → sidecarName → state
	for ev := range w.ResultChan() {
		un, ok := ev.Object.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		taskName, _, _ := unstructured.NestedString(un.Object, "metadata", "labels", "tekton.dev/pipelineTask")
		trName := un.GetName()
		// Diff sidecar statuses and emit transition events.
		b.emitSidecarTransitions(in, taskName, trName, un, sidecarSeen)

		podName, found, _ := unstructured.NestedString(un.Object, "status", "podName")
		if !found || streamed[podName] {
			continue
		}
		streamed[podName] = true
		go b.streamPodLogs(ctx, in, ns, podName, taskName)
	}
}

// sidecarSeenState tracks which lifecycle events have already been
// emitted for a given (taskRun, sidecar) pair. Both flags monotonic
// — once set they never flip back, so we never emit duplicate
// events even if the watch loop re-observes the same status.
type sidecarSeenState struct {
	startedEmitted    bool
	terminatedEmitted bool
}

// sidecarEventEmitter is the optional interface a LogSink may
// satisfy to receive sidecar lifecycle events. Mirrors the docker
// backend's helper of the same name; the production
// reporter.LogSink implements it.
type sidecarEventEmitter interface {
	EmitSidecarStart(taskName, sidecarName string)
	EmitSidecarEnd(taskName, sidecarName string, exitCode int, status, message string)
}

// emitSidecarTransitions diffs status.sidecars[] against the prior
// per-TaskRun seen map and emits EvtSidecarStart / EvtSidecarEnd
// for each transition. Pure-helper-driven (parsePodSidecarStatuses)
// for unit-testability; this wrapper does the LogSink emission and
// state-tracking the helper deliberately omits.
func (b *Backend) emitSidecarTransitions(in backend.PipelineRunInvocation, taskName, trName string, tr *unstructured.Unstructured, seen map[string]map[string]sidecarSeenState) {
	if in.LogSink == nil {
		return
	}
	emitter, ok := in.LogSink.(sidecarEventEmitter)
	if !ok {
		return
	}
	statuses := parsePodSidecarStatuses(tr)
	if len(statuses) == 0 {
		return
	}
	if seen[trName] == nil {
		seen[trName] = map[string]sidecarSeenState{}
	}
	for _, s := range statuses {
		st := seen[trName][s.Name]
		if s.Running && !st.startedEmitted {
			emitter.EmitSidecarStart(taskName, s.Name)
			st.startedEmitted = true
		}
		if s.Terminated && !st.terminatedEmitted {
			status := "succeeded"
			msg := ""
			if s.ExitCode != 0 {
				status = "failed"
				msg = "exited non-zero"
			}
			emitter.EmitSidecarEnd(taskName, s.Name, int(s.ExitCode), status, msg)
			st.terminatedEmitted = true
		}
		seen[trName][s.Name] = st
	}
}

func (b *Backend) streamPodLogs(ctx context.Context, in backend.PipelineRunInvocation, ns, pod, taskName string) {
	if in.LogSink == nil {
		return
	}
	// Wait briefly for the pod to have containers.
	time.Sleep(500 * time.Millisecond)
	p, err := b.client.Kube.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return
	}
	// Build a step-name -> displayName lookup from the input bundle so
	// log events can carry the same display_name docker mode does.
	// Source-of-truth is the input YAML, NOT the controller verdict.
	stepDisplayByName := stepDisplayNameLookup(in, taskName)
	for _, c := range p.Spec.Containers {
		var stepName, sidecarName string
		switch {
		case strings.HasPrefix(c.Name, "step-"):
			stepName = strings.TrimPrefix(c.Name, "step-")
		case strings.HasPrefix(c.Name, "sidecar-"):
			sidecarName = strings.TrimPrefix(c.Name, "sidecar-")
		default:
			continue
		}
		stepDisplayName := stepDisplayByName[stepName]
		req := b.client.Kube.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{Container: c.Name, Follow: true})
		rc, err := req.Stream(ctx)
		if err != nil {
			continue
		}
		go func(stepName, stepDisplayName, sidecarName string, rc io.ReadCloser) {
			defer rc.Close()
			s := bufio.NewScanner(rc)
			s.Buffer(make([]byte, 64*1024), 1024*1024)
			for s.Scan() {
				if sidecarName != "" {
					in.LogSink.SidecarLog(taskName, sidecarName, "sidecar-stdout", s.Text())
				} else {
					in.LogSink.StepLog(taskName, stepName, stepDisplayName, "stdout", s.Text())
				}
			}
		}(stepName, stepDisplayName, sidecarName, rc)
	}
}

// stepDisplayNameLookup returns step-name -> displayName for the
// PipelineTask named taskName, using the input bundle as the
// source-of-truth (mirrors docker mode). Returns an empty map if the
// PipelineTask isn't found or its resolved spec has no steps.
func stepDisplayNameLookup(in backend.PipelineRunInvocation, taskName string) map[string]string {
	out := map[string]string{}
	var pt *tektontypes.PipelineTask
	for i := range in.Pipeline.Spec.Tasks {
		if in.Pipeline.Spec.Tasks[i].Name == taskName {
			pt = &in.Pipeline.Spec.Tasks[i]
			break
		}
	}
	if pt == nil {
		for i := range in.Pipeline.Spec.Finally {
			if in.Pipeline.Spec.Finally[i].Name == taskName {
				pt = &in.Pipeline.Spec.Finally[i]
				break
			}
		}
	}
	if pt == nil {
		return out
	}
	var spec tektontypes.TaskSpec
	switch {
	case pt.TaskSpec != nil:
		spec = *pt.TaskSpec
	case pt.TaskRef != nil:
		t, ok := in.Tasks[pt.TaskRef.Name]
		if !ok {
			return out
		}
		spec = t.Spec
	default:
		return out
	}
	for _, s := range spec.Steps {
		if s.DisplayName != "" {
			out[s.Name] = s.DisplayName
		}
	}
	return out
}

// extractPipelineResults reads `pr.status.results` (Tekton v1) into a
// generic map. Each entry has shape {name, value}; value may be a
// string, a []any (array of strings), or a map[string]any (object).
// We preserve the JSON-decoded shape: ParamTypeString → string,
// ParamTypeArray → []string, ParamTypeObject → map[string]string,
// matching how the docker-path resolvePipelineResults populates
// RunResult.Results.
//
// Tekton v1 is the only schema the cluster integration installs, so
// we read v1's `status.results` only. Earlier Tekton releases used
// `status.pipelineResults`; tkn-act doesn't support those.
func extractPipelineResults(pr *unstructured.Unstructured) map[string]any {
	results, found, _ := unstructured.NestedSlice(pr.Object, "status", "results")
	if !found || len(results) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, r := range results {
		rm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name, _ := rm["name"].(string)
		if name == "" {
			continue
		}
		switch v := rm["value"].(type) {
		case string:
			out[name] = v
		case []any:
			arr := make([]string, 0, len(v))
			for _, item := range v {
				if s, ok := item.(string); ok {
					arr = append(arr, s)
				}
			}
			out[name] = arr
		case map[string]any:
			obj := make(map[string]string, len(v))
			for k, item := range v {
				if s, ok := item.(string); ok {
					obj[k] = s
				}
			}
			out[name] = obj
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
