package runstore_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/runstore"
)

var ulidRE = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

func TestNewRunID_Shape(t *testing.T) {
	id := runstore.NewRunID(time.Now())
	if !ulidRE.MatchString(string(id)) {
		t.Errorf("NewRunID returned %q, not a Crockford-base32 ULID", id)
	}
}

func TestNewRunID_TimeMonotonic(t *testing.T) {
	a := runstore.NewRunID(time.Unix(1_700_000_000, 0))
	b := runstore.NewRunID(time.Unix(1_700_000_001, 0))
	if a >= b {
		t.Errorf("later time should sort later: a=%q b=%q", a, b)
	}
}

func TestParseRunID_OK(t *testing.T) {
	id := runstore.NewRunID(time.Now())
	if _, err := runstore.ParseRunID(string(id)); err != nil {
		t.Errorf("ParseRunID(%q): %v", id, err)
	}
}

func TestParseRunID_BadInput(t *testing.T) {
	if _, err := runstore.ParseRunID("not-a-ulid"); err == nil {
		t.Errorf("expected error for bad input")
	}
}

func TestRunID_Time_RoundTrip(t *testing.T) {
	want := time.Unix(1_700_000_000, 0).UTC()
	id := runstore.NewRunID(want)
	got, err := id.Time()
	if err != nil {
		t.Fatalf("Time: %v", err)
	}
	// ULIDs encode millisecond precision, so compare truncated.
	if got.UnixMilli() != want.UnixMilli() {
		t.Errorf("Time round-trip: got %v, want %v", got, want)
	}
}
