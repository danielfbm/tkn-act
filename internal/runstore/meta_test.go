package runstore_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	if got.RunID != m.RunID || got.ULID != m.ULID || got.Seq != m.Seq ||
		got.WriterVersion != m.WriterVersion || got.PipelineRef != m.PipelineRef ||
		!got.StartedAt.Equal(m.StartedAt) || !got.EndedAt.Equal(m.EndedAt) ||
		got.ExitCode != m.ExitCode || got.Status != m.Status ||
		!equalStringSlice(got.Args, m.Args) {
		t.Errorf("round-trip mismatch:\n got=%+v\nwant=%+v", got, m)
	}
}

func TestMetaJSONShape(t *testing.T) {
	m := runstore.Meta{RunID: "x", ULID: "y", Seq: 1, WriterVersion: "v"}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, want := range []string{`"run_id":"x"`, `"ulid":"y"`, `"seq":1`, `"writer_version":"v"`} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %s in %s", want, got)
		}
	}
}

func TestReadMeta_NotFound(t *testing.T) {
	_, err := runstore.ReadMeta(filepath.Join(t.TempDir(), "nope.json"))
	if !os.IsNotExist(err) {
		t.Errorf("want os.IsNotExist, got %v", err)
	}
}

func TestWriteMeta_AtomicReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")
	if err := runstore.WriteMeta(path, runstore.Meta{RunID: "first"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := runstore.WriteMeta(path, runstore.Meta{RunID: "second"}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	m, err := runstore.ReadMeta(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if m.RunID != "second" {
		t.Errorf("got RunID %q, want second", m.RunID)
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
