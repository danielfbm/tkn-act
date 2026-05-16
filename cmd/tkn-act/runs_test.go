package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/exitcode"
	"github.com/danielfbm/tkn-act/internal/runstore"
)

// runsFixture writes two finalized runs (one succeeded, one failed)
// into the given dir.
func runsFixture(t *testing.T, dir string) {
	t.Helper()
	s, err := runstore.Open(dir, "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	r1, _ := s.NewRun(time.Unix(1_700_000_000, 0), "a.yaml", nil)
	r1.Finalize(time.Unix(1_700_000_001, 0), 0, "succeeded")
	r2, _ := s.NewRun(time.Unix(1_700_000_002, 0), "b.yaml", nil)
	r2.Finalize(time.Unix(1_700_000_003, 0), 5, "failed")
}

func TestRunsList_JSON(t *testing.T) {
	dir := t.TempDir()
	runsFixture(t, dir)
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
	if got[1].ExitCode != 5 || got[1].Status != "failed" {
		t.Errorf("second entry = %+v", got[1])
	}
}

func TestRunsList_PrettyTable(t *testing.T) {
	dir := t.TempDir()
	runsFixture(t, dir)
	gf = globalFlags{output: "pretty", stateDir: dir}

	var buf bytes.Buffer
	if err := runRunsList(&buf, false); err != nil {
		t.Fatalf("runRunsList: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"a.yaml", "b.yaml", "succeeded", "failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("pretty output missing %q:\n%s", want, out)
		}
	}
}

func TestRunsList_Empty_StateDirAbsent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-existed")
	gf = globalFlags{output: "json", stateDir: dir}
	var buf bytes.Buffer
	if err := runRunsList(&buf, false); err != nil {
		t.Fatalf("runRunsList: %v", err)
	}
	// Empty state-dir is not an error condition; we emit []/no rows.
	if strings.TrimSpace(buf.String()) != "[]" {
		t.Errorf("empty list JSON = %q, want []", buf.String())
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("runs list created state-dir (%v); must be read-only", statErr)
	}
}

func TestRunsList_TruncatesTo20ByDefault(t *testing.T) {
	dir := t.TempDir()
	s, _ := runstore.Open(dir, "v")
	for i := 0; i < 25; i++ {
		r, _ := s.NewRun(time.Unix(int64(1_700_000_000+i), 0), "p", nil)
		r.Finalize(time.Unix(int64(1_700_000_000+i)+1, 0), 0, "succeeded")
	}
	gf = globalFlags{output: "json", stateDir: dir}
	var buf bytes.Buffer
	runRunsList(&buf, false)
	var got []runstore.IndexEntry
	json.Unmarshal(buf.Bytes(), &got)
	if len(got) != 20 {
		t.Errorf("default truncation = %d, want 20", len(got))
	}
	// Truncation keeps the MOST RECENT entries.
	if got[len(got)-1].Seq != 25 {
		t.Errorf("last entry seq = %d, want 25", got[len(got)-1].Seq)
	}

	// --all shows everything.
	buf.Reset()
	runRunsList(&buf, true)
	json.Unmarshal(buf.Bytes(), &got)
	if len(got) != 25 {
		t.Errorf("--all = %d, want 25", len(got))
	}
}

func TestRunsShow_HumanAndJSON(t *testing.T) {
	dir := t.TempDir()
	runsFixture(t, dir)

	gf = globalFlags{output: "pretty", stateDir: dir}
	var buf bytes.Buffer
	if err := runRunsShow(&buf, "1"); err != nil {
		t.Fatalf("runRunsShow: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"seq:", "ulid:", "a.yaml", "succeeded"} {
		if !strings.Contains(out, want) {
			t.Errorf("pretty output missing %q:\n%s", want, out)
		}
	}

	gf.output = "json"
	buf.Reset()
	if err := runRunsShow(&buf, "1"); err != nil {
		t.Fatalf("runRunsShow JSON: %v", err)
	}
	var meta runstore.Meta
	if err := json.Unmarshal(buf.Bytes(), &meta); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%s", err, buf.String())
	}
	if meta.PipelineRef != "a.yaml" {
		t.Errorf("meta.PipelineRef = %q, want a.yaml", meta.PipelineRef)
	}
}

func TestRunsShow_NotFound(t *testing.T) {
	dir := t.TempDir()
	gf = globalFlags{output: "pretty", stateDir: dir}
	err := runRunsShow(new(bytes.Buffer), "99")
	if err == nil {
		t.Fatalf("want error")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want Usage(2)", got)
	}
}

