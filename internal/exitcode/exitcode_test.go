package exitcode_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/danielfbm/tkn-act/internal/exitcode"
)

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
