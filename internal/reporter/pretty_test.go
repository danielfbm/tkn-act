package reporter

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// formatResultValue truncates long values to 80 chars with an ellipsis.
// Truncating at the byte boundary `s[:max-1]` can land in the middle of
// a multibyte UTF-8 sequence, producing a malformed string and a stray
// replacement character once printed. The truncator must work in runes,
// not bytes. Regression for PR #18 reviewer Min-3.
func TestFormatResultValueRuneAwareTruncation(t *testing.T) {
	// Each Chinese char is 3 bytes in UTF-8, so 100 of them is 300 bytes
	// — well past the 80-char/rune ceiling. After truncation the result
	// must still be valid UTF-8 and end with the ellipsis.
	long := strings.Repeat("中", 100)
	got := formatResultValue(long)
	if !utf8.ValidString(got) {
		t.Errorf("truncated output is not valid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated output should end with ellipsis: %q", got)
	}
	// The truncated rune count should be the 80-rune ceiling: 79 source
	// runes + 1 ellipsis rune = 80 runes total.
	if rc := utf8.RuneCountInString(got); rc != 80 {
		t.Errorf("rune count = %d, want 80", rc)
	}
}

// Sanity check that short strings (< ceiling) pass through unchanged
// in either byte- or rune-counting modes.
func TestFormatResultValueShortStringPassthrough(t *testing.T) {
	in := "hello"
	if got := formatResultValue(in); got != in {
		t.Errorf("short string: got %q, want %q", got, in)
	}
}

// ASCII at exactly the boundary should not be truncated.
func TestFormatResultValueAtBoundary(t *testing.T) {
	in := strings.Repeat("a", 80)
	if got := formatResultValue(in); got != in {
		t.Errorf("80-char string should pass through, got len=%d", len(got))
	}
}