func TestRunsPrune_AppliesPolicy(t *testing.T) {
	dir := t.TempDir()
	s, _ := runstore.Open(dir, "v")
	for i := 0; i < 5; i++ {
		r, _ := s.NewRun(time.Unix(int64(1_700_000_000+i), 0), "p", nil)
		r.Finalize(time.Unix(int64(1_700_000_000+i)+1, 0), 0, "succeeded")
	}
	t.Setenv("TKN_ACT_KEEP_RUNS", "2")
	t.Setenv("TKN_ACT_KEEP_DAYS", "0")
	gf = globalFlags{stateDir: dir}
	var buf bytes.Buffer
	if err := runRunsPrune(&buf, false, false); err != nil {
		t.Fatalf("runRunsPrune: %v", err)
	}
	if !strings.Contains(buf.String(), "Pruned 3") {
		t.Errorf("output should report 3 pruned; got %q", buf.String())
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "runs"))
	if len(entries) != 2 {
		t.Errorf("dirs = %d, want 2", len(entries))
	}
}

func TestRunsPrune_All_RequiresYes(t *testing.T) {
	dir := t.TempDir()
	runsFixture(t, dir)
	gf = globalFlags{stateDir: dir}
	err := runRunsPrune(new(bytes.Buffer), true, false)
	if err == nil {
		t.Fatalf("--all without --yes must error")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want Usage(2)", got)
	}
}

func TestRunsPrune_YesWithoutAll_Errors(t *testing.T) {
	dir := t.TempDir()
	runsFixture(t, dir)
	gf = globalFlags{stateDir: dir}
	err := runRunsPrune(new(bytes.Buffer), false, true)
	if err == nil {
		t.Fatalf("--yes without --all must error")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want Usage(2)", got)
	}
}

func TestRunsList_NoSideEffects(t *testing.T) {
	// `runs list` against a non-existent state-dir must NOT create
	// anything on disk. Same invariant for an existing-but-empty dir.
	dir := filepath.Join(t.TempDir(), "never-existed")
	gf = globalFlags{output: "json", stateDir: dir}
	var buf bytes.Buffer
	if err := runRunsList(&buf, false); err != nil {
		t.Fatalf("runRunsList: %v", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("runs list created state-dir as side effect (%v)", statErr)
	}

	// After a single `run` happens, the state-dir is created. But
	// subsequent `runs list` invocations must not create new files
	// (no index.lock).
	pre, _ := runstore.Open(dir, "v")
	r, _ := pre.NewRun(time.Now(), "p", nil)
	r.Finalize(time.Now(), 0, "succeeded")
	before := must(os.ReadDir(dir))
	if err := runRunsList(&buf, false); err != nil {
		t.Fatalf("runRunsList: %v", err)
	}
	after := must(os.ReadDir(dir))
	if len(after) != len(before) {
		t.Errorf("runs list created %d new file(s)", len(after)-len(before))
		// Report what's new for debug.
		seen := map[string]bool{}
		for _, e := range before {
			seen[e.Name()] = true
		}
		for _, e := range after {
			if !seen[e.Name()] {
				t.Errorf("  new: %s", e.Name())
			}
		}
	}
}

func TestRunsShow_EmptyPositional_Errors(t *testing.T) {
	gf = globalFlags{stateDir: t.TempDir()}
	cmd := newRunsCmd()
	cmd.SetArgs([]string{"show", ""})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("want error on empty positional")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit = %d, want Usage(2)", got)
	}
}

func TestRunsPrune_Pluralization(t *testing.T) {
	dir := t.TempDir()
	s, _ := runstore.Open(dir, "v")
	// 1 run + KeepRuns=0 (disable) + All=false: nothing pruned.
	r, _ := s.NewRun(time.Now(), "p", nil)
	r.Finalize(time.Now(), 0, "succeeded")
	t.Setenv("TKN_ACT_KEEP_RUNS", "0")
	t.Setenv("TKN_ACT_KEEP_DAYS", "0")
	gf = globalFlags{stateDir: dir}
	var buf bytes.Buffer
	runRunsPrune(&buf, false, false)
	if !strings.Contains(buf.String(), "Pruned 0 runs") {
		t.Errorf("plural for 0: %q", buf.String())
	}

	// 1 run + KeepRuns=0 + All=true: prunes the 1 finalized run.
	buf.Reset()
	if err := runRunsPrune(&buf, true, true); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if !strings.Contains(buf.String(), "Pruned 1 run ") {
		t.Errorf("singular for 1: %q", buf.String())
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestRunsPrune_AllWithYes(t *testing.T) {
	dir := t.TempDir()
	runsFixture(t, dir)
	gf = globalFlags{stateDir: dir}
	var buf bytes.Buffer
	if err := runRunsPrune(&buf, true, true); err != nil {
		t.Fatalf("runRunsPrune: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "runs"))
	if len(entries) != 0 {
		t.Errorf("dirs after --all = %d, want 0", len(entries))
	}
}
