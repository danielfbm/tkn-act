package runstore

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// RunID is the on-disk identifier for a stored run. The string form
// is a 26-character Crockford-base32 ULID; the leading 10 characters
// encode the run's start timestamp so RunIDs sort chronologically
// under a plain lexical compare.
type RunID string

// NewRunID generates a fresh RunID using the given clock time.
func NewRunID(now time.Time) RunID {
	ms := ulid.Timestamp(now)
	entropy := ulid.Monotonic(rand.Reader, 0)
	return RunID(ulid.MustNew(ms, entropy).String())
}

// ParseRunID validates that s is a well-formed ULID and returns it
// as a RunID.
func ParseRunID(s string) (RunID, error) {
	if _, err := ulid.Parse(s); err != nil {
		return "", fmt.Errorf("invalid run id %q: %w", s, err)
	}
	return RunID(s), nil
}

// Time returns the timestamp encoded in the RunID.
func (r RunID) Time() (time.Time, error) {
	id, err := ulid.Parse(string(r))
	if err != nil {
		return time.Time{}, err
	}
	return ulid.Time(id.Time()).UTC(), nil
}
