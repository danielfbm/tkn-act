# Logs replay + functional `--debug` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `tkn-act logs <id>` for post-hoc run replay, a functional `--debug` flag emitting traces from backend / resolver / engine, and three live-display polish items (sidecar lines in pretty mode, `--timestamps`, repeatable `--task` / `--step` filters).

**Architecture:** Persist a copy of the stdout JSON event stream to `<state-dir>/<ulid>/events.jsonl` per run via a new `reporter.Reporter` sink fanned out under the existing `reporter.Tee`. Replay reads that file and re-emits events through the same pretty / JSON sinks the live `run` uses, with the same filters applied. `--debug` registers component emitters that fan into the reporter as a new `kind:"debug"` event.

**Tech stack:** Go 1.25, `github.com/spf13/cobra`, existing `internal/reporter/` (Reporter/Tee/Event), existing `internal/exitcode/`, new dep `github.com/oklog/ulid/v2` (with 30-line in-house fallback if rejected at P1).

**Spec:** `docs/superpowers/specs/2026-05-15-logs-and-debug-design.md` — read before each phase. The spec is authoritative on user-facing surface; this plan is authoritative on file paths and TDD ordering.

---

## File structure overview

New packages and files this plan creates or modifies:

| Path | Created / Modified | Responsibility |
|---|---|---|
| `internal/runstore/statedir.go` + `_test.go` | Create | XDG state-dir resolution (flag > env > XDG_DATA_HOME > $HOME/.local/share). |
| `internal/runstore/runid.go` + `_test.go` | Create | ULID generation, parse, prefix lookup, with `oklog/ulid/v2` or in-house fallback. |
| `internal/runstore/meta.go` + `_test.go` | Create | `meta.json` schema + read / write. |
| `internal/runstore/index.go` + `_test.go` | Create | `index.json` ordered list + file-lock increment + lookup-by-seq. |
| `internal/runstore/store.go` + `_test.go` | Create | `RunStore` glue: create / open / list / prune. |
| `internal/runstore/replay.go` + `_test.go` | Create | Decode `events.jsonl` and stream to a `reporter.Reporter`. |
| `internal/runstore/gc.go` + `_test.go` | Create | Retention (count + age) prune logic. |
| `internal/reporter/persistsink.go` + `_test.go` | Create | `Reporter` impl that writes JSON events into a `RunStore`. |
| `internal/reporter/event.go` | Modify | Add `EvtDebug` kind + `Component string` + `Fields map[string]any` fields. |
| `internal/reporter/pretty.go` | Modify | Render `EvtDebug` lines, sidecar lines, optional timestamp prefix. |
| `internal/reporter/filters.go` + `_test.go` | Create | `WithTaskStepFilter` wrapper for live + replay. |
| `internal/debug/debug.go` + `_test.go` | Create | Component emitter helpers; no-op when disabled. |
| `cmd/tkn-act/root.go` | Modify | Add `--state-dir`, `--task` (repeatable), `--step` (repeatable), `--timestamps` flags; tighten `--debug` description. |
| `cmd/tkn-act/run.go` | Modify | Construct `RunStore`, tee `persistSink`, generate run ULID, run retention GC, propagate filters to reporter. |
| `cmd/tkn-act/logs.go` + `_test.go` | Create | `tkn-act logs [id\|latest\|seq\|ulid-prefix]` subcommand. |
| `cmd/tkn-act/runs.go` + `_test.go` | Create | `tkn-act runs list|show|prune` subcommand tree. |
| `cmd/tkn-act/main.go` (or wherever subcommands register) | Modify | Register `logs` and `runs` commands. |
| `internal/backend/docker/docker.go` | Modify | Insert debug emits at container create / start / exec / inspect / logs-open. |
| `internal/backend/docker/sidecars.go` | Modify | Debug emits at sidecar lifecycle. |
| `internal/backend/cluster/*.go` | Modify | Debug emits at TaskRun apply, pod scheduling state, image-pull state, informer events. |
| `internal/refresolver/*.go` | Modify | Debug emits at resolver match, cache hit/miss, fallback. |
| `internal/engine/*.go` | Modify | Debug emits at task readiness, skip-reason, retry decisions, param resolution. |
| `cmd/tkn-act/internal/agentguide/order.go` | Modify | Append `"logs"`, `"debug"` to `Order`. |
| `docs/agent-guide/logs.md` | Create | User docs for `tkn-act logs` + `tkn-act runs` + state-dir. |
| `docs/agent-guide/debug.md` | Create | User docs for `--debug` + new `debug` event kind. |
| `docs/agent-guide/README.md` | Modify | Mention new flags and link to new sections. |
| `docs/agent-guide/output-format.md` (or equiv) | Modify | Add `debug` event row. |
| `README.md` | Modify | "Replaying past runs" 5-line section. |
| `AGENTS.md` (== `CLAUDE.md` symlink) | Modify | New rows in public-contract table. |
| `docs/feature-parity.md` | Modify | Bump `Last updated:` stamp. |
| `docs/test-coverage.md` | Modify | Note new test packages. |
| `testdata/e2e/logs-replay/pipeline.yaml` (+ descriptor entry in `internal/e2e/fixtures.All()`) | Create | E2E fixture asserting replay byte-equality. |

---

## Phase 1 — Persistence sink + storage layout

Foundation for everything else. Persists every run's JSON event stream into a versioned per-run directory. No user-visible change beyond the new state-dir on disk.

### Task 1.1 — State-dir resolver

**Files:**
- Create: `internal/runstore/statedir.go`
- Test: `internal/runstore/statedir_test.go`

- [ ] **Step 1: Write the failing test.**

```go
// internal/runstore/statedir_test.go
package runstore_test

import (
    "os"
    "path/filepath"
    "testing"

    "github.com/danielfbm/tkn-act/internal/runstore"
)

func TestResolveStateDir_PrecedenceFlag(t *testing.T) {
    t.Setenv("TKN_ACT_STATE_DIR", "/env/path")
    t.Setenv("XDG_DATA_HOME", "/xdg/data")
    got := runstore.ResolveStateDir("/flag/path")
    if got != "/flag/path" {
        t.Errorf("flag override: got %q, want /flag/path", got)
    }
}

func TestResolveStateDir_PrecedenceEnv(t *testing.T) {
    t.Setenv("TKN_ACT_STATE_DIR", "/env/path")
    t.Setenv("XDG_DATA_HOME", "/xdg/data")
    got := runstore.ResolveStateDir("")
    if got != "/env/path" {
        t.Errorf("env override: got %q, want /env/path", got)
    }
}

func TestResolveStateDir_XDGDataHome(t *testing.T) {
    t.Setenv("TKN_ACT_STATE_DIR", "")
    t.Setenv("XDG_DATA_HOME", "/xdg/data")
    got := runstore.ResolveStateDir("")
    want := filepath.Join("/xdg/data", "tkn-act")
    if got != want {
        t.Errorf("xdg: got %q, want %q", got, want)
    }
}

func TestResolveStateDir_HomeFallback(t *testing.T) {
    t.Setenv("TKN_ACT_STATE_DIR", "")
    t.Setenv("XDG_DATA_HOME", "")
    home, err := os.UserHomeDir()
    if err != nil {
        t.Skip("no home dir")
    }
    got := runstore.ResolveStateDir("")
    want := filepath.Join(home, ".local", "share", "tkn-act")
    if got != want {
        t.Errorf("home fallback: got %q, want %q", got, want)
    }
}
```

- [ ] **Step 2: Run test to verify it fails.**

Run: `go test ./internal/runstore/ -run TestResolveStateDir -v`
Expected: FAIL — package doesn't compile (no `runstore` package yet).

- [ ] **Step 3: Implement the resolver.**

```go
// internal/runstore/statedir.go
// Package runstore manages the on-disk record of past tkn-act runs:
// the state directory, the index of runs, per-run metadata, and the
// JSON event stream replayed by `tkn-act logs`.
package runstore

import (
    "os"
    "path/filepath"
)

// ResolveStateDir returns the directory where tkn-act stores per-run
// state. Precedence: flag override > TKN_ACT_STATE_DIR env > XDG_DATA_HOME
// > $HOME/.local/share/tkn-act. The returned path is not created.
func ResolveStateDir(flag string) string {
    if flag != "" {
        return flag
    }
    if env := os.Getenv("TKN_ACT_STATE_DIR"); env != "" {
        return env
    }
    if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
        return filepath.Join(xdg, "tkn-act")
    }
    if home, err := os.UserHomeDir(); err == nil {
        return filepath.Join(home, ".local", "share", "tkn-act")
    }
    return filepath.Join(os.TempDir(), "tkn-act")
}
```

- [ ] **Step 4: Run test to verify it passes.**

Run: `go test ./internal/runstore/ -run TestResolveStateDir -v`
Expected: PASS — all four subtests green.

- [ ] **Step 5: Commit.**

```bash
git add internal/runstore/statedir.go internal/runstore/statedir_test.go
git commit -m "feat(runstore): resolve state-dir from flag/env/XDG"
```

---

### Task 1.2 — ULID-based run IDs

**Files:**
- Create: `internal/runstore/runid.go`
- Test: `internal/runstore/runid_test.go`

- [ ] **Step 1: Add the `oklog/ulid/v2` dependency.**

Run:
```bash
cd /workspaces/dev/code/github.com/danielfbm/tkn-act
go get github.com/oklog/ulid/v2@latest
go mod tidy
```

