package backend_test

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
)

func TestScanLogLines_Semantics(t *testing.T) {
	var got []string
	backend.ScanLogLines(strings.NewReader("a\r\n\nb\nlast-no-newline"), func(s string) { got = append(got, s) })
	want := []string{"a", "", "b", "last-no-newline"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %q, want %q", got, want)
	}
	got = nil
	backend.ScanLogLines(strings.NewReader(""), func(s string) { got = append(got, s) })
	if len(got) != 0 {
		t.Errorf("empty input emitted %q", got)
	}
}

// TestScanLogLines_LongLineTruncatedAndContinues: bufio.Scanner used
// to stop on a >1MiB line, losing every following line.
func TestScanLogLines_LongLineTruncatedAndContinues(t *testing.T) {
	in := strings.Repeat("y", backend.MaxLogLineBytes+12345) + "\nafter\n"
	var got []string
	backend.ScanLogLines(strings.NewReader(in), func(s string) { got = append(got, s) })
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2", len(got))
	}
	if !strings.HasSuffix(got[0], "[tkn-act: line truncated, 12345 bytes dropped]") {
		t.Errorf("missing truncation marker: ...%q", got[0][len(got[0])-60:])
	}
	if !strings.HasPrefix(got[0], strings.Repeat("y", backend.MaxLogLineBytes)) {
		t.Error("first MaxLogLineBytes bytes not kept")
	}
	if got[1] != "after" {
		t.Errorf("second line = %q", got[1])
	}
}

type errReader struct{ n int }

func (e *errReader) Read(p []byte) (int, error) {
	if e.n == 0 {
		e.n++
		return copy(p, "partial"), nil
	}
	return 0, errors.New("boom")
}

func TestScanLogLines_ReadErrorFlushesPartial(t *testing.T) {
	var got []string
	backend.ScanLogLines(io.Reader(&errReader{}), func(s string) { got = append(got, s) })
	if len(got) != 1 || got[0] != "partial" {
		t.Errorf("got %q", got)
	}
}

// TestScanLogLinesTruncatesMidChunk covers truncation when a read chunk
// straddles MaxLogLineBytes. MaxLogLineBytes is a multiple of the 64KiB
// read buffer, so a short preceding line is needed to shift chunk
// boundaries off the cap; the kept prefix must still be exactly
// MaxLogLineBytes and the dropped count exact.
func TestScanLogLinesTruncatesMidChunk(t *testing.T) {
	const extra = 123
	long := strings.Repeat("a", backend.MaxLogLineBytes) + strings.Repeat("b", extra)
	input := "x\n" + long + "\nafter\n"

	var got []string
	backend.ScanLogLines(strings.NewReader(input), func(s string) { got = append(got, s) })

	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3", len(got))
	}
	if got[0] != "x" || got[2] != "after" {
		t.Errorf("neighbouring lines = %q / %q, want x / after", got[0], got[2])
	}
	wantPrefix := strings.Repeat("a", backend.MaxLogLineBytes)
	wantMarker := fmt.Sprintf(" [tkn-act: line truncated, %d bytes dropped]", extra)
	if got[1] != wantPrefix+wantMarker {
		t.Errorf("truncated line: len=%d, suffix=%q; want len=%d ending in %q",
			len(got[1]), got[1][len(got[1])-60:], len(wantPrefix)+len(wantMarker), wantMarker)
	}
}
