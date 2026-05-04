//go:build integration

package docker_test

import (
	"context"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/backend/docker"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// TestSidecarLifecycleHappyPath asserts the canonical pause-container
// model: a redis sidecar starts before the steps; the step reaches
// it on localhost:6379 (shared netns) and receives a PONG.
func TestSidecarLifecycleHappyPath(t *testing.T) {
	be, err := docker.New(docker.Options{})
	if err != nil {
		t.Skipf("no docker: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := be.Prepare(ctx, backend.RunSpec{
		RunID:  "sidetest1",
		Images: []string{"redis:7-alpine", "alpine:3"},
	}); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer func() { _ = be.Cleanup(context.Background()) }()

	resultsHost := t.TempDir()
	res, err := be.RunTask(ctx, backend.TaskInvocation{
		RunID:       "sidetest1",
		TaskName:    "t",
		TaskRunName: "tt",
		ResultsHost: resultsHost,
		LogSink:     &captureLogs{},
		Task: tektontypes.TaskSpec{
			Sidecars: []tektontypes.Sidecar{
				{Name: "redis", Image: "redis:7-alpine"},
			},
			Steps: []tektontypes.Step{
				{
					Name:   "ping",
					Image:  "redis:7-alpine",
					Script: "for i in 1 2 3 4 5 6 7 8 9 10; do redis-cli -h 127.0.0.1 -p 6379 PING && exit 0; sleep 1; done; exit 1",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != backend.TaskSucceeded {
		t.Errorf("status = %s, want succeeded", res.Status)
	}
}

// TestSidecarStartFailMarksInfraFailed asserts that a sidecar whose
// image cannot be pulled fails the Task with infrafailed before any
// Step runs.
func TestSidecarStartFailMarksInfraFailed(t *testing.T) {
	be, err := docker.New(docker.Options{})
	if err != nil {
		t.Skipf("no docker: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = be.Prepare(ctx, backend.RunSpec{RunID: "sidetest2", Images: []string{"alpine:3"}})
	defer func() { _ = be.Cleanup(context.Background()) }()

	res, _ := be.RunTask(ctx, backend.TaskInvocation{
		RunID: "sidetest2", TaskName: "t", TaskRunName: "tt",
		ResultsHost: t.TempDir(), LogSink: &captureLogs{},
		Task: tektontypes.TaskSpec{
			Sidecars: []tektontypes.Sidecar{
				{Name: "broken", Image: "this-image-definitely-does-not-exist.example.invalid:never"},
			},
			Steps: []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}},
		},
	})
	if res.Status != backend.TaskInfraFailed {
		t.Errorf("status = %s, want infrafailed (sidecar pull failed)", res.Status)
	}
}

// TestSidecarCrashMidTaskDoesNotFailTask asserts the pause-container
// model: a sidecar dying mid-task records a sidecar-end event but
// does NOT fail the Task. Matches upstream "sidecars are best-effort".
// This is the test that would have FAILED under the previous
// "first-sidecar-as-netns-owner" design.
func TestSidecarCrashMidTaskDoesNotFailTask(t *testing.T) {
	be, err := docker.New(docker.Options{})
	if err != nil {
		t.Skipf("no docker: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := be.Prepare(ctx, backend.RunSpec{
		RunID: "sidetest3", Images: []string{"alpine:3"},
	}); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer func() { _ = be.Cleanup(context.Background()) }()

	res, err := be.RunTask(ctx, backend.TaskInvocation{
		RunID: "sidetest3", TaskName: "t", TaskRunName: "tt",
		ResultsHost: t.TempDir(), LogSink: &captureLogs{},
		Task: tektontypes.TaskSpec{
			Sidecars: []tektontypes.Sidecar{
				// A sidecar that lives long enough to start
				// successfully (so the start-grace passes), then
				// exits mid-task while the second step is sleeping.
				{Name: "shortlived", Image: "alpine:3", Script: "sleep 3; exit 0"},
			},
			Steps: []tektontypes.Step{
				{Name: "wait", Image: "alpine:3", Script: "sleep 5"},
				{Name: "after", Image: "alpine:3", Script: "true"},
			},
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != backend.TaskSucceeded {
		t.Errorf("status = %s, want succeeded (sidecar crash must not fail Task — upstream parity)", res.Status)
	}
}
