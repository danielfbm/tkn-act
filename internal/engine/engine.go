// Package engine orchestrates a Tekton PipelineRun. It builds the DAG, resolves
// params and results, evaluates when-expressions, runs `finally` tasks, and
// drives a Backend. Pure of I/O except via the Backend and the Reporter.
package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/debug"
	"github.com/danielfbm/tkn-act/internal/engine/dag"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/refresolver"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/resolver"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"golang.org/x/sync/errgroup"
)

type Options struct {
	MaxParallel int
	// VolumeResolver materialises a Task's volumes onto host paths just
	// before the task runs. The CLI sets this; tests may leave it nil
	// (volumes are then unsupported in that test).
	VolumeResolver VolumeResolver
	// Refresolver dispatches resolver-backed taskRefs at task-dispatch
	// time. nil-safe: when nil, any PipelineTask whose taskRef.resolver
	// is non-empty will fail with a clear error from lookupTaskSpecLazy.
	// The CLI builds this from --resolver-allow / --resolver-cache-dir /
	// --offline; tests inject a Registry with an inline stub resolver.
	Refresolver *refresolver.Registry
	// Debug is the verbose-trace emitter. The CLI builds this from the
	// --debug flag; nil means "no debug emissions" (the engine
	// substitutes debug.Nop in New). When non-nil, the engine propagates
	// it to the backend (via SetDebug if the backend implements
	// DebugSetter) and to Refresolver (via Registry.SetDebug) at
	// run-start, so all three components emit through the same channel.
	Debug debug.Emitter
}

// DebugSetter is implemented by backends that accept a debug.Emitter.
// The engine type-asserts the backend against this interface at
// run-start; backends that don't implement it stay silent on --debug.
type DebugSetter interface {
	SetDebug(d debug.Emitter)
}

// VolumeResolver is the engine's hook for the volumes package. Returns
// map[volumeName] -> hostPath.
type VolumeResolver func(taskName string, vs []tektontypes.Volume) (map[string]string, error)

type Engine struct {
	be   backend.Backend
	rep  reporter.Reporter
	opts Options
	dbg  debug.Emitter
}

func New(be backend.Backend, rep reporter.Reporter, opts Options) *Engine {
	if opts.MaxParallel <= 0 {
		opts.MaxParallel = 4
	}
	dbg := opts.Debug
	if dbg == nil {
		dbg = debug.Nop()
	}
	// Propagate the emitter so resolver and backend emit through the
	// same reporter the engine writes to. Done at New (not RunPipeline)
	// so callers that drive sub-flows directly (tests, the
	// remote-resolver dispatch) get the same wiring.
	if opts.Refresolver != nil {
		opts.Refresolver.SetDebug(dbg)
	}
	if ds, ok := be.(DebugSetter); ok {
		ds.SetDebug(dbg)
	}
	return &Engine{be: be, rep: rep, opts: opts, dbg: dbg}
}

