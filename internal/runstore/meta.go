package runstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Meta is the run-level metadata persisted alongside events.jsonl in
// each run's directory. Field names use snake_case per the project's
// JSON convention for new multi-word fields.
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
// Parent directories are created on demand.
func WriteMeta(path string, m Meta) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".meta-*.tmp")
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
