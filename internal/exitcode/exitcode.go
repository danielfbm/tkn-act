// Package exitcode defines the stable exit-code contract for the tkn-act CLI
// and provides a small error-wrapper that lets command implementations
// associate an error with a specific code without coupling them to the main
// package.
//
// The codes are part of tkn-act's public contract for AI agents and shell
// scripts; do not renumber them. New categories should append rather than
// reuse.
package exitcode

import "errors"

const (
	OK        = 0   // success
	Generic   = 1   // unexpected / uncategorized error
	Usage     = 2   // bad flags, contradictory inputs, missing required arg
	Env       = 3   // environment is missing a dependency (Docker, k3d, ...)
	Validate  = 4   // Tekton YAML rejected before run
	Pipeline  = 5   // a Task or finally task failed during run
	Cancelled = 130 // SIGINT / SIGTERM
)

// Error wraps an underlying error with a CLI exit code.
type Error struct {
	Code int
	Err  error
}

func (e *Error) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

// Wrap attaches the given exit code to err. Returns nil if err is nil.
func Wrap(code int, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Err: err}
}

// From returns the exit code that should be used for err.
//   - nil               -> OK
//   - *Error            -> its Code
//   - anything else     -> Generic
func From(err error) int {
	if err == nil {
		return OK
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return Generic
}
