package main

import (
	"fmt"
	"io"
	"time"

	"github.com/danielfbm/tkn-act/internal/exitcode"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/runstore"
)

// openRunRecord opens the runstore at stateDir and creates a new run
// record for the named pipeline. Both operations are fail-soft: any
// error is reported to warnSink as a single warning line and nil is
// returned, leaving the caller to proceed without persistence.
func openRunRecord(warnSink io.Writer, stateDir, pipelineName string, args []string) *runstore.Run {
	store, err := runstore.Open(stateDir, version)
	if err != nil {
		fmt.Fprintf(warnSink, "warning: state-dir %s: %v (run will not be persisted)\n", stateDir, err)
		return nil
	}
	r, err := store.NewRun(time.Now(), pipelineName, args)
	if err != nil {
		fmt.Fprintf(warnSink, "warning: could not record run: %v (run will not be persisted)\n", err)
		return nil
	}
	return r
}

// finalizeRun records the run's terminal state in meta.json and
// index.json. status is "succeeded" when retErr is nil, otherwise
// "failed"; exit code is derived via exitcode.From.
func finalizeRun(run *runstore.Run, retErr error) {
	status := "succeeded"
	code := 0
	if retErr != nil {
		code = exitcode.From(retErr)
		status = "failed"
	}
	_ = run.Finalize(time.Now(), code, status)
}

// wrapReporterWithPersist returns liveRep unchanged when run is nil,
// otherwise returns a Tee of liveRep + a persist sink writing to
// run.EventsPath(). The second return is a close function that
// callers must defer; it is nil when no persist sink was attached.
// A failure to open the events file is fail-soft: warnSink gets one
// line and the live reporter is returned alone.
func wrapReporterWithPersist(warnSink io.Writer, liveRep reporter.Reporter, run *runstore.Run) (reporter.Reporter, func() error) {
	if run == nil {
		return liveRep, nil
	}
	persistRep, err := reporter.NewPersistSink(run.EventsPath())
	if err != nil {
		fmt.Fprintf(warnSink, "warning: open events file: %v (run will not be persisted)\n", err)
		return liveRep, nil
	}
	tee := reporter.NewTee(liveRep, persistRep)
	return tee, tee.Close
}

// setupRunPersistence wires the run-store and the persist sink into
// the reporter pipeline. It returns the reporter (live alone when
// persistence couldn't start) and a cleanup function the caller must
// defer with the run's terminal error. cleanup is safe to call with
// nil err and is a no-op when persistence couldn't start.
//
// Pulling this out of runWith keeps the new statements off the run
// path (which is hard to unit-test because it drives the engine and
// the backend); the helpers above each have a focused test.
func setupRunPersistence(warnSink io.Writer, stateDir, pipelineName string, args []string, liveRep reporter.Reporter) (reporter.Reporter, func(retErr error)) {
	run := openRunRecord(warnSink, stateDir, pipelineName, args)
	rep, closeRep := wrapReporterWithPersist(warnSink, liveRep, run)
	cleanup := func(retErr error) {
		if closeRep != nil {
			// Close before Finalize so the persist sink's bufio writer
			// is flushed and events.jsonl is complete on disk before
			// meta.json's end_at lands.
			_ = closeRep()
		}
		if run != nil {
			finalizeRun(run, retErr)
		}
	}
	return rep, cleanup
}
