//go:build integration

package e2e_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend/docker"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/refresolver"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/runstore"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/danielfbm/tkn-act/internal/volumes"
	"github.com/danielfbm/tkn-act/internal/workspace"
)

// TestLogsReplay_ByteEquality runs a real fixture through the engine
// with a Tee'd live-JSON reporter + persist sink, then replays the
// recorded events.jsonl back through a fresh JSON reporter and asserts
// the two byte streams are identical. This is the integration-level
// proof of the persist-and-replay contract that the unit tests in
// internal/reporter and internal/runstore exercise piecewise.
//
// The test deliberately does NOT register a new fixture in
// fixtures.All(): the byte-equality property is backend-agnostic and
// adding a fixture there would force the cross-backend invariant to
// run the same persistence check on the cluster harness (5+ minutes
// of CI for no additional signal). One docker-backed round-trip is
// the right cost / coverage trade-off.
//
// Uses the `hello` fixture because it's the smallest cross-backend
// fixture in the tree.
func TestLogsReplay_ByteEquality(t *testing.T) {
	ctx := context.Background()

	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "e2e", "hello", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no fixture files in testdata/e2e/hello")
	}
	b, err := loader.LoadFiles(files)
	if err != nil {
		t.Fatal(err)
	}

	mgr := workspace.NewManager(t.TempDir(), "logs-replay")
	wsHost := map[string]string{}
	for _, w := range b.Pipelines["hello"].Spec.Workspaces {
		p, perr := mgr.Provision(w.Name, "")
		if perr != nil {
			t.Fatal(perr)
		}
		wsHost[w.Name] = p
	}
	engine.SetResultsDirProvisioner(func(_, taskName string) (string, error) {
		return mgr.ProvisionResultsDir(taskName)
	})

	be, err := docker.New(docker.Options{
		Remote:     os.Getenv("TKN_ACT_REMOTE_DOCKER"),
		PauseImage: os.Getenv("TKN_ACT_PAUSE_IMAGE"),
	})
	if err != nil {
		t.Skipf("docker: %v", err)
	}

	// Create a run-store backed by a per-test tmpdir so we can record
	// events.jsonl and then replay it.
	stateDir := t.TempDir()
	store, err := runstore.Open(stateDir, "byte-equality-test/v1")
	if err != nil {
		t.Fatalf("runstore.Open: %v", err)
	}
	run, err := store.NewRun(time.Now(), "hello", []string{"run"})
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}

	// Tee: live JSON into a buffer + persist sink into events.jsonl.
	// This mirrors how the real `tkn-act run -o json` wires its
	// reporters (live → stdout, persist → events.jsonl).
	var live bytes.Buffer
	liveRep := reporter.NewJSON(&live)
	persistRep, err := reporter.NewPersistSink(run.EventsPath())
	if err != nil {
		t.Fatalf("NewPersistSink: %v", err)
	}
	rep := reporter.NewTee(liveRep, persistRep)

	cmStore := volumes.NewStore("")
	secStore := volumes.NewStore("")
	volResolver := func(taskName string, vs []tektontypes.Volume) (map[string]string, error) {
		base, perr := mgr.ProvisionVolumesDir(taskName)
		if perr != nil {
			return nil, perr
		}
		return volumes.MaterializeForTask(taskName, vs, base, cmStore, secStore)
	}

	engOpts := engine.Options{
		MaxParallel:    4,
		VolumeResolver: volResolver,
		Refresolver: refresolver.NewDefaultRegistry(refresolver.Options{
			Allow:    []string{"git", "hub", "http", "bundles"},
			CacheDir: t.TempDir(),
		}),
	}
	res, err := engine.New(be, rep, engOpts).RunPipeline(ctx, engine.PipelineInput{
		Bundle:     b,
		Name:       "hello",
		Workspaces: wsHost,
	})
	if err != nil {
		_ = rep.Close()
		t.Fatalf("run: %v", err)
	}
	if !strings.EqualFold(res.Status, "succeeded") {
		_ = rep.Close()
		t.Fatalf("status = %s, want succeeded", res.Status)
	}
	if err := rep.Close(); err != nil {
		t.Fatalf("rep.Close: %v", err)
	}

	// Replay events.jsonl through a fresh JSON reporter into a second
	// buffer. The two byte streams must be identical — that's the
	// promise `tkn-act logs latest -o json` makes to scripts that
	// dropped the live stream.
	var replay bytes.Buffer
	replayRep := reporter.NewJSON(&replay)
	if err := runstore.Replay(run.EventsPath(), replayRep); err != nil {
		t.Fatalf("runstore.Replay: %v", err)
	}
	if err := replayRep.Close(); err != nil {
		t.Fatalf("replayRep.Close: %v", err)
	}

	if !bytes.Equal(live.Bytes(), replay.Bytes()) {
		t.Errorf("byte-equality broken between live JSON and replay\n\n--- live (%d bytes) ---\n%s\n--- replay (%d bytes) ---\n%s",
			live.Len(), live.String(), replay.Len(), replay.String())
	}
}