func (e *Engine) RunPipeline(ctx context.Context, in PipelineInput) (RunResult, error) {
	// Eager top-level pipelineRef.resolver resolution. A PipelineRun
	// whose spec.pipelineRef carries a resolver block is resolved
	// SYNCHRONOUSLY at load time (spec §7) — the resolved Pipeline
	// replaces in.Name and gets injected into the bundle before DAG
	// build. Top-level resolution emits resolver-start / resolver-end
	// with an empty Task field; the consumer disambiguates "this is a
	// top-level pipelineRef resolution" from "this is a per-task
	// resolution" via the absence of the task field.
	if pl, ok := maybeResolveTopLevelPipelineRef(ctx, in, e.opts.Refresolver, e.rep); ok {
		// Inject a synthetic name if the PipelineRun didn't carry one,
		// or the resolved Pipeline's metadata.name differs.
		name := pl.Metadata.Name
		if name == "" {
			name = "resolved"
			pl.Metadata.Name = name
		}
		if in.Bundle.Pipelines == nil {
			in.Bundle.Pipelines = map[string]tektontypes.Pipeline{}
		}
		in.Bundle.Pipelines[name] = pl
		in.Name = name
	}
	// When in.Name == "" and no top-level resolver matched, fall through
	// to the not-found branch below (Pipelines[""] lookup returns !ok,
	// which then checks hasTopLevelPipelineRefResolver for a clean exit).
	pl, ok := in.Bundle.Pipelines[in.Name]
	if !ok {
		// If the engine emitted a top-level resolver-end with status
		// failed, treat the run as already-terminated. Detect that by
		// checking whether maybeResolveTopLevelPipelineRef saw a
		// resolver block; if it did, we already emitted run-start /
		// run-end ourselves and can return cleanly.
		if hasTopLevelPipelineRefResolver(in.Bundle) {
			return RunResult{Status: "failed"}, nil
		}
		return RunResult{}, fmt.Errorf("pipeline %q not found", in.Name)
	}
	if pb, ok := e.be.(backend.PipelineBackend); ok {
		return e.runViaPipelineBackend(ctx, pb, in, pl)
	}
	runID := in.RunID
	if runID == "" {
		runID = newRunID()
	}
	pipelineRunName := pl.Metadata.Name + "-" + runID[:8]

	params, err := applyDefaults(pl.Spec.Params, in.Params)
	if err != nil {
		return RunResult{}, err
	}

	// Fan out PipelineTask.matrix into expansion children before
	// building the DAG. The DAG layer is matrix-unaware after this
	// pass — every expansion looks like an ordinary PipelineTask.
	pl, err = expandMatrix(pl, params)
	if err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: err.Error(), DisplayName: pl.Spec.DisplayName})
		return RunResult{Status: "failed"}, err
	}

	results := map[string]map[string]string{} // task → result name → value
	outcomes := map[string]TaskOutcome{}      // task → outcome

	e.rep.Emit(reporter.Event{
		Kind: reporter.EvtRunStart, Time: time.Now(),
		RunID: runID, Pipeline: pl.Metadata.Name,
		DisplayName: pl.Spec.DisplayName,
		Description: pl.Spec.Description,
	})

	// Pre-pull images.
	images := uniqueImages(in.Bundle, pl)
	if err := e.be.Prepare(ctx, backend.RunSpec{RunID: runID, Pipeline: pl.Metadata.Name, Images: images, Workspaces: in.Workspaces}); err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: err.Error(), DisplayName: pl.Spec.DisplayName})
		return RunResult{Status: "failed"}, err
	}
	defer func() { _ = e.be.Cleanup(context.Background()) }()

	// Build DAG (main only).
	g := dag.New()
	main := map[string]tektontypes.PipelineTask{}
	for _, pt := range pl.Spec.Tasks {
		g.AddNode(pt.Name)
		main[pt.Name] = pt
	}
	// Matrix parent → expansion names, across main + finally. Used to
	// map result references to a matrix parent onto every expansion.
	expansions := matrixExpansions(pl.Spec.Tasks, pl.Spec.Finally)
	for _, pt := range pl.Spec.Tasks {
		for _, dep := range pt.RunAfter {
			g.AddEdge(dep, pt.Name)
		}
		// Implicit edges from $(tasks.X.results.Y) references in
		// pt.Params, pt.When and pt.TaskRef.ResolverParams. Mirrors
		// upstream Tekton; lazy-resolved taskRefs depend on this so a
		// resolver.params reference to an upstream result schedules
		// after the upstream task. A reference to a matrix parent
		// waits on every expansion. Refs to tasks not in the main
		// DAG are silently dropped here (the validator catches
		// dangling refs separately).
		for _, ref := range implicitParamEdges(pt) {
			for _, dep := range expandRefNames(ref, expansions) {
				if _, ok := main[dep]; !ok {
					continue
				}
				if dep == pt.Name || sameMatrixParent(main[dep], pt) {
					continue
				}
				g.AddEdge(dep, pt.Name)
			}
		}
	}
	levels, err := g.Levels()
	if err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: err.Error(), DisplayName: pl.Spec.DisplayName})
		return RunResult{Status: "failed"}, err
	}

	overallStart := time.Now()
	overall := "succeeded"

	timeouts, err := parsePipelineTimeouts(pl.Spec.Timeouts)
	if err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: err.Error(), DisplayName: pl.Spec.DisplayName})
		return RunResult{Status: "failed"}, err
	}
	pipeCtx, pipeCancel := withMaybeBudget(ctx, timeouts.Pipeline)
	defer pipeCancel()

	// Execute levels under the tasks budget.
	tasksCtx, tasksCancel := withMaybeBudget(pipeCtx, timeouts.tasksBudget())
	var mu sync.Mutex

	// stopReason names what stopped scheduling once a budget context
	// (tasksCtx / finallyCtx) is done: root cancellation, the pipeline
	// budget, or the phase's own budget.
	stopReason := func(phase string, budget time.Duration) string {
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			return "pipeline run cancelled"
		case timeouts.Pipeline > 0 && pipeCtx.Err() != nil:
			return fmt.Sprintf("pipeline timeout %s exceeded", timeouts.Pipeline)
		default:
			return fmt.Sprintf("%s timeout %s exceeded", phase, budget)
		}
	}
	tasksStopReason := func() string { return stopReason("tasks", timeouts.tasksBudget()) }
	finallyStopReason := func() string { return stopReason("finally", timeouts.finallyBudget()) }

	// markNotRun records a task that will not run and emits its
	// task-skip. Safe to call from dispatch goroutines.
	markNotRun := func(pt tektontypes.PipelineTask, msg string, kind skipKind) {
		mu.Lock()
		outcomes[pt.Name] = TaskOutcome{Status: "not-run", Message: msg, Matrix: pt.MatrixInfo, skip: kind}
		maybeAggregateMatrix(pt, pl, outcomes, results)
		mu.Unlock()
		e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskSkip, Time: time.Now(), Task: pt.Name, Message: msg, DisplayName: pt.DisplayName, Matrix: matrixEventFor(pt)})
	}

	// finishTask records a dispatched task's terminal outcome and emits
	// its terminal event. A when-skip ("skipped") emits task-skip and no
	// task-end — the same shape for matrix rows and plain tasks (runOne
	// already emitted the plain task's task-skip).
	finishTask := func(pt tektontypes.PipelineTask, oc TaskOutcome) {
		if pt.MatrixInfo != nil {
			oc.Matrix = pt.MatrixInfo
		}
		if oc.Status == "skipped" {
			if pt.MatrixInfo != nil {
				e.rep.Emit(reporter.Event{
					Kind: reporter.EvtTaskSkip, Time: time.Now(), Task: pt.Name,
					Message: oc.Message, DisplayName: pt.DisplayName,
					Matrix: matrixEventFor(pt),
				})
			}
			oc.skip = skipWhen
			mu.Lock()
			outcomes[pt.Name] = oc
			maybeAggregateMatrix(pt, pl, outcomes, results)
			mu.Unlock()
			return
		}
		e.rep.Emit(reporter.Event{
			Kind: reporter.EvtTaskEnd, Time: time.Now(), Task: pt.Name,
			Status: oc.Status, Duration: oc.Duration, Message: oc.Message, Attempt: oc.Attempt,
			DisplayName: pt.DisplayName,
			Matrix:      matrixEventFor(pt),
		})
		mu.Lock()
		outcomes[pt.Name] = oc
		// Finally-task results enter the same `results` map as main
		// tasks: Pipeline.spec.results may reference
		// $(tasks.<finally>.results.<name>).
		if oc.Results != nil {
			results[pt.Name] = oc.Results
		}
		maybeAggregateMatrix(pt, pl, outcomes, results)
		switch oc.Status {
		case "failed", "infrafailed":
			if overall != "timeout" {
				overall = "failed"
			}
		case "timeout":
			overall = "timeout"
		}
		mu.Unlock()
	}

	// reclassifyBudgetKill rewrites a cancellation-class outcome
	// ("infrafailed" / "failed") of a task that was in flight when its
	// phase budget (or the pipeline budget) fired to "timeout", so the
	// task-end event and the run status agree on what killed it.
	// Root-context cancellation is left alone (the run ends cancelled).
	reclassifyBudgetKill := func(oc TaskOutcome, budgetCtx context.Context, reason func() string) TaskOutcome {
		if budgetCtx.Err() == nil || errors.Is(ctx.Err(), context.Canceled) {
			return oc
		}
		if oc.Status == "infrafailed" || oc.Status == "failed" {
			oc.Status = "timeout"
			oc.Message = reason()
		}
		return oc
	}

	resultsSnapshot := func() map[string]map[string]string {
		mu.Lock()
		defer mu.Unlock()
		snap := make(map[string]map[string]string, len(results))
		for k, v := range results {
			snap[k] = v
		}
		return snap
	}

	for _, level := range levels {
		// Once the tasks budget fired (or the run was cancelled), mark
		// everything not yet started as not-run and keep walking the
		// remaining levels so every task gets a task-skip event and a
		// RunResult.Tasks entry.
		if tasksCtx.Err() != nil {
			reason := tasksStopReason()
			for _, taskName := range level {
				mu.Lock()
				_, alreadyDone := outcomes[taskName]
				mu.Unlock()
				if alreadyDone {
					continue
				}
				markNotRun(main[taskName], reason, skipStopped)
			}
			continue
		}
		eg := &errgroup.Group{}
		eg.SetLimit(e.opts.MaxParallel)
		for _, taskName := range level {
			tname := taskName
			pt := main[tname]

			mu.Lock()
			reason, blocked := upstreamBlocksDispatch(g, tname, main, outcomes)
			if !blocked {
				reason, blocked = missingResultRefs(pt, outcomes, expansions)
			}
			mu.Unlock()
			if blocked {
				markNotRun(pt, reason, skipBlocked)
				continue
			}

			// Resolve the Task spec (best-effort) so we can carry its
			// description on task-start. Any lookup error is handled by
			// runOne; it's safe to ignore here.
			taskSpec, _ := lookupTaskSpec(in.Bundle, pt)

			// Snapshot the results map so the per-task substitution
			// sees a stable view. Concurrent tasks at the same level
			// write to `results` after their runs end; without a
			// snapshot a goroutine reading resolver.Context.Results
			// races with those writes.
			resultsSnap := resultsSnapshot()

			eg.Go(func() error {
				// eg.Go blocks while MaxParallel tasks are in flight;
				// the budget may have fired in the meantime.
				if tasksCtx.Err() != nil {
					markNotRun(pt, tasksStopReason(), skipStopped)
					return nil
				}
				e.dbg.Emit(debug.Engine, func() (string, map[string]any) {
					return "task ready", map[string]any{"task": tname}
				})
				e.rep.Emit(reporter.Event{
					Kind: reporter.EvtTaskStart, Time: time.Now(), Task: tname,
					DisplayName: pt.DisplayName,
					Description: taskSpec.Description,
					Matrix:      matrixEventFor(pt),
				})
				oc := e.runOneWithPolicy(tasksCtx, in, pl, pt, params, resultsSnap, runID, pipelineRunName)
				finishTask(pt, reclassifyBudgetKill(oc, tasksCtx, tasksStopReason))
				return nil
			})
		}
		_ = eg.Wait()
	}
	if tasksCtx.Err() != nil {
		overall = "timeout"
	}
	tasksCancel()

	// Finally tasks always run, under the finally budget (rooted at
	// pipeCtx, not tasksCtx — exhausting tasks must not shorten
	// finally). Tekton runs every finally task in parallel (finally
	// tasks cannot runAfter or consume each other's results), so they
	// dispatch concurrently, bounded by MaxParallel like the main DAG.
	finallyCtx, finallyCancel := withMaybeBudget(pipeCtx, timeouts.finallyBudget())
	defer finallyCancel()
	// $(tasks.status) / $(tasks.<t>.status) for every finally task,
	// computed once now that every main task is terminal.
	finallyCtx = withFinallyStatus(finallyCtx, pl.Spec.Tasks, outcomes)
	// Every main task is terminal here: compute the finally-scoped view
	// once, before any finally task dispatches.
	finallyResults := resultsSnapshot()
	feg := &errgroup.Group{}
	feg.SetLimit(e.opts.MaxParallel)
	for _, pt := range pl.Spec.Finally {
		pt := pt
		// A finally task consuming a result of a task that failed or
		// was skipped is skipped (Tekton: "Results were missing").
		mu.Lock()
		reason, missing := missingResultRefs(pt, outcomes, expansions)
		mu.Unlock()
		if missing {
			markNotRun(pt, reason, skipBlocked)
			continue
		}
		finallyTaskSpec, _ := lookupTaskSpec(in.Bundle, pt)
		feg.Go(func() error {
			if finallyCtx.Err() != nil {
				markNotRun(pt, finallyStopReason(), skipStopped)
				return nil
			}
			e.rep.Emit(reporter.Event{
				Kind: reporter.EvtTaskStart, Time: time.Now(), Task: pt.Name,
				DisplayName: pt.DisplayName,
				Description: finallyTaskSpec.Description,
				Matrix:      matrixEventFor(pt),
			})
			oc := e.runOneWithPolicy(finallyCtx, in, pl, pt, params, finallyResults, runID, pipelineRunName)
			finishTask(pt, reclassifyBudgetKill(oc, finallyCtx, finallyStopReason))
			return nil
		})
	}
	_ = feg.Wait()

	// Either budget firing means the run timed out, regardless of how
	// individual task outcomes shook out (a budget kill is a "timeout"
	// even if backends reported infrafailed mid-flight).
	if pipeCtx.Err() != nil || finallyCtx.Err() != nil {
		overall = "timeout"
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		overall = "cancelled"
	}

	// Resolve Pipeline.spec.results once every task (incl. finally) is
	// terminal. Drops are non-fatal: each surfaces as an EvtError but
	// does not change overall status or the exit code.
	pipelineResults, resultErrs := resolvePipelineResults(pl, results)
	for _, err := range resultErrs {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtError, Time: time.Now(), Message: err.Error()})
	}

	e.rep.Emit(reporter.Event{
		Kind: reporter.EvtRunEnd, Time: time.Now(),
		Status: overall, Duration: time.Since(overallStart),
		Results:     pipelineResults,
		DisplayName: pl.Spec.DisplayName,
	})

	return RunResult{Status: overall, Tasks: outcomes, Results: pipelineResults}, nil
}

