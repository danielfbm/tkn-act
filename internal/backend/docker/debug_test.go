package docker

import (
	"testing"

	"github.com/danielfbm/tkn-act/internal/debug"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

// recordingReporter is a reporter.Reporter that captures events for
// assertions in plumbing tests.
type recordingReporter struct {
	events []reporter.Event
}

func (r *recordingReporter) Emit(e reporter.Event) { r.events = append(r.events, e) }
func (r *recordingReporter) Close() error          { return nil }

// newTestBackend returns a *Backend with the dbg field seeded to a
// Nop emitter — mirrors New() without spinning up a daemon.
func newTestBackend() *Backend {
	b := &Backend{}
	nop := debug.Nop()
	b.dbgVal.Store(&nop)
	return b
}

// TestSetDebug_ReplacesEmitter: the engine wires a live emitter
// post-construction via SetDebug. Confirm that emitting through the
// new emitter actually lands on the reporter.
func TestSetDebug_ReplacesEmitter(t *testing.T) {
	b := newTestBackend()
	if b.dbg() == nil {
		t.Fatal("b.dbg() returned nil before SetDebug")
	}
	rep := &recordingReporter{}
	b.SetDebug(debug.New(rep, true))
	b.dbg().Emit(debug.Backend, func() (string, map[string]any) {
		return "test", map[string]any{"k": "v"}
	})
	if len(rep.events) != 1 {
		t.Fatalf("want 1 event after SetDebug, got %d", len(rep.events))
	}
	if rep.events[0].Component != "backend" {
		t.Errorf("Component = %q, want backend", rep.events[0].Component)
	}
	if rep.events[0].Message != "test" {
		t.Errorf("Message = %q, want test", rep.events[0].Message)
	}
}

// TestSetDebug_NilFallsBackToNop: SetDebug(nil) should not panic and
// must leave a Nop emitter in place so emit sites stay safe.
func TestSetDebug_NilFallsBackToNop(t *testing.T) {
	b := newTestBackend()
	b.SetDebug(nil)
	if b.dbg() == nil {
		t.Fatal("b.dbg() returned nil after SetDebug(nil)")
	}
	// Emitting on the Nop must not panic and must produce no events.
	rep := &recordingReporter{}
	b.dbg().Emit(debug.Backend, func() (string, map[string]any) {
		// The Nop must NOT invoke the build closure.
		t.Errorf("closure invoked on Nop emitter")
		return "x", nil
	})
	if len(rep.events) != 0 {
		t.Errorf("Nop emitted %d events", len(rep.events))
	}
}

// TestSetDebug_ConcurrentSwap: SetDebug racing with concurrent emit
// reads must be safe (atomic.Pointer-backed). `go test -race` is the
// real check.
func TestSetDebug_ConcurrentSwap(t *testing.T) {
	b := newTestBackend()
	rep := &recordingReporter{}
	b.SetDebug(debug.New(rep, true))

	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			b.dbg().Emit(debug.Backend, func() (string, map[string]any) {
				return "tick", nil
			})
		}
		close(done)
	}()
	for i := 0; i < 50; i++ {
		b.SetDebug(debug.New(rep, true))
	}
	<-done
}

// TestShortID: truncates docker IDs to 12 chars, matches docker CLI.
func TestShortID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"abc", "abc"},
		{"abcdef012345", "abcdef012345"},
		{"abcdef012345xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", "abcdef012345"},
	}
	for _, c := range cases {
		if got := shortID(c.in); got != c.want {
			t.Errorf("shortID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
