package runstore

import (
	"encoding/json"
	"errors"
	"fmt"
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
//
// Locking strategy: the flock is held on a sibling file `index.lock`,
// not on `index.json` itself. This decouples the lock identity from
// the data file's inode so flush can use a write-temp-rename atomic
// update without invalidating concurrent holders' locks. (`flock(2)`
// is per-inode; renaming a locked file unlinks the locked inode and
// breaks the lock contract.)
//
// Filesystem caveat: `flock(2)` is a Linux/Darwin local-FS advisory
// lock. On NFSv3-without-nlm, CIFS, virtiofs, and some FUSE backends
// it either no-ops or returns ENOLCK. Concurrent runs against a
// state-dir on such filesystems may collide on seq numbers; the
// agent-guide (Phase 6) calls this out and recommends pointing
// TKN_ACT_STATE_DIR at a local path.
type Index struct {
	dir    string
	lockFD *os.File // flock holder; nil after Close
	data   indexFile
}

// OpenIndex opens (or creates) index.json under an exclusive lock
// (on a sibling `index.lock` file). The state-dir and its `runs/`
// subdirectory are created on demand.
func OpenIndex(stateDir string) (*Index, error) {
	if err := os.MkdirAll(filepath.Join(stateDir, "runs"), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir state-dir: %w", err)
	}
	lockPath := filepath.Join(stateDir, "index.lock")
	lf, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		lf.Close()
		return nil, fmt.Errorf("lock index: %w", err)
	}
	idx := &Index{dir: stateDir, lockFD: lf}

	// Now read index.json under the lock.
	dataPath := filepath.Join(stateDir, "index.json")
	if data, err := os.ReadFile(dataPath); err != nil {
		if !os.IsNotExist(err) {
			idx.Close()
			return nil, fmt.Errorf("read index: %w", err)
		}
		idx.data = indexFile{NextSeq: 1}
	} else if len(data) == 0 {
		idx.data = indexFile{NextSeq: 1}
	} else {
		if err := json.Unmarshal(data, &idx.data); err != nil {
			idx.Close()
			return nil, fmt.Errorf("decode index: %w", err)
		}
		if idx.data.NextSeq == 0 {
			idx.data.NextSeq = len(idx.data.Entries) + 1
		}
	}
	return idx, nil
}

// Close releases the lock and closes the lock file.
func (i *Index) Close() error {
	if i.lockFD == nil {
		return nil
	}
	_ = syscall.Flock(int(i.lockFD.Fd()), syscall.LOCK_UN)
	err := i.lockFD.Close()
	i.lockFD = nil
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

// Sentinel errors callers (notably `tkn-act logs`) can route on:
// "user-supplied id didn't match" becomes Usage(2) at the caller,
// while underlying I/O / corruption errors stay Env(3).
var (
	// ErrNoRuns is returned by Latest when the index is empty.
	ErrNoRuns = errors.New("no runs recorded")
	// ErrNotFound is returned by BySeq / ByULIDPrefix when nothing
	// matches the given identifier.
	ErrNotFound = errors.New("no matching run")
	// ErrAmbiguous is returned by ByULIDPrefix when more than one
	// entry shares the prefix.
	ErrAmbiguous = errors.New("ambiguous run identifier")
)

// BySeq returns the entry with the given sequence number. Returns
// ErrNotFound (wrapped) when no entry matches.
func (i *Index) BySeq(seq int) (IndexEntry, error) {
	for _, e := range i.data.Entries {
		if e.Seq == seq {
			return e, nil
		}
	}
	return IndexEntry{}, fmt.Errorf("no run with seq %d: %w", seq, ErrNotFound)
}

// ByULIDPrefix returns the entry whose ULID has the given prefix.
// Returns ErrNotFound (zero matches) or ErrAmbiguous (multiple
// matches) wrapped with context.
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
		return IndexEntry{}, fmt.Errorf("no run matching ulid prefix %q: %w", prefix, ErrNotFound)
	case 1:
		return found[0], nil
	default:
		return IndexEntry{}, fmt.Errorf("ulid prefix %q matches %d runs: %w", prefix, len(found), ErrAmbiguous)
	}
}

// Latest returns the most recently appended entry. Returns ErrNoRuns
// when the index is empty.
func (i *Index) Latest() (IndexEntry, error) {
	if len(i.data.Entries) == 0 {
		return IndexEntry{}, ErrNoRuns
	}
	return i.data.Entries[len(i.data.Entries)-1], nil
}

// All returns a copy of every entry, in order of appending (oldest first).
func (i *Index) All() []IndexEntry {
	out := make([]IndexEntry, len(i.data.Entries))
	copy(out, i.data.Entries)
	return out
}

// ReadIndexEntries returns the index.json entries without taking a
// lock or creating any files. Used by read-only commands like
// `tkn-act runs list` that mustn't touch the state-dir as a side
// effect. Returns an empty slice (not an error) when index.json is
// absent — "no runs recorded yet" is a valid state.
func ReadIndexEntries(stateDir string) ([]IndexEntry, error) {
	path := filepath.Join(stateDir, "index.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read index: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var f indexFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("decode index: %w", err)
	}
	return f.Entries, nil
}

// replaceEntries replaces the entries slice (in-memory) and flushes
// to disk. Used by retention GC to remove pruned rows in bulk under
// the existing flock.
func (i *Index) replaceEntries(entries []IndexEntry) error {
	i.data.Entries = entries
	return i.flush()
}

// flush atomically rewrites index.json: encode into a temp file in the
// same directory, fsync it, then rename onto the canonical name. The
// flock on i.f (held since OpenIndex) ensures only one writer races.
//
// Atomicity matters: an in-place Truncate+Write would leave index.json
// at size zero if the process crashed between the truncate and the
// final Write, and OpenIndex treats a zero-byte index.json as a fresh
// state-dir, silently losing every prior run record.
func (i *Index) flush() error {
	tmp, err := os.CreateTemp(i.dir, ".index-*.tmp")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(i.data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(i.dir, "index.json"))
}
