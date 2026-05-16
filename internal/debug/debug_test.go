package debug_test

import (
	"testing"

	"github.com/danielfbm/tkn-act/internal/debug"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

type capture struct {
	events []reporter.Event
}

func (c *capture) Emit(e reporter.Event) { c.events = append(c.events, e) }
func (c *capture) Close() error          { return nil }

func TestEmitter_EnabledFalse_NoEvents(t *testing.T) {
	c := &capture{}
	e := debug.New(c, false)
	if e.Enabled() {
		t.Errorf("Enabled() returned true when disabled")
	}
	e.Emit(debug.Resolver, func() (string, map[string]any) {
		t.Errorf("closure invoked when disabled")
		return "x", nil
	})
	if len(c.events) != 0 {
		t.Errorf("emitted %d events when disabled", len(c.events))
	}
}

func TestEmitter_EnabledTrue_Emits(t *testing.T) {
	c := &capture{}
	e := debug.New(c, true)
	if !e.Enabled() {
		t.Errorf("Enabled() returned false when enabled")
	}
	e.Emit(debug.Backend, func() (string, map[string]any) {
		return "container created", map[string]any{"id": "abc123"}
	})
	if len(c.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(c.events))
	}
	ev := c.events[0]
	if ev.Kind != reporter.EvtDebug {
		t.Errorf("Kind = %q, want debug", ev.Kind)
	}
	if ev.Component != "backend" {
		t.Errorf("Component = %q, want backend", ev.Component)
	}
	if ev.Message != "container created" {
		t.Errorf("Message = %q, want container created", ev.Message)
	}
	if ev.Fields["id"] != "abc123" {
		t.Errorf("Fields.id = %v", ev.Fields["id"])
	}
	if ev.Time.IsZero() {
		t.Errorf("Time was not stamped on the event")
	}
}

func TestNop_IsDisabled(t *testing.T) {
	e := debug.Nop()
	if e.Enabled() {
		t.Errorf("Nop().Enabled() returned true")
	}
	called := false
	e.Emit(debug.Engine, func() (string, map[string]any) {
		called = true
		return "x", nil
	})
	if called {
		t.Errorf("Nop emitter invoked the build closure")
	}
}

func TestEmitter_Components(t *testing.T) {
	// Sanity: each component label is the lowercase plain-text
	// the user sees in --debug output and in the JSON event stream.
	cases := map[debug.Component]string{
		debug.Backend:  "backend",
		debug.Resolver: "resolver",
		debug.Engine:   "engine",
	}
	for c, want := range cases {
		if string(c) != want {
			t.Errorf("Component %v = %q, want %q", c, string(c), want)
		}
	}
}