func (e *Engine) runOne(ctx context.Context, in PipelineInput, pl tektontypes.Pipeline, pt tektontypes.PipelineTask, params map[string]tektontypes.ParamValue, results map[string]map[string]string, runID, pipelineRunName string) TaskOutcome {
	// Build resolver context. For matrix-fanned expansions we layer
	// the row's matrix-contributed params on top of the pipeline-level
	// params so $(params.<matrix-name>) inside `when:` resolves to the
	// row's value (Tekton's per-row when semantics).
	rctxParams := flattenStringParams(params)
	if pt.MatrixInfo != nil {
		// pt.MatrixInfo.Params already holds the row's string-keyed
		// string values. Layer them onto the pipeline-level view.
		merged := make(map[string]string, len(rctxParams)+len(pt.MatrixInfo.Params))
		for k, v := range rctxParams {
			merged[k] = v
		}
		for k, v := range pt.MatrixInfo.Params {
			merged[k] = v
		}
		rctxParams = merged
	}
	rctx := resolver.Context{
		Params:       rctxParams,
		ArrayParams:  arrayParams(params),
		ObjectParams: objectParams(params),
		Results:      results,
		ContextVars:  taskContextVars(ctx, pl, pt, runID, pipelineRunName),
	}
	// $(tasks.status) / $(tasks.<t>.status): set only for finally tasks.
	applyFinallyStatus(ctx, &rctx)

	// Emit a "params resolved" debug event with the count of resolved
	// keys and a truncated peek at each value. Useful diagnostic when
	// $(...) substitution surfaces a surprise upstream — and cheap when
	// disabled because the build closure short-circuits.
	//
	// Param values whose key name matches the secret-like pattern
	// (secret|token|password|key|credential|auth) are redacted to
	// `<redacted>` so `--debug -o json > events.jsonl` doesn't
	// archive credentials. The check is intentionally over-cautious
	// — false positives are harmless; a missed exposure is not.
	e.dbg.Emit(debug.Engine, func() (string, map[string]any) {
		preview := make(map[string]string, len(rctx.Params))
		for k, v := range rctx.Params {
			if looksLikeSecretName(k) {
				preview[k] = "<redacted>"
				continue
			}
			preview[k] = truncate(v, 64)
		}
		return "params resolved", map[string]any{
			"task":             pt.Name,
			"count":            len(rctx.Params),
			"truncated_values": preview,
		}
	})

	// Evaluate when expressions.
	pass, reason, err := evaluateWhen(pt.When, rctx)
	if err != nil {
		return TaskOutcome{Status: "failed", Message: err.Error()}
	}
	if !pass {
		// "task skipped" debug event carries:
		//   - expression: the raw `pt.When` (pre-substitution); useful
		//     when the user wants to grep for clauses by literal value.
		//   - evaluated:  the post-substitution form (the reason
		//     string from evaluateWhen). Agents that don't want to
		//     do their own substitution can read this directly.
		//   - reason:     short human label ("in mismatch" / "notin
		//     match") matching what surfaces in the EvtTaskSkip message.
		// Matrix-fanned skips also include matrix_row so N expansions
		// of the same parent task don't produce N identical events.
		e.dbg.Emit(debug.Engine, func() (string, map[string]any) {
			expr := ""
			if len(pt.When) > 0 {
				expr = fmt.Sprintf("%v", pt.When)
			}
			fields := map[string]any{
				"task":       pt.Name,
				"reason":     reason,
				"expression": truncate(expr, 64),
				"evaluated":  truncate(reason, 64),
			}
			if pt.MatrixInfo != nil {
				fields["matrix_row"] = pt.MatrixInfo.Index
				fields["matrix_of"] = pt.MatrixInfo.Of
				fields["matrix_parent"] = pt.MatrixInfo.Parent
			}
			return "task skipped", fields
		})
		// For matrix-fanned tasks, the *expansion-name* skip is
		// emitted by the caller (RunPipeline's eg.Go closure) so
		// that Matrix is populated; suppress the inner skip event
		// here to avoid duplicates.
		if pt.MatrixInfo == nil {
			e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskSkip, Time: time.Now(), Task: pt.Name, Message: reason, DisplayName: pt.DisplayName})
		}
		return TaskOutcome{Status: "skipped", Message: reason}
	}

	// Resolve task spec, then merge StepTemplate into each Step
	// before any further substitution / validation runs. If the
	// PipelineTask uses a resolver-backed taskRef, lazy-dispatch
	// kicks in: substitute resolver.params against rctx, call the
	// registry, validate the bytes, and return the inlined TaskSpec.
	var spec tektontypes.TaskSpec
	if pt.TaskRef != nil && pt.TaskRef.Resolver != "" {
		var lerr error
		spec, _, lerr = lookupTaskSpecLazy(ctx, pt, rctx, e.opts.Refresolver, e.rep)
		if lerr != nil {
			return TaskOutcome{Status: "failed", Message: lerr.Error()}
		}
	} else {
		var lerr error
		spec, lerr = lookupTaskSpec(in.Bundle, pt)
		if lerr != nil {
			return TaskOutcome{Status: "failed", Message: lerr.Error()}
		}
	}
	// Expand any Step.Ref → StepAction body BEFORE stepTemplate
	// inheritance and before substitution. After this point every
	// downstream pass sees a TaskSpec where every Step has a body.
	{
		var lerr error
		spec, lerr = resolveStepActions(spec, in.Bundle)
		if lerr != nil {
			return TaskOutcome{Status: "failed", Message: lerr.Error()}
		}
	}
	spec = applyStepTemplate(spec)

	// Resolve task-level params (PipelineTask params override Task defaults).
	taskParams := map[string]tektontypes.ParamValue{}
	for _, decl := range spec.Params {
		if decl.Default != nil {
			taskParams[decl.Name] = *decl.Default
		}
	}
	for _, p := range pt.Params {
		// substitute the value (which may reference $(params.x) or
		// $(tasks.X.results.Y)); whole-array `[*]` refs pass through as
		// array values.
		v, err := bindTaskParamValue(p.Value, rctx)
		if err != nil {
			return TaskOutcome{Status: "failed", Message: fmt.Sprintf("param %q: %v", p.Name, err)}
		}
		taskParams[p.Name] = v
	}

	// Build a task-scoped resolver context (uses the task's own param view).
	taskCtx := resolver.Context{
		Params:       flattenStringParams(taskParams),
		ArrayParams:  arrayParams(taskParams),
		ObjectParams: objectParams(taskParams),
		Results:      results,
		ContextVars:  rctx.ContextVars,
		// $(workspaces.<name>.bound)
		BoundWorkspaces: boundWorkspaces(spec, pt),
	}

	taskCtx.WorkspacePaths = workspaceMountPaths(spec.Workspaces)
	// Substitute throughout the resolved Task spec. A substitution
	// error fails the task: never run a step whose fields were blanked.
	resolved, serr := substituteSpec(spec, taskCtx)
	if serr != nil {
		return TaskOutcome{Status: "failed", Message: serr.Error()}
	}

	// Workspace mounts.
	wsMap := map[string]backend.WorkspaceMount{}
	for _, w := range pt.Workspaces {
		host, ok := in.Workspaces[w.Workspace]
		if !ok {
			return TaskOutcome{Status: "failed", Message: fmt.Sprintf("workspace %q not provisioned", w.Workspace)}
		}
		wsMap[w.Name] = backend.WorkspaceMount{HostPath: host, SubPath: w.SubPath}
	}

	// Allocate per-task results dir.
	resultsDir, err := provisionResultsDir(in.Workspaces["__results"], pt.Name) // sentinel key for the manager-supplied results parent
	if err != nil {
		return TaskOutcome{Status: "failed", Message: err.Error()}
	}

	// Materialise Task volumes (emptyDir / hostPath / configMap / secret).
	var volumeHosts map[string]string
	if len(resolved.Volumes) > 0 {
		if e.opts.VolumeResolver == nil {
			return TaskOutcome{Status: "infrafailed", Message: "volumes declared but no VolumeResolver configured"}
		}
		var verr error
		volumeHosts, verr = e.opts.VolumeResolver(pt.Name, resolved.Volumes)
		if verr != nil {
			return TaskOutcome{Status: "infrafailed", Message: verr.Error()}
		}
	}

	// Resolver-backed taskRefs: the Task spec (and so its timeout) is
	// only known here, after lazy resolution, so runOneWithPolicy could
	// not apply it. Apply it around the backend call instead.
	runCtx := ctx
	var lazyTimeout time.Duration
	if pt.TaskRef != nil && pt.TaskRef.Resolver != "" && resolved.Timeout != "" {
		d, perr := time.ParseDuration(resolved.Timeout)
		if perr != nil {
			return TaskOutcome{Status: "failed", Message: fmt.Sprintf("task timeout %q: %v", resolved.Timeout, perr)}
		}
		if d > 0 {
			var cancel context.CancelFunc
			runCtx, cancel = context.WithTimeout(ctx, d)
			defer cancel()
			lazyTimeout = d
		}
	}

	taskRunName := pipelineRunName + "-" + pt.Name
	start := time.Now()
	res, err := e.be.RunTask(runCtx, backend.TaskInvocation{
		RunID:       runID,
		PipelineRun: pipelineRunName,
		TaskName:    pt.Name,
		TaskRunName: taskRunName,
		Task:        resolved,
		Params:      taskParams,
		Workspaces:  wsMap,
		ContextVars: taskCtx.ContextVars,
		ResultsHost: resultsDir,
		VolumeHosts: volumeHosts,
		LogSink:     reporter.NewLogSink(e.rep),
	})
	dur := time.Since(start)
	if lazyTimeout > 0 && ctx.Err() == nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return TaskOutcome{Status: "timeout", Message: fmt.Sprintf("task timeout %s exceeded", lazyTimeout), Duration: dur}
	}
	if err != nil {
		return TaskOutcome{Status: "failed", Message: err.Error(), Duration: dur}
	}
	status := string(res.Status)
	msg := ""
	if res.Err != nil {
		msg = res.Err.Error()
	}
	return TaskOutcome{Status: status, Message: msg, Results: res.Results, Duration: dur}
}

