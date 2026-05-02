package exitcode_test

import (
	"errors"
	"testing"

	"github.com/danielfbm/tkn-act/internal/exitcode"
)

// TestStableNumbers locks the public exit-code contract. Renumbering any of
// these is a major-version-bump change.
func TestStableNumbers(t *testing.T) {
	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"OK", exitcode.OK, 0},
		{"Generic", exitcode.Generic, 1},
		{"Usage", exitcode.Usage, 2},
		{"Env", exitcode.Env, 3},
		{"Validate", exitcode.Validate, 4},
		{"Pipeline", exitcode.Pipeline, 5},
		{"Timeout", exitcode.Timeout, 6},
		{"Cancelled", exitcode.Cancelled, 130},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

func TestWrapAndFrom(t *testing.T) {
	if got := exitcode.From(nil); got != exitcode.OK {
		t.Errorf("From(nil) = %d, want OK", got)
	}
	if got := exitcode.From(errors.New("plain")); got != exitcode.Generic {
		t.Errorf("From(plain) = %d, want Generic", got)
	}
	wrapped := exitcode.Wrap(exitcode.Timeout, errors.New("boom"))
	if got := exitcode.From(wrapped); got != exitcode.Timeout {
		t.Errorf("From(wrap-timeout) = %d, want Timeout", got)
	}
}
