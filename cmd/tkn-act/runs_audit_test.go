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

func restoreGF(t *testing.T) {
	t.Helper()
	saved := gf
	t.Cleanup(func() { gf = saved })
}

// E8: `runs prune -o json` emits a JSON object, not prose.
func TestRunsPrune_JSON(t *testing.T) {
	restoreGF(t)
	dir := t.TempDir()
	runsFixture(t, dir)
	gf = globalFlags{output: "json", stateDir: dir}
	var buf bytes.Buffer
	if err := runRunsPrune(&buf, true, true); err != nil {
		t.Fatal(err)
	}
	var got runsPruneResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if got.Pruned != 2 || got.StateDir != dir {
		t.Errorf("got %+v", got)
	}

	gf = globalFlags{output: "json", stateDir: filepath.Join(dir, "absent")}
	buf.Reset()
	if err := runRunsPrune(&buf, false, false); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil || got.Pruned != 0 {
		t.Fatalf("absent state dir: %v %s", err, buf.String())
	}
}

// E6: a crashed (never finalized, old) run is listed with
// status "unknown" and without a bogus zero ended_at.
func TestRunsList_AbandonedRunStatusUnknown(t *testing.T) {
	restoreGF(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	id := string(runstore.NewRunID(time.Now().Add(-48 * time.Hour)))
	idx := `{"next_seq":2,"entries":[{"seq":1,"ulid":"` + id + `","pipeline_ref":"p","started_at":"` +
		time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339) + `","exit_code":0}]}`
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	gf = globalFlags{output: "json", stateDir: dir}
	var buf bytes.Buffer
	if err := runRunsList(&buf, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "0001-01-01") {
		t.Errorf("zero time in output: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"status": "unknown"`) {
		t.Errorf("want status unknown: %s", buf.String())
	}
	// --all prune removes it (it isn't in flight).
	buf.Reset()
	if err := runRunsPrune(&buf, true, true); err != nil {
		t.Fatal(err)
	}
	var got runsPruneResult
	_ = json.Unmarshal(buf.Bytes(), &got)
	if got.Pruned != 1 {
		t.Errorf("prune --all removed %d, want 1 (abandoned run)", got.Pruned)
	}
}

// E5: `runs show` / `logs` refuse index entries that would escape runs/.
func TestRunsShow_RejectsTraversalULID(t *testing.T) {
	restoreGF(t)
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "runs"), 0o755)
	idx := `{"next_seq":2,"entries":[{"seq":1,"ulid":"../../etc","started_at":"2024-01-01T00:00:00Z","ended_at":"2024-01-01T00:00:01Z","exit_code":0,"status":"succeeded"}]}`
	_ = os.WriteFile(filepath.Join(dir, "index.json"), []byte(idx), 0o644)
	gf = globalFlags{output: "json", stateDir: dir}
	if err := runRunsShow(new(bytes.Buffer), "1"); exitcode.From(err) != exitcode.Env {
		t.Errorf("runs show: want env error, got %v", err)
	}
	if err := runLogs(new(bytes.Buffer), "latest"); exitcode.From(err) != exitcode.Env {
		t.Errorf("logs: want env error, got %v", err)
	}
}

// E10: logs / runs show must not take the write lock (no index.lock).
func TestLogsAndShow_DoNotCreateLockFile(t *testing.T) {
	restoreGF(t)
	dir := t.TempDir()
	runsFixture(t, dir)
	_ = os.Remove(filepath.Join(dir, "index.lock"))
	gf = globalFlags{output: "json", stateDir: dir}
	if err := runRunsShow(new(bytes.Buffer), "latest"); err != nil {
		t.Fatal(err)
	}
	_ = runLogs(new(bytes.Buffer), "1")
	if _, err := os.Stat(filepath.Join(dir, "index.lock")); !os.IsNotExist(err) {
		t.Errorf("read-only commands created index.lock")
	}
}

// E8: command groups honour -o json; agent-guide wraps markdown.
func TestGroupsAndAgentGuide_JSON(t *testing.T) {
	isolateEnv(t)
	for _, grp := range []string{"runs", "cache"} {
		stdout, _, err := runRoot(t, []string{grp, "-o", "json"})
		if err != nil {
			t.Fatalf("%s: %v", grp, err)
		}
		var got groupHelpJSON
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("%s -o json not JSON: %v\n%s", grp, err, stdout)
		}
		if got.Path != "tkn-act "+grp || len(got.Subcommands) < 3 {
			t.Errorf("%s: %+v", grp, got)
		}
	}
	stdout, _, err := runRoot(t, []string{"agent-guide", "--section", "logs", "-o", "json"})
	if err != nil {
		t.Fatal(err)
	}
	var g agentGuideJSON
	if err := json.Unmarshal([]byte(stdout), &g); err != nil {
		t.Fatalf("agent-guide -o json not JSON: %v", err)
	}
	if g.Section != "logs" || !strings.Contains(g.Markdown, "#") {
		t.Errorf("got %+v", g)
	}
}

// E10: `list -C` on a missing directory is a usage error, not an
// empty success.
func TestList_MissingDirIsUsageError(t *testing.T) {
	_, _, err := runRoot(t, []string{"list", "-C", filepath.Join(t.TempDir(), "nope"), "-o", "json"})
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Fatalf("exit = %d, want %d", got, exitcode.Usage)
	}
}

// E10: ambiguous ULID prefixes list the candidates (to stderr via the
// returned error) and lower-case prefixes work.
func TestLogs_AmbiguousPrefixListsCandidates(t *testing.T) {
	restoreGF(t)
	dir := t.TempDir()
	s, _ := runstore.Open(dir, "test")
	t0 := time.Unix(1_700_000_000, 0)
	r1, _ := s.NewRun(t0, "one", nil)
	_ = r1.Finalize(t0, 0, "succeeded")
	r2, _ := s.NewRun(t0, "two", nil)
	_ = r2.Finalize(t0, 0, "succeeded")
	gf = globalFlags{output: "json", stateDir: dir}
	err := runLogs(new(bytes.Buffer), strings.ToLower(string(r1.ID)[:10]))
	if exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), string(r2.ID)) {
		t.Fatalf("want usage error listing candidates, got %v", err)
	}
	if err := runRunsShow(new(bytes.Buffer), strings.ToLower(string(r2.ID))); err != nil {
		t.Fatalf("lower-case ULID: %v", err)
	}
}
