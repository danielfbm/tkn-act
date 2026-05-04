package main

import (
	"errors"
	"testing"

	"github.com/danielfbm/tkn-act/internal/exitcode"
)

// TestSidecarInfraFailExitCodeDistinctFromTimeout locks the contract
// that a sidecar-driven infrafailed run does NOT collide with a
// Task.spec.timeout / Pipeline.spec.timeouts driven timeout run on
// the exit-code wire. Both can produce status "infrafailed" on
// per-event payloads on the way to the run-end (a sidecar failing
// to start surfaces as infrafailed); the resolved CLI exit code
// must be different — Timeout is 6, anything else surfaces a
// non-Timeout code.
//
// This is a regression lock: a future contributor wiring a new
// exit-code mapping must not collapse the two paths.
func TestSidecarInfraFailExitCodeDistinctFromTimeout(t *testing.T) {
	infra := exitcode.Wrap(exitcode.Pipeline, errors.New("pipeline \"p\" infrafailed"))
	to := exitcode.Wrap(exitcode.Timeout, errors.New("pipeline \"p\" timeout"))
	if got := exitcode.From(infra); got == exitcode.Timeout {
		t.Errorf("infrafailed wrapped as Timeout (got %d) — must NOT collide with timeout=6", got)
	}
	if got := exitcode.From(to); got != exitcode.Timeout {
		t.Errorf("timeout-wrapped → exit %d, want %d", got, exitcode.Timeout)
	}
	// And the canonical mapping table itself: 5 vs 6 stay distinct.
	if exitcode.Pipeline == exitcode.Timeout {
		t.Fatalf("exitcode.Pipeline (%d) must NOT equal exitcode.Timeout (%d)", exitcode.Pipeline, exitcode.Timeout)
	}
}
