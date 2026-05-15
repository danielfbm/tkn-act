//go:build integration

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend/docker"
	"github.com/danielfbm/tkn-act/internal/e2e/fixtures"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/refresolver"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/danielfbm/tkn-act/internal/volumes"
	"github.com/danielfbm/tkn-act/internal/workspace"
)

// captureSink is a reporter.Reporter that records every emitted event so
// the docker-e2e tests can assert on the JSON event shape.
type captureSink struct {
	mu     sync.Mutex
	events []reporter.Event
}

func (s *captureSink) Emit(e reporter.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}
func (s *captureSink) Close() error { return nil }
func (s *captureSink) snapshot() []reporter.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]reporter.Event, len(s.events))
	copy(out, s.events)
	return out
}

func TestE2E(t *testing.T) {
	for _, f := range fixtures.All() {
		f := f
		if f.ClusterOnly {
			continue
		}
		t.Run(f.TestName(), func(t *testing.T) {
			runFixtureDocker(t, f)
		})
	}
}

func runFixtureDocker(t *testing.T, f fixtures.Fixture) {
	t.Helper()
	ctx := context.Background()
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "e2e", f.Dir, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no fixture files in %s", f.Dir)
	}
	b, err := loader.LoadFiles(files)
	if err != nil {
		t.Fatal(err)
	}

	mgr := workspace.NewManager(t.TempDir(), "e2e")
	wsHost := map[string]string{}
	for _, w := range b.Pipelines[f.Pipeline].Spec.Workspaces {
		p, err := mgr.Provision(w.Name, "")
		if err != nil {
			t.Fatal(err)
		}
		wsHost[w.Name] = p
	}

	engine.SetResultsDirProvisioner(func(_, taskName string) (string, error) {
		return mgr.ProvisionResultsDir(taskName)
	})

	// The remote-docker-integration workflow runs this same fixture
	// table against a dind service container with $DOCKER_HOST set
	// and TKN_ACT_REMOTE_DOCKER=on. Auto-detect would also classify
	// remote in that environment, but the env var is honored here so
	// a regression in auto-detect doesn't silently flip to bind mounts
	// (which the dind daemon's filesystem can't see). Empty env →
	// "" → "auto" inside decideRemote, matching the prior behaviour
	// of the docker-integration workflow.
	be, err := docker.New(docker.Options{Remote: os.Getenv("TKN_ACT_REMOTE_DOCKER")})
	if err != nil {
		t.Skipf("docker: %v", err)
	}

	// Capture every event to a buffer so a failing fixture's logs
	// surface in the test output (CI failures otherwise lose the
	// step output and the only signal is "status = failed").
	var eventBuf bytes.Buffer
	jsonRep := reporter.NewJSON(&eventBuf)
	cap := &captureSink{}
	rep := reporter.NewTee(jsonRep, cap)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("event stream:\n%s", eventBuf.String())
		}
	})
	pmap := map[string]tektontypes.ParamValue{}
	for k, v := range f.Params {
		pmap[k] = tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: v}
	}

	// resolver-git: build a per-test bare repo from the fixture's
	// seed/ subtree and inject the file:// URL as the repoURL param.
	// Mirrors the cluster-e2e harness so both backends exercise the
	// same direct-git-resolver code path.
	if f.Dir == "resolver-git" {
		seed := filepath.Join("..", "..", "testdata", "e2e", "resolver-git", "seed")
		url, err := fixtures.BuildBareRepoFromSeed(seed, t.TempDir())
		if err != nil {
			t.Fatalf("build bare repo: %v", err)
		}
		pmap["repoURL"] = tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: url}
	}

	// Resolver fixtures (Phase 3 of Track 1 #9): bring up an httptest
	// server + Registry instance the engine can dispatch through. The
	// helper returns nil when f.Resolver is empty, so non-resolver
	// fixtures are unaffected.
	rh, rerr := fixtures.NewResolverHarness(filepath.Join("..", "..", "testdata", "e2e", f.Dir), f.Resolver)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if rh != nil {
		defer rh.Close()
		if rh.ExtraParamName != "" {
			pmap[rh.ExtraParamName] = tektontypes.ParamValue{
				Type:      tektontypes.ParamTypeString,
				StringVal: rh.ExtraParamValue,
			}
		}
	}

	cmStore := volumes.NewStore("")
	// Bundle-loaded CM/Secret resources (kind: ConfigMap / kind: Secret
	// embedded in the fixture's -f stream) sit at the lowest precedence
	// layer; inline f.ConfigMaps / f.Secrets entries below shadow them.
	for name, bytesByKey := range b.ConfigMaps {
		cmStore.LoadBytes(name, bytesByKey)
	}
	for name, kv := range f.ConfigMaps {
		for k, v := range kv {
			cmStore.Add(name, k, v)
		}
	}
	secStore := volumes.NewStore("")
	for name, bytesByKey := range b.Secrets {
		secStore.LoadBytes(name, bytesByKey)
	}
	for name, kv := range f.Secrets {
		for k, v := range kv {
			secStore.Add(name, k, v)
		}
	}
	volResolver := func(taskName string, vs []tektontypes.Volume) (map[string]string, error) {
		volBase, perr := mgr.ProvisionVolumesDir(taskName)
		if perr != nil {
			return nil, perr
		}
		return volumes.MaterializeForTask(taskName, vs, volBase, cmStore, secStore)
	}

	// Wire the default refresolver registry so resolver-backed
	// fixtures (Track 1 #9) dispatch the same way `tkn-act run` does.
	// The cache dir is a per-test tmpdir so subtests don't share
	// resolved bytes across runs. For Phase-3 (hub/http) fixtures,
	// the harness builds its own Registry pointed at an httptest
	// server, which takes precedence.
	engOpts := engine.Options{MaxParallel: 4, VolumeResolver: volResolver}
	if rh != nil {
		engOpts.Refresolver = rh.Registry
	} else {
		engOpts.Refresolver = refresolver.NewDefaultRegistry(refresolver.Options{
			Allow:    []string{"git", "hub", "http", "bundles"},
			CacheDir: t.TempDir(),
		})
	}
	res, err := engine.New(be, rep, engOpts).RunPipeline(ctx, engine.PipelineInput{
		Bundle: b, Name: f.Pipeline, Params: pmap, Workspaces: wsHost,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.EqualFold(res.Status, f.WantStatus) {
		t.Errorf("status = %s, want %s (%s)", res.Status, f.WantStatus, f.Description)
	}
	// Cross-backend Pipeline.spec.results fidelity: when a fixture sets
	// WantResults, the engine's resolved Results map must match it on
	// both backends. Without this assertion a regression that silently
	// dropped pipeline results would still leave WantStatus green.
	if f.WantResults != nil && !fixtures.ResultsEqual(res.Results, f.WantResults) {
		t.Errorf("results = %v, want %v (%s)", res.Results, f.WantResults, f.Description)
	}
	assertEventShape(t, f, cap.snapshot())
}

// assertEventShape checks per-fixture invariants on the captured event
// stream. Today the only structured assertion is WantEventFields, but
// the helper exists so future cross-backend invariants (e.g., emitted
// step-level events) have a single place to land.
func assertEventShape(t *testing.T, f fixtures.Fixture, events []reporter.Event) {
	t.Helper()
	if len(f.WantEventFields) > 0 {
		// First event by kind.
		first := map[reporter.EventKind]reporter.Event{}
		for _, e := range events {
			if _, ok := first[e.Kind]; !ok {
				first[e.Kind] = e
			}
		}
		for kindStr, want := range f.WantEventFields {
			ev, ok := first[reporter.EventKind(kindStr)]
			if !ok {
				t.Errorf("WantEventFields: no %q event in captured stream", kindStr)
				continue
			}
			// Marshal the event back to JSON so we assert against the
			// public contract (snake_case keys), not Go field names.
			raw, _ := json.Marshal(ev)
			var got map[string]any
			_ = json.Unmarshal(raw, &got)
			for key, expected := range want {
				if fmt.Sprint(got[key]) != expected {
					t.Errorf("WantEventFields[%s][%s] = %v, want %q", kindStr, key, got[key], expected)
				}
			}
		}
	}
}