// upstreamBlocksDispatch decides whether `target` should be skipped
// because of its ancestors (runAfter + implicit result edges), and
// returns the task-skip message when it should.
//
// Tekton semantics (pipelines.md "Guarding a Task and its dependent
// Tasks"): a task skipped by its own `when` expressions does NOT block
// ordering-only dependents — they still run. Any other non-success
// ancestor blocks: failed / infrafailed / timeout, and not-run tasks
// that were themselves blocked or stopped (Tekton's ParentTasksSkip).
// Result consumers of a when-skipped task are handled separately by
// missingResultRefs.
//
// Matrix-fanned ancestors are treated as one logical node per parent:
// if AT LEAST ONE expansion succeeded, downstream may proceed (spec
// § 6.3). Otherwise the parent blocks iff some expansion is blocking
// (an all-rows-when-skipped matrix does not block).
func upstreamBlocksDispatch(g *dag.Graph, target string, main map[string]tektontypes.PipelineTask, outcomes map[string]TaskOutcome) (string, bool) {
	type group struct {
		anyOK     bool
		blocking  bool
		anyFailed bool
	}
	groups := map[string]*group{}
	keyFor := func(name string) string {
		pt, ok := main[name]
		if ok && pt.MatrixInfo != nil {
			return "matrix:" + pt.MatrixInfo.Parent
		}
		return name
	}
	for _, ancestor := range upstream(g, target) {
		k := keyFor(ancestor)
		grp, ok := groups[k]
		if !ok {
			grp = &group{}
			groups[k] = grp
		}
		// Not-yet-run ancestors don't block dispatch — the engine
		// relies on level ordering.
		oc, present := outcomes[ancestor]
		if !present {
			continue
		}
		switch {
		case oc.Status == "succeeded":
			grp.anyOK = true
		case oc.skip == skipWhen:
			// when-skipped: ordering-only, never blocking.
		default:
			grp.blocking = true
			if oc.Status != "not-run" {
				grp.anyFailed = true
			}
		}
	}
	blocked, failed := false, false
	for _, grp := range groups {
		if grp.anyOK || !grp.blocking {
			continue
		}
		blocked = true
		if grp.anyFailed {
			failed = true
		}
	}
	switch {
	case !blocked:
		return "", false
	case failed:
		return "upstream failure", true
	default:
		return "parent task skipped", true
	}
}

