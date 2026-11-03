package engine_test

import (
	"context"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

type captureInvBackend struct {
	invs []backend.TaskInvocation
}

func (c *captureInvBackend) Prepare(context.Context, backend.RunSpec) error { return nil }
func (c *captureInvBackend) Cleanup(context.Context) error                  { return nil }
func (c *captureInvBackend) RunTask(_ context.Context, inv backend.TaskInvocation) (backend.TaskResult, error) {
	c.invs = append(c.invs, inv)
	return backend.TaskResult{Status: backend.TaskSucceeded, Results: map[string]string{}}, nil
}

// TestWorkspacePathHonoursMountPath: $(workspaces.X.path) must resolve
// to the Task's declared mountPath (where Tekton, and now the docker
// backend, mount it), not a hard-coded /workspace/X.
func TestWorkspacePathHonoursMountPath(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  workspaces:
    - {name: src, mountPath: /custom/src}
    - {name: out}
  steps:
    - {name: s, image: alpine, script: 'ls $(workspaces.src.path) $(workspaces.out.path)'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  workspaces: [{name: shared}]
  tasks:
    - name: a
      taskRef: {name: t}
      workspaces:
        - {name: src, workspace: shared, subPath: sub}
        - {name: out, workspace: shared}
`))
	if err != nil {
		t.Fatal(err)
	}
	cb := &captureInvBackend{}
	e := engine.New(cb, reporter.NewJSON(&strings.Builder{}), engine.Options{MaxParallel: 1})
	res, err := e.RunPipeline(context.Background(), engine.PipelineInput{
		Bundle: b, Name: "p", Workspaces: map[string]string{"shared": t.TempDir()},
	})
	if err != nil || res.Status != "succeeded" {
		t.Fatalf("run: %v %s", err, res.Status)
	}
	if len(cb.invs) != 1 {
		t.Fatalf("invocations = %d", len(cb.invs))
	}
	inv := cb.invs[0]
	if got := inv.Task.Steps[0].Script; got != "ls /custom/src /workspace/out" {
		t.Errorf("script = %q", got)
	}
	if inv.Workspaces["src"].SubPath != "sub" {
		t.Errorf("subPath not propagated: %+v", inv.Workspaces["src"])
	}
}