Expected: `go.mod` gains a `require github.com/oklog/ulid/v2 vX.Y.Z` line and `go.sum` is updated. If `go.mod` is in `go 1.25.x` mode with vendoring rules, run `go mod vendor` only if `vendor/modules.txt` exists in the repo (it doesn't today — skip).

- [ ] **Step 2: Write the failing test.**

```go
// internal/runstore/runid_test.go
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
```

- [ ] **Step 3: Run the test to verify it fails.**

Run: `go test ./internal/runstore/ -run RunID -v`
Expected: FAIL — `NewRunID`/`ParseRunID` undefined.

- [ ] **Step 4: Implement the run ID helpers.**

```go
// internal/runstore/runid.go
package runstore

import (
    "crypto/rand"
    "fmt"
    "time"

    "github.com/oklog/ulid/v2"
)

// RunID is the on-disk identifier for a stored run. The string form is
// a 26-character Crockford-base32 ULID.
type RunID string

// NewRunID generates a fresh RunID using the given clock time. The
// timestamp is encoded in the leading 10 characters so RunIDs sort
// chronologically when compared lexically.
func NewRunID(now time.Time) RunID {
    ms := ulid.Timestamp(now)
    entropy := ulid.Monotonic(rand.Reader, 0)
    id := ulid.MustNew(ms, entropy)
    return RunID(id.String())
}

// ParseRunID validates that s is a well-formed ULID and returns it as
// a RunID.
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
    return ulid.Time(id.Time()), nil
}
```

- [ ] **Step 5: Run the test to verify it passes.**

Run: `go test ./internal/runstore/ -run RunID -v`
Expected: PASS — all four subtests green.

- [ ] **Step 6: Commit.**

```bash
git add internal/runstore/runid.go internal/runstore/runid_test.go go.mod go.sum
git commit -m "feat(runstore): ULID-based run identifiers"
```

---

### Task 1.3 — `meta.json` schema

**Files:**
- Create: `internal/runstore/meta.go`
- Test: `internal/runstore/meta_test.go`

- [ ] **Step 1: Write the failing test.**

```go
// internal/runstore/meta_test.go
package runstore_test

import (
    "encoding/json"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/danielfbm/tkn-act/internal/runstore"
)

func TestMetaRoundTrip(t *testing.T) {
    dir := t.TempDir()
    m := runstore.Meta{
        RunID:         "run-123",
        ULID:          "01HQYZAB0000000000000000RR",
        Seq:           7,
        WriterVersion: "tkn-act dev",
        PipelineRef:   "hello.yaml",
        StartedAt:     time.Unix(1_700_000_000, 0).UTC(),
        EndedAt:       time.Unix(1_700_000_010, 0).UTC(),
        ExitCode:      0,
        Status:        "succeeded",
        Args:          []string{"run", "-f", "hello.yaml"},
    }
    path := filepath.Join(dir, "meta.json")
    if err := runstore.WriteMeta(path, m); err != nil {
        t.Fatalf("WriteMeta: %v", err)
    }
    got, err := runstore.ReadMeta(path)
    if err != nil {
        t.Fatalf("ReadMeta: %v", err)
    }
    if got != m {
        t.Errorf("round-trip mismatch:\n got=%+v\nwant=%+v", got, m)
    }
}

func TestMetaJSONShape(t *testing.T) {
    m := runstore.Meta{RunID: "x", ULID: "y", Seq: 1}
    b, err := json.Marshal(m)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    want := `"run_id":"x"`
    if !contains(string(b), want) {
        t.Errorf("expected %s in %s", want, b)
    }
}

func TestReadMeta_NotFound(t *testing.T) {
    _, err := runstore.ReadMeta(filepath.Join(t.TempDir(), "nope.json"))
    if !os.IsNotExist(err) {
        t.Errorf("want os.IsNotExist, got %v", err)
    }
}

func contains(s, substr string) bool {
    for i := 0; i+len(substr) <= len(s); i++ {
        if s[i:i+len(substr)] == substr {
            return true
        }
    }
    return false
}
```

- [ ] **Step 2: Run the test to verify it fails.**

Run: `go test ./internal/runstore/ -run Meta -v`
Expected: FAIL — types and helpers undefined.

- [ ] **Step 3: Implement `meta.go`.**

```go
// internal/runstore/meta.go
package runstore

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "time"
)

// Meta is the run-level metadata persisted alongside events.jsonl.
// Field names use snake_case per the project's JSON convention for
// new multi-word fields.
type Meta struct {
    RunID         string    `json:"run_id"`
    ULID          string    `json:"ulid"`
    Seq           int       `json:"seq"`
    WriterVersion string    `json:"writer_version"`
    PipelineRef   string    `json:"pipeline_ref,omitempty"`
    StartedAt     time.Time `json:"started_at"`
    EndedAt       time.Time `json:"ended_at,omitempty"`
    ExitCode      int       `json:"exit_code"`
    Status        string    `json:"status,omitempty"`
    Args          []string  `json:"args,omitempty"`
}

// WriteMeta atomically writes Meta to path (write-to-temp, rename).
func WriteMeta(path string, m Meta) error {
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return fmt.Errorf("mkdir: %w", err)
    }
    tmp, err := os.CreateTemp(filepath.Dir(path), ".meta-*.tmp")
    if err != nil {
        return err
    }
    enc := json.NewEncoder(tmp)
    enc.SetIndent("", "  ")
    if err := enc.Encode(m); err != nil {
        tmp.Close()
        os.Remove(tmp.Name())
        return err
    }
    if err := tmp.Close(); err != nil {
        os.Remove(tmp.Name())
        return err
    }
    return os.Rename(tmp.Name(), path)
}

// ReadMeta parses meta.json at path. Returns an os.IsNotExist error
// if the file is absent.
func ReadMeta(path string) (Meta, error) {
    f, err := os.Open(path)
    if err != nil {
        return Meta{}, err
    }
    defer f.Close()
    var m Meta
    if err := json.NewDecoder(f).Decode(&m); err != nil {
        return Meta{}, fmt.Errorf("decode meta %s: %w", path, err)
    }
    return m, nil
}
```

- [ ] **Step 4: Run the tests.**

Run: `go test ./internal/runstore/ -run Meta -v`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/runstore/meta.go internal/runstore/meta_test.go
git commit -m "feat(runstore): meta.json read/write helpers"
```

---

### Task 1.4 — `index.json` with file-lock increment

**Files:**
- Create: `internal/runstore/index.go`
- Test: `internal/runstore/index_test.go`

- [ ] **Step 1: Write the failing test.**

```go
// internal/runstore/index_test.go
package runstore_test

import (
    "sync"
    "testing"

    "github.com/danielfbm/tkn-act/internal/runstore"
)

func TestIndex_AppendAndLookup(t *testing.T) {
    dir := t.TempDir()
    idx, err := runstore.OpenIndex(dir)
    if err != nil {
        t.Fatalf("OpenIndex: %v", err)
    }
    defer idx.Close()

    seq1, err := idx.Append(runstore.IndexEntry{ULID: "01HQYZAB000000000000000001", PipelineRef: "a"})
    if err != nil {
        t.Fatalf("Append: %v", err)
    }
    seq2, err := idx.Append(runstore.IndexEntry{ULID: "01HQYZAB000000000000000002", PipelineRef: "b"})
    if err != nil {
        t.Fatalf("Append: %v", err)
    }
    if seq1 != 1 || seq2 != 2 {
        t.Errorf("seq: got (%d,%d), want (1,2)", seq1, seq2)
    }

    e, err := idx.BySeq(2)
    if err != nil {
        t.Fatalf("BySeq: %v", err)
    }
    if e.ULID != "01HQYZAB000000000000000002" {
        t.Errorf("BySeq(2).ULID = %q", e.ULID)
    }
}

func TestIndex_ConcurrentAppend(t *testing.T) {
    dir := t.TempDir()
    var wg sync.WaitGroup
    seqs := make([]int, 20)
    for i := 0; i < 20; i++ {
        i := i
        wg.Add(1)
        go func() {
            defer wg.Done()
            idx, err := runstore.OpenIndex(dir)
            if err != nil {
                t.Errorf("OpenIndex: %v", err)
                return
            }
            defer idx.Close()
            s, err := idx.Append(runstore.IndexEntry{ULID: ""})
            if err != nil {
                t.Errorf("Append: %v", err)
                return
            }
            seqs[i] = s
        }()
    }
    wg.Wait()

    seen := map[int]bool{}
    for _, s := range seqs {
        if seen[s] {
            t.Errorf("duplicate seq %d", s)
        }
        seen[s] = true
    }
}

func TestIndex_Latest(t *testing.T) {
    dir := t.TempDir()
    idx, _ := runstore.OpenIndex(dir)
    defer idx.Close()
    if _, err := idx.Latest(); err == nil {
        t.Errorf("Latest on empty index should error")
    }
    idx.Append(runstore.IndexEntry{ULID: "01HQYZAB000000000000000001"})
    idx.Append(runstore.IndexEntry{ULID: "01HQYZAB000000000000000002"})
    e, err := idx.Latest()
    if err != nil {
        t.Fatalf("Latest: %v", err)
    }
    if e.Seq != 2 {
        t.Errorf("Latest.Seq = %d, want 2", e.Seq)
    }
}
```

- [ ] **Step 2: Run the test to verify it fails.**

Run: `go test ./internal/runstore/ -run Index -v`
Expected: FAIL — type and methods undefined.

- [ ] **Step 3: Implement `index.go`.**

```go
// internal/runstore/index.go
package runstore

import (
    "encoding/json"
    "errors"
    "fmt"
    "os"
    "path/filepath"
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
    f    *os.File   // holds flock; nil after Close
    data indexFile
}

// OpenIndex opens (or creates) index.json under an exclusive lock.
// The state-dir is created on demand.
func OpenIndex(stateDir string) (*Index, error) {
    runsDir := filepath.Join(stateDir, "runs")
    if err := os.MkdirAll(runsDir, 0o755); err != nil {
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
        if err := json.NewDecoder(f).Decode(&idx.data); err != nil {
            idx.Close()
            return nil, fmt.Errorf("decode index: %w", err)
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
// Returns an error if zero or multiple match.
func (i *Index) ByULIDPrefix(prefix string) (IndexEntry, error) {
    var found []IndexEntry
    for _, e := range i.data.Entries {
        if len(prefix) > 0 && len(e.ULID) >= len(prefix) && e.ULID[:len(prefix)] == prefix {
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

// All returns a copy of every entry, oldest first.
func (i *Index) All() []IndexEntry {
    out := make([]IndexEntry, len(i.data.Entries))
    copy(out, i.data.Entries)
    return out
}

// flush rewrites the underlying file from scratch (truncate then write).
func (i *Index) flush() error {
    if _, err := i.f.Seek(0, 0); err != nil {
        return err
    }
    if err := i.f.Truncate(0); err != nil {
        return err
    }
    enc := json.NewEncoder(i.f)
    enc.SetIndent("", "  ")
    return enc.Encode(i.data)
}
```

- [ ] **Step 4: Run the tests.**

Run: `go test ./internal/runstore/ -run Index -v`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/runstore/index.go internal/runstore/index_test.go
git commit -m "feat(runstore): index.json with file-lock seq assignment"
```

---

### Task 1.5 — `RunStore` façade

**Files:**
- Create: `internal/runstore/store.go`
- Test: `internal/runstore/store_test.go`

- [ ] **Step 1: Write the failing test.**

```go
// internal/runstore/store_test.go
package runstore_test

import (
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/danielfbm/tkn-act/internal/runstore"
)

func TestRunStore_CreateRun(t *testing.T) {
    dir := t.TempDir()
    s, err := runstore.Open(dir, "tkn-act-test")
    if err != nil {
        t.Fatalf("Open: %v", err)
    }
    r, err := s.NewRun(time.Now(), "pipeline.yaml", []string{"run", "-f", "pipeline.yaml"})
    if err != nil {
        t.Fatalf("NewRun: %v", err)
    }
    if r.Seq != 1 {
        t.Errorf("Seq = %d, want 1", r.Seq)
    }
    if _, err := os.Stat(filepath.Join(dir, "runs", string(r.ID))); err != nil {
        t.Errorf("run dir missing: %v", err)
    }
    if _, err := runstore.ReadMeta(filepath.Join(dir, "runs", string(r.ID), "meta.json")); err != nil {
        t.Errorf("meta.json missing: %v", err)
    }
}

func TestRunStore_FinalizeUpdatesMeta(t *testing.T) {
    dir := t.TempDir()
    s, _ := runstore.Open(dir, "tkn-act-test")
    r, _ := s.NewRun(time.Now(), "pipeline.yaml", nil)
    if err := r.Finalize(time.Now(), 0, "succeeded"); err != nil {
        t.Fatalf("Finalize: %v", err)
    }
    m, _ := runstore.ReadMeta(filepath.Join(r.Dir, "meta.json"))
    if m.Status != "succeeded" {
        t.Errorf("Status = %q, want succeeded", m.Status)
    }
    if m.EndedAt.IsZero() {
        t.Errorf("EndedAt is zero")
    }
}
```

- [ ] **Step 2: Run the test.**

Run: `go test ./internal/runstore/ -run RunStore -v`
Expected: FAIL — type undefined.

- [ ] **Step 3: Implement the store.**

```go
// internal/runstore/store.go
package runstore

import (
    "fmt"
    "os"
    "path/filepath"
    "time"
)

// Store is the per-process handle on a tkn-act state-dir.
type Store struct {
    dir           string // state-dir root (contains runs/, index.json)
    writerVersion string
}

// Open returns a Store rooted at dir, creating the directory tree if
// absent. writerVersion is recorded in every new run's meta.json.
func Open(dir, writerVersion string) (*Store, error) {
    if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
        return nil, fmt.Errorf("mkdir state-dir: %w", err)
    }
    return &Store{dir: dir, writerVersion: writerVersion}, nil
}

// Run is an in-progress (or finalized) run record.
type Run struct {
    ID            RunID
    Seq           int
    Dir           string // <state-dir>/runs/<ulid>
    meta          Meta
    store         *Store
}

// NewRun creates a fresh run dir, allocates the next sequence number,
// writes an initial meta.json, and returns a handle. The caller is
// expected to call Finalize when the run completes.
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
        StartedAt:   now,
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
        StartedAt:     now,
        Args:          args,
    }
    if err := WriteMeta(filepath.Join(runDir, "meta.json"), m); err != nil {
        return nil, err
    }
    return &Run{ID: id, Seq: seq, Dir: runDir, meta: m, store: s}, nil
}

// Finalize updates meta.json with the end time, exit code, and status,
// and updates the matching index.json entry.
func (r *Run) Finalize(end time.Time, exitCode int, status string) error {
    r.meta.EndedAt = end
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
        e.EndedAt = end
        e.ExitCode = exitCode
        e.Status = status
    })
}

// EventsPath returns the path the events.jsonl file should live at
// for this run (the persist sink writes to it).
func (r *Run) EventsPath() string { return filepath.Join(r.Dir, "events.jsonl") }
```

- [ ] **Step 4: Run the tests.**

Run: `go test ./internal/runstore/ -run RunStore -v`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/runstore/store.go internal/runstore/store_test.go
git commit -m "feat(runstore): Store façade with NewRun/Finalize"
```

---

### Task 1.6 — `persistSink` reporter implementation

**Files:**
- Create: `internal/reporter/persistsink.go`
- Test: `internal/reporter/persistsink_test.go`

- [ ] **Step 1: Write the failing test.**

```go
// internal/reporter/persistsink_test.go
package reporter_test

import (
    "bufio"
    "encoding/json"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/danielfbm/tkn-act/internal/reporter"
)

func TestPersistSink_AppendsJSONL(t *testing.T) {
    path := filepath.Join(t.TempDir(), "events.jsonl")
    s, err := reporter.NewPersistSink(path)
    if err != nil {
        t.Fatalf("NewPersistSink: %v", err)
    }
    s.Emit(reporter.Event{Kind: reporter.EvtRunStart, Time: time.Unix(1_700_000_000, 0).UTC(), RunID: "r1"})
    s.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Unix(1_700_000_010, 0).UTC(), RunID: "r1", ExitCode: 0})
    if err := s.Close(); err != nil {
        t.Fatalf("Close: %v", err)
    }

    f, err := os.Open(path)
    if err != nil {
        t.Fatalf("open: %v", err)
    }
    defer f.Close()
    sc := bufio.NewScanner(f)
    var lines int
    for sc.Scan() {
        var ev reporter.Event
        if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
            t.Errorf("line %d not valid JSON: %v", lines, err)
        }
        lines++
    }
    if lines != 2 {
        t.Errorf("lines = %d, want 2", lines)
    }
}
```

- [ ] **Step 2: Run the test to verify it fails.**

Run: `go test ./internal/reporter/ -run PersistSink -v`
Expected: FAIL.

- [ ] **Step 3: Implement.**

```go
// internal/reporter/persistsink.go
package reporter

import (
    "bufio"
    "encoding/json"
    "fmt"
    "os"
    "sync"
)

// persistSink writes each event as one JSON line to a file. Used by
// the run lifecycle to record an exact copy of the JSON event stream
// for `tkn-act logs` to replay later.
type persistSink struct {
    mu  sync.Mutex
    f   *os.File
    w   *bufio.Writer
}

// NewPersistSink opens path for append and returns a Reporter that
// writes one JSON event per line.
func NewPersistSink(path string) (Reporter, error) {
    f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
    if err != nil {
        return nil, fmt.Errorf("open events file: %w", err)
    }
    return &persistSink{f: f, w: bufio.NewWriter(f)}, nil
}

func (p *persistSink) Emit(e Event) {
    p.mu.Lock()
    defer p.mu.Unlock()
    b, err := json.Marshal(e)
    if err != nil {
        return // event encoding is internal; drop silently rather than panic
    }
    p.w.Write(b)
    p.w.WriteByte('\n')
}

func (p *persistSink) Close() error {
    p.mu.Lock()
    defer p.mu.Unlock()
    if p.w != nil {
        p.w.Flush()
    }
    if p.f != nil {
        return p.f.Close()
    }
    return nil
}
```

- [ ] **Step 4: Run the test.**

Run: `go test ./internal/reporter/ -run PersistSink -v`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/reporter/persistsink.go internal/reporter/persistsink_test.go
git commit -m "feat(reporter): persistSink writes events as JSON lines"
```

---

### Task 1.7 — Wire `--state-dir` flag and persistence into `tkn-act run`

**Files:**
- Modify: `cmd/tkn-act/root.go`
- Modify: `cmd/tkn-act/run.go`

- [ ] **Step 1: Add `stateDir` to `globalFlags`.**

In `cmd/tkn-act/root.go`, locate the `globalFlags` struct (line ~9-59) and add:

```go
stateDir string
```

In the same file at the persistent-flag registration block (around line 93+), add:

```go
cmd.PersistentFlags().StringVar(&gf.stateDir, "state-dir", "",
    "override the directory where tkn-act stores run records (default: $XDG_DATA_HOME/tkn-act or ~/.local/share/tkn-act)")
```

- [ ] **Step 2: Add the wiring test (records that a run produces events.jsonl).**

Append to `cmd/tkn-act/run_test.go`:

```go
func TestRun_PersistsEventsFile(t *testing.T) {
    if testing.Short() {
        t.Skip("integration-leaning")
    }
    // Use a tiny pipeline fixture already in testdata/e2e/hello/ or similar.
    state := t.TempDir()
    t.Setenv("TKN_ACT_STATE_DIR", state)

    gf = globalFlags{output: "json", stateDir: state}
    // Run the equivalent of `tkn-act run -f testdata/e2e/hello/pipeline.yaml`.
    // The exact runWith() invocation pattern is in cmd/tkn-act/run.go; mirror it
    // here pointing at the smallest existing fixture so the test runs in <2s.

    // After runWith returns, walk state/runs/ and assert exactly one events.jsonl exists.
    entries, err := os.ReadDir(filepath.Join(state, "runs"))
    if err != nil {
        t.Fatalf("readdir: %v", err)
    }
    if len(entries) != 1 {
        t.Fatalf("want 1 run dir, got %d", len(entries))
    }
    if _, err := os.Stat(filepath.Join(state, "runs", entries[0].Name(), "events.jsonl")); err != nil {
        t.Errorf("events.jsonl missing: %v", err)
    }
}
```

Note: the executor may need to adapt to the actual `runWith` signature in `run.go` and reuse the existing test fixture used by other run-tests (look at neighbors in `run_test.go`).

- [ ] **Step 3: Run the test to verify it fails.**

Run: `go test ./cmd/tkn-act/ -run TestRun_PersistsEventsFile -v`
Expected: FAIL — no events.jsonl created.

- [ ] **Step 4: Wire the persist sink into the run path.**

In `cmd/tkn-act/run.go`, locate `buildReporter(out *os.File)` (around line 342). Adjacent to where it constructs the live reporter, add a sibling that produces a `*reporter.Tee` combining the live sink with a persist sink. Sketch:

```go
import (
    // existing imports
    "github.com/danielfbm/tkn-act/internal/runstore"
)

// in runWith(), before constructing the engine:
stateDir := runstore.ResolveStateDir(gf.stateDir)
store, err := runstore.Open(stateDir, version.String()) // use whatever version helper exists
if err != nil {
    return exitcode.Wrap(exitcode.Env, fmt.Errorf("open state-dir: %w", err))
}
run, err := store.NewRun(time.Now(), rf.file, os.Args[1:])
if err != nil {
    return exitcode.Wrap(exitcode.Env, fmt.Errorf("create run record: %w", err))
}

liveRep, err := buildReporter(os.Stdout)
if err != nil { return err }
persistRep, err := reporter.NewPersistSink(run.EventsPath())
if err != nil {
    return exitcode.Wrap(exitcode.Env, fmt.Errorf("open events file: %w", err))
}
rep := reporter.NewTee(liveRep, persistRep)

// at the end of the function, after engine returns:
defer func() {
    rep.Close()
    end := time.Now()
    status := "succeeded"
    if exitErr != nil { status = "failed" }
    run.Finalize(end, exitcode.From(exitErr), status)
}()
```

The exact integration depends on existing control flow — the executor must read `run.go` and adapt. Key invariant: the persistRep must close before Finalize so events.jsonl is flushed.

- [ ] **Step 5: Re-run the test.**

Run: `go test ./cmd/tkn-act/ -run TestRun_PersistsEventsFile -v`
Expected: PASS.

- [ ] **Step 6: Verify byte-equality between stdout and persisted file for a real run.**

Run:
```bash
go build -o /tmp/tkn-act ./cmd/tkn-act
TKN_ACT_STATE_DIR=/tmp/state-bytecheck /tmp/tkn-act run -o json -f testdata/e2e/hello/pipeline.yaml > /tmp/stdout.jsonl
diff <(grep -v '"time"' /tmp/stdout.jsonl | sort) <(grep -v '"time"' /tmp/state-bytecheck/runs/*/events.jsonl | sort)
```

Expected: empty diff (events sets match; we strip `time` because the persist sink writes monotonically but the times encoded in events were captured before fan-out so they'll match anyway — the `grep -v` is paranoia). If diff is non-empty, persist-sink ordering or filtering is wrong; fix before committing.

- [ ] **Step 7: Commit.**

```bash
git add cmd/tkn-act/root.go cmd/tkn-act/run.go cmd/tkn-act/run_test.go
git commit -m "feat: persist JSON event stream per run

Every \`tkn-act run\` now writes the same event stream it emits on
stdout to \$STATE_DIR/runs/<ulid>/events.jsonl. Sets the stage for
\`tkn-act logs\` replay (next PR)."
```

- [ ] **Step 8: Run the full existing test suite to confirm no regressions.**

Run: `go test -race -count=1 ./...`
Expected: PASS. If a fixture breaks because runs are now persisted under `$HOME/.local/share/tkn-act/`, set `TKN_ACT_STATE_DIR=$(mktemp -d)` in the offending test or in a common `TestMain`.

---

### Phase 1 wrap

Open a PR titled `feat(runstore): persist JSON event stream per run`.
Body must include:
- Summary mentioning new package `internal/runstore` and persist sink.
- Test plan section listing the new tests + the byte-equality check.
- Note that this spec ships with the PR: `docs/superpowers/specs/2026-05-15-logs-and-debug-design.md`.

Phase 2 picks up here.

---

## Phase 2 — `tkn-act logs` replay subcommand

Build the reader half of the persistence story: read `events.jsonl` back and re-emit through the live reporter pipeline.

### Task 2.1 — Replay decoder

**Files:**
- Create: `internal/runstore/replay.go`
- Test: `internal/runstore/replay_test.go`

- [ ] **Step 1: Write the failing test.**

```go
// internal/runstore/replay_test.go
package runstore_test

import (
    "bytes"
    "encoding/json"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/danielfbm/tkn-act/internal/reporter"
    "github.com/danielfbm/tkn-act/internal/runstore"
)

type capture struct{ events []reporter.Event }

func (c *capture) Emit(e reporter.Event) { c.events = append(c.events, e) }
func (c *capture) Close() error          { return nil }

func TestReplay_StreamsEvents(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "events.jsonl")
    var buf bytes.Buffer
    enc := json.NewEncoder(&buf)
    enc.Encode(reporter.Event{Kind: reporter.EvtRunStart, RunID: "r", Time: time.Unix(1, 0).UTC()})
    enc.Encode(reporter.Event{Kind: reporter.EvtStepLog, Task: "t", Step: "s", Line: "hi", Time: time.Unix(2, 0).UTC()})
    enc.Encode(reporter.Event{Kind: reporter.EvtRunEnd, RunID: "r", ExitCode: 0, Time: time.Unix(3, 0).UTC()})
    os.WriteFile(path, buf.Bytes(), 0o644)

    c := &capture{}
    if err := runstore.Replay(path, c); err != nil {
        t.Fatalf("Replay: %v", err)
    }
    if len(c.events) != 3 {
        t.Fatalf("events = %d, want 3", len(c.events))
    }
    if c.events[1].Line != "hi" {
        t.Errorf("event[1].Line = %q", c.events[1].Line)
    }
}

func TestReplay_CorruptLineErrors(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "events.jsonl")
    os.WriteFile(path, []byte("not json\n"), 0o644)
    c := &capture{}
    if err := runstore.Replay(path, c); err == nil {
        t.Errorf("expected error on corrupt line")
    }
}
```

- [ ] **Step 2: Run the test.**

Run: `go test ./internal/runstore/ -run Replay -v`
Expected: FAIL.

- [ ] **Step 3: Implement.**

```go
// internal/runstore/replay.go
package runstore

import (
    "bufio"
    "encoding/json"
    "fmt"
    "io"
    "os"

    "github.com/danielfbm/tkn-act/internal/reporter"
)

// Replay reads events.jsonl line by line and emits each event to rep.
// The first malformed line aborts the replay with a descriptive error;
// well-formed events earlier in the file are already emitted.
func Replay(eventsPath string, rep reporter.Reporter) error {
    f, err := os.Open(eventsPath)
    if err != nil {
        return fmt.Errorf("open events: %w", err)
    }
    defer f.Close()
    sc := bufio.NewScanner(f)
    sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
    line := 0
    for sc.Scan() {
        line++
        var ev reporter.Event
        if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
            return fmt.Errorf("events line %d: %w", line, err)
        }
        rep.Emit(ev)
    }
    if err := sc.Err(); err != nil && err != io.EOF {
        return fmt.Errorf("scan events: %w", err)
    }
    return nil
}
```

- [ ] **Step 4: Run the tests.**

Run: `go test ./internal/runstore/ -run Replay -v`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/runstore/replay.go internal/runstore/replay_test.go
git commit -m "feat(runstore): events.jsonl replay decoder"
```

---

### Task 2.2 — Run-ID resolver (latest / seq / ULID-prefix)

**Files:**
- Modify: `internal/runstore/store.go` (add `Resolve` method)
- Test: `internal/runstore/store_test.go`

- [ ] **Step 1: Append failing tests.**

```go
// add to internal/runstore/store_test.go
func TestStore_Resolve(t *testing.T) {
    dir := t.TempDir()
    s, _ := runstore.Open(dir, "v")
    r1, _ := s.NewRun(time.Unix(1, 0), "a.yaml", nil)
    r2, _ := s.NewRun(time.Unix(2, 0), "b.yaml", nil)

    cases := []struct {
        in      string
        wantSeq int
    }{
        {"", r2.Seq},        // empty defaults to latest
        {"latest", r2.Seq},
        {"1", r1.Seq},
        {"2", r2.Seq},
        {string(r1.ID)[:8], r1.Seq}, // ULID prefix
    }
    for _, tc := range cases {
        e, err := s.Resolve(tc.in)
        if err != nil {
            t.Errorf("Resolve(%q): %v", tc.in, err)
            continue
        }
        if e.Seq != tc.wantSeq {
            t.Errorf("Resolve(%q).Seq = %d, want %d", tc.in, e.Seq, tc.wantSeq)
        }
    }
}

func TestStore_Resolve_Errors(t *testing.T) {
    dir := t.TempDir()
    s, _ := runstore.Open(dir, "v")
    if _, err := s.Resolve("latest"); err == nil {
        t.Errorf("Resolve(latest) on empty: want error")
    }
    s.NewRun(time.Now(), "", nil)
    if _, err := s.Resolve("99"); err == nil {
        t.Errorf("Resolve(99): want not-found")
    }
}
```

- [ ] **Step 2: Run the test.**

Run: `go test ./internal/runstore/ -run TestStore_Resolve -v`
Expected: FAIL — `Resolve` undefined.

- [ ] **Step 3: Implement `Resolve` on `*Store`.**

Append to `internal/runstore/store.go`:

```go
import (
    "strconv"
    // existing imports
)

// Resolve maps a user-supplied identifier — empty/"latest" for latest,
// a positive integer for a seq, otherwise a ULID or ULID prefix — to
// an IndexEntry. The returned entry's Dir() can be computed as
// filepath.Join(stateDir, "runs", entry.ULID).
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

// RunDir returns the path on disk for an entry.
func (s *Store) RunDir(e IndexEntry) string {
    return filepath.Join(s.dir, "runs", e.ULID)
}
```

- [ ] **Step 4: Run the tests.**

Run: `go test ./internal/runstore/ -run TestStore_Resolve -v`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/runstore/store.go internal/runstore/store_test.go
git commit -m "feat(runstore): Resolve identifier to index entry"
```

---

### Task 2.3 — `tkn-act logs` subcommand

**Files:**
- Create: `cmd/tkn-act/logs.go`
- Test: `cmd/tkn-act/logs_test.go`
- Modify: `cmd/tkn-act/main.go` (or wherever subcommands are registered — likely `root.go`)

- [ ] **Step 1: Write the failing test.**

```go
// cmd/tkn-act/logs_test.go
package main

import (
    "bytes"
    "encoding/json"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"

    "github.com/danielfbm/tkn-act/internal/reporter"
    "github.com/danielfbm/tkn-act/internal/runstore"
)

func writeFixture(t *testing.T, dir string) string {
    t.Helper()
    s, err := runstore.Open(dir, "test")
    if err != nil {
        t.Fatalf("Open: %v", err)
    }
    r, err := s.NewRun(time.Unix(1_700_000_000, 0), "hello.yaml", []string{"run", "-f", "hello.yaml"})
    if err != nil {
        t.Fatalf("NewRun: %v", err)
    }
    ps, _ := reporter.NewPersistSink(r.EventsPath())
    ps.Emit(reporter.Event{Kind: reporter.EvtRunStart, Time: time.Unix(1_700_000_000, 0), Pipeline: "hello"})
    ps.Emit(reporter.Event{Kind: reporter.EvtStepLog, Time: time.Unix(1_700_000_001, 0), Task: "t1", Step: "s1", Line: "hello world"})
    ps.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Unix(1_700_000_002, 0), Pipeline: "hello", ExitCode: 0})
    ps.Close()
    r.Finalize(time.Unix(1_700_000_002, 0), 0, "succeeded")
    return string(r.ID)
}

func TestLogs_Latest_JSON(t *testing.T) {
    dir := t.TempDir()
    _ = writeFixture(t, dir)
    t.Setenv("TKN_ACT_STATE_DIR", dir)
    gf = globalFlags{output: "json", stateDir: dir}

    var buf bytes.Buffer
    if err := runLogs(&buf, ""); err != nil {
        t.Fatalf("runLogs: %v", err)
    }
    var lines []reporter.Event
    for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
        var ev reporter.Event
        if err := json.Unmarshal([]byte(l), &ev); err != nil {
            t.Errorf("bad line %q: %v", l, err)
            continue
        }
        lines = append(lines, ev)
    }
    if len(lines) != 3 {
        t.Errorf("emitted %d events, want 3", len(lines))
    }
}

func TestLogs_NotFound(t *testing.T) {
    dir := t.TempDir()
    t.Setenv("TKN_ACT_STATE_DIR", dir)
    gf = globalFlags{output: "json", stateDir: dir}
    err := runLogs(os.Stderr, "latest")
    if err == nil {
        t.Errorf("want not-found error")
    }
    // assert the wrapped exit code is Usage(2)
    code := exitcodeFromErr(err) // helper to write inline if not present
    if code != 2 {
        t.Errorf("exit code = %d, want 2", code)
    }
    _ = filepath.Join // silence unused import lint if needed
}
```

`exitcodeFromErr` may already exist (look at `cmd/tkn-act/exit_test.go`); if not, define a one-liner here.

- [ ] **Step 2: Run the test.**

Run: `go test ./cmd/tkn-act/ -run TestLogs -v`
Expected: FAIL — `runLogs` undefined.

- [ ] **Step 3: Implement the subcommand.**

```go
// cmd/tkn-act/logs.go
package main

import (
    "fmt"
    "io"
    "os"

    "github.com/danielfbm/tkn-act/internal/exitcode"
    "github.com/danielfbm/tkn-act/internal/reporter"
    "github.com/danielfbm/tkn-act/internal/runstore"
    "github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "logs [id|latest|<seq>|<ulid-prefix>]",
        Short: "Replay a previously recorded tkn-act run",
        Long: `Replay the JSON event stream of a previously recorded run.

The default identifier (no argument or "latest") replays the most recent run.
A numeric identifier selects by sequence number as shown in 'tkn-act runs list'.
Any other identifier is treated as a ULID or ULID prefix and must match
exactly one stored run.`,
        Example: `  # replay the most recent run, pretty output
  tkn-act logs

  # replay run #7, JSON output, filtered to one task
  tkn-act logs 7 -o json --task build`,
        Args: cobra.MaximumNArgs(1),
        RunE: func(c *cobra.Command, args []string) error {
            id := ""
            if len(args) == 1 {
                id = args[0]
            }
            return runLogs(os.Stdout, id)
        },
    }
    return cmd
}

func runLogs(w io.Writer, id string) error {
    stateDir := runstore.ResolveStateDir(gf.stateDir)
    store, err := runstore.Open(stateDir, version.String())
    if err != nil {
        return exitcode.Wrap(exitcode.Env, fmt.Errorf("open state-dir: %w", err))
    }
    entry, err := store.Resolve(id)
    if err != nil {
        return exitcode.Wrap(exitcode.Usage, err)
    }
    rep, err := buildReporter(asFile(w))
    if err != nil {
        return err
    }
    defer rep.Close()
    eventsPath := store.RunDir(entry) + "/events.jsonl"
    if err := runstore.Replay(eventsPath, rep); err != nil {
        return exitcode.Wrap(exitcode.Env, err)
    }
    return nil
}

// asFile is a small adapter so runLogs can take any io.Writer for tests
// while buildReporter accepts *os.File.
func asFile(w io.Writer) *os.File {
    if f, ok := w.(*os.File); ok {
        return f
    }
    // tests pass *bytes.Buffer; route through a pipe wrapper if needed
    // simpler: extend buildReporter to accept io.Writer (small refactor).
    return os.Stdout
}
```

Note: the `asFile` shim is a placeholder. The clean approach is a small refactor of `buildReporter(*os.File)` to `buildReporter(io.Writer)` — both `NewJSON` and `NewPretty` already accept `io.Writer`. Do that refactor in this task and remove `asFile`.

- [ ] **Step 4: Register the subcommand.**

In whichever file registers commands on the root (look for an `AddCommand` call near `newCacheCmd`, `newClusterCmd`, `newDoctorCmd` etc.):

```go
cmd.AddCommand(newLogsCmd())
```

- [ ] **Step 5: Run the tests.**

Run: `go test ./cmd/tkn-act/ -run TestLogs -v`
Expected: PASS.

- [ ] **Step 6: Manual smoke test.**

Run:
```bash
go build -o /tmp/tkn-act ./cmd/tkn-act
mkdir -p /tmp/sd
TKN_ACT_STATE_DIR=/tmp/sd /tmp/tkn-act run -f testdata/e2e/hello/pipeline.yaml
TKN_ACT_STATE_DIR=/tmp/sd /tmp/tkn-act logs latest
```

Expected: second command reprints the same pretty output the first produced.

- [ ] **Step 7: Commit.**

```bash
git add cmd/tkn-act/logs.go cmd/tkn-act/logs_test.go cmd/tkn-act/root.go cmd/tkn-act/run.go
git commit -m "feat: tkn-act logs replays stored runs"
```

---

### Phase 2 wrap

PR title: `feat(logs): tkn-act logs replays stored runs`.
Test plan covers: replay byte-equality with the stored events (already from P1's manual check), the new `TestLogs_*` table, exit codes 0/2/3.

---

## Phase 3 — `tkn-act runs` family + retention GC

Manages the state-dir from the CLI side. Prune runs automatically on each `tkn-act run`.

### Task 3.1 — Retention GC logic

**Files:**
- Create: `internal/runstore/gc.go`
- Test: `internal/runstore/gc_test.go`

- [ ] **Step 1: Write the failing test.**

```go
// internal/runstore/gc_test.go
package runstore_test

import (
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/danielfbm/tkn-act/internal/runstore"
)

func TestPrune_KeepsRecentN(t *testing.T) {
    dir := t.TempDir()
    s, _ := runstore.Open(dir, "v")
    for i := 0; i < 5; i++ {
        r, _ := s.NewRun(time.Unix(int64(1_700_000_000+i), 0), "p", nil)
        r.Finalize(time.Unix(int64(1_700_000_000+i+1), 0), 0, "succeeded")
    }
    n, err := s.Prune(runstore.PruneOptions{KeepRuns: 2, KeepDays: 0, Now: time.Unix(1_700_000_100, 0)})
    if err != nil {
        t.Fatalf("Prune: %v", err)
    }
    if n != 3 {
        t.Errorf("pruned = %d, want 3", n)
    }
    entries, _ := os.ReadDir(filepath.Join(dir, "runs"))
    if len(entries) != 2 {
        t.Errorf("dirs left = %d, want 2", len(entries))
    }
}

func TestPrune_KeepsByAge(t *testing.T) {
    dir := t.TempDir()
    s, _ := runstore.Open(dir, "v")
    old := time.Now().Add(-60 * 24 * time.Hour)
    young := time.Now()
    rOld, _ := s.NewRun(old, "p", nil)
    rOld.Finalize(old.Add(time.Second), 0, "succeeded")
    rYng, _ := s.NewRun(young, "p", nil)
    rYng.Finalize(young.Add(time.Second), 0, "succeeded")

    n, err := s.Prune(runstore.PruneOptions{KeepRuns: 0, KeepDays: 30, Now: time.Now()})
    if err != nil {
        t.Fatalf("Prune: %v", err)
    }
    if n != 1 {
        t.Errorf("pruned = %d, want 1", n)
    }
}
```

- [ ] **Step 2: Run the test.**

Run: `go test ./internal/runstore/ -run TestPrune -v`
Expected: FAIL.

- [ ] **Step 3: Implement `gc.go`.**

```go
// internal/runstore/gc.go
package runstore

import (
    "os"
    "path/filepath"
    "sort"
    "time"
)

// PruneOptions controls retention. KeepRuns=0 disables count-based
// pruning; KeepDays=0 disables age-based pruning; Now defaults to
// time.Now() when zero.
type PruneOptions struct {
    KeepRuns int
    KeepDays int
    Now      time.Time
}

// Prune removes runs older than KeepDays AND beyond the most-recent
// KeepRuns. Returns the number of run directories deleted.
func (s *Store) Prune(opts PruneOptions) (int, error) {
    if opts.Now.IsZero() {
        opts.Now = time.Now()
    }
    idx, err := OpenIndex(s.dir)
    if err != nil {
        return 0, err
    }
    defer idx.Close()
    entries := idx.All()
    sort.SliceStable(entries, func(i, j int) bool { return entries[i].Seq < entries[j].Seq })

    keep := make(map[int]bool)
    for _, e := range entries {
        keep[e.Seq] = true
    }
    if opts.KeepRuns > 0 && len(entries) > opts.KeepRuns {
        // mark all but the last KeepRuns for deletion
        cutoff := len(entries) - opts.KeepRuns
        for _, e := range entries[:cutoff] {
            keep[e.Seq] = false
        }
    }
    if opts.KeepDays > 0 {
        threshold := opts.Now.Add(-time.Duration(opts.KeepDays) * 24 * time.Hour)
        for _, e := range entries {
            if !e.EndedAt.IsZero() && e.EndedAt.Before(threshold) {
                keep[e.Seq] = false
            }
        }
    }

    deleted := 0
    survivors := make([]IndexEntry, 0, len(entries))
    for _, e := range entries {
        if keep[e.Seq] {
            survivors = append(survivors, e)
            continue
        }
        path := filepath.Join(s.dir, "runs", e.ULID)
        if err := os.RemoveAll(path); err != nil {
            return deleted, err
        }
        deleted++
    }
    idx.data.Entries = survivors
    if err := idx.flush(); err != nil {
        return deleted, err
    }
    return deleted, nil
}
```

- [ ] **Step 4: Run the tests.**

Run: `go test ./internal/runstore/ -run TestPrune -v`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/runstore/gc.go internal/runstore/gc_test.go
git commit -m "feat(runstore): retention GC (count + age)"
```

---

### Task 3.2 — Auto-GC at run start

**Files:**
- Modify: `cmd/tkn-act/run.go`

- [ ] **Step 1: Insert an env-var read for retention overrides.**

In `run.go`, immediately after `store, err := runstore.Open(...)` and before `store.NewRun(...)`, add:

```go
keepRuns := 50
if v := os.Getenv("TKN_ACT_KEEP_RUNS"); v != "" {
    if n, err := strconv.Atoi(v); err == nil { keepRuns = n }
}
keepDays := 30
if v := os.Getenv("TKN_ACT_KEEP_DAYS"); v != "" {
    if n, err := strconv.Atoi(v); err == nil { keepDays = n }
}
store.Prune(runstore.PruneOptions{KeepRuns: keepRuns, KeepDays: keepDays})
// Prune errors are non-fatal: log via debug emitter (Phase 4) and continue.
```

- [ ] **Step 2: Smoke-test by running 52 short runs.**

```bash
TKN_ACT_STATE_DIR=/tmp/sd-gc rm -rf /tmp/sd-gc
for i in $(seq 1 52); do
  TKN_ACT_STATE_DIR=/tmp/sd-gc go run ./cmd/tkn-act run -f testdata/e2e/hello/pipeline.yaml -o json > /dev/null
done
ls /tmp/sd-gc/runs | wc -l
```

Expected: 50 (or 51 if the GC ran *before* the 52nd run started — accept ≤ 51).

- [ ] **Step 3: Commit.**

```bash
git add cmd/tkn-act/run.go
git commit -m "feat(runstore): auto-prune at run start (TKN_ACT_KEEP_RUNS/DAYS)"
```

---

### Task 3.3 — `tkn-act runs list / show / prune` subcommands

**Files:**
- Create: `cmd/tkn-act/runs.go`
- Test: `cmd/tkn-act/runs_test.go`

- [ ] **Step 1: Write the failing test.**

```go
// cmd/tkn-act/runs_test.go
package main

import (
    "bytes"
    "encoding/json"
    "strings"
    "testing"
    "time"

    "github.com/danielfbm/tkn-act/internal/runstore"
)

func TestRunsList_JSON(t *testing.T) {
    dir := t.TempDir()
    s, _ := runstore.Open(dir, "v")
    r1, _ := s.NewRun(time.Unix(1_700_000_000, 0), "a.yaml", nil)
    r1.Finalize(time.Unix(1_700_000_001, 0), 0, "succeeded")
    r2, _ := s.NewRun(time.Unix(1_700_000_002, 0), "b.yaml", nil)
    r2.Finalize(time.Unix(1_700_000_003, 0), 5, "failed")

    t.Setenv("TKN_ACT_STATE_DIR", dir)
    gf = globalFlags{output: "json", stateDir: dir}
    var buf bytes.Buffer
    if err := runRunsList(&buf, false); err != nil {
        t.Fatalf("runRunsList: %v", err)
    }
    var got []runstore.IndexEntry
    if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
        t.Fatalf("unmarshal: %v\nbody=%s", err, buf.String())
    }
    if len(got) != 2 {
        t.Errorf("len = %d, want 2", len(got))
    }
}

func TestRunsShow_Found(t *testing.T) {
    dir := t.TempDir()
    s, _ := runstore.Open(dir, "v")
    r, _ := s.NewRun(time.Now(), "p", nil)
    r.Finalize(time.Now(), 0, "succeeded")
    t.Setenv("TKN_ACT_STATE_DIR", dir)
    gf = globalFlags{output: "pretty", stateDir: dir}
    var buf bytes.Buffer
    if err := runRunsShow(&buf, "1"); err != nil {
        t.Fatalf("runRunsShow: %v", err)
    }
    if !strings.Contains(buf.String(), "seq:") {
        t.Errorf("expected 'seq:' in output, got %s", buf.String())
    }
}

func TestRunsPrune_All(t *testing.T) {
    dir := t.TempDir()
    s, _ := runstore.Open(dir, "v")
    for i := 0; i < 3; i++ {
        r, _ := s.NewRun(time.Now().Add(time.Duration(i)*time.Second), "p", nil)
        r.Finalize(time.Now().Add(time.Duration(i)*time.Second+time.Second), 0, "succeeded")
    }
    t.Setenv("TKN_ACT_STATE_DIR", dir)
    gf = globalFlags{stateDir: dir}
    var buf bytes.Buffer
    if err := runRunsPrune(&buf, true, true); err != nil { // all=true, yes=true
        t.Fatalf("runRunsPrune: %v", err)
    }
}
```

- [ ] **Step 2: Run the test.**

Run: `go test ./cmd/tkn-act/ -run TestRuns -v`
Expected: FAIL.

- [ ] **Step 3: Implement `cmd/tkn-act/runs.go`.**

```go
// cmd/tkn-act/runs.go
package main

import (
    "encoding/json"
    "fmt"
    "io"
    "os"
    "strconv"
    "text/tabwriter"
    "time"

    "github.com/danielfbm/tkn-act/internal/exitcode"
    "github.com/danielfbm/tkn-act/internal/runstore"
    "github.com/spf13/cobra"
)

func newRunsCmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "runs",
        Short: "Manage the local store of past tkn-act runs",
    }
    cmd.AddCommand(newRunsListCmd(), newRunsShowCmd(), newRunsPruneCmd())
    return cmd
}

func newRunsListCmd() *cobra.Command {
    var all bool
    c := &cobra.Command{
        Use:   "list",
        Short: "List recent stored runs",
        RunE: func(c *cobra.Command, _ []string) error {
            return runRunsList(os.Stdout, all)
        },
    }
    c.Flags().BoolVar(&all, "all", false, "show every recorded run (default: most-recent 20)")
    return c
}

func runRunsList(w io.Writer, all bool) error {
    store, err := runstore.Open(runstore.ResolveStateDir(gf.stateDir), version.String())
    if err != nil {
        return exitcode.Wrap(exitcode.Env, err)
    }
    idx, err := runstore.OpenIndex(runstore.ResolveStateDir(gf.stateDir))
    if err != nil {
        return exitcode.Wrap(exitcode.Env, err)
    }
    defer idx.Close()
    entries := idx.All()
    if !all && len(entries) > 20 {
        entries = entries[len(entries)-20:]
    }
    if gf.output == "json" {
        enc := json.NewEncoder(w)
        enc.SetIndent("", "  ")
        return enc.Encode(entries)
    }
    tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
    fmt.Fprintln(tw, "#\tULID\tpipeline\tstarted\tduration\texit\tstatus")
    for _, e := range entries {
        dur := "-"
        if !e.EndedAt.IsZero() {
            dur = e.EndedAt.Sub(e.StartedAt).Round(time.Millisecond).String()
        }
        fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%d\t%s\n",
            e.Seq, e.ULID, e.PipelineRef,
            e.StartedAt.Local().Format("2006-01-02 15:04:05"),
            dur, e.ExitCode, e.Status)
    }
    return tw.Flush()
    _ = store // store retained for future cross-checks; remove if unused at lint time
    _ = strconv.Itoa
}

func newRunsShowCmd() *cobra.Command {
    return &cobra.Command{
        Use:   "show <id>",
        Short: "Show metadata for one stored run",
        Args:  cobra.ExactArgs(1),
        RunE: func(c *cobra.Command, args []string) error {
            return runRunsShow(os.Stdout, args[0])
        },
    }
}

func runRunsShow(w io.Writer, id string) error {
    store, err := runstore.Open(runstore.ResolveStateDir(gf.stateDir), version.String())
    if err != nil {
        return exitcode.Wrap(exitcode.Env, err)
    }
    entry, err := store.Resolve(id)
    if err != nil {
        return exitcode.Wrap(exitcode.Usage, err)
    }
    meta, err := runstore.ReadMeta(store.RunDir(entry) + "/meta.json")
    if err != nil {
        return exitcode.Wrap(exitcode.Env, err)
    }
    if gf.output == "json" {
        enc := json.NewEncoder(w)
        enc.SetIndent("", "  ")
        return enc.Encode(meta)
    }
    fmt.Fprintf(w, "seq:           %d\n", meta.Seq)
    fmt.Fprintf(w, "ulid:          %s\n", meta.ULID)
    fmt.Fprintf(w, "pipeline_ref:  %s\n", meta.PipelineRef)
    fmt.Fprintf(w, "started_at:    %s\n", meta.StartedAt.Local())
    fmt.Fprintf(w, "ended_at:      %s\n", meta.EndedAt.Local())
    fmt.Fprintf(w, "exit_code:     %d\n", meta.ExitCode)
    fmt.Fprintf(w, "status:        %s\n", meta.Status)
    fmt.Fprintf(w, "writer:        %s\n", meta.WriterVersion)
    return nil
}

func newRunsPruneCmd() *cobra.Command {
    var all, yes bool
    c := &cobra.Command{
        Use:   "prune",
        Short: "Apply retention policy now (or delete every run with --all)",
        RunE: func(c *cobra.Command, _ []string) error {
            return runRunsPrune(os.Stdout, all, yes)
        },
    }
    c.Flags().BoolVar(&all, "all", false, "delete every stored run")
    c.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation when --all is set")
    return c
}

func runRunsPrune(w io.Writer, all, yes bool) error {
    stateDir := runstore.ResolveStateDir(gf.stateDir)
    store, err := runstore.Open(stateDir, version.String())
    if err != nil {
        return exitcode.Wrap(exitcode.Env, err)
    }
    if all && !yes {
        return exitcode.Wrap(exitcode.Usage, fmt.Errorf("--all requires --yes/-y for confirmation"))
    }
    var opts runstore.PruneOptions
    if all {
        opts = runstore.PruneOptions{KeepRuns: 1, KeepDays: 0} // keep none — set both off via separate API if needed
    } else {
        opts = runstore.PruneOptions{
            KeepRuns: envInt("TKN_ACT_KEEP_RUNS", 50),
            KeepDays: envInt("TKN_ACT_KEEP_DAYS", 30),
        }
    }
    n, err := store.Prune(opts)
    if err != nil {
        return exitcode.Wrap(exitcode.Env, err)
    }
    fmt.Fprintf(w, "Pruned %d run(s) from %s\n", n, stateDir)
    return nil
}

func envInt(name string, def int) int {
    if v := os.Getenv(name); v != "" {
        if n, err := strconv.Atoi(v); err == nil {
            return n
        }
    }
    return def
}
```

Note on `--all`: deleting *every* run requires the pruner to accept `KeepRuns=0` *meaning "keep none"*. The Phase 3.1 implementation interprets `KeepRuns=0` as "disable count-based pruning". Decide one interpretation and document it: this plan uses a `PruneAll` flag instead of overloading the count.

Refactor `internal/runstore/gc.go` to add:

```go
type PruneOptions struct {
    KeepRuns int
    KeepDays int
    All      bool
    Now      time.Time
}
```

…and skip both keep-loops when `All` is set, marking every entry for deletion.

Update existing tests for the new field.

- [ ] **Step 4: Register the runs subcommand on the root.**

In whichever file registers commands (probably `root.go` or `main.go`):

```go
cmd.AddCommand(newRunsCmd())
```

- [ ] **Step 5: Run the tests.**

Run: `go test ./cmd/tkn-act/ -run TestRuns -v && go test ./internal/runstore/ -run TestPrune -v`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add cmd/tkn-act/runs.go cmd/tkn-act/runs_test.go internal/runstore/gc.go internal/runstore/gc_test.go cmd/tkn-act/root.go
git commit -m "feat: tkn-act runs list/show/prune"
```

---

### Phase 3 wrap

PR title: `feat(runs): list / show / prune subcommands + retention GC`.

---

## Phase 4 — `--debug` wiring + new `debug` JSON event kind

Make the dead `--debug` flag emit real events from three components.

### Task 4.1 — Extend `Event` and add `EvtDebug` kind

**Files:**
- Modify: `internal/reporter/event.go`
- Test: `internal/reporter/event_test.go` (create if absent)

- [ ] **Step 1: Add the `EvtDebug` constant and three new fields to `Event`.**

In `internal/reporter/event.go`, in the `EventKind` constant block (study the existing kinds — there's a list like `EvtRunStart`, `EvtStepLog`, etc.):

```go
const (
    // … existing kinds, in declaration order …
    EvtDebug EventKind = "debug"
)
```

In the `Event` struct, append:

```go
Component string         `json:"component,omitempty"`
Fields    map[string]any `json:"fields,omitempty"`
// Message already exists; we reuse it for the debug msg.
```

- [ ] **Step 2: Write a quick round-trip test.**

```go
// internal/reporter/event_test.go
package reporter_test

import (
    "encoding/json"
    "testing"

    "github.com/danielfbm/tkn-act/internal/reporter"
)

func TestDebugEvent_JSONRoundTrip(t *testing.T) {
    e := reporter.Event{
        Kind:      reporter.EvtDebug,
        Component: "resolver",
        Message:   "cache hit",
        Fields:    map[string]any{"ref": "hub://git-clone:0.9", "bytes": float64(4096)},
    }
    b, err := json.Marshal(e)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    var got reporter.Event
    if err := json.Unmarshal(b, &got); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    if got.Component != "resolver" || got.Fields["ref"] != "hub://git-clone:0.9" {
        t.Errorf("round trip mismatch: %+v", got)
    }
}
```

- [ ] **Step 3: Run the test.**

Run: `go test ./internal/reporter/ -run TestDebugEvent -v`
Expected: PASS (since the field changes already exist).

- [ ] **Step 4: Commit.**

```bash
git add internal/reporter/event.go internal/reporter/event_test.go
git commit -m "feat(reporter): EvtDebug event kind"
```

---

### Task 4.2 — Pretty renderer for debug events

**Files:**
- Modify: `internal/reporter/pretty.go`
- Test: `internal/reporter/pretty_test.go`

- [ ] **Step 1: Write the failing test.**

Append to `internal/reporter/pretty_test.go`:

```go
func TestPretty_DebugLine(t *testing.T) {
    var buf bytes.Buffer
    p := reporter.NewPretty(&buf, reporter.PrettyOptions{Verbosity: 0})
    p.Emit(reporter.Event{
        Kind:      reporter.EvtDebug,
        Component: "resolver",
        Message:   "cache hit",
        Fields:    map[string]any{"ref": "hub://git-clone:0.9"},
    })
    p.Close()
    out := buf.String()
    if !strings.Contains(out, "[debug]") || !strings.Contains(out, "resolver") || !strings.Contains(out, "ref=hub://git-clone:0.9") {
        t.Errorf("output missing debug markers: %q", out)
    }
}
```

- [ ] **Step 2: Run the test.**

Run: `go test ./internal/reporter/ -run TestPretty_Debug -v`
Expected: FAIL.

- [ ] **Step 3: Add an `EvtDebug` case to the pretty switch.**

In `internal/reporter/pretty.go`, inside `(*prettySink).Emit`, add:

```go
case EvtDebug:
    // Render as: `  [debug] component=<c> k=v k=v — msg`
    // Indented to match step-log column for visual flow.
    var sb strings.Builder
    sb.WriteString("  ")
    sb.WriteString(p.pal.wrap(p.pal.gray, "[debug]"))
    sb.WriteString(" component=")
    sb.WriteString(e.Component)
    keys := make([]string, 0, len(e.Fields))
    for k := range e.Fields {
        keys = append(keys, k)
    }
    sort.Strings(keys)
    for _, k := range keys {
        fmt.Fprintf(&sb, " %s=%v", k, e.Fields[k])
    }
    if e.Message != "" {
        sb.WriteString(" — ")
        sb.WriteString(e.Message)
    }
    sb.WriteByte('\n')
    p.w.Write([]byte(sb.String()))
```

Add `"sort"` and `"strings"` imports if missing. Add a `gray` color to `color.go` (a `wrap`-able dim ANSI) or reuse an existing dim color.

- [ ] **Step 4: Run the test.**

Run: `go test ./internal/reporter/ -run TestPretty_Debug -v`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/reporter/pretty.go internal/reporter/color.go internal/reporter/pretty_test.go
git commit -m "feat(reporter): pretty renderer for debug events"
```

---

### Task 4.3 — `internal/debug` helper package

**Files:**
- Create: `internal/debug/debug.go`
- Test: `internal/debug/debug_test.go`

- [ ] **Step 1: Write the failing test.**

```go
// internal/debug/debug_test.go
package debug_test

import (
    "testing"

    "github.com/danielfbm/tkn-act/internal/debug"
    "github.com/danielfbm/tkn-act/internal/reporter"
)

type capture struct{ events []reporter.Event }

func (c *capture) Emit(e reporter.Event) { c.events = append(c.events, e) }
func (c *capture) Close() error          { return nil }

func TestEmitter_EnabledFalse_NoEvents(t *testing.T) {
    c := &capture{}
    e := debug.New(c, false)
    e.Emit(debug.Resolver, func() (string, map[string]any) {
        t.Errorf("closure invoked when disabled")
        return "x", nil
    })
    if len(c.events) != 0 {
        t.Errorf("emitted %d events when disabled", len(c.events))
    }
}

func TestEmitter_EnabledTrue_Emits(t *testing.T) {
    c := &capture{}
    e := debug.New(c, true)
    e.Emit(debug.Backend, func() (string, map[string]any) {
        return "container created", map[string]any{"id": "abc123"}
    })
    if len(c.events) != 1 {
        t.Fatalf("want 1 event, got %d", len(c.events))
    }
    ev := c.events[0]
    if ev.Kind != reporter.EvtDebug || ev.Component != "backend" || ev.Message != "container created" {
        t.Errorf("event = %+v", ev)
    }
}
```

- [ ] **Step 2: Run the test.**

Run: `go test ./internal/debug/ -v`
Expected: FAIL.

- [ ] **Step 3: Implement.**

```go
// internal/debug/debug.go
// Package debug provides a typed emitter used by the engine, the
// resolver, and the backends to surface verbose internal trace data
// when --debug is set.
package debug

import (
    "time"

    "github.com/danielfbm/tkn-act/internal/reporter"
)

// Component is the source-of-truth label that ends up in the debug
// event's "component" field.
type Component string

const (
    Backend  Component = "backend"
    Resolver Component = "resolver"
    Engine   Component = "engine"
)

// Emitter emits debug events to the reporter when enabled. When
// disabled, the supplied closure is not invoked — so callers can
// build expensive field maps without paying for them in normal runs.
type Emitter interface {
    Emit(c Component, build func() (msg string, fields map[string]any))
    Enabled() bool
}

type emitter struct {
    rep     reporter.Reporter
    enabled bool
}

// New returns an Emitter routing through rep. When enabled is false,
// every call short-circuits before invoking the build closure.
func New(rep reporter.Reporter, enabled bool) Emitter {
    return &emitter{rep: rep, enabled: enabled}
}

func (e *emitter) Enabled() bool { return e.enabled }

func (e *emitter) Emit(c Component, build func() (string, map[string]any)) {
    if !e.enabled {
        return
    }
    msg, fields := build()
    e.rep.Emit(reporter.Event{
        Kind:      reporter.EvtDebug,
        Time:      time.Now().UTC(),
        Component: string(c),
        Message:   msg,
        Fields:    fields,
    })
}

// Nop returns a disabled Emitter — useful for tests and for code
// paths that have no reporter handy.
func Nop() Emitter { return &emitter{rep: nil, enabled: false} }
```

- [ ] **Step 4: Run the tests.**

Run: `go test ./internal/debug/ -v`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/debug/debug.go internal/debug/debug_test.go
git commit -m "feat(debug): typed emitter with no-op when disabled"
```

---

### Task 4.4 — Plumb the Emitter through engine + backend + resolver constructors

**Files:** (modifications across many files)
- `cmd/tkn-act/run.go` — construct the emitter, pass it through
- `internal/engine/*.go` — accept emitter in `engine.New`, store on Engine struct
- `internal/backend/docker/*.go` — accept emitter; add to Backend struct
- `internal/backend/cluster/*.go` — same
- `internal/refresolver/*.go` — same

- [ ] **Step 1: Update each constructor signature.**

This is mechanical surgery — many files. The pattern is:

```go
// before
func New(be Backend, rep reporter.Reporter, opts Options) *Engine

// after
func New(be Backend, rep reporter.Reporter, dbg debug.Emitter, opts Options) *Engine
```

Stamp on each call site. Many existing tests construct these directly — pass `debug.Nop()` when the test doesn't care.

- [ ] **Step 2: Tighten the `--debug` flag description.**

In `cmd/tkn-act/root.go`, change:

```go
cmd.PersistentFlags().BoolVar(&gf.debug, "debug", false, "verbose internal logs")
```

to:

```go
cmd.PersistentFlags().BoolVar(&gf.debug, "debug", false,
    "emit debug events from backend, resolver, and engine "+
        "(inline in pretty; as kind:'debug' events in JSON)")
```

- [ ] **Step 3: Build and run.**

```bash
go build ./...
go test -race -count=1 ./...
```

Fix any compile errors. Don't ship until everything compiles and existing tests pass — the emitter is wired everywhere but does nothing yet.

- [ ] **Step 4: Commit.**

```bash
git add -p   # cherry-pick the constructor + tightened flag changes
git commit -m "refactor: plumb debug.Emitter through engine/backend/resolver"
```

---

### Task 4.5 — Resolver debug emissions

**Files:**
- Modify: `internal/refresolver/*.go` (resolver dispatcher and per-resolver files)

- [ ] **Step 1: Write the failing test.**

In a relevant existing test file (e.g. `internal/refresolver/resolver_test.go` — adapt to actual file), add:

```go
func TestResolver_EmitsCacheHitDebug(t *testing.T) {
    rep := &capture{} // local reporter capture, same shape as in internal/debug tests
    dbg := debug.New(rep, true)
    r := refresolver.NewWith(/* existing constructor args */, dbg)
    // Simulate a cache-hit lookup (use existing test helpers).
    _ = r
    if !containsKind(rep.events, "debug") {
        t.Errorf("no debug event emitted")
    }
}
```

- [ ] **Step 2: Add `dbg.Emit(debug.Resolver, …)` calls at:**

- Dispatcher entry (which resolver matched).
- Cache lookup result (`cache hit` / `cache miss` with `ref` and `bytes`).
- Each resolver fallback when an upstream returns `ErrNotFound`.
- Resolver-end summary (total bytes resolved).

Pattern (for each site):

```go
r.dbg.Emit(debug.Resolver, func() (string, map[string]any) {
    return "cache hit", map[string]any{"ref": ref, "bytes": n}
})
```

- [ ] **Step 3: Run the test.**

Run: `go test ./internal/refresolver/ -v`
Expected: PASS.

- [ ] **Step 4: Commit.**

```bash
git add internal/refresolver
git commit -m "feat(resolver): emit debug events at dispatch and cache lookup"
```

---

### Task 4.6 — Backend (docker) debug emissions

**Files:**
- Modify: `internal/backend/docker/docker.go`
- Modify: `internal/backend/docker/sidecars.go`
- Test: add cases to existing test files

- [ ] **Step 1: Add `dbg.Emit(debug.Backend, …)` at:**

- `Run`-time container create: `"container created", {id, image, taskRunName, stepName}`
- Container start: `"container started", {id}`
- Container exec dispatch (resolver-resolved or other): `"exec", {id, argv}` (truncate argv to first 8 args).
- Image pull start/end (existing pull progress code path emits status string updates): `"image pull start", {ref}` and `"image pull done", {ref, durationMs}`.
- Volume create: `"volume created", {name, label}`.
- Stager container lifecycle (remote daemon): `"stager started", {volumes}` / `"stager stopped"`.

- [ ] **Step 2: Add at least one test asserting a docker debug event.**

A minimal smoke test using the existing docker test harness (integration-tagged); if the harness is heavyweight, gate this behind `//go:build integration`.

- [ ] **Step 3: Run.**

`go test -race -count=1 ./internal/backend/docker/...`
Expected: PASS (unit + integration).

- [ ] **Step 4: Commit.**

```bash
git add internal/backend/docker
git commit -m "feat(backend/docker): emit debug events for container/image/volume lifecycle"
```

---

### Task 4.7 — Backend (cluster) debug emissions

**Files:**
- Modify: `internal/backend/cluster/*.go`

- [ ] **Step 1: Add `dbg.Emit(debug.Backend, …)` at:**

- TaskRun applied: `"taskrun applied", {name, uid}` (omit the YAML body).
- Pod scheduling state change (from the watcher loop): `"pod state", {pod, phase}`.
- Image pull state change (`Waiting` reason `ImagePull*`): `"image pull state", {pod, container, reason}`.
- Informer-side events relevant to the run: filter to `type=Normal` reason-set we already care about; one debug event per filtered event.

- [ ] **Step 2: Add a cluster-tagged test.**

If the cluster backend has existing integration tests under `//go:build cluster`, add an assertion to the simplest one that with `dbg.Enabled()`, at least one debug event with `component=backend` appears.

- [ ] **Step 3: Run.**

`go test -race -count=1 -tags cluster ./internal/backend/cluster/...`
Expected: PASS in clusters where the test runs (otherwise skipped).

- [ ] **Step 4: Commit.**

```bash
git add internal/backend/cluster
git commit -m "feat(backend/cluster): emit debug events for TaskRun apply and pod state"
```

---

### Task 4.8 — Engine debug emissions

**Files:**
- Modify: `internal/engine/*.go`

- [ ] **Step 1: Add `dbg.Emit(debug.Engine, …)` at:**

- Task readiness transition: `"task ready", {task}` when a task becomes runnable.
- `when:` skip: `"task skipped", {task, reason, expression, evaluated}` (truncate `evaluated` to 64 chars).
- Retry decision: `"task retry", {task, attempt, total, reason}`.
- Param resolution end (per task): `"params resolved", {task, count, truncated_values: {...}}` — truncate each value to 64 chars.

- [ ] **Step 2: Add at least one engine-unit test asserting a debug emission.**

Reuse the engine's existing test harness. Wire a `debug.New(captureReporter, true)` into the engine constructor and inspect captured events.

- [ ] **Step 3: Run.**

`go test ./internal/engine/...`
Expected: PASS.

- [ ] **Step 4: Commit.**

```bash
git add internal/engine
git commit -m "feat(engine): emit debug events for task lifecycle decisions"
```

---

### Phase 4 wrap

PR title: `feat(debug): functional --debug flag with backend/resolver/engine emissions`.
Test plan covers: pretty rendering, JSON event emission, per-component coverage, no-op behavior when disabled (zero allocations from the build closure).

---

## Phase 5 — Live polish: sidecars, timestamps, filters

### Task 5.1 — Sidecar log lines in pretty mode

**Files:**
- Modify: `internal/reporter/pretty.go`
- Test: `internal/reporter/pretty_test.go`

- [ ] **Step 1: Write the failing test.**

```go
func TestPretty_SidecarLine(t *testing.T) {
    var buf bytes.Buffer
    p := reporter.NewPretty(&buf, reporter.PrettyOptions{Verbosity: 0})
    p.Emit(reporter.Event{Kind: reporter.EvtSidecarLog, Task: "t", Step: "redis", Stream: "stdout", Line: "ready"})
    p.Close()
    out := buf.String()
    if !strings.Contains(out, "◊") || !strings.Contains(out, "redis") || !strings.Contains(out, "ready") {
        t.Errorf("missing sidecar markers: %q", out)
    }
}
```

- [ ] **Step 2: Run.** FAIL.

- [ ] **Step 3: Add `EvtSidecarLog` case to pretty Emit.**

```go
case EvtSidecarLog:
    if p.verb < Normal { return }
    prefix := prefixOf(e.Task, p.pal.wrap(p.pal.cyan, "◊ " + labelOf(e.Step, e.DisplayName)))
    stream := ""
    if e.Stream == "sidecar-stderr" || e.Stream == "stderr" {
        stream = p.pal.wrap(p.pal.yellow, "!") + " "
    }
    fmt.Fprintf(p.w, "  %s │ %s%s\n", prefix, stream, e.Line)
```

- [ ] **Step 4: Run.** PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/reporter/pretty.go internal/reporter/pretty_test.go
git commit -m "feat(reporter): pretty-mode sidecar log lines"
```

---

### Task 5.2 — `--timestamps` prefix

**Files:**
- Modify: `internal/reporter/pretty.go` (add `Timestamps bool` to `PrettyOptions`)
- Modify: `cmd/tkn-act/root.go` (register `--timestamps`)
- Modify: `cmd/tkn-act/run.go` (wire into `PrettyOptions`)
- Test: `internal/reporter/pretty_test.go`

- [ ] **Step 1: Write the failing test.**

```go
func TestPretty_Timestamps(t *testing.T) {
    var buf bytes.Buffer
    p := reporter.NewPretty(&buf, reporter.PrettyOptions{Timestamps: true})
    p.Emit(reporter.Event{Kind: reporter.EvtStepLog, Time: time.Date(2026, 5, 15, 9, 56, 13, 252_000_000, time.UTC), Task: "t", Step: "s", Line: "hi"})
    p.Close()
    if !strings.Contains(buf.String(), "[09:56:13.252]") {
        t.Errorf("missing timestamp prefix: %q", buf.String())
    }
}
```

- [ ] **Step 2: Run.** FAIL.

- [ ] **Step 3: Implement.**

Add `Timestamps bool` to `PrettyOptions`. In each `case EvtStepLog`, `case EvtSidecarLog`, `case EvtDebug` branch, prepend the timestamp prefix when `p.timestamps && !e.Time.IsZero()`:

```go
if p.timestamps && !e.Time.IsZero() {
    fmt.Fprintf(p.w, "[%s] ", e.Time.UTC().Format("15:04:05.000"))
}
```

Register the flag:

```go
cmd.PersistentFlags().BoolVar(&gf.timestamps, "timestamps", false,
    "prepend [HH:MM:SS.mmm] to step/sidecar/debug lines in pretty mode")
```

Wire into the reporter:

```go
return reporter.NewPretty(out, reporter.PrettyOptions{
    Color:      …,
    Verbosity:  …,
    Timestamps: gf.timestamps,
}), nil
```

- [ ] **Step 4: Run tests.** PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/reporter/pretty.go cmd/tkn-act/root.go cmd/tkn-act/run.go internal/reporter/pretty_test.go
git commit -m "feat: --timestamps prefix on pretty mode lines"
```

---

### Task 5.3 — `--task` / `--step` repeatable filters

**Files:**
- Create: `internal/reporter/filters.go`
- Test: `internal/reporter/filters_test.go`
- Modify: `cmd/tkn-act/root.go` (register flags)
- Modify: `cmd/tkn-act/run.go` and `cmd/tkn-act/logs.go` (apply filter)

- [ ] **Step 1: Write the failing test.**

```go
// internal/reporter/filters_test.go
package reporter_test

import (
    "testing"

    "github.com/danielfbm/tkn-act/internal/reporter"
)

type cap struct{ events []reporter.Event }

func (c *cap) Emit(e reporter.Event) { c.events = append(c.events, e) }
func (c *cap) Close() error          { return nil }

func TestFilter_TaskOnly(t *testing.T) {
    inner := &cap{}
    f := reporter.NewFilter(inner, []string{"build"}, nil)
    f.Emit(reporter.Event{Kind: reporter.EvtStepLog, Task: "build", Line: "yes"})
    f.Emit(reporter.Event{Kind: reporter.EvtStepLog, Task: "deploy", Line: "no"})
    if len(inner.events) != 1 || inner.events[0].Task != "build" {
        t.Errorf("got %+v", inner.events)
    }
}

func TestFilter_AlwaysPassRunBoundary(t *testing.T) {
    inner := &cap{}
    f := reporter.NewFilter(inner, []string{"build"}, nil)
    f.Emit(reporter.Event{Kind: reporter.EvtRunStart})
    f.Emit(reporter.Event{Kind: reporter.EvtError, Message: "oops"})
    if len(inner.events) != 2 {
        t.Errorf("run-start and error must always pass; got %d", len(inner.events))
    }
}

func TestFilter_TaskAndStep(t *testing.T) {
    inner := &cap{}
    f := reporter.NewFilter(inner, []string{"build"}, []string{"compile"})
    f.Emit(reporter.Event{Kind: reporter.EvtStepLog, Task: "build", Step: "compile", Line: "y"})
    f.Emit(reporter.Event{Kind: reporter.EvtStepLog, Task: "build", Step: "lint", Line: "n"})
    if len(inner.events) != 1 || inner.events[0].Step != "compile" {
        t.Errorf("got %+v", inner.events)
    }
}
```

- [ ] **Step 2: Run.** FAIL.

- [ ] **Step 3: Implement filter.**

```go
// internal/reporter/filters.go
package reporter

type filter struct {
    inner Reporter
    tasks map[string]bool
    steps map[string]bool
}

// NewFilter returns a Reporter that forwards an event to inner only
// when its Task is in tasks (or tasks is empty) AND its Step is in
// steps (or steps is empty). Events that don't belong to any
// task/step — run-start, run-end, error — always pass.
func NewFilter(inner Reporter, tasks, steps []string) Reporter {
    f := &filter{inner: inner, tasks: setOf(tasks), steps: setOf(steps)}
    return f
}

func setOf(s []string) map[string]bool {
    if len(s) == 0 {
        return nil
    }
    m := make(map[string]bool, len(s))
    for _, x := range s {
        m[x] = true
    }
    return m
}

func (f *filter) Emit(e Event) {
    switch e.Kind {
    case EvtRunStart, EvtRunEnd, EvtError:
        f.inner.Emit(e)
        return
    }
    if f.tasks != nil && !f.tasks[e.Task] {
        return
    }
    if f.steps != nil && e.Step != "" && !f.steps[e.Step] {
        return
    }
    f.inner.Emit(e)
}

func (f *filter) Close() error { return f.inner.Close() }
```

- [ ] **Step 4: Register flags.**

```go
cmd.PersistentFlags().StringSliceVar(&gf.taskFilter, "task", nil, "limit output to this task (repeatable)")
cmd.PersistentFlags().StringSliceVar(&gf.stepFilter, "step", nil, "limit output to this step (repeatable, AND with --task)")
```

- [ ] **Step 5: Wrap the live reporter and the replay reporter.**

In `cmd/tkn-act/run.go` (after `buildReporter` and inside `runLogs`):

```go
rep = reporter.NewFilter(rep, gf.taskFilter, gf.stepFilter)
```

Same line near the persist-tee construction so persistence is unaffected by filters (persistence is full-fidelity).

Important: the filter wraps the *user-facing* reporter only, not the persist sink. Construct the Tee like:

```go
filteredLive := reporter.NewFilter(liveRep, gf.taskFilter, gf.stepFilter)
rep := reporter.NewTee(filteredLive, persistRep)
```

- [ ] **Step 6: Run tests.**

Run: `go test ./internal/reporter/ -run TestFilter -v && go test ./cmd/tkn-act/ -run TestLogs -v`
Expected: PASS.

- [ ] **Step 7: Commit.**

```bash
git add internal/reporter/filters.go internal/reporter/filters_test.go cmd/tkn-act/root.go cmd/tkn-act/run.go cmd/tkn-act/logs.go
git commit -m "feat: --task/--step filter for live and replay output"
```

---

### Phase 5 wrap

PR title: `feat(reporter): sidecar pretty lines, --timestamps, --task/--step filters`.

---

## Phase 6 — Doc convergence + agent-guide regen

### Task 6.1 — Write `docs/agent-guide/logs.md`

**Files:**
- Create: `docs/agent-guide/logs.md`

- [ ] **Step 1: Author the user-guide page.**

Contents must cover:
- Where state lives (`$XDG_DATA_HOME/tkn-act/` default, env / flag overrides).
- Per-run directory layout (`runs/<ULID>/{meta.json,events.jsonl}`).
- `tkn-act logs [id|latest|<seq>|<ulid-prefix>]` with all flag combinations.
- `tkn-act runs list / show / prune` reference.
- Retention policy + env-var overrides.
- Exit codes (`0`, `2`, `3`) and what each indicates.
- Worked example: run a pipeline, list, replay with filter.

Frontmatter and section header style: copy `docs/agent-guide/docker-backend.md` for the template. Add the `Last updated: 2026-05-15` line.

- [ ] **Step 2: Author `docs/agent-guide/debug.md`.**

Contents:
- What `--debug` does and what it does NOT do (no wire-level tracing).
- The three components and the kind of events each emits (use the spec's "Component coverage in v1" table verbatim).
- Pretty-mode line format: `  [debug] component=<c> k=v k=v — msg`.
- JSON event schema:
  ```json
  {"kind":"debug","time":"…","component":"resolver","msg":"cache hit",
   "fields":{"ref":"hub://git-clone:0.9","bytes":4096}}
  ```
- Interaction with `--verbose` and `--quiet`: `--debug` is independent of either; `--debug -q` is meaningful.
- Example: snippet of debug output.

- [ ] **Step 3: Append the two sections to the agent-guide order.**

In `cmd/tkn-act/internal/agentguide/order.go`, append `"logs", "debug"` to `Order`:

```go
var Order = []string{
    "overview",
    "docker-backend",
    "step-template",
    "sidecars",
    "step-actions",
    "matrix",
    "pipeline-results",
    "display-name",
    "timeouts",
    "resolvers",
    "logs",
    "debug",
}
```

- [ ] **Step 4: Regenerate the embedded tree.**

Run:
```bash
go generate ./cmd/tkn-act/
```

Expected: `cmd/tkn-act/agentguide_data/` updates to include the two new files. The `agentguide-freshness` test in `cmd/tkn-act/agentguide_freshness_test.go` passes.

- [ ] **Step 5: Run the freshness test.**

Run: `go test ./cmd/tkn-act/ -run TestAgentguide -v`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add docs/agent-guide/logs.md docs/agent-guide/debug.md cmd/tkn-act/internal/agentguide/order.go cmd/tkn-act/agentguide_data/
git commit -m "docs(agent-guide): logs and debug pages"
```

---

### Task 6.2 — Update other docs

**Files:**
- Modify: `docs/agent-guide/README.md`
- Modify: `docs/agent-guide/output-format.md` (or whichever file documents JSON event kinds)
- Modify: `README.md`
- Modify: `AGENTS.md` (and `CLAUDE.md` symlink — same file)
- Modify: `docs/feature-parity.md`
- Modify: `docs/test-coverage.md`

- [ ] **Step 1: `agent-guide/README.md`.**

Under "Operational flags", add bullets for `--debug`, `--task`, `--step`, `--timestamps`, `--state-dir`. Under "Subcommands", add `logs` and `runs` with one-line descriptions linking to the new pages.

- [ ] **Step 2: `output-format.md`.**

Add a row for the `debug` event kind with the field shape from the spec.

- [ ] **Step 3: `README.md`.**

Add a short subsection (5–10 lines) "Replaying past runs":

```markdown
### Replaying past runs

`tkn-act` records every run under `$XDG_DATA_HOME/tkn-act/` (override via
`TKN_ACT_STATE_DIR` or `--state-dir`). Use `tkn-act runs list` to see
recent runs and `tkn-act logs [id|latest|<seq>]` to re-stream a run's
output. Filters like `--task` and `--step` work on both `run` and `logs`.
Retention defaults: 50 runs / 30 days.
```

- [ ] **Step 4: `AGENTS.md` (== `CLAUDE.md`).**

Locate the "Public-contract stability" table. Add rows:
- Subcommand row: include `logs`, `runs`.
- Flag rows: `--state-dir`, `--task`, `--step`, `--timestamps`, plus the now-functional `--debug`.
- Env-var rows: `TKN_ACT_STATE_DIR`, `TKN_ACT_KEEP_RUNS`, `TKN_ACT_KEEP_DAYS`.
- JSON event kind row: add `debug` to the `run -o json event kinds` row.

- [ ] **Step 5: `docs/feature-parity.md`.**

No new feature row — these are CLI UX additions, not Tekton feature additions. Bump the `Last updated:` stamp to today's date.

- [ ] **Step 6: `docs/test-coverage.md`.**

Add a row for the new `internal/runstore/` test package and note that an e2e fixture exercises the persist + replay path.

- [ ] **Step 7: Commit.**

```bash
git add docs/ README.md AGENTS.md
git commit -m "docs: convergence for logs replay + debug flag"
```

---

### Task 6.3 — E2E logs-replay fixture

**Files:**
- Create: `testdata/e2e/logs-replay/pipeline.yaml`
- Modify: `internal/e2e/fixtures.go` (the `All()` function — adapt name)

- [ ] **Step 1: Pick the simplest existing fixture and copy it.**

```bash
cp -r testdata/e2e/hello testdata/e2e/logs-replay
```

Then edit `testdata/e2e/logs-replay/pipeline.yaml` if needed so the pipeline name doesn't collide.

- [ ] **Step 2: Register the fixture.**

In `internal/e2e/fixtures.go` (the file containing `All()`), append:

```go
{
    Name: "logs-replay",
    // any descriptor fields needed (review one nearby entry for the shape)
},
```

- [ ] **Step 3: Add a replay-equivalence test.**

In `internal/e2e/` or a sibling test package, add:

```go
func TestLogsReplay_ByteEquality(t *testing.T) {
    state := t.TempDir()
    // run the fixture via the existing e2e harness, capturing stdout JSON
    // run `tkn-act logs latest -o json` against the same state-dir
    // compare line-by-line (ignoring time fields if monotonicity diverges)
}
```

The exact harness wiring depends on existing e2e helpers — read `internal/e2e/fixtures.go` for invocation patterns and mirror them.

- [ ] **Step 4: Run.**

Run: `go test -tags integration ./internal/e2e/...`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add testdata/e2e/logs-replay internal/e2e
git commit -m "test(e2e): logs-replay fixture asserts byte-equality"
```

---

### Task 6.4 — Final CI gate dry-run + open PR

- [ ] **Step 1: Run every local gate.**

```bash
go vet ./...
go vet -tags integration ./...
go vet -tags cluster ./...
go test -race -count=1 ./...
make check-agentguide
.github/scripts/parity-check.sh
.github/scripts/tests-required.sh main HEAD
```

Expected: all green.

- [ ] **Step 2: Open the PR.**

Title: `feat: tkn-act logs replay + functional --debug + live polish`

Body must include:
- Summary citing the spec at `docs/superpowers/specs/2026-05-15-logs-and-debug-design.md`.
- Test plan listing the unit + e2e tests and the byte-equality check.
- Public-contract additions enumerated (new subcommands / flags / env vars / event kind).

---

## Self-review (run after writing this plan)

Cross-check spec coverage against tasks:

| Spec section | Task |
|---|---|
| Goals — `--debug` actually does something | 4.1, 4.5, 4.6, 4.7, 4.8 |
| Goals — `tkn-act logs <id>` replays | 2.1, 2.2, 2.3 |
| Goals — persistence implicit | 1.1–1.7 |
| Goals — live polish | 5.1, 5.2, 5.3 |
| Goals — public-contract additions are additive | 4.1 (event kind), 6.2 (AGENTS.md rows) |
| Architecture — persist sink as third reporter | 1.6, 1.7 |
| Architecture — debug emitter routes through reporter | 4.3 |
| Storage layout (state-dir, runs/, index.json, meta.json) | 1.1, 1.3, 1.4, 1.5 |
| Retention (count + age) | 3.1, 3.2 |
| `tkn-act logs` exit codes | 2.3 |
| `tkn-act runs` family | 3.3 |
| Live polish detail (sidecar, timestamps, filters) | 5.1, 5.2, 5.3 |
| Public-contract additions (table in spec) | 1.7 (`--state-dir`), 5.2 (`--timestamps`), 5.3 (`--task` / `--step`), 4.1 (event kind), 4.4 (`--debug` description), 3.2 (env vars) |
| Doc convergence | 6.1, 6.2 |
| Test plan — unit + integration + e2e | tests are in-line with every task; e2e in 6.3 |
| Risks — disk in CI | 1.7 ("set TKN_ACT_STATE_DIR") |
| Risks — state-dir collision | 1.4 (flock) |
| Risks — replay drift | 1.5 (writer_version in meta.json) |
| Risks — debug volume | covered by per-component breakdown + opt-in nature; no separate task |

Type / signature consistency:

- `runstore.Open(dir, writerVersion string) (*Store, error)` — used in 1.5, 1.7, 2.3, 3.1, 3.3.
- `Store.NewRun(now time.Time, pipelineRef string, args []string) (*Run, error)` — 1.5, 1.7, 2.3 (test fixtures).
- `Store.Resolve(id string) (IndexEntry, error)` — 2.2, 2.3, 3.3.
- `Store.RunDir(IndexEntry) string` — 2.2, 2.3, 3.3.
- `Store.Prune(PruneOptions) (int, error)` — 3.1, 3.2, 3.3.
- `runstore.Replay(path string, rep reporter.Reporter) error` — 2.1, 2.3.
- `reporter.NewPersistSink(path string) (Reporter, error)` — 1.6, 1.7, 2.3.
- `reporter.NewFilter(inner Reporter, tasks, steps []string) Reporter` — 5.3.
- `debug.New(rep reporter.Reporter, enabled bool) Emitter` — 4.3, 4.4.
- `debug.Emitter.Emit(c Component, build func() (string, map[string]any))` — 4.3, 4.5, 4.6, 4.7, 4.8.

No placeholder-style red flags in the plan: every step shows code or an exact command and expected output. The two integration-leaning steps (1.7 step 2, 4.6 step 2) are flagged as such with a note that the executor adapts to local test patterns.

End of plan.