// missingResultRefs reports whether pt references (via params, when or
// resolver params) a result of a task that is terminal but did not
// succeed — skipped, not-run, failed or timed out. Tekton skips such a
// task with reason MissingResultsSkip ("Results were missing") instead
// of running it and failing on the unresolvable reference; this
// applies to finally tasks too. A matrix parent counts as producing its
// results when at least one expansion succeeded. Caller holds the
// outcomes lock.
func missingResultRefs(pt tektontypes.PipelineTask, outcomes map[string]TaskOutcome, expansions map[string][]string) (string, bool) {
	for _, ref := range implicitParamEdges(pt) {
		terminal, ok := false, false
		for _, n := range expandRefNames(ref, expansions) {
			oc, present := outcomes[n]
			if !present {
				continue
			}
			terminal = true
			if oc.Status == "succeeded" {
				ok = true
			}
		}
		if terminal && !ok {
			return fmt.Sprintf("Results were missing: task %q did not succeed", ref), true
		}
	}
	return "", false
}

// sameMatrixParent reports whether a and b are expansions of the same
// matrix-fanned parent (never true for non-matrix tasks).
func sameMatrixParent(a, b tektontypes.PipelineTask) bool {
	return a.MatrixInfo != nil && b.MatrixInfo != nil && a.MatrixInfo.Parent == b.MatrixInfo.Parent
}

