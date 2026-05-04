//go:build integration

package docker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/backend/docker"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

type captureLogs struct{ lines []string }

func (c *captureLogs) StepLog(_, _, _, _, line string) { c.lines = append(c.lines, line) }

func TestRunSingleStep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	be, err := docker.New(docker.Options{})
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}
	t.Cleanup(func() { _ = be.Cleanup(ctx) })

	if err := be.Prepare(ctx, backend.RunSpec{RunID: "test", Images: []string{"alpine:3"}}); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	logs := &captureLogs{}
	resultsDir := t.TempDir()
	res, err := be.RunTask(ctx, backend.TaskInvocation{
		RunID: "test", TaskRunName: "tr-hello",
		ResultsHost: resultsDir,
		LogSink:     logs,
		Task: tektontypes.TaskSpec{
			Steps: []tektontypes.Step{{
				Name:   "say",
				Image:  "alpine:3",
				Script: "echo hello-tkn-act",
			}},
		},
	})
	if err != nil {
		t.Fatalf("runtask: %v", err)
	}
	if res.Status != backend.TaskSucceeded {
		t.Errorf("status = %s", res.Status)
	}
	found := false
	for _, l := range logs.lines {
		if l == "hello-tkn-act" {
			found = true
		}
	}
	if !found {
		t.Errorf("did not see expected log; got %v", logs.lines)
	}
}

func TestRunStepCapturesResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	be, err := docker.New(docker.Options{})
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = be.Cleanup(ctx) })
	if err := be.Prepare(ctx, backend.RunSpec{RunID: "rt", Images: []string{"alpine:3"}}); err != nil {
		t.Fatal(err)
	}

	resultsDir := t.TempDir()
	_, err = be.RunTask(ctx, backend.TaskInvocation{
		RunID: "rt", TaskRunName: "tr-r",
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
	got, err := os.ReadFile(filepath.Join(resultsDir, "version"))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "1.2.3" {
		t.Errorf("result = %q", got)
	}
}
