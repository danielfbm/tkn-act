package reporter

// filter is a Reporter that forwards an event to its inner sink only
// when its Task is in the configured task set (or the set is empty)
// AND its Step is in the configured step set (or the set is empty OR
// the event carries no Step). Events that don't belong to any
// specific task — run-start, run-end, error, and any other event
// with empty Task (e.g. top-level resolver-start / resolver-end for
// pipeline-ref resolution) — always pass so the user can see the
// run envelope, out-of-band failures, and pre-task resolution
// progress even with narrow filters.
//
// The filter wraps the user-facing reporter only; the persistence
// sink stays full-fidelity so a later `tkn-act logs` replay can
// reapply any filter against the recorded stream.
type filter struct {
	inner Reporter
	tasks map[string]bool
	steps map[string]bool
}

// NewFilter wraps inner with the configured --task / --step filter.
// Empty tasks (nil or zero-length) means "no task restriction"; same
// for steps. When both are empty the filter is a no-op wrapper that
// still delegates Close — useful so the call site can wrap
// unconditionally.
func NewFilter(inner Reporter, tasks, steps []string) Reporter {
	return &filter{
		inner: inner,
		tasks: setOf(tasks),
		steps: setOf(steps),
	}
}

func setOf(s []string) map[string]bool {
	if len(s) == 0 {
		return nil
	}
	m := make(map[string]bool, len(s))
	for _, x := range s {
		m[x] = true
	}
	return m
}

func (f *filter) Emit(e Event) {
	switch e.Kind {
	case EvtRunStart, EvtRunEnd, EvtError:
		f.inner.Emit(e)
		return
	}
	// Events without a Task are "envelope" events that don't belong
	// to any specific task — top-level resolver-start / resolver-end
	// for pipeline-ref resolution is the canonical example. Pass
	// them through so a user narrowing to --task=build still sees
	// "what resolved the pipeline I'm filtering into" and pre-task
	// resolver failures.
	if e.Task == "" {
		f.inner.Emit(e)
		return
	}
	if f.tasks != nil && !f.tasks[e.Task] {
		return
	}
	// Step filter only refuses events that DO carry a Step that's
	// not on the list. Events without a Step (task-start, task-end,
	// sidecar-start, etc.) still pass — otherwise filtering by step
	// would suppress every non-step-log event for the matching task.
	if f.steps != nil && e.Step != "" && !f.steps[e.Step] {
		return
	}
	f.inner.Emit(e)
}

func (f *filter) Close() error { return f.inner.Close() }