// upstream returns nodes that have a path to target.
func upstream(g *dag.Graph, target string) []string {
	// Reverse traversal: collect any node whose Descendants() includes target.
	var out []string
	for _, n := range g.Nodes() {
		if n == target {
			continue
		}
		for _, d := range g.Descendants(n) {
			if d == target {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

func lookupTaskSpec(b *loader.Bundle, pt tektontypes.PipelineTask) (tektontypes.TaskSpec, error) {
	if pt.TaskSpec != nil {
		return *pt.TaskSpec, nil
	}
	if pt.TaskRef != nil {
		t, ok := b.Tasks[pt.TaskRef.Name]
		if !ok {
			return tektontypes.TaskSpec{}, fmt.Errorf("task %q not loaded", pt.TaskRef.Name)
		}
		return t.Spec, nil
	}
	return tektontypes.TaskSpec{}, fmt.Errorf("pipeline task %q has neither taskRef nor taskSpec", pt.Name)
}

// substituteSpec resolves every $(...) reference in the Task's Steps and
// Sidecars against the task-scoped context. Step-scoped refs
// ($(steps.X.results.Y), $(step.results.X.path)) are left for the docker
// backend's per-step pass; refs outside Tekton's namespaces (shell
// `$(date)`) are left literal; any other unresolvable ref is an error.
func substituteSpec(spec tektontypes.TaskSpec, ctx resolver.Context) (tektontypes.TaskSpec, error) {
	out := spec
	out.Steps = make([]tektontypes.Step, len(spec.Steps))
	for i, st := range spec.Steps {
		ns, err := substituteContainer(st.Name, "step", containerFields{
			image: st.Image, script: st.Script, workingDir: st.WorkingDir,
			command: st.Command, args: st.Args, env: st.Env,
		}, ctx, resolver.SubstituteDeferStepRefs, resolver.SubstituteArgsDeferStepRefs)
		if err != nil {
			return spec, err
		}
		cp := st
		cp.Image, cp.Script, cp.WorkingDir = ns.image, ns.script, ns.workingDir
		cp.Command, cp.Args, cp.Env = ns.command, ns.args, ns.env
		out.Steps[i] = cp
	}
	// Sidecars start before any step in the Task and are torn down
	// after the last step, so step-result references don't apply
	// (use the strict Substitute, not the step-deferring variant).
	if len(spec.Sidecars) > 0 {
		out.Sidecars = make([]tektontypes.Sidecar, len(spec.Sidecars))
		for i, sc := range spec.Sidecars {
			ns, err := substituteContainer(sc.Name, "sidecar", containerFields{
				image: sc.Image, script: sc.Script, workingDir: sc.WorkingDir,
				command: sc.Command, args: sc.Args, env: sc.Env,
			}, ctx, resolver.Substitute, resolver.SubstituteArgs)
			if err != nil {
				return spec, err
			}
			cp := sc
			cp.Image, cp.Script, cp.WorkingDir = ns.image, ns.script, ns.workingDir
			cp.Command, cp.Args, cp.Env = ns.command, ns.args, ns.env
			out.Sidecars[i] = cp
		}
	}
	return out, nil
}

// containerFields is the substitutable subset shared by Step and Sidecar.
type containerFields struct {
	image, script, workingDir string
	command, args             []string
	env                       []tektontypes.EnvVar
}

func substituteContainer(
	name, kind string,
	in containerFields,
	ctx resolver.Context,
	sub func(string, resolver.Context) (string, error),
	subArgs func([]string, resolver.Context) ([]string, error),
) (containerFields, error) {
	wrap := func(field string, err error) error {
		return fmt.Errorf("%s %q: %s: %w", kind, name, field, err)
	}
	var out containerFields
	var err error
	if out.image, err = sub(in.image, ctx); err != nil {
		return out, wrap("image", err)
	}
	if out.script, err = sub(in.script, ctx); err != nil {
		return out, wrap("script", err)
	}
	if out.workingDir, err = sub(in.workingDir, ctx); err != nil {
		return out, wrap("workingDir", err)
	}
	if len(in.command) > 0 {
		if out.command, err = subArgs(in.command, ctx); err != nil {
			return out, wrap("command", err)
		}
	}
	if len(in.args) > 0 {
		if out.args, err = subArgs(in.args, ctx); err != nil {
			return out, wrap("args", err)
		}
	}
	out.env = make([]tektontypes.EnvVar, len(in.env))
	for j, e := range in.env {
		v, err := sub(e.Value, ctx)
		if err != nil {
			return out, wrap("env "+e.Name, err)
		}
		out.env[j] = tektontypes.EnvVar{Name: e.Name, Value: v}
	}
	return out, nil
}

func uniqueImages(b *loader.Bundle, pl tektontypes.Pipeline) []string {
	seen := map[string]struct{}{}
	for _, pt := range append(append([]tektontypes.PipelineTask{}, pl.Spec.Tasks...), pl.Spec.Finally...) {
		var spec tektontypes.TaskSpec
		if pt.TaskRef != nil && pt.TaskRef.Resolver != "" {
			// Resolver-backed taskRef: bytes aren't available at
			// pre-pull time; the runtime image pull falls back to
			// per-step IfNotPresent semantics. Skip silently here
			// (resolver-end events surface what was fetched).
			continue
		}
		if pt.TaskRef != nil {
			if t, ok := b.Tasks[pt.TaskRef.Name]; ok {
				spec = t.Spec
			}
		} else if pt.TaskSpec != nil {
			spec = *pt.TaskSpec
		}
		// Expand StepAction refs first so referenced StepAction images
		// are pre-pulled, then merge stepTemplate so inherited images
		// count too. Errors here are silently skipped (the validator
		// catches missing-ref / ref+inline issues at exit 4 before
		// pre-pull runs); a Step with Ref pointing to a missing
		// StepAction would otherwise contribute the empty string.
		if expanded, err := resolveStepActions(spec, b); err == nil {
			spec = expanded
		}
		spec = applyStepTemplate(spec)
		// Pre-pull is best-effort: image strings still containing
		// `$(...)` (param / context / matrix-row references) are not
		// valid OCI references and would fail ImagePull immediately,
		// even though they would resolve correctly at task-dispatch
		// time after substituteSpec runs. Skip them here and rely on
		// the per-step ensureImage path in the backend, which sees
		// the substituted image and pulls IfNotPresent.
		for _, s := range spec.Steps {
			if s.Image != "" && !strings.Contains(s.Image, "$(") {
				seen[s.Image] = struct{}{}
			}
		}
		for _, sc := range spec.Sidecars {
			if sc.Image != "" && !strings.Contains(sc.Image, "$(") {
				seen[sc.Image] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for img := range seen {
		out = append(out, img)
	}
	return out
}

// provisionResultsDir is a thin wrapper that the CLI replaces with a real
// workspace.Manager closure. For unit tests with the fake backend, it returns
// an empty path which the fake ignores.
var provisionResultsDir = func(_, _ string) (string, error) { return "", nil }

// SetResultsDirProvisioner is called once by the CLI to wire the engine's
// per-task results dir creation into a workspace.Manager. Tests don't need
// this — the unit test fake backend ignores the empty path.
func SetResultsDirProvisioner(fn func(parent, taskName string) (string, error)) {
	provisionResultsDir = fn
}

func (e *Engine) runViaPipelineBackend(ctx context.Context, pb backend.PipelineBackend, in PipelineInput, pl tektontypes.Pipeline) (RunResult, error) {
	runID := in.RunID
	if runID == "" {
		runID = newRunID()
	}
	pipelineRunName := pl.Metadata.Name + "-" + runID[:8]

	params, err := applyDefaults(pl.Spec.Params, in.Params)
	if err != nil {
		return RunResult{}, err
	}

	// Cluster-backend lazy-resolve. Local k3d's Tekton has no
	// resolver credentials and no access to --resolver-cache-dir, so
	// we resolve in tkn-act and inline the resulting TaskSpec into
	// the Pipeline before submission. Phase 1 handles resolver.params
	// that reference run-scope only (params, context); upstream-result
	// deps within a single submission are deferred to a Phase 2+
	// extension that submits one PipelineRun per dispatch level.
	rctx := resolver.Context{
		Params:       flattenStringParams(params),
		ArrayParams:  arrayParams(params),
		ObjectParams: objectParams(params),
		Results:      map[string]map[string]string{},
		ContextVars: map[string]string{
			"pipelineRun.name": pipelineRunName,
			"pipeline.name":    pl.Metadata.Name,
		},
	}
	if rerr := inlineResolverBackedTasks(ctx, &pl, rctx, e.opts.Refresolver, e.rep); rerr != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: rerr.Error()})
		return RunResult{Status: "failed"}, rerr
	}

	images := uniqueImages(in.Bundle, pl)
	if err := pb.Prepare(ctx, backend.RunSpec{RunID: runID, Pipeline: pl.Metadata.Name, Images: images, Workspaces: in.Workspaces}); err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: err.Error(), DisplayName: pl.Spec.DisplayName})
		return RunResult{Status: "failed"}, err
	}

	e.rep.Emit(reporter.Event{
		Kind: reporter.EvtRunStart, Time: time.Now(),
		RunID: runID, Pipeline: pl.Metadata.Name,
		DisplayName: pl.Spec.DisplayName,
		Description: pl.Spec.Description,
	})

	var paramList []tektontypes.Param
	for k, v := range params {
		paramList = append(paramList, tektontypes.Param{Name: k, Value: v})
	}

	wsMap := map[string]backend.WorkspaceMount{}
	for k, host := range in.Workspaces {
		wsMap[k] = backend.WorkspaceMount{HostPath: host}
	}

	// Expand StepAction refs in every Task and every inline PipelineTask.taskSpec
	// before handing them to the pipeline backend. Cluster mode submits the
	// inlined Step shape — the cluster's Tekton controller never sees a
	// Ref-bearing Step (it would try to resolve `stepactions.tekton.dev/<name>`
	// from the per-run namespace, which we never apply). Errors here drop
	// the run with status:failed.
	expandedTasks, plExpanded, expandErr := expandBundleStepActions(in.Bundle, pl)
	if expandErr != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: expandErr.Error(), DisplayName: pl.Spec.DisplayName})
		return RunResult{Status: "failed"}, expandErr
	}

	start := time.Now()
	res, err := pb.RunPipeline(ctx, backend.PipelineRunInvocation{
		RunID:           runID,
		PipelineRunName: pipelineRunName,
		Pipeline:        plExpanded,
		Tasks:           expandedTasks,
		Params:          paramList,
		Workspaces:      wsMap,
		LogSink:         reporter.NewLogSink(e.rep),
	})
	dur := time.Since(start)
	if err != nil {
		// A user interrupt surfaces from the cluster backend as a
		// wrapped context.Canceled; report it as "cancelled" like the
		// docker engine does, so run-end agrees with exit code 130.
		status := "failed"
		if errors.Is(ctx.Err(), context.Canceled) {
			status = "cancelled"
		}
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: status, Duration: dur, Message: err.Error(), DisplayName: pl.Spec.DisplayName})
		return RunResult{Status: status}, err
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		res.Status = "cancelled"
	}
	e.emitClusterTaskEvents(pl, in.Bundle, res.Tasks)
	// Cross-backend EvtError parity for dropped pipeline results: the
	// docker engine emits one EvtError per declared spec.results name
	// the engine couldn't resolve. The cluster backend reads
	// pr.status.results post-hoc, so missing entries surface here as
	// "declared by the Pipeline but absent from the backend's verdict."
	// Emit one EvtError per such drop, in stable name order, so a
	// silent regression on the cluster path can't slip past CI.
	for _, name := range droppedClusterResultNames(pl, res.Results) {
		e.rep.Emit(reporter.Event{
			Kind: reporter.EvtError, Time: time.Now(),
			Message: fmt.Sprintf(
				"pipeline result %q dropped: not produced by Tekton (referenced task may have failed or skipped the result)",
				name),
		})
	}
	// Surface the backend's terminal Reason/Message on the run-end
	// event so a misclassification (status doesn't match what the test
	// expected) can be attributed to a specific backend code path
	// without needing to re-run with extra logging.
	endMsg := res.Message
	if res.Reason != "" {
		if endMsg != "" {
			endMsg = res.Reason + ": " + endMsg
		} else {
			endMsg = res.Reason
		}
	}
	e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: res.Status, Duration: dur, Message: endMsg, Results: res.Results, DisplayName: pl.Spec.DisplayName})
	out := RunResult{
		Status:  res.Status,
		Reason:  res.Reason,
		Message: res.Message,
		Results: res.Results,
		Tasks:   map[string]TaskOutcome{},
	}
	for n, oc := range res.Tasks {
		out.Tasks[n] = TaskOutcome{Status: oc.Status, Message: oc.Message, Results: oc.Results}
	}
	return out, nil
}

