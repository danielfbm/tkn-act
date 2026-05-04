//go:build cluster

package clustere2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	clusterbe "github.com/danielfbm/tkn-act/internal/backend/cluster"
	"github.com/danielfbm/tkn-act/internal/cluster/k3d"
	"github.com/danielfbm/tkn-act/internal/e2e/fixtures"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/danielfbm/tkn-act/internal/volumes"
	"github.com/danielfbm/tkn-act/internal/workspace"
)

// captureSink is a reporter.Reporter that records every emitted event so
// the cluster-e2e tests can assert on the JSON event shape (task-retry
// events, Attempt counts, ...) coming from real Tekton.
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

// One k3d cluster + Tekton install is shared across the whole fixture
// table — the per-fixture cost should be one PipelineRun, not a fresh
// cluster bring-up. Each subtest creates an ephemeral namespace inside
// that shared cluster.
func TestClusterE2E(t *testing.T) {
	dir := t.TempDir()
	kubecfg := filepath.Join(dir, "kubeconfig")
	cmStore := volumes.NewStore("")
	secStore := volumes.NewStore("")
	cb := clusterbe.New(clusterbe.Options{
		CacheDir:   dir,
		Driver:     k3d.New(k3d.Options{ClusterName: "tkn-act-e2e", KubeconfigPath: kubecfg}),
		ConfigMaps: cmStore,
		Secrets:    secStore,
	})
	t.Cleanup(func() { _ = cb.Cleanup(context.Background()) })

	for _, f := range fixtures.All() {
		f := f
		if f.DockerOnly {
			continue
		}
		t.Run(f.TestName(), func(t *testing.T) {
			runFixtureCluster(t, cb, cmStore, secStore, f)
		})
	}

	_ = backend.Backend(cb) // compile-time check
}

func runFixtureCluster(t *testing.T, cb *clusterbe.Backend, cmStore, secStore *volumes.Store, f fixtures.Fixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

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

	mgr := workspace.NewManager(t.TempDir(), "cluster-e2e")
	engine.SetResultsDirProvisioner(func(_, taskName string) (string, error) {
		return mgr.ProvisionResultsDir(taskName)
	})

	// Per-fixture isolation: clear any inline / bundle entries left
	// over from a prior subtest. The Backend holds a fixed pointer to
	// these stores (one Backend amortizes the k3d bring-up cost across
	// the table), so the cheapest way to keep fixtures from polluting
	// each other is to reset the in-memory layers in place. Without
	// this, the `volumes` fixture's inline `app-config/greeting`
	// shadows the next fixture's bundle-loaded `app-config/greeting`
	// (Inline > Bundle in volumes.Store), and cluster CI fails the
	// `configmap-from-yaml` step's `test "$..." = "hello-from-yaml"`
	// with the prior fixture's `hello-from-cm` value.
	cmStore.Reset()
	secStore.Reset()

	// Bundle-loaded CM/Secret resources (kind: ConfigMap / kind: Secret
	// embedded in the fixture's -f stream) sit at the lowest precedence
	// layer; inline f.ConfigMaps / f.Secrets entries below shadow them.
	for name, bytesByKey := range b.ConfigMaps {
		cmStore.LoadBytes(name, bytesByKey)
	}
	for name, bytesByKey := range b.Secrets {
		secStore.LoadBytes(name, bytesByKey)
	}
	for name, kv := range f.ConfigMaps {
		for k, v := range kv {
			cmStore.Add(name, k, v)
		}
	}
	for name, kv := range f.Secrets {
		for k, v := range kv {
			secStore.Add(name, k, v)
		}
	}

	pmap := map[string]tektontypes.ParamValue{}
	for k, v := range f.Params {
		pmap[k] = tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: v}
	}

	jsonRep := reporter.NewJSON(io.Discard)
	cap := &captureSink{}
	rep := reporter.NewTee(jsonRep, cap)
	res, err := engine.New(cb, rep, engine.Options{}).RunPipeline(ctx, engine.PipelineInput{
		Bundle: b, Name: f.Pipeline, Params: pmap,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != f.WantStatus {
		// Include the Tekton reason + message and per-task outcomes so a
		// flake (or a real classification bug) is debuggable from CI
		// logs alone. Without this, every cluster-CI mismatch surfaces
		// as `status = X, want Y ()` with zero attribution.
		t.Errorf("status = %s, want %s (%s) reason=%q message=%q tasks=%s",
			res.Status, f.WantStatus, f.Description,
			res.Reason, res.Message, taskOutcomesString(res.Tasks))
	}
	// Cross-backend Pipeline.spec.results fidelity: the cluster path
	// reads pr.status.results from the Tekton verdict; the docker path
	// resolves locally. WantResults asserts both produce the same map.
	if f.WantResults != nil && !fixtures.ResultsEqual(res.Results, f.WantResults) {
		t.Errorf("results = %v, want %v (%s)", res.Results, f.WantResults, f.Description)
	}
	assertEventShape(t, f, cap.snapshot())
}

// taskOutcomesString renders the per-task map as a stable
// "name=status,name=status" string for one-line failure attribution.
// Empty input returns "{}" so callers always see something.
func taskOutcomesString(tasks map[string]engine.TaskOutcome) string {
	if len(tasks) == 0 {
		return "{}"
	}
	names := make([]string, 0, len(tasks))
	for n := range tasks {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		oc := tasks[n]
		if oc.Message != "" {
			parts = append(parts, n+"="+oc.Status+":"+oc.Message)
			continue
		}
		parts = append(parts, n+"="+oc.Status)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// assertEventShape checks the per-fixture invariants that fall out of
// the Track 2 #4 work — the JSON event stream from the cluster backend
// must match the docker stream's *shape* for these specific fixtures.
// Cross-backend fidelity is a checkable invariant, not just an
// aspiration.
func assertEventShape(t *testing.T, f fixtures.Fixture, events []reporter.Event) {
	t.Helper()
	switch f.Dir {
	case "retries":
		// retries/ has retries: 3 and the task succeeds on the third
		// attempt → cluster backend must report 2 task-retry events
		// and a task-end with Attempt: 3, matching docker.
		retries, end := 0, reporter.Event{}
		for _, e := range events {
			switch e.Kind {
			case reporter.EvtTaskRetry:
				retries++
			case reporter.EvtTaskEnd:
				if e.Task != "" {
					end = e
				}
			}
		}
		if retries != 2 {
			t.Errorf("retries fixture: task-retry events = %d, want 2", retries)
		}
		if end.Attempt != 3 {
			t.Errorf("retries fixture: task-end attempt = %d, want 3", end.Attempt)
		}
		if end.Status != "succeeded" {
			t.Errorf("retries fixture: task-end status = %q, want succeeded", end.Status)
		}
	case "timeout":
		// timeout/ must end with task-end status=timeout (Track 2 #2
		// invariant).
		var saw bool
		for _, e := range events {
			if e.Kind == reporter.EvtTaskEnd && e.Status == "timeout" {
				saw = true
			}
		}
		if !saw {
			t.Errorf("timeout fixture: no task-end with status=timeout in event stream")
		}
	}

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
