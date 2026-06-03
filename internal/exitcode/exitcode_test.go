package exitcode_test

import (
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/spf13/pflag"

	"github.com/danielfbm/tkn-act/internal/exitcode"
)

// newUsageFlagSet builds a FlagSet whose Parse errors exercise each pflag
// usage-error type that From classifies as exitcode.Usage.
func newUsageFlagSet() *pflag.FlagSet {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.String("name", "", "a string flag that needs a value")
	fs.Int("num", 0, "an int flag with typed parsing")
	return fs
}

func TestFromClassifiesPflagUsageErrorsAsUsage(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"unknown flag (NotExistError)", []string{"--bogus"}},
		{"missing value (ValueRequiredError)", []string{"--name"}},
		{"invalid value (InvalidValueError)", []string{"--num", "abc"}},
		{"bad syntax (InvalidSyntaxError)", []string{"--=foo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := newUsageFlagSet().Parse(tc.args)
			if err == nil {
				t.Fatalf("expected a parse error for args %v", tc.args)
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Fatalf("From(%v) = %d, want Usage(%d); err=%v", tc.args, got, exitcode.Usage, err)
			}
		})
	}
}

func TestFromClassifiesUnknownCommandAsUsage(t *testing.T) {
	// Cobra's unknown-subcommand path returns a plain error with this stable
	// prefix rather than a dedicated type, so From matches the prefix.
	err := fmt.Errorf("unknown command %q for %q", "bogus", "tkn-act")
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Fatalf("From(unknown command) = %d, want Usage(%d)", got, exitcode.Usage)
	}
}

func TestFromLeavesUnrelatedErrorsGeneric(t *testing.T) {
	// A message that merely contains (but does not start with) the prefix must
	// not be mis-classified as a usage error.
	err := errors.New("ran unknown command logic and failed")
	if got := exitcode.From(err); got != exitcode.Generic {
		t.Fatalf("From(non-usage) = %d, want Generic(%d)", got, exitcode.Generic)
	}
}

func TestFromNil(t *testing.T) {
	if got := exitcode.From(nil); got != exitcode.OK {
		t.Fatalf("From(nil) = %d, want %d", got, exitcode.OK)
	}
}

func TestFromPlainError(t *testing.T) {
	if got := exitcode.From(errors.New("boom")); got != exitcode.Generic {
		t.Fatalf("From(plain) = %d, want %d", got, exitcode.Generic)
	}
}

func TestFromWrappedError(t *testing.T) {
	cases := []int{exitcode.Usage, exitcode.Env, exitcode.Validate, exitcode.Pipeline, exitcode.Cancelled}
	for _, code := range cases {
		err := exitcode.Wrap(code, errors.New("x"))
		if got := exitcode.From(err); got != code {
			t.Errorf("From(Wrap(%d)) = %d", code, got)
		}
	}
}

func TestWrapNil(t *testing.T) {
	if got := exitcode.Wrap(exitcode.Usage, nil); got != nil {
		t.Fatalf("Wrap(_, nil) = %v, want nil", got)
	}
}

func TestErrorUnwrap(t *testing.T) {
	inner := errors.New("inner")
	err := exitcode.Wrap(exitcode.Validate, inner)
	if !errors.Is(err, inner) {
		t.Fatalf("errors.Is should find the inner error")
	}
	want := "inner"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestErrorFormatting(t *testing.T) {
	err := exitcode.Wrap(exitcode.Env, fmt.Errorf("docker: %w", errors.New("not running")))
	if err.Error() != "docker: not running" {
		t.Fatalf("Error() = %q", err.Error())
	}
}
