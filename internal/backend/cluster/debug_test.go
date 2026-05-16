package cluster

import (
	"testing"

	"github.com/danielfbm/tkn-act/internal/debug"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

// recordingReporter captures events for SetDebug plumbing assertions
// that don't require a k8s cluster.
type recordingReporter struct {
	events []reporter.Event
}

func (r *recordingReporter) Emit(e reporter.Event) { r.events = append(r.events, e) }
func (r *recordingReporter) Close() error          { return nil }

// TestCluster_SetDebug_ReplacesEmitter: backend.SetDebug must install
// the supplied emitter so subsequent Emit calls flow through.
func TestCluster_SetDebug_ReplacesEmitter(t *testing.T) {
	b := New(Options{})
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
}

// TestCluster_SetDebug_NilFallsBackToNop: passing nil must not panic
// and the emitter stays callable.
func TestCluster_SetDebug_NilFallsBackToNop(t *testing.T) {
	b := New(Options{})
	b.SetDebug(nil)
	if b.dbg() == nil {
		t.Fatal("b.dbg() returned nil after SetDebug(nil)")
	}
	b.dbg().Emit(debug.Backend, func() (string, map[string]any) {
		t.Errorf("closure invoked on Nop emitter")
		return "x", nil
	})
}

// TestCluster_SetDebug_ConcurrentSwap: SetDebug must be safe to call
// while goroutines are emitting via b.dbg(). go test -race catches
// any data race; the assertion just makes sure the events ended up
// somewhere reasonable.
func TestCluster_SetDebug_ConcurrentSwap(t *testing.T) {
	b := New(Options{})
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
	// Swap the emitter while the reader spins.
	for i := 0; i < 50; i++ {
		b.SetDebug(debug.New(rep, true))
	}
	<-done
}
