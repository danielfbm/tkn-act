package runstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// IndexEntry is one row of index.json — a thin reference back to a
// per-run directory inside the state-dir.
type IndexEntry struct {
	Seq         int       `json:"seq"`
	ULID        string    `json:"ulid"`
	PipelineRef string    `json:"pipeline_ref,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	EndedAt     time.Time `json:"ended_at,omitempty"`
	ExitCode    int       `json:"exit_code"`
	Status      string    `json:"status,omitempty"`
}

// indexFile is the on-disk shape of index.json.
type indexFile struct {
	NextSeq int          `json:"next_seq"`
	Entries []IndexEntry `json:"entries"`
}

// Index is a handle on the state-dir's index.json under an exclusive
// file lock. Callers must Close to release the lock.
type Index struct {
	dir  string
	f    *os.File // holds flock; nil after Close
	data indexFile
}

// OpenIndex opens (or creates) index.json under an exclusive lock.
// The state-dir and its `runs/` subdirectory are created on demand.
func OpenIndex(stateDir string) (*Index, error) {
	if err := os.MkdirAll(filepath.Join(stateDir, "runs"), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir state-dir: %w", err)
	}
	path := filepath.Join(stateDir, "index.json")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open index: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock index: %w", err)
	}
	idx := &Index{dir: stateDir, f: f}
	info, err := f.Stat()
	if err != nil {
		idx.Close()
		return nil, err
	}
	if info.Size() == 0 {
		idx.data = indexFile{NextSeq: 1}
	} else {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			idx.Close()
			return nil, err
		}
		if err := json.NewDecoder(f).Decode(&idx.data); err != nil {
			idx.Close()
			return nil, fmt.Errorf("decode index: %w", err)
		}
		if idx.data.NextSeq == 0 {
			idx.data.NextSeq = len(idx.data.Entries) + 1
		}
	}
	return idx, nil
}

// Close releases the lock and closes the file.
func (i *Index) Close() error {
	if i.f == nil {
		return nil
	}
	syscall.Flock(int(i.f.Fd()), syscall.LOCK_UN)
	err := i.f.Close()
	i.f = nil
	return err
}

// Append assigns the next seq, appends the entry, and flushes the file.
func (i *Index) Append(e IndexEntry) (int, error) {
	seq := i.data.NextSeq
	e.Seq = seq
	i.data.NextSeq = seq + 1
	i.data.Entries = append(i.data.Entries, e)
	return seq, i.flush()
}

// Update finds the entry with seq and replaces it. No-op if missing.
func (i *Index) Update(seq int, mutator func(*IndexEntry)) error {
	for k := range i.data.Entries {
		if i.data.Entries[k].Seq == seq {
			mutator(&i.data.Entries[k])
			return i.flush()
		}
	}
	return nil
}

// BySeq returns the entry with the given sequence number.
func (i *Index) BySeq(seq int) (IndexEntry, error) {
	for _, e := range i.data.Entries {
		if e.Seq == seq {
			return e, nil
		}
	}
	return IndexEntry{}, fmt.Errorf("no run with seq %d", seq)
}

// ByULIDPrefix returns the entry whose ULID has the given prefix.
// Returns an error if zero or multiple entries match.
func (i *Index) ByULIDPrefix(prefix string) (IndexEntry, error) {
	if prefix == "" {
		return IndexEntry{}, errors.New("ulid prefix must not be empty")
	}
	var found []IndexEntry
	for _, e := range i.data.Entries {
		if strings.HasPrefix(e.ULID, prefix) {
			found = append(found, e)
		}
	}
	switch len(found) {
	case 0:
		return IndexEntry{}, fmt.Errorf("no run matching ulid prefix %q", prefix)
	case 1:
		return found[0], nil
	default:
		return IndexEntry{}, fmt.Errorf("ulid prefix %q ambiguous (%d matches)", prefix, len(found))
	}
}

// Latest returns the most recently appended entry.
func (i *Index) Latest() (IndexEntry, error) {
	if len(i.data.Entries) == 0 {
		return IndexEntry{}, errors.New("no runs recorded")
	}
	return i.data.Entries[len(i.data.Entries)-1], nil
}

// All returns a copy of every entry, in order of appending (oldest first).
func (i *Index) All() []IndexEntry {
	out := make([]IndexEntry, len(i.data.Entries))
	copy(out, i.data.Entries)
	return out
}

// flush rewrites the underlying file from scratch (truncate then write).
func (i *Index) flush() error {
	if _, err := i.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := i.f.Truncate(0); err != nil {
		return err
	}
	enc := json.NewEncoder(i.f)
	enc.SetIndent("", "  ")
	return enc.Encode(i.data)
}

// replaceEntries replaces the entries slice in-memory and flushes.
// Used by retention GC to remove pruned rows in bulk.
func (i *Index) replaceEntries(entries []IndexEntry) error {
	i.data.Entries = entries
	return i.flush()
}
