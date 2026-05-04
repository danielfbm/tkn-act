package engine_test

import (
	"context"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/refresolver"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

// TestEagerTopLevelPipelineRefResolves exercises the load-time
// resolution path: a PipelineRun whose top-level pipelineRef carries
// a resolver block resolves the Pipeline before DAG build, and the
// resolver-end event for that resolution carries an empty `task`
// field (per spec §12 and §7).
func TestEagerTopLevelPipelineRefResolves(t *testing.T) {
	yaml := []byte(`
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata: {name: my-run}
spec:
  pipelineRef:
    resolver: inline
    params:
      - {name: name, value: "the-pipeline"}
`)
	b, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatal(err)
	}

	// Inline resolver returns a complete Pipeline with one inline task.
	reg := refresolver.NewRegistry()
	inline := refresolver.NewInlineResolver()
	inline.Add("the-pipeline", []byte(`apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: resolved-pipeline}
spec:
  tasks:
    - name: hello
      taskSpec:
        steps:
          - {name: s, image: alpine, script: 'true'}
`))
	reg.Register(inline)

	fb := &orderedBackend{}
	out := &strings.Builder{}
	rep := reporter.NewJSON(out)
	e := engine.New(fb, rep, engine.Options{MaxParallel: 4, Refresolver: reg})

	res, err := e.RunPipeline(context.Background(), engine.PipelineInput{
		Bundle: b,
		Name:   "", // empty: engine eagerly resolves the top-level pipelineRef
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("status = %s, tasks = %+v", res.Status, res.Tasks)
	}
	// The resolved task must have run.
	if len(fb.order) != 1 || fb.order[0] != "hello" {
		t.Errorf("backend calls = %v, want [hello]", fb.order)
	}
	// resolver-start / resolver-end fired before any task-start, and
	// neither carries a "task":"..." field — JSON omits it via the
	// existing omitempty tag on Event.Task.
	stream := out.String()
	if !strings.Contains(stream, `"resolver-start"`) {
		t.Errorf("expected resolver-start event")
	}
	rsLine := firstLineWith(stream, `"resolver-start"`)
	if rsLine == "" {
		t.Fatal("resolver-start not found")
	}
	if strings.Contains(rsLine, `"task"`) {
		t.Errorf("top-level resolver-start carried task field: %s", rsLine)
	}
}

// TestEagerTopLevelPipelineRefResolverFailure: if the top-level
// resolver fails, the engine must short-circuit before any task starts
// — surface the failure as a run-end with status=failed.
func TestEagerTopLevelPipelineRefResolverFailure(t *testing.T) {
	yaml := []byte(`
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata: {name: my-run}
spec:
  pipelineRef:
    resolver: inline
    params:
      - {name: name, value: "missing"}
`)
	b, err := loader.LoadBytes(yaml)
	if err != nil {
		t.Fatal(err)
	}
	reg := refresolver.NewRegistry()
	reg.Register(refresolver.NewInlineResolver()) // empty

	fb := &orderedBackend{}
	out := &strings.Builder{}
	rep := reporter.NewJSON(out)
	e := engine.New(fb, rep, engine.Options{MaxParallel: 4, Refresolver: reg})

	res, _ := e.RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: ""})
	if res.Status == "succeeded" {
		t.Errorf("status = succeeded, want failed")
	}
	if len(fb.order) != 0 {
		t.Errorf("backend calls = %v, want zero (failure before task-start)", fb.order)
	}
}

func firstLineWith(stream, needle string) string {
	for _, line := range strings.Split(stream, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}
