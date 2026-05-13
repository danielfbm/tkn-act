//go:build integration

package docker_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/backend/docker"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// uniqueRunID returns a per-test RunID of the form "<prefix>-<hex>".
// Fixed RunIDs would conflict on stager container name and volume
// name across reruns when a previous test leaked resources (which
// the per-run volume design is supposed to make impossible, but we
// don't rely on that in test harnesses).
func uniqueRunID(t *testing.T, prefix string) string {
	t.Helper()
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return prefix + "-" + hex.EncodeToString(buf[:])
}

// TestRemoteStaging_ForcedOn_Hello runs the canonical hello-fixture
// through the volume-staging path against the local daemon by forcing
// Remote: "on". This is the Phase 3 gate from the plan: one fixture
// passes end-to-end with --remote-docker=on before the dind workflow
// (P5a) lands. Catches integration breakage early.
func TestRemoteStaging_ForcedOn_Hello(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	be, err := docker.New(docker.Options{Remote: "on"})
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}
	t.Cleanup(func() { _ = be.Cleanup(ctx) })

	runID := uniqueRunID(t, "stage-hello")
	if err := be.Prepare(ctx, backend.RunSpec{RunID: runID, Images: []string{"alpine:3"}}); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	logs := &captureLogs{}
	resultsDir := t.TempDir()
	res, err := be.RunTask(ctx, backend.TaskInvocation{
		RunID: runID, TaskRunName: "tr-hello",
		ResultsHost: resultsDir,
		LogSink:     logs,
		Task: tektontypes.TaskSpec{
			Steps: []tektontypes.Step{{
				Name:   "say",
				Image:  "alpine:3",
				Script: "echo hello-from-volume",
			}},
		},
	})
	if err != nil {
		t.Fatalf("runtask: %v", err)
	}
	if res.Status != backend.TaskSucceeded {
		t.Errorf("status = %s; want succeeded", res.Status)
	}
	found := false
	for _, l := range logs.lines {
		if l == "hello-from-volume" {
			found = true
		}
	}
	if !found {
		t.Errorf("did not see expected log; got %v", logs.lines)
	}
}

// TestRemoteStaging_ForcedOn_ResultsRoundtrip writes a Task-level
// result inside the container and verifies it shows up on the host
// after Cleanup pulls /staged/results/<taskRun>/ back. Same shape as
// TestRunStepCapturesResult but goes through the remote-mode pull
// path instead of the bind mount.
func TestRemoteStaging_ForcedOn_ResultsRoundtrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	be, err := docker.New(docker.Options{Remote: "on"})
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}
	t.Cleanup(func() { _ = be.Cleanup(ctx) })
	runID := uniqueRunID(t, "stage-results")
	if err := be.Prepare(ctx, backend.RunSpec{RunID: runID, Images: []string{"alpine:3"}}); err != nil {
		t.Fatal(err)
	}

	resultsDir := t.TempDir()
	res, err := be.RunTask(ctx, backend.TaskInvocation{
		RunID: runID, TaskRunName: "tr-r",
		ResultsHost: resultsDir,
		LogSink:     &captureLogs{},
		Task: tektontypes.TaskSpec{
			Results: []tektontypes.ResultSpec{{Name: "version"}},
			Steps: []tektontypes.Step{{
				Name:   "emit",
				Image:  "alpine:3",
				Script: "printf 1.2.3 > /tekton/results/version",
			}},
		},
	})
	if err != nil {
		t.Fatalf("runtask: %v", err)
	}
	if res.Status != backend.TaskSucceeded {
		t.Fatalf("status = %s; want succeeded", res.Status)
	}
	if got := res.Results["version"]; got != "1.2.3" {
		t.Errorf("res.Results[version] = %q, want %q", got, "1.2.3")
	}
	// Belt-and-braces: the engine reads from disk, so the file must be
	// physically present at <resultsHost>/version too — the pull-back
	// is what put it there in remote mode.
	got, err := os.ReadFile(filepath.Join(resultsDir, "version"))
	if err != nil {
		t.Fatalf("read result file: %v", err)
	}
	if string(got) != "1.2.3" {
		t.Errorf("disk file = %q", got)
	}
}

// TestRemoteStaging_ForcedOn_StepResultsBetweenSteps ensures that
// per-step results pulled after step N are visible to step N+1's
// substitution pass. Local mode gets this for free via the bind
// mount; remote mode has to round-trip via CopyFromContainer.
func TestRemoteStaging_ForcedOn_StepResultsBetweenSteps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	be, err := docker.New(docker.Options{Remote: "on"})
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}
	t.Cleanup(func() { _ = be.Cleanup(ctx) })
	runID := uniqueRunID(t, "stage-step-r")
	if err := be.Prepare(ctx, backend.RunSpec{RunID: runID, Images: []string{"alpine:3"}}); err != nil {
		t.Fatal(err)
	}

	logs := &captureLogs{}
	res, err := be.RunTask(ctx, backend.TaskInvocation{
		RunID: runID, TaskRunName: "tr-sr",
		ResultsHost: t.TempDir(),
		LogSink:     logs,
		Task: tektontypes.TaskSpec{
			Steps: []tektontypes.Step{
				{
					Name:    "first",
					Image:   "alpine:3",
					Results: []tektontypes.ResultSpec{{Name: "answer"}},
					Script:  "printf 42 > $(step.results.answer.path)",
				},
				{
					Name:   "second",
					Image:  "alpine:3",
					Script: `echo "got=$(steps.first.results.answer)"`,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("runtask: %v", err)
	}
	if res.Status != backend.TaskSucceeded {
		t.Errorf("status = %s; want succeeded", res.Status)
	}
	found := false
	for _, l := range logs.lines {
		if l == "got=42" {
			found = true
		}
	}
	if !found {
		t.Errorf("did not see substituted step result in logs; got %v", logs.lines)
	}
}
