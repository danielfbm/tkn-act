package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/danielfbm/tkn-act/internal/debug"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// runOneWithPolicy wraps runOne with the v1.2 task-level policy loop:
// per-task timeout (TaskSpec.Timeout) and PipelineTask.Retries.
//
// Retry is triggered for terminal "failed" / "infrafailed" outcomes only;
// "succeeded" / "skipped" / "timeout" outcomes are returned immediately. A
// task-retry event is emitted between attempts. The final task-end event
// carries the attempt number that produced the final outcome.
//
// When the task carries a Timeout, runOne is invoked under
// context.WithTimeout. If the deadline triggers, the outcome is rewritten to
// TaskTimeout regardless of how runOne classified the cancellation.
func (e *Engine) runOneWithPolicy(
	ctx context.Context,
	in PipelineInput,
	pl tektontypes.Pipeline,
	pt tektontypes.PipelineTask,
	params map[string]tektontypes.ParamValue,
	results map[string]map[string]string,
	runID, pipelineRunName string,
) TaskOutcome {
	taskTimeout, _ := taskTimeoutFor(in, pt)
	maxAttempts := 1 + pt.Retries
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var oc TaskOutcome
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		runCtx, cancel := withMaybeTimeout(ctx, taskTimeout)
		oc = e.runOne(runCtx, in, pl, pt, params, results, runID, pipelineRunName)
		// If the parent context died (cancellation), don't retry; bubble out.
		parentDead := ctx.Err() != nil
		// Detect task timeout: runCtx exceeded *its own* deadline (not the
		// parent's). If runCtx hit its deadline and ctx didn't, the task
		// timed out.
		timedOut := false
		if taskTimeout > 0 && runCtx.Err() != nil && !parentDead {
			timedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)
		}
		cancel()

		if timedOut {
			oc.Status = "timeout"
			oc.Message = fmt.Sprintf("task timeout %s exceeded", taskTimeout)
			break
		}
		if parentDead {
			break
		}
		if oc.Status == "succeeded" || oc.Status == "skipped" {
			oc.Attempt = attempt
			return oc
		}
		// Failure-class outcomes ("failed", "infrafailed") are retryable.
		if attempt < maxAttempts {
			currentAttempt, totalAttempts, ocStatus, ocMessage := attempt, maxAttempts, oc.Status, oc.Message
			e.dbg.Emit(debug.Engine, func() (string, map[string]any) {
				return "task retry", map[string]any{
					"task":    pt.Name,
					"attempt": currentAttempt,
					"of":      totalAttempts,
					"reason":  ocStatus,
					"message": truncate(ocMessage, 64),
				}
			})
			e.rep.Emit(reporter.Event{
				Kind:        reporter.EvtTaskRetry,
				Time:        time.Now(),
				Task:        pt.Name,
				Status:      oc.Status,
				Message:     oc.Message,
				Attempt:     attempt,
				DisplayName: pt.DisplayName,
			})
			continue
		}
	}
	if oc.Attempt == 0 {
		oc.Attempt = maxAttempts
	}
	return oc
}

// withMaybeTimeout returns a derived context. If d == 0 it returns the
// parent context with a no-op cancel. Callers must always call cancel.
func withMaybeTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, d)
}

// taskTimeoutFor returns the resolved task timeout. The PipelineTask itself
// doesn't carry a timeout in v1.2 — it lives on the resolved TaskSpec, so we
// have to look it up.
func taskTimeoutFor(in PipelineInput, pt tektontypes.PipelineTask) (time.Duration, error) {
	var spec tektontypes.TaskSpec
	switch {
	case pt.TaskSpec != nil:
		spec = *pt.TaskSpec
	case pt.TaskRef != nil:
		t, ok := in.Bundle.Tasks[pt.TaskRef.Name]
		if !ok {
			return 0, nil
		}
		spec = t.Spec
	default:
		return 0, nil
	}
	if spec.Timeout == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(spec.Timeout)
	if err != nil {
		return 0, err
	}
	return d, nil
}
