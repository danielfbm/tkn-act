package backend

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// MaxLogLineBytes caps a single emitted log line. Longer lines are
// truncated with a marker; the rest of the line is read and dropped so
// the stream keeps flowing (a reader that stops early would block the
// producer and leak the connection behind it).
const MaxLogLineBytes = 1 << 20

// ScanLogLines reads r to EOF and calls emit for every line, with
// bufio.ScanLines semantics (trailing "\r\n" / "\n" stripped, a final
// unterminated line emitted). Unlike bufio.Scanner it never stops on a
// long line: bytes past MaxLogLineBytes are discarded and counted in a
// truncation marker appended to the emitted line. On a read error the
// remainder of r is drained so the writer side never blocks.
func ScanLogLines(r io.Reader, emit func(string)) {
	br := bufio.NewReaderSize(r, 64*1024)
	line := make([]byte, 0, 4096)
	dropped := 0
	flush := func() {
		if n := len(line); n > 0 && line[n-1] == '\r' && dropped == 0 {
			line = line[:n-1]
		}
		s := string(line)
		if dropped > 0 {
			s += fmt.Sprintf(" [tkn-act: line truncated, %d bytes dropped]", dropped)
		}
		emit(s)
		line = line[:0]
		dropped = 0
	}
	for {
		chunk, err := br.ReadSlice('\n')
		eol := err == nil
		if eol {
			chunk = chunk[:len(chunk)-1]
		}
		if room := MaxLogLineBytes - len(line); room >= len(chunk) {
			line = append(line, chunk...)
		} else {
			if room > 0 {
				line = append(line, chunk[:room]...)
				chunk = chunk[room:]
			}
			dropped += len(chunk)
		}
		switch {
		case eol:
			flush()
		case errors.Is(err, bufio.ErrBufferFull):
			// Partial line; keep accumulating.
		default:
			if len(line) > 0 || dropped > 0 {
				flush()
			}
			if !errors.Is(err, io.EOF) {
				_, _ = io.Copy(io.Discard, r)
			}
			return
		}
	}
}
