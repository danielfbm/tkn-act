package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// pipelineTimeouts is the parsed form of tektontypes.Timeouts. Zero
// values mean "no budget at this level" — the engine treats them as
// pass-through.
type pipelineTimeouts struct {
	Pipeline time.Duration
	Tasks    time.Duration
	Finally  time.Duration
}

// parsePipelineTimeouts converts the raw spec into durations. Returns
// (zero, nil) when t is nil or all fields are empty — the caller is
// expected to skip wrapping in that case.
func parsePipelineTimeouts(t *tektontypes.Timeouts) (pipelineTimeouts, error) {
	out := pipelineTimeouts{}
	if t == nil {
		return out, nil
	}
	parse := func(field, s string) (time.Duration, error) {
		if s == "" {
			return 0, nil
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("timeouts.%s: %w", field, err)
		}
		return d, nil
	}
	var err error
	if out.Pipeline, err = parse("pipeline", t.Pipeline); err != nil {
		return pipelineTimeouts{}, err
	}
	if out.Tasks, err = parse("tasks", t.Tasks); err != nil {
		return pipelineTimeouts{}, err
	}
	if out.Finally, err = parse("finally", t.Finally); err != nil {
		return pipelineTimeouts{}, err
	}
	return out, nil
}

// tasksBudget returns the wall-clock available for the tasks DAG given
// the parsed timeouts. Falls through `pipeline - finally` when only
// `pipeline` is set; returns 0 (no budget) when neither is set.
func (p pipelineTimeouts) tasksBudget() time.Duration {
	if p.Tasks > 0 {
		return p.Tasks
	}
	if p.Pipeline > 0 && p.Finally > 0 {
		return p.Pipeline - p.Finally
	}
	if p.Pipeline > 0 {
		return p.Pipeline
	}
	return 0
}

// finallyBudget returns the wall-clock available for finally tasks.
func (p pipelineTimeouts) finallyBudget() time.Duration {
	if p.Finally > 0 {
		return p.Finally
	}
	if p.Pipeline > 0 && p.Tasks > 0 {
		return p.Pipeline - p.Tasks
	}
	if p.Pipeline > 0 {
		return p.Pipeline
	}
	return 0
}

// withMaybeBudget wraps ctx in a deadline if d > 0; otherwise returns
// the parent untouched with a no-op cancel.
func withMaybeBudget(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, d)
}
