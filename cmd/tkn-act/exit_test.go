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

func TestCobraUsageErrorsExitWithUsageCode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
	}{
		{name: "unknown flag", args: []string{"--bogus"}},
		{name: "missing flag arg", args: []string{"run", "--param"}},
		{name: "invalid flag value", args: []string{"cache", "prune", "--older-than", "banana"}},
		{name: "unknown command", args: []string{"bogus"}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runRoot(t, tc.args)
			if err == nil {
				t.Fatalf("expected error for args %v", tc.args)
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Fatalf("exit code for %v = %d, want %d; err=%v", tc.args, got, exitcode.Usage, err)
			}
		})
	}
}

func TestCommandGroupsRejectUnknownSubcommands(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
		want int
	}{
		{name: "cluster unknown subcommand", args: []string{"cluster", "bogus"}, want: exitcode.Usage},
		{name: "cache unknown subcommand", args: []string{"cache", "bogus"}, want: exitcode.Usage},
		{name: "runs unknown subcommand", args: []string{"runs", "bogus"}, want: exitcode.Usage},
		{name: "bare cluster stays success", args: []string{"cluster"}, want: exitcode.OK},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runRoot(t, tc.args)
			if got := exitcode.From(err); got != tc.want {
				t.Fatalf("exit code for %v = %d, want %d; err=%v", tc.args, got, tc.want, err)
			}
			if tc.want != exitcode.OK && err == nil {
				t.Fatalf("expected error for args %v", tc.args)
			}
		})
	}
}
