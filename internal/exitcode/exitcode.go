// Package exitcode defines the stable exit-code contract for the tkn-act CLI
// and provides a small error-wrapper that lets command implementations
// associate an error with a specific code without coupling them to the main
// package.
//
// The codes are part of tkn-act's public contract for AI agents and shell
// scripts; do not renumber them. New categories should append rather than
// reuse.
package exitcode

import (
	"errors"
	"strings"

	"github.com/spf13/pflag"
)

const (
	OK        = 0   // success
	Generic   = 1   // unexpected / uncategorized error
	Usage     = 2   // bad flags, contradictory inputs, missing required arg
	Env       = 3   // environment is missing a dependency (Docker, k3d, ...)
	Validate  = 4   // Tekton YAML rejected before run
	Pipeline  = 5   // a Task or finally task failed during run
	Timeout   = 6   // a Task or finally task ended due to its declared timeout
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
	if isUsageError(err) {
		return Usage
	}
	return Generic
}

func isUsageError(err error) bool {
	var notExist *pflag.NotExistError
	if errors.As(err, &notExist) {
		return true
	}
	var valueRequired *pflag.ValueRequiredError
	if errors.As(err, &valueRequired) {
		return true
	}
	var invalidValue *pflag.InvalidValueError
	if errors.As(err, &invalidValue) {
		return true
	}
	var invalidSyntax *pflag.InvalidSyntaxError
	if errors.As(err, &invalidSyntax) {
		return true
	}

	// Cobra's unknown-command path returns a plain fmt.Errorf, not a
	// dedicated exported error type, so classify the stable message prefix.
	return strings.HasPrefix(err.Error(), "unknown command ")
}