// emitClusterTaskEvents synthesises the per-task event sequence the docker
// engine produces live (task-start, [task-retry]*, task-end) from the
// cluster backend's post-hoc per-TaskRun summary. Cluster-mode events
// arrive at the end of the run rather than interleaved with execution,
// but their *shape* matches docker so an agent listening to --output json
// doesn't have to special-case the backend.
//
// pl + bundle are the input source-of-truth for displayName /
// description on synthesised events: we read them from the YAML the user
// submitted, not from the controller verdict, so cluster mode mirrors
// docker mode for these UX fields.
func (e *Engine) emitClusterTaskEvents(pl tektontypes.Pipeline, bundle *loader.Bundle, tasks map[string]backend.TaskOutcomeOnCluster) {
	// Emit in pipeline-declared order (main tasks first, then finally),
	// mirroring docker's interleaved order. Without this, ranging over
	// the controller's per-task map gives Go's randomised iteration
	// order — agents asserting on "first task-start" would flake.
	emitOne := func(pt tektontypes.PipelineTask, taskKey string, oc backend.TaskOutcomeOnCluster) {
		spec, _ := lookupTaskSpec(bundle, pt)
		now := time.Now()
		e.rep.Emit(reporter.Event{
			Kind: reporter.EvtTaskStart, Time: now, Task: taskKey,
			DisplayName: pt.DisplayName,
			Description: spec.Description,
			Matrix:      matrixEventFromInfo(oc.Matrix),
		})
		for _, r := range oc.RetryAttempts {
			t := r.Time
			if t.IsZero() {
				t = now
			}
			e.rep.Emit(reporter.Event{
				Kind:        reporter.EvtTaskRetry,
				Time:        t,
				Task:        taskKey,
				Status:      r.Status,
				Message:     r.Message,
				Attempt:     r.Attempt,
				DisplayName: pt.DisplayName,
				Matrix:      matrixEventFromInfo(oc.Matrix),
			})
		}
		attempt := oc.Attempts
		if attempt == 0 {
			attempt = 1
		}
		e.rep.Emit(reporter.Event{
			Kind:        reporter.EvtTaskEnd,
			Time:        time.Now(),
			Task:        taskKey,
			Status:      oc.Status,
			Message:     oc.Message,
			Attempt:     attempt,
			DisplayName: pt.DisplayName,
			Matrix:      matrixEventFromInfo(oc.Matrix),
		})
	}
	emit := func(pt tektontypes.PipelineTask) {
		// Non-matrix path: outcomes keyed by parent name.
		if pt.Matrix == nil {
			oc, ok := tasks[pt.Name]
			if !ok {
				return
			}
			emitOne(pt, pt.Name, oc)
			return
		}
		// Matrix path: walk every outcome whose Matrix.Parent == pt.Name
		// in row order so the JSON event sequence matches docker.
		type kv struct {
			name string
			oc   backend.TaskOutcomeOnCluster
		}
		var matches []kv
		for k, oc := range tasks {
			if oc.Matrix != nil && oc.Matrix.Parent == pt.Name {
				matches = append(matches, kv{name: k, oc: oc})
			}
		}
		sort.Slice(matches, func(i, j int) bool {
			return matches[i].oc.Matrix.Index < matches[j].oc.Matrix.Index
		})
		for _, m := range matches {
			emitOne(pt, m.name, m.oc)
		}
	}
	for _, pt := range pl.Spec.Tasks {
		emit(pt)
	}
	for _, pt := range pl.Spec.Finally {
		emit(pt)
	}
}

// truncate clips s to at most max runes, appending an ellipsis when
// truncation actually happens. Used to keep debug field values
// bounded so a giant resolved param doesn't bloat events.jsonl. Works
// on runes (not bytes) so a multibyte UTF-8 boundary doesn't render
// a replacement character.
func truncate(s string, maxVal int) string {
	rs := []rune(s)
	if len(rs) <= maxVal {
		return s
	}
	return string(rs[:maxVal-1]) + "…"
}

// secretNamePatterns is the case-insensitive substring set that
// triggers redaction in the "params resolved" debug event. Matches
// the most common credential-bearing param-name conventions
// across Tekton catalogs. Adding a new pattern requires a
// corresponding test in TestLooksLikeSecretName.
var secretNamePatterns = []string{
	"secret",
	"token",
	"password",
	"passwd",
	"key",
	"credential",
	"auth",
}

// looksLikeSecretName returns true when name appears to identify a
// secret-bearing param (case-insensitive substring match against
// secretNamePatterns). False positives are harmless — they only
// suppress the value in a debug trace; the engine still substitutes
// the real value into the resolved task.
func looksLikeSecretName(name string) bool {
	lower := strings.ToLower(name)
	for _, p := range secretNamePatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}
