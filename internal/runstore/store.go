package runstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Store is the per-process handle on a tkn-act state directory.
// It is cheap to construct; concurrent runs each open their own Store.
type Store struct {
	dir           string // state-dir root (contains runs/, index.json)
	writerVersion string
}

// Open returns a Store rooted at dir, creating the directory tree if
// absent. writerVersion is recorded in every new run's meta.json so
// later replays can detect schema drift.
func Open(dir, writerVersion string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir state-dir: %w", err)
	}
	return &Store{dir: dir, writerVersion: writerVersion}, nil
}

// Dir returns the state-dir root.
func (s *Store) Dir() string { return s.dir }

// Run is an in-progress (or finalized) run record.
type Run struct {
	ID    RunID
	Seq   int
	Dir   string // <state-dir>/runs/<ulid>
	meta  Meta
	store *Store
}

// NewRun creates a fresh run dir, allocates the next sequence number
// under the index lock, writes an initial meta.json, and returns a
// handle. The caller is expected to call Finalize when the run
// completes.
func (s *Store) NewRun(now time.Time, pipelineRef string, args []string) (*Run, error) {
	idx, err := OpenIndex(s.dir)
	if err != nil {
		return nil, err
	}
	defer idx.Close()
	id := NewRunID(now)
	runDir := filepath.Join(s.dir, "runs", string(id))
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir run-dir: %w", err)
	}
	entry := IndexEntry{
		ULID:        string(id),
		PipelineRef: pipelineRef,
		StartedAt:   now.UTC(),
	}
	seq, err := idx.Append(entry)
	if err != nil {
		return nil, err
	}
	m := Meta{
		RunID:         string(id),
		ULID:          string(id),
		Seq:           seq,
		WriterVersion: s.writerVersion,
		PipelineRef:   pipelineRef,
		StartedAt:     now.UTC(),
		Args:          args,
	}
	if err := WriteMeta(filepath.Join(runDir, "meta.json"), m); err != nil {
		return nil, err
	}
	return &Run{ID: id, Seq: seq, Dir: runDir, meta: m, store: s}, nil
}

// EventsPath returns the path the events.jsonl file should live at
// for this run (the persist sink writes to it).
func (r *Run) EventsPath() string { return filepath.Join(r.Dir, "events.jsonl") }

// Finalize updates meta.json with the end time, exit code, and
// status, and updates the matching index.json entry.
func (r *Run) Finalize(end time.Time, exitCode int, status string) error {
	r.meta.EndedAt = end.UTC()
	r.meta.ExitCode = exitCode
	r.meta.Status = status
	if err := WriteMeta(filepath.Join(r.Dir, "meta.json"), r.meta); err != nil {
		return err
	}
	idx, err := OpenIndex(r.store.dir)
	if err != nil {
		return err
	}
	defer idx.Close()
	return idx.Update(r.Seq, func(e *IndexEntry) {
		e.EndedAt = end.UTC()
		e.ExitCode = exitCode
		e.Status = status
	})
}

// Resolve maps a user-supplied identifier — empty/"latest" for the
// most recent run, a positive integer for a seq, otherwise a ULID or
// ULID prefix — to an IndexEntry.
func (s *Store) Resolve(id string) (IndexEntry, error) {
	idx, err := OpenIndex(s.dir)
	if err != nil {
		return IndexEntry{}, err
	}
	defer idx.Close()
	if id == "" || id == "latest" {
		return idx.Latest()
	}
	if n, err := strconv.Atoi(id); err == nil && n > 0 {
		return idx.BySeq(n)
	}
	return idx.ByULIDPrefix(id)
}

// RunDir returns the on-disk directory for the given index entry.
func (s *Store) RunDir(e IndexEntry) string {
	return filepath.Join(s.dir, "runs", e.ULID)
}
