// Package debug provides a typed emitter used by the engine, the
// resolver, and the backends to surface verbose internal trace data
// when --debug is set.
//
// Callers wrap their field-building work in a closure; the emitter
// invokes the closure only when debug is enabled, so production runs
// pay nothing for the call other than the interface dispatch and a
// boolean check.
package debug

import (
	"time"

	"github.com/danielfbm/tkn-act/internal/reporter"
)

// Component is the source-of-truth label that ends up in the debug
// event's "component" field. Stable strings: agents key off these
// values, so they're part of the public contract once shipped.
type Component string

const (
	Backend  Component = "backend"
	Resolver Component = "resolver"
	Engine   Component = "engine"
)

// Emitter emits debug events to the reporter when enabled. When
// disabled, the supplied closure is not invoked — so callers can
// build expensive field maps without paying for them in normal runs.
type Emitter interface {
	Emit(c Component, build func() (msg string, fields map[string]any))
	Enabled() bool
}

type emitter struct {
	rep     reporter.Reporter
	enabled bool
}

// New returns an Emitter routing through rep. When enabled is false,
// every call short-circuits before invoking the build closure.
func New(rep reporter.Reporter, enabled bool) Emitter {
	return &emitter{rep: rep, enabled: enabled}
}

func (e *emitter) Enabled() bool { return e.enabled }

func (e *emitter) Emit(c Component, build func() (string, map[string]any)) {
	if !e.enabled {
		return
	}
	msg, fields := build()
	e.rep.Emit(reporter.Event{
		Kind:      reporter.EvtDebug,
		Time:      time.Now().UTC(),
		Component: string(c),
		Message:   msg,
		Fields:    fields,
	})
}

// Nop returns a disabled Emitter — useful for tests and for code
// paths that have no reporter handy.
func Nop() Emitter { return &emitter{rep: nil, enabled: false} }
