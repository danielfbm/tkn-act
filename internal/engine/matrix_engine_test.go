package engine_test

import (
	"context"
	"sort"
	"testing"

	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

func TestMatrixEndToEndCrossProduct(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: emit}
spec:
  params:
    - {name: os}
    - {name: goversion}
  results: [{name: tag}]
  steps:
    - {name: s, image: alpine, script: 'true'}
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef: {name: emit}
      matrix:
        params:
          - {name: os, value: [linux, darwin]}
          - {name: goversion, value: ["1.21", "1.22"]}
`))
	if err != nil {
		t.Fatal(err)
	}
	be := &captureBackend{}
	sink := &sliceSink{}
	if _, err := engine.New(be, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"}); err != nil {
		t.Fatal(err)
	}

	// Four invocations, one per cross-product row.
	var names []string
	for n := range be.steps {
		names = append(names, n)
	}
	sort.Strings(names)
	want := []string{"build-0", "build-1", "build-2", "build-3"}
	if len(names) != 4 {
		t.Fatalf("ran %d tasks (%v), want 4 (%v)", len(names), names, want)
	}
	for i, n := range want {
		if names[i] != n {
			t.Errorf("name[%d] = %q, want %q", i, names[i], n)
		}
	}

	// Every task-end event for an expansion carries Matrix.Parent="build".
	var endsWithMatrix int
	var startsWithMatrix int
	for _, ev := range sink.events {
		if ev.Matrix != nil && ev.Matrix.Parent == "build" {
			switch ev.Kind {
			case reporter.EvtTaskEnd:
				endsWithMatrix++
			case reporter.EvtTaskStart:
				startsWithMatrix++
			}
		}
	}
	if endsWithMatrix != 4 {
		t.Errorf("matrix task-end events = %d, want 4", endsWithMatrix)
	}
	if startsWithMatrix != 4 {
		t.Errorf("matrix task-start events = %d, want 4", startsWithMatrix)
	}
}

func TestMatrixDownstreamWaitsOnAllExpansions(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef: {name: t}
      matrix:
        params:
          - {name: os, value: [linux, darwin]}
    - name: publish
      taskRef: {name: t}
      runAfter: [build]
`))
	if err != nil {
		t.Fatal(err)
	}
	be := &captureBackend{}
	sink := &sliceSink{}
	if _, err := engine.New(be, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"}); err != nil {
		t.Fatal(err)
	}
	// Verify publish ran AFTER both build expansions (recorded by
	// captureBackend in invocation order).
	var order []string
	for _, inv := range be.invocationsInOrder {
		order = append(order, inv.TaskName)
	}
	publishIdx := -1
	for i, n := range order {
		if n == "publish" {
			publishIdx = i
		}
	}
	if publishIdx == -1 || publishIdx < 2 {
		t.Errorf("publish ran at index %d in %v; expected after both build-* expansions", publishIdx, order)
	}
}

func TestMatrixWhenSkipsRowsIndependently(t *testing.T) {
	// Per-row when:. when: $(params.os) in [linux] runs build-0
	// (os=linux) and SKIPS build-1 (os=darwin). Each skipped row
	// emits its own EvtTaskSkip under the expansion name with
	// Matrix populated.
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: os}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef: {name: t}
      when:
        - input: $(params.os)
          operator: in
          values: [linux]
      matrix:
        params:
          - {name: os, value: [linux, darwin]}
`))
	if err != nil {
		t.Fatal(err)
	}
	be := &captureBackend{}
	sink := &sliceSink{}
	if _, err := engine.New(be, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"}); err != nil {
		t.Fatal(err)
	}
	// Exactly the linux row ran.
	if len(be.steps) != 1 {
		t.Errorf("want 1 task to run (the linux row); got %v", keys(be.steps))
	}
	if _, ok := be.steps["build-0"]; !ok {
		t.Errorf("build-0 (os=linux) should have run; got %v", keys(be.steps))
	}
	// Exactly one skip event, under the EXPANSION name, with Matrix
	// populated.
	var skips []reporter.Event
	for _, ev := range sink.events {
		if ev.Kind == reporter.EvtTaskSkip && ev.Matrix != nil && ev.Matrix.Parent == "build" {
			skips = append(skips, ev)
		}
	}
	if len(skips) != 1 {
		t.Fatalf("matrix task-skip events = %d, want 1", len(skips))
	}
	if skips[0].Task != "build-1" {
		t.Errorf("skip Task = %q, want %q", skips[0].Task, "build-1")
	}
	if skips[0].Matrix.Parent != "build" || skips[0].Matrix.Index != 1 {
		t.Errorf("skip Matrix = %+v, want {Parent:build Index:1 ...}", skips[0].Matrix)
	}
}

func TestMatrixResultsAggregateUnderParent(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: emit}
spec:
  params: [{name: os}, {name: goversion}]
  results: [{name: tag}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: tags
      type: array
      value: $(tasks.build.results.tag[*])
  tasks:
    - name: build
      taskRef: {name: emit}
      matrix:
        params:
          - {name: os, value: [linux, darwin]}
          - {name: goversion, value: ["1.21", "1.22"]}
`))
	if err != nil {
		t.Fatal(err)
	}
	// Script the per-expansion result. Expansion order matches
	// expandMatrix's row iteration: outermost iterates slowest.
	be := &captureBackend{
		results: map[string]map[string]string{
			"build-0": {"tag": "linux-1.21"},
			"build-1": {"tag": "linux-1.22"},
			"build-2": {"tag": "darwin-1.21"},
			"build-3": {"tag": "darwin-1.22"},
		},
	}
	sink := &sliceSink{}
	res, err := engine.New(be, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := res.Results["tags"].([]string)
	if !ok {
		t.Fatalf("Results[tags] type = %T, want []string; full=%+v", res.Results["tags"], res.Results)
	}
	want := []string{"linux-1.21", "linux-1.22", "darwin-1.21", "darwin-1.22"}
	if len(got) != len(want) {
		t.Fatalf("Results[tags] len = %d, want 4 (%v)", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Results[tags][%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
