# `Pipeline.spec.results` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Honor Tekton's `Pipeline.spec.results` — a list of named values, each a `ParamValue` expression that may reference `$(tasks.X.results.Y)` — by resolving them after every task (and finally) has run, surfacing the resolved map on `RunResult.Results`, on the `run-end` JSON event as a `results` field, and as a one-line-per-result tail in pretty output. Cluster mode reads the values Tekton's controller already wrote to `pr.status.results`; docker mode resolves them locally from the accumulated task-result map.

**Architecture:** A new pure helper `resolvePipelineResults(pl, results)` lives in `internal/engine/pipeline_results.go`. After the finally loop in `RunPipeline`, the docker path calls it against the in-memory `results` map and the resolved map is attached to `RunResult.Results` and added to the `run-end` event. The cluster path mirrors the same shape: `internal/backend/cluster/run.go` already extracts per-TaskRun `status.results`; we add an extractor for `pr.status.results` (Tekton v1) and `pr.status.pipelineResults` (legacy fallback) into `backend.PipelineRunResult.Results`, which `runViaPipelineBackend` then forwards into the same `RunResult.Results` slot. Resolution failures (referenced task didn't succeed, missing result name, malformed expression) drop that one entry and emit a non-fatal `error` event; they never change `Status` or the exit code.

**Tech Stack:** Go 1.25, no new dependencies. Reuses `internal/resolver` for `$(tasks.X.results.Y)` substitution, the `tektontypes.PipelineResultSpec` and `ParamValue` types that already exist, the `internal/e2e/fixtures` cross-backend table, and the `parity-check` + `tests-required` CI gates.

---

## Track 1 #6 context

This closes Track 1 #6 of `docs/short-term-goals.md`. The Status column says: *"Type exists; resolver/JSON output don't surface them. Small."* The plan's job is the surfacing — types are in place, the resolver already understands `$(tasks.X.results.Y)`, and the JSON event struct only needs one new field.

## Tekton-upstream behavior we're matching

Tekton's `PipelineSpec.Results` (v1):

```yaml
spec:
  results:
    - name: revision
      description: the SHA the build used
      value: $(tasks.checkout.results.commit)
    - name: report
      value:
        - $(tasks.test.results.summary)
        - $(finally.notify.results.id)
    - name: meta
      value:
        owner: $(params.team)
        sha: $(tasks.checkout.results.commit)
  tasks: [...]
  finally: [...]
```

Resolution semantics (mirrored verbatim from upstream as far as user-visible behavior, simplified internally):

- **When**: after the entire run completes — including finally — regardless of overall status (succeeded / failed / timeout). A failed run can still surface results from tasks that did succeed.
- **Source**: the same accumulated task-result map the resolver already uses for `$(tasks.X.results.Y)` in PipelineTask params. Finally tasks are first-class result producers (Tekton uses `$(finally.X.results.Y)` syntactically; tkn-act's existing resolver normalizes both into the `tasks` namespace because the engine writes finally outcomes into the same `results` map).
- **Failure**: if a referenced task didn't produce the named result (status was not `succeeded`, the result was never written, or the expression doesn't parse), Tekton omits that pipeline result from `pr.status.results` and writes a per-result reason in `pr.status.skippedTasks`/`pr.status.conditions`. tkn-act mirrors omission, but raises a single `EvtError` event per dropped result (so JSON consumers see the failure) and **does not** change run status or exit code — these stay independent.
- **Types**: `ParamValue` is one of string / array of string / object (string→string). The resolver returns the same shape; the `run-end` event encodes string values as JSON strings, arrays as JSON arrays, objects as JSON objects.

For tkn-act the small-scope decisions baked into this plan:

- **Cluster mode**: do NOT re-resolve locally. Tekton's controller already populates `pr.status.results` (v1; field name was `pipelineResults` pre-v1 — read both for backward compatibility and pick whichever is present). The cluster backend forwards what the controller produced; this avoids a double-source-of-truth bug where docker and cluster could disagree on resolution edge cases.
- **Pretty output**: one line per resolved result after the existing `PipelineRun <status> in <dur>` summary, indented two spaces, name in bold, value truncated to 80 chars (TBD threshold reaffirmed by self-review). Skipped only if the resolved map is empty.
- **Skipped/dropped results**: emit a `EvtError` event with message `pipeline result %q dropped: %s` (one per dropped result); these are non-fatal and don't change the exit code.
- **Pipeline-result-substitution back into other expressions**: NOT supported. Tekton itself doesn't substitute pipeline results into in-run expressions either; results are output-only.

## Files to create / modify

| File | Why |
|---|---|
| `internal/engine/pipeline_results.go` (new) | `resolvePipelineResults(pl, results) (map[string]any, []error)` — the pure helper |
| `internal/engine/pipeline_results_test.go` (new) | Unit tests: nil/empty, string/array/object, missing-task drop, missing-result drop, malformed-expression drop |
| `internal/engine/run.go` | Add `Results map[string]any` to `RunResult` |
| `internal/engine/engine.go` | Call `resolvePipelineResults` after the finally loop in the docker path; attach to `RunResult.Results` and to the `run-end` event; in `runViaPipelineBackend` forward `res.Results` |
| `internal/engine/pipeline_results_engine_test.go` (new) | Integration: pipeline with results → run-end event has the right `results` field; failed task reference drops the entry but doesn't fail the run |
| `internal/reporter/event.go` | Add `Results map[string]any \`json:"results,omitempty"\`` to `Event` |
| `internal/reporter/reporter_test.go` | Assert `results` on the marshalled `run-end` event |
| `internal/reporter/pretty.go` | Append one line per resolved result after the run summary |
| `internal/backend/backend.go` | Add `Results map[string]any` to `PipelineRunResult` |
| `internal/backend/cluster/run.go` | In `watchPipelineRun`, read `pr.status.results` (v1) / `pr.status.pipelineResults` (legacy) into `res.Results`; same `ParamValue`-style decoding |
| `internal/backend/cluster/runpipeline_test.go` | Unit test: PR with `status.results` populated returns `Results` on `PipelineRunResult` |
| `internal/validator/validator.go` | New rule: every `$(tasks.X.results.Y)` in `Pipeline.spec.results[].value` must reference an X in `pipeline.spec.tasks` ∪ `pipeline.spec.finally`. (Result-name existence is checked at resolution time, not validation, because some Tasks compute results dynamically.) |
| `internal/validator/validator_test.go` | Negative: unknown task ref in `spec.results.value`; positive: valid ref including a finally task |
| `testdata/e2e/pipeline-results/pipeline.yaml` (new) | Cross-backend fixture: a string result from a main task, an array result containing a finally-task ref, exercises both the "result resolved" path and pretty-output tail |
| `internal/e2e/fixtures/fixtures.go` | Add the fixture entry |
| `internal/e2e/run_test.go` (or sibling) | Per-fixture extra assertion: run-end event includes the expected `results` map (not just `WantStatus`). May require extending the harness — see Task 7 |
| `cmd/tkn-act/agentguide_data.md` | Document the `results` field on `run-end` and the resolution semantics |
| `AGENTS.md` | Mirror the same section (kept in sync with the embedded copy) |
| `docs/test-coverage.md` | List the new fixture under `### -tags integration` |
| `docs/short-term-goals.md` | Mark Track 1 #6 done |
| `docs/feature-parity.md` | Flip `Pipeline.spec.results` row from `gap` to `shipped`, populate `e2e fixture` |
| `README.md` | Add a one-line bullet under "Tekton features supported" |

## Out of scope (don't do here)

- **Pipeline-result substitution back into other expressions.** Tekton itself doesn't substitute pipeline results into other expressions within the same run — they're output-only. Don't extend the resolver to handle `$(results.X)` or similar.
- **`Result.Type` validation beyond what `ParamValue` enforces.** Tekton has a separate `Result.Type` field for `array` / `object` declarations; we let the runtime shape of `value` (`StringVal` / `ArrayVal` / `ObjectVal`) dictate the JSON encoding and don't add a new validator rule beyond ref-resolvability.
- **A new exit code for resolution failure.** Result drops are warnings, not errors — exit codes 0/4/5/6/130 stay as documented.
- **Pretty-output truncation algorithm beyond a fixed 80 chars.** Keep it simple; revisit only if a real-world pipeline emits results that don't fit.
- **Making the cluster backend re-resolve locally as a fallback.** If Tekton's controller fails to produce `pr.status.results` for some reason, that's a bug to file upstream — we don't second-guess it. Empty `Results` is a valid outcome.
- **Renaming any existing field.** `tektontypes.PipelineResultSpec` and `PipelineSpec.Results` already exist — verify before adding (`grep -n PipelineResultSpec internal/tektontypes/types.go`).

---

### Task 1: Add `Results` field to `RunResult`, `PipelineRunResult`, and `Event`

**Files:**
- Modify: `internal/engine/run.go`
- Modify: `internal/backend/backend.go`
- Modify: `internal/reporter/event.go`
- Test: `internal/reporter/reporter_test.go`

This is pure plumbing. The new field is `map[string]any` because it must hold string/[]string/map[string]string equally.

- [ ] **Step 1: Write the failing test**

Append to `internal/reporter/reporter_test.go`:

```go
func TestJSONRunEndIncludesResults(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewJSON(&buf)
	r.Emit(reporter.Event{
		Kind:   reporter.EvtRunEnd,
		Status: "succeeded",
		Results: map[string]any{
			"revision": "abc123",
			"files":    []string{"a.txt", "b.txt"},
			"meta":     map[string]string{"owner": "team-a"},
		},
	})
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	res, ok := got["results"].(map[string]any)
	if !ok {
		t.Fatalf("results field missing or wrong type: %T %v", got["results"], got["results"])
	}
	if res["revision"] != "abc123" {
		t.Errorf("results.revision = %v, want abc123", res["revision"])
	}
}
```

If `bytes` / `encoding/json` aren't imported in `reporter_test.go`, add them.

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -count=1 -run TestJSONRunEndIncludesResults ./internal/reporter/...
```

Expected: FAIL with `Results undefined` (or compile error).

- [ ] **Step 3: Add the field to `Event`**

In `internal/reporter/event.go`, append to the `Event` struct (after the existing `Attempt` field):

```go
	// Results holds resolved Pipeline.spec.results on a run-end event.
	// Map values are one of: string, []string, map[string]string. Empty
	// or nil when the pipeline declared no results.
	Results map[string]any `json:"results,omitempty"`
```

- [ ] **Step 4: Add the field to `RunResult`**

In `internal/engine/run.go`, append to the `RunResult` struct (after `Message`):

```go
	// Results holds resolved Pipeline.spec.results once the run is
	// terminal. Each value is one of: string, []string, map[string]string.
	// Empty when the Pipeline didn't declare spec.results, or when every
	// declared entry was dropped (referenced task didn't succeed). A
	// dropped entry surfaces as an EvtError on the event stream.
	Results map[string]any
```

- [ ] **Step 5: Add the field to `PipelineRunResult`**

In `internal/backend/backend.go`, append to `PipelineRunResult` (after `Message`):

```go
	// Results holds the resolved Pipeline.spec.results values the
	// backend extracted from the controller (cluster) or computed
	// locally (docker — populated by the engine, not by the docker
	// backend itself). Same value shape as RunResult.Results.
	Results map[string]any
```

- [ ] **Step 6: Run tests**

```bash
go test -count=1 -run TestJSONRunEndIncludesResults ./internal/reporter/...
go vet ./...
```

Expected: PASS, vet clean.

- [ ] **Step 7: Commit**

```bash
git add internal/reporter/event.go internal/reporter/reporter_test.go internal/engine/run.go internal/backend/backend.go
git commit -m "feat(reporter,engine,backend): add Results field for Pipeline.spec.results"
```

---

### Task 2: `resolvePipelineResults` helper

**Files:**
- Create: `internal/engine/pipeline_results.go`
- Test: `internal/engine/pipeline_results_test.go`

A pure function. Takes the parsed pipeline spec and the accumulated task-result map; returns the resolved map plus a list of per-result drop errors (the engine surfaces those as `EvtError` events).

- [ ] **Step 1: Write the failing test**

Create `internal/engine/pipeline_results_test.go`:

```go
package engine

import (
	"reflect"
	"testing"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

func TestResolvePipelineResultsNil(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{}}
	got, errs := resolvePipelineResults(pl, map[string]map[string]string{})
	if got != nil {
		t.Errorf("got = %v, want nil for pipeline without spec.results", got)
	}
	if len(errs) != 0 {
		t.Errorf("errs = %v, want none", errs)
	}
}

func TestResolvePipelineResultsString(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Results: []tektontypes.PipelineResultSpec{
			{Name: "revision", Value: tektontypes.ParamValue{
				Type: tektontypes.ParamTypeString, StringVal: "$(tasks.checkout.results.commit)",
			}},
		},
	}}
	results := map[string]map[string]string{"checkout": {"commit": "abc123"}}
	got, errs := resolvePipelineResults(pl, results)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	want := map[string]any{"revision": "abc123"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got = %v, want %v", got, want)
	}
}

func TestResolvePipelineResultsArray(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Results: []tektontypes.PipelineResultSpec{
			{Name: "files", Value: tektontypes.ParamValue{
				Type: tektontypes.ParamTypeArray,
				ArrayVal: []string{
					"$(tasks.scan.results.first)",
					"static",
					"$(tasks.scan.results.second)",
				},
			}},
		},
	}}
	results := map[string]map[string]string{"scan": {"first": "a.txt", "second": "b.txt"}}
	got, errs := resolvePipelineResults(pl, results)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	want := map[string]any{"files": []string{"a.txt", "static", "b.txt"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got = %v, want %v", got, want)
	}
}

func TestResolvePipelineResultsObject(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Results: []tektontypes.PipelineResultSpec{
			{Name: "meta", Value: tektontypes.ParamValue{
				Type: tektontypes.ParamTypeObject,
				ObjectVal: map[string]string{
					"owner": "team-a",
					"sha":   "$(tasks.checkout.results.commit)",
				},
			}},
		},
	}}
	results := map[string]map[string]string{"checkout": {"commit": "abc123"}}
	got, errs := resolvePipelineResults(pl, results)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	wantMeta := map[string]string{"owner": "team-a", "sha": "abc123"}
	gotMeta, ok := got["meta"].(map[string]string)
	if !ok {
		t.Fatalf("meta not a map[string]string: %T", got["meta"])
	}
	if !reflect.DeepEqual(gotMeta, wantMeta) {
		t.Errorf("meta = %v, want %v", gotMeta, wantMeta)
	}
}

func TestResolvePipelineResultsDropsMissingTask(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Results: []tektontypes.PipelineResultSpec{
			{Name: "good", Value: tektontypes.ParamValue{
				Type: tektontypes.ParamTypeString, StringVal: "$(tasks.ok.results.v)",
			}},
			{Name: "bad", Value: tektontypes.ParamValue{
				Type: tektontypes.ParamTypeString, StringVal: "$(tasks.failed.results.v)",
			}},
		},
	}}
	results := map[string]map[string]string{"ok": {"v": "yes"}}
	got, errs := resolvePipelineResults(pl, results)
	if got["good"] != "yes" {
		t.Errorf("good = %v, want yes", got["good"])
	}
	if _, present := got["bad"]; present {
		t.Errorf("bad should be dropped, got = %v", got["bad"])
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly 1 drop error", errs)
	}
	if !strings.Contains(errs[0].Error(), `"bad"`) {
		t.Errorf("err message = %q, want it to mention the dropped result name", errs[0].Error())
	}
}

func TestResolvePipelineResultsDropsMissingResultName(t *testing.T) {
	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Results: []tektontypes.PipelineResultSpec{
			{Name: "x", Value: tektontypes.ParamValue{
				Type: tektontypes.ParamTypeString, StringVal: "$(tasks.t.results.absent)",
			}},
		},
	}}
	results := map[string]map[string]string{"t": {"present": "v"}}
	got, errs := resolvePipelineResults(pl, results)
	if _, present := got["x"]; present {
		t.Errorf("x should be dropped (referenced result name not produced)")
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want 1", errs)
	}
}
```

If `strings` isn't already imported in the new test file, add it.

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -count=1 -run TestResolvePipelineResults ./internal/engine/...
```

Expected: FAIL with `resolvePipelineResults undefined`.

- [ ] **Step 3: Implement the helper**

Create `internal/engine/pipeline_results.go`:

```go
package engine

import (
	"fmt"

	"github.com/danielfbm/tkn-act/internal/resolver"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// resolvePipelineResults evaluates Pipeline.spec.results once every task
// (including finally) is terminal. It returns the map of resolved
// values keyed by result name, plus a slice of per-result errors for
// entries that had to be dropped (referenced task didn't produce the
// result, expression couldn't resolve, etc.). Drops are not fatal —
// the caller emits each as an EvtError but does not change run status
// or exit code.
//
// Value shape mirrors ParamValue:
//   - ParamTypeString → string
//   - ParamTypeArray  → []string
//   - ParamTypeObject → map[string]string
//
// Returns (nil, nil) when the pipeline declared no results.
func resolvePipelineResults(pl tektontypes.Pipeline, results map[string]map[string]string) (map[string]any, []error) {
	if len(pl.Spec.Results) == 0 {
		return nil, nil
	}
	ctx := resolver.Context{
		// Pipeline results only ever reference task results — no params,
		// no context vars, no workspaces. Keeping the context narrow
		// guarantees a cleaner failure message if someone tries to use
		// other refs ("unknown reference $(params.x)" rather than a
		// silent miss).
		Results: results,
	}
	out := map[string]any{}
	var errs []error
	for _, spec := range pl.Spec.Results {
		switch spec.Value.Type {
		case tektontypes.ParamTypeString, "":
			s, err := resolver.Substitute(spec.Value.StringVal, ctx)
			if err != nil {
				errs = append(errs, fmt.Errorf("pipeline result %q dropped: %w", spec.Name, err))
				continue
			}
			out[spec.Name] = s
		case tektontypes.ParamTypeArray:
			items := make([]string, 0, len(spec.Value.ArrayVal))
			dropped := false
			for _, item := range spec.Value.ArrayVal {
				s, err := resolver.Substitute(item, ctx)
				if err != nil {
					errs = append(errs, fmt.Errorf("pipeline result %q dropped: %w", spec.Name, err))
					dropped = true
					break
				}
				items = append(items, s)
			}
			if !dropped {
				out[spec.Name] = items
			}
		case tektontypes.ParamTypeObject:
			obj := make(map[string]string, len(spec.Value.ObjectVal))
			dropped := false
			for k, v := range spec.Value.ObjectVal {
				s, err := resolver.Substitute(v, ctx)
				if err != nil {
					errs = append(errs, fmt.Errorf("pipeline result %q dropped: %w", spec.Name, err))
					dropped = true
					break
				}
				obj[k] = s
			}
			if !dropped {
				out[spec.Name] = obj
			}
		default:
			errs = append(errs, fmt.Errorf("pipeline result %q dropped: unknown value type %q", spec.Name, spec.Value.Type))
		}
	}
	return out, errs
}
```

- [ ] **Step 4: Run tests**

```bash
go test -count=1 -run TestResolvePipelineResults ./internal/engine/...
```

Expected: PASS (all 6 subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/engine/pipeline_results.go internal/engine/pipeline_results_test.go
git commit -m "feat(engine): resolvePipelineResults helper for Pipeline.spec.results"
```

---

### Task 3: Wire `resolvePipelineResults` into the docker-path engine

**Files:**
- Modify: `internal/engine/engine.go` (after the finally loop, before the `EvtRunEnd` emission)
- Test: `internal/engine/pipeline_results_engine_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `internal/engine/pipeline_results_engine_test.go`:

```go
package engine_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

// resultsBackend lets tests script per-task results.
type resultsBackend struct {
	results map[string]map[string]string // taskName → result name → value
	failFor map[string]bool              // taskName → return TaskFailed
}

func (b *resultsBackend) Prepare(_ context.Context, _ backend.RunSpec) error { return nil }
func (b *resultsBackend) Cleanup(_ context.Context) error                    { return nil }
func (b *resultsBackend) RunTask(_ context.Context, inv backend.TaskInvocation) (backend.TaskResult, error) {
	if b.failFor[inv.TaskName] {
		return backend.TaskResult{Status: backend.TaskFailed}, nil
	}
	return backend.TaskResult{
		Status:  backend.TaskSucceeded,
		Results: b.results[inv.TaskName],
	}, nil
}

func TestPipelineResultsSurfacedOnRunEnd(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: emit}
spec:
  results: [{name: commit}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: revision
      value: $(tasks.checkout.results.commit)
  tasks:
    - {name: checkout, taskRef: {name: emit}}
`))
	if err != nil {
		t.Fatal(err)
	}
	be := &resultsBackend{results: map[string]map[string]string{"checkout": {"commit": "abc123"}}}
	sink := &sliceSink{}
	res, err := engine.New(be, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Results["revision"]; got != "abc123" {
		t.Errorf("RunResult.Results[revision] = %v, want abc123", got)
	}
	// run-end event must carry the same map.
	var endEvt *reporter.Event
	for i := range sink.events {
		if sink.events[i].Kind == reporter.EvtRunEnd {
			ev := sink.events[i]
			endEvt = &ev
		}
	}
	if endEvt == nil {
		t.Fatalf("no run-end event in stream")
	}
	if got := endEvt.Results["revision"]; got != "abc123" {
		t.Errorf("run-end event Results[revision] = %v, want abc123", got)
	}
}

func TestPipelineResultsDroppedOnTaskFailure(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  results: [{name: v}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: surfaced
      value: $(tasks.ok.results.v)
    - name: dropped
      value: $(tasks.bad.results.v)
  tasks:
    - {name: ok,  taskRef: {name: t}}
    - {name: bad, taskRef: {name: t}}
`))
	if err != nil {
		t.Fatal(err)
	}
	be := &resultsBackend{
		results: map[string]map[string]string{"ok": {"v": "yes"}},
		failFor: map[string]bool{"bad": true},
	}
	sink := &sliceSink{}
	res, err := engine.New(be, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "failed" {
		t.Errorf("status = %q, want failed (a task failed; result drop must not change run status)", res.Status)
	}
	if got := res.Results["surfaced"]; got != "yes" {
		t.Errorf("Results[surfaced] = %v, want yes (task succeeded; result must surface)", got)
	}
	if _, present := res.Results["dropped"]; present {
		t.Errorf("Results[dropped] should be omitted, got %v", res.Results["dropped"])
	}
	// One EvtError event for the dropped result.
	var errEvts int
	for _, ev := range sink.events {
		if ev.Kind == reporter.EvtError {
			errEvts++
		}
	}
	if errEvts != 1 {
		t.Errorf("EvtError count = %d, want 1 (one dropped result)", errEvts)
	}
}

func TestPipelineResultsArrayAndObject(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: emit}
spec:
  results: [{name: a}, {name: b}]
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: list
      value:
        - $(tasks.t.results.a)
        - $(tasks.t.results.b)
    - name: meta
      value:
        first:  $(tasks.t.results.a)
        second: $(tasks.t.results.b)
  tasks:
    - {name: t, taskRef: {name: emit}}
`))
	if err != nil {
		t.Fatal(err)
	}
	be := &resultsBackend{results: map[string]map[string]string{"t": {"a": "alpha", "b": "beta"}}}
	sink := &sliceSink{}
	res, err := engine.New(be, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	gotList, ok := res.Results["list"].([]string)
	if !ok {
		t.Fatalf("Results[list] type = %T, want []string", res.Results["list"])
	}
	if !reflect.DeepEqual(gotList, []string{"alpha", "beta"}) {
		t.Errorf("Results[list] = %v, want [alpha beta]", gotList)
	}
	gotMeta, ok := res.Results["meta"].(map[string]string)
	if !ok {
		t.Fatalf("Results[meta] type = %T, want map[string]string", res.Results["meta"])
	}
	if gotMeta["first"] != "alpha" || gotMeta["second"] != "beta" {
		t.Errorf("Results[meta] = %v, want first=alpha second=beta", gotMeta)
	}
}
```

`sliceSink` already exists in `internal/engine/policy_test.go` (same `engine_test` package). Reuse it.

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -count=1 -run 'TestPipelineResults' ./internal/engine/...
```

Expected: FAIL — `Results` is empty on `RunResult` and on the run-end event.

- [ ] **Step 3: Wire the resolution call into `RunPipeline`**

In `internal/engine/engine.go`, find this block (around line 217-228):

```go
	// Either budget firing means the run timed out, regardless of how
	// individual task outcomes shook out (a budget kill is a "timeout"
	// even if backends reported infrafailed mid-flight).
	if pipeCtx.Err() != nil || finallyCtx.Err() != nil {
		overall = "timeout"
	}

	e.rep.Emit(reporter.Event{
		Kind: reporter.EvtRunEnd, Time: time.Now(),
		Status: overall, Duration: time.Since(overallStart),
	})

	return RunResult{Status: overall, Tasks: outcomes}, nil
```

Replace with:

```go
	// Either budget firing means the run timed out, regardless of how
	// individual task outcomes shook out (a budget kill is a "timeout"
	// even if backends reported infrafailed mid-flight).
	if pipeCtx.Err() != nil || finallyCtx.Err() != nil {
		overall = "timeout"
	}

	// Resolve Pipeline.spec.results once every task (incl. finally) is
	// terminal. Drops are non-fatal: each surfaces as an EvtError but
	// does not change overall status or the exit code.
	pipelineResults, resultErrs := resolvePipelineResults(pl, results)
	for _, err := range resultErrs {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtError, Time: time.Now(), Message: err.Error()})
	}

	e.rep.Emit(reporter.Event{
		Kind: reporter.EvtRunEnd, Time: time.Now(),
		Status: overall, Duration: time.Since(overallStart),
		Results: pipelineResults,
	})

	return RunResult{Status: overall, Tasks: outcomes, Results: pipelineResults}, nil
```

- [ ] **Step 4: Run tests**

```bash
go test -count=1 -race -run 'TestPipelineResults' ./internal/engine/...
```

Expected: PASS (all 3).

- [ ] **Step 5: Run the full engine suite to confirm no regressions**

```bash
go test -race -count=1 ./internal/engine/...
```

Expected: every test OK — including `TestEngine*`, the v1.3 timeout tests, and the v1.4 step-template tests.

- [ ] **Step 6: Commit**

```bash
git add internal/engine/engine.go internal/engine/pipeline_results_engine_test.go
git commit -m "feat(engine): surface Pipeline.spec.results on RunResult and run-end"
```

---

### Task 4: Wire pipeline results in the cluster path

**Files:**
- Modify: `internal/engine/engine.go` (the `runViaPipelineBackend` branch — forward `res.Results`)
- Modify: `internal/backend/cluster/run.go` (`watchPipelineRun` — read `pr.status.results` / legacy `pipelineResults` into `res.Results`)
- Test: `internal/backend/cluster/runpipeline_test.go`

The cluster controller already evaluates `Pipeline.spec.results` and writes the resolved values onto `pr.status.results` (Tekton v1; some Tekton releases used `pipelineResults` instead — read both for compatibility). We just have to extract them.

- [ ] **Step 1: Write the failing test (cluster)**

Append to `internal/backend/cluster/runpipeline_test.go`:

```go
// TestRunPipelineSurfacesResults: when the Tekton controller writes
// `status.results` on the PipelineRun, the cluster backend must
// forward those into PipelineRunResult.Results so the engine can
// emit them on the run-end event.
func TestRunPipelineSurfacesResults(t *testing.T) {
	be, dyn, _, _, _ := fakeBackend(t)

	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Results: []tektontypes.PipelineResultSpec{
			{Name: "revision", Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "$(tasks.t.results.commit)"}},
		},
		Tasks: []tektontypes.PipelineTask{{Name: "t", TaskRef: &tektontypes.TaskRef{Name: "x"}}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		Results: []tektontypes.ResultSpec{{Name: "commit"}},
		Steps:   []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}},
	}}
	tk.Metadata.Name = "x"

	prName := "p-resabcde"
	ns := "tkn-act-resabcde"

	// Driver writes Succeeded=True AND status.results = [{name:revision,value:abc}].
	stop := flipStatusWithResultsUntilStop(t, dyn, ns, prName, "True", "Succeeded",
		[]any{map[string]any{"name": "revision", "value": "abc"}})
	defer close(stop)

	res, err := be.RunPipeline(context.Background(), backend.PipelineRunInvocation{
		RunID: "resabcde", PipelineRunName: prName,
		Pipeline: pl, Tasks: map[string]tektontypes.Task{"x": tk},
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("status = %q, want succeeded", res.Status)
	}
	if got := res.Results["revision"]; got != "abc" {
		t.Errorf("Results[revision] = %v, want abc", got)
	}
}

// flipStatusWithResultsUntilStop is flipStatusUntilStop but also writes
// `status.results` to the PR.
func flipStatusWithResultsUntilStop(t *testing.T, dyn *dynamicfake.FakeDynamicClient, ns, prName, status, reason string, results []any) chan struct{} {
	t.Helper()
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-stop:
				return
			case <-deadline.C:
				return
			case <-ticker.C:
				obj, err := dyn.Resource(gvrPipelineRunTest).Namespace(ns).Get(context.Background(), prName, metav1.GetOptions{})
				if err != nil {
					continue
				}
				_ = unstructured.SetNestedSlice(obj.Object, []any{
					map[string]any{"type": "Succeeded", "status": status, "reason": reason},
				}, "status", "conditions")
				_ = unstructured.SetNestedSlice(obj.Object, results, "status", "results")
				_, _ = dyn.Resource(gvrPipelineRunTest).Namespace(ns).Update(context.Background(), obj, metav1.UpdateOptions{})
			}
		}
	}()
	return stop
}
```

- [ ] **Step 2: Run cluster test to verify it fails**

```bash
go test -count=1 -run TestRunPipelineSurfacesResults ./internal/backend/cluster/...
```

Expected: FAIL with `Results[revision] = <nil>, want abc`.

- [ ] **Step 3: Extract results in `watchPipelineRun`**

In `internal/backend/cluster/run.go`, find the block in `watchPipelineRun` that sets the terminal `res.Status` (around the `res.Tasks = b.collectTaskOutcomes(...)` line):

```go
				res.Status = mapPipelineRunStatus(status, reason)
				res.Reason = reason
				res.Message = message
				res.Ended = time.Now()
				res.Tasks = b.collectTaskOutcomes(ctx, in, ns)
```

Insert one line after `res.Tasks = ...`:

```go
				res.Results = extractPipelineResults(un)
```

Then add the helper at the bottom of the file (next to `mapPipelineRunStatus`):

```go
// extractPipelineResults reads `pr.status.results` (Tekton v1) into a
// generic map. Each entry has shape {name, value}; value may be a
// string, a []any (array of strings), or a map[string]any (object).
// We preserve the JSON-decoded shape: ParamTypeString → string,
// ParamTypeArray → []string, ParamTypeObject → map[string]string,
// matching how the docker-path resolvePipelineResults populates
// RunResult.Results.
//
// Falls back to the legacy `pr.status.pipelineResults` slot
// (pre-Tekton-v1) if `status.results` is missing — older Tekton
// releases the cluster integration may target use that name.
func extractPipelineResults(pr *unstructured.Unstructured) map[string]any {
	results, found, _ := unstructured.NestedSlice(pr.Object, "status", "results")
	if !found {
		results, found, _ = unstructured.NestedSlice(pr.Object, "status", "pipelineResults")
		if !found {
			return nil
		}
	}
	if len(results) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, r := range results {
		rm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name, _ := rm["name"].(string)
		if name == "" {
			continue
		}
		switch v := rm["value"].(type) {
		case string:
			out[name] = v
		case []any:
			arr := make([]string, 0, len(v))
			for _, item := range v {
				if s, ok := item.(string); ok {
					arr = append(arr, s)
				}
			}
			out[name] = arr
		case map[string]any:
			obj := make(map[string]string, len(v))
			for k, item := range v {
				if s, ok := item.(string); ok {
					obj[k] = s
				}
			}
			out[name] = obj
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
```

- [ ] **Step 4: Run cluster test**

```bash
go test -count=1 -run TestRunPipelineSurfacesResults ./internal/backend/cluster/...
```

Expected: PASS.

- [ ] **Step 5: Forward `res.Results` in the engine's pipeline-backend path**

In `internal/engine/engine.go`, find `runViaPipelineBackend`. The terminal block looks like:

```go
	out := RunResult{
		Status:  res.Status,
		Reason:  res.Reason,
		Message: res.Message,
		Tasks:   map[string]TaskOutcome{},
	}
	for n, oc := range res.Tasks {
		out.Tasks[n] = TaskOutcome{Status: oc.Status, Message: oc.Message, Results: oc.Results}
	}
	return out, nil
```

Replace with:

```go
	out := RunResult{
		Status:  res.Status,
		Reason:  res.Reason,
		Message: res.Message,
		Results: res.Results,
		Tasks:   map[string]TaskOutcome{},
	}
	for n, oc := range res.Tasks {
		out.Tasks[n] = TaskOutcome{Status: oc.Status, Message: oc.Message, Results: oc.Results}
	}
	return out, nil
```

And update the `EvtRunEnd` emission a few lines above:

```go
	e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: res.Status, Duration: dur, Message: endMsg})
```

→

```go
	e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: res.Status, Duration: dur, Message: endMsg, Results: res.Results})
```

- [ ] **Step 6: Run all cluster + engine tests**

```bash
go test -count=1 -race ./internal/backend/cluster/... ./internal/engine/...
```

Expected: every test OK.

- [ ] **Step 7: Commit**

```bash
git add internal/backend/cluster/run.go internal/backend/cluster/runpipeline_test.go internal/engine/engine.go
git commit -m "feat(cluster): forward pr.status.results into RunResult.Results"
```

---

### Task 5: Validator rejects results that reference unknown tasks

**Files:**
- Modify: `internal/validator/validator.go`
- Test: `internal/validator/validator_test.go`

We're conservative: only reject when the *task* in `$(tasks.X.results.Y)` doesn't exist in the pipeline's main + finally task names. Result-name existence isn't validated (some Tasks compute results dynamically; upstream Tekton doesn't reject at validate time either).

- [ ] **Step 1: Write the failing tests**

Append to `internal/validator/validator_test.go`:

```go
func TestValidatePipelineResultsReferencesUnknownTask(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  results: [{name: v}]
  steps: [{name: s, image: alpine, script: "true"}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: r
      value: $(tasks.notthere.results.v)
  tasks:
    - {name: a, taskRef: {name: t}}
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for unknown task ref in spec.results")
	}
	var found bool
	for _, e := range errs {
		if strings.Contains(e.Error(), "notthere") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("error did not name the unknown task: %v", errs)
	}
}

func TestValidatePipelineResultsKnownTaskOK(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  results: [{name: v}]
  steps: [{name: s, image: alpine, script: "true"}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: from-main
      value: $(tasks.a.results.v)
    - name: from-finally
      value: $(tasks.f.results.v)
  tasks:
    - {name: a, taskRef: {name: t}}
  finally:
    - {name: f, taskRef: {name: t}}
`)
	if errs := validator.Validate(b, "p", nil); len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestValidatePipelineResultsArrayAndObjectChecked(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  results: [{name: v}]
  steps: [{name: s, image: alpine, script: "true"}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  results:
    - name: list
      value:
        - $(tasks.a.results.v)
        - $(tasks.unknown.results.v)
    - name: obj
      value:
        ok:  $(tasks.a.results.v)
        bad: $(tasks.alsomissing.results.v)
  tasks:
    - {name: a, taskRef: {name: t}}
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) < 2 {
		t.Fatalf("expected at least 2 errors (unknown + alsomissing), got %v", errs)
	}
	joined := ""
	for _, e := range errs {
		joined += e.Error() + "\n"
	}
	if !strings.Contains(joined, "unknown") || !strings.Contains(joined, "alsomissing") {
		t.Errorf("errors did not name both unknown tasks: %v", errs)
	}
}
```

If `strings` isn't imported in `validator_test.go`, add it. `mustLoad` already exists from earlier validator tests; reuse it.

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -count=1 -run TestValidatePipelineResults ./internal/validator/...
```

Expected: FAIL — no current rule checks `Pipeline.spec.results` refs.

- [ ] **Step 3: Add the validation rule**

In `internal/validator/validator.go`, add a new section after the existing `8b. Pipeline-level timeouts` block and before section `9. Step.OnError values`:

```go
	// 8c. Pipeline.spec.results: every $(tasks.X.results.Y) reference
	// must name a task that exists in spec.tasks ∪ spec.finally. Result-
	// name existence isn't checked here (some Tasks compute results
	// dynamically; resolution-time error handling drops unknown names
	// non-fatally).
	if len(pl.Spec.Results) > 0 {
		known := map[string]bool{}
		for _, pt := range pl.Spec.Tasks {
			known[pt.Name] = true
		}
		for _, pt := range pl.Spec.Finally {
			known[pt.Name] = true
		}
		for _, r := range pl.Spec.Results {
			collectStrings(r.Value, func(s string) {
				for _, ref := range extractTaskRefs(s) {
					if !known[ref] {
						errs = append(errs, fmt.Errorf("pipeline result %q references unknown task %q (must be in spec.tasks or spec.finally)", r.Name, ref))
					}
				}
			})
		}
	}
```

Then add the helpers at the bottom of the file (after `parseTimeout`):

```go
// taskResultRefPat matches $(tasks.<name>.results.<anything>) — we
// only need to extract the <name> for ref validation.
var taskResultRefPat = regexp.MustCompile(`\$\(tasks\.([a-zA-Z][\w-]*)\.results\.[\w.-]+\)`)

// extractTaskRefs returns every task name referenced via
// $(tasks.X.results.Y) in s (in source order; duplicates allowed —
// the caller's known-set check is set-based anyway).
func extractTaskRefs(s string) []string {
	matches := taskResultRefPat.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// collectStrings calls fn once per string atom in v. For string-typed
// values that's the single StringVal; for array-typed, each element;
// for object-typed, each map value.
func collectStrings(v tektontypes.ParamValue, fn func(string)) {
	switch v.Type {
	case tektontypes.ParamTypeArray:
		for _, item := range v.ArrayVal {
			fn(item)
		}
	case tektontypes.ParamTypeObject:
		for _, item := range v.ObjectVal {
			fn(item)
		}
	default:
		fn(v.StringVal)
	}
}
```

Add `"regexp"` to the import block if not already present.

- [ ] **Step 4: Run tests**

```bash
go test -count=1 -run TestValidatePipelineResults ./internal/validator/...
```

Expected: PASS (all 3).

- [ ] **Step 5: Commit**

```bash
git add internal/validator/validator.go internal/validator/validator_test.go
git commit -m "feat(validator): reject Pipeline.spec.results refs to unknown tasks"
```

---

### Task 6: Pretty output appends one line per resolved result

**Files:**
- Modify: `internal/reporter/pretty.go`
- Test: `internal/reporter/reporter_test.go`

After the existing `PipelineRun <status> in <dur>` line, print `  <name>: <value>` for each resolved result (when the map is non-empty). Truncate values to 80 chars.

- [ ] **Step 1: Write the failing test**

Append to `internal/reporter/reporter_test.go`:

```go
func TestPrettyRunEndPrintsResults(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{Color: false, Verbosity: reporter.Normal})
	r.Emit(reporter.Event{
		Kind:     reporter.EvtRunEnd,
		Status:   "succeeded",
		Duration: 1500 * time.Millisecond,
		Results: map[string]any{
			"revision": "abc123",
			"files":    []string{"a.txt", "b.txt"},
			"meta":     map[string]string{"owner": "team-a"},
		},
	})
	out := buf.String()
	if !strings.Contains(out, "PipelineRun") {
		t.Fatalf("missing run summary line: %q", out)
	}
	for _, want := range []string{"revision", "abc123", "files", "a.txt", "meta", "team-a"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull output: %s", want, out)
		}
	}
}

func TestPrettyRunEndOmitsResultsWhenEmpty(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{Color: false, Verbosity: reporter.Normal})
	r.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Status: "succeeded", Duration: 100 * time.Millisecond})
	out := buf.String()
	if strings.Contains(out, "results:") {
		t.Errorf("output should not include a results section when none resolved: %q", out)
	}
}
```

If `time` / `strings` aren't already imported in `reporter_test.go`, add them.

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -count=1 -run TestPrettyRunEnd ./internal/reporter/...
```

Expected: FAIL — `revision` etc. not in output.

- [ ] **Step 3: Append the results tail to pretty's run-end branch**

In `internal/reporter/pretty.go`, find the `case EvtRunEnd:` block:

```go
	case EvtRunEnd:
		dur := e.Duration.Round(time.Millisecond)
		if p.verb >= Normal {
			fmt.Fprintln(p.w, p.pal.wrap(p.pal.dim, strings.Repeat("─", 40)))
		}
		fmt.Fprintf(p.w, "%s PipelineRun %s in %s",
			glyph(e.Status, p.pal),
			p.pal.wrap(p.pal.bold, statusWord(e.Status)),
			dur,
		)
		if e.Message != "" {
			fmt.Fprintf(p.w, "  %s", p.pal.wrap(p.pal.red, e.Message))
		}
		fmt.Fprintln(p.w)
```

Append immediately after the closing `fmt.Fprintln(p.w)`:

```go
		if len(e.Results) > 0 {
			// Stable iteration order so output is deterministic across runs.
			names := make([]string, 0, len(e.Results))
			for k := range e.Results {
				names = append(names, k)
			}
			sort.Strings(names)
			for _, name := range names {
				fmt.Fprintf(p.w, "  %s %s\n",
					p.pal.wrap(p.pal.bold, name+":"),
					formatResultValue(e.Results[name]),
				)
			}
		}
```

Then add the helper at the bottom of the file (after `or`):

```go
// formatResultValue renders a Pipeline.spec.results value for pretty
// output. Strings are passed through (truncated to 80 chars with an
// ellipsis if longer); arrays render as `[a, b, c]`; objects as
// `{k1: v1, k2: v2}`. Stable key order on objects.
func formatResultValue(v any) string {
	const max = 80
	truncate := func(s string) string {
		if len(s) <= max {
			return s
		}
		return s[:max-1] + "…"
	}
	switch t := v.(type) {
	case string:
		return truncate(t)
	case []string:
		return truncate("[" + strings.Join(t, ", ") + "]")
	case map[string]string:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+": "+t[k])
		}
		return truncate("{" + strings.Join(parts, ", ") + "}")
	default:
		return truncate(fmt.Sprintf("%v", v))
	}
}
```

Add `"sort"` to the import block if not already present.

- [ ] **Step 4: Run tests**

```bash
go test -count=1 -run 'TestPretty|TestJSONRunEndIncludesResults' ./internal/reporter/...
```

Expected: PASS — and existing pretty tests still PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/reporter/pretty.go internal/reporter/reporter_test.go
git commit -m "feat(reporter): pretty output appends Pipeline.spec.results after run summary"
```

---

### Task 7: Cross-backend e2e fixture

**Files:**
- Create: `testdata/e2e/pipeline-results/pipeline.yaml`
- Modify: `internal/e2e/fixtures/fixtures.go`

The fixture needs two results: a string from a main task (the simplest case) and an array containing a finally-task ref (proving finally outputs feed pipeline results). `WantStatus: "succeeded"` is enough — assertion of the resolved values themselves happens in unit tests (Tasks 3 and 4); the e2e fixture's job is "no crash, no schema-rejection on the cluster, run reaches terminal succeeded."

- [ ] **Step 1: Write the fixture YAML**

Create `testdata/e2e/pipeline-results/pipeline.yaml`:

```yaml
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: emit-commit}
spec:
  results:
    - name: commit
  steps:
    - name: emit
      image: alpine:3
      script: |
        printf abc123 > $(results.commit.path)
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: emit-id}
spec:
  results:
    - name: id
  steps:
    - name: emit
      image: alpine:3
      script: |
        printf notify-42 > $(results.id.path)
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: pipeline-results}
spec:
  results:
    - name: revision
      value: $(tasks.checkout.results.commit)
    - name: report
      value:
        - $(tasks.checkout.results.commit)
        - $(tasks.notify.results.id)
  tasks:
    - name: checkout
      taskRef: {name: emit-commit}
  finally:
    - name: notify
      taskRef: {name: emit-id}
```

- [ ] **Step 2: Add the fixture to the shared table**

In `internal/e2e/fixtures/fixtures.go`, inside the `All()` table, add this entry just after the existing `step-template` entry:

```go
		{Dir: "pipeline-results", Pipeline: "pipeline-results", WantStatus: "succeeded"},
```

- [ ] **Step 3: Compile-check both tag builds**

```bash
go vet -tags integration ./...
go vet -tags cluster ./...
```

Expected: both exit 0.

- [ ] **Step 4: Run the docker fixture locally if Docker is available (optional)**

```bash
docker info >/dev/null 2>&1 && go test -tags integration -run TestE2E/pipeline-results -count=1 ./internal/e2e/... || echo "no docker; CI will run it"
```

Expected: PASS if Docker is up.

- [ ] **Step 5: Commit**

```bash
git add testdata/e2e/pipeline-results/pipeline.yaml internal/e2e/fixtures/fixtures.go
git commit -m "test(e2e): pipeline-results fixture (cross-backend)"
```

---

### Task 8: Documentation convergence

**Files:**
- Modify: `cmd/tkn-act/agentguide_data.md`
- Modify: `AGENTS.md`
- Modify: `docs/test-coverage.md`
- Modify: `docs/short-term-goals.md`
- Modify: `docs/feature-parity.md`
- Modify: `README.md`

- [ ] **Step 1: Note pipeline results in `AGENTS.md`**

In `AGENTS.md`, find the existing `## Timeout disambiguation` section. Right above it (after `## Documentation rule: keep related docs in sync ...`), insert a new section:

```markdown
## Pipeline results (`Pipeline.spec.results`)

A Pipeline can declare named results computed from task results once
the run terminates. Tekton's syntax:

```yaml
spec:
  results:
    - name: revision
      value: $(tasks.checkout.results.commit)
    - name: report
      value:
        - $(tasks.test.results.summary)
        - $(tasks.notify.results.id)        # finally tasks count too
    - name: meta
      value:
        owner: $(params.team)               # only $(tasks.X.results.Y) actually resolves; other refs drop the entry
        sha:   $(tasks.checkout.results.commit)
```

Resolution semantics tkn-act follows:

| Aspect | Behavior |
|---|---|
| When | After the entire run completes (tasks + finally), regardless of overall status. |
| Source | The same accumulated task-result map that powers `$(tasks.X.results.Y)` in PipelineTask params. Finally tasks contribute. |
| Failure handling | If a referenced task didn't succeed, or the result name wasn't produced, the pipeline result is **dropped** (omitted from the output). One `error` event per dropped result is emitted; the run's status and exit code are NOT changed. |
| Types | string / array / object (mirrors `ParamValue`). JSON-encoded as the matching shape. |
| Cluster mode | tkn-act reads `pr.status.results` from the Tekton controller's verdict — it does not re-resolve locally. |

Where they show up:

- **JSON (`-o json`)**: a `results` map on the `run-end` event, e.g.
  `{"kind":"run-end","status":"succeeded","results":{"revision":"abc123","report":["abc123","notify-42"]}}`.
- **Pretty output**: one line per resolved result after the run-summary
  line, in stable (alphabetical) key order, values truncated to 80 chars.
- **Library API (`engine.RunResult`)**: a new `Results map[string]any`
  field with values typed as string / `[]string` / `map[string]string`.

Pipeline-result-substitution back into other expressions (e.g.
referencing `$(results.X)` somewhere in the same run) is **not**
supported — Tekton itself doesn't do this; pipeline results are
output-only.
```

- [ ] **Step 2: Mirror `AGENTS.md` into `cmd/tkn-act/agentguide_data.md`**

```bash
go generate ./cmd/tkn-act/
diff cmd/tkn-act/agentguide_data.md AGENTS.md
```

Expected: no diff.

- [ ] **Step 3: Run the embedded-guide test**

```bash
go test -count=1 ./cmd/tkn-act/...
```

Expected: PASS.

- [ ] **Step 4: Update `docs/test-coverage.md`**

In `docs/test-coverage.md`, find the `### -tags integration` table. Insert a row right after `step-template/`:

```markdown
| `pipeline-results/` | `Pipeline.spec.results`: string + array result, one referencing finally output |
```

- [ ] **Step 5: Mark Track 1 #6 done in `docs/short-term-goals.md`**

In the Track 1 table, change row 6's Status cell from:

```
| Type exists; resolver/JSON output don't surface them. Small. |
```

to:

```
| Done in v1.5 (PR for `feat: Pipeline.spec.results`). Engine resolves after finally; cluster reads `pr.status.results`; `run-end` event carries `results`. |
```

- [ ] **Step 6: Flip the `feature-parity.md` row**

In `docs/feature-parity.md` under `### DAG, params, results`, change:

```
| Pipeline-level `results` (surfaced on run-end) | `Pipeline.spec.results` | gap | both | none | none | docs/short-term-goals.md (Track 1 #6) |
```

to:

```
| Pipeline-level `results` (surfaced on run-end) | `Pipeline.spec.results` | shipped | both | pipeline-results | none | docs/superpowers/plans/2026-05-03-pipeline-results.md (Track 1 #6) |
```

- [ ] **Step 7: Run parity-check**

```bash
bash .github/scripts/parity-check.sh
```

Expected: `parity-check: docs/feature-parity.md, testdata/e2e/, and testdata/limitations/ are consistent.`

- [ ] **Step 8: Add a bullet to `README.md`**

In `README.md`, under `## Tekton features supported`, find the existing block of bullets. Insert this bullet right after the `Results (file-based at /tekton/results/<n>) and $(tasks.X.results.Y)` bullet:

```markdown
- `Pipeline.spec.results` — named outputs surfaced on the `run-end`
  event (string / array / object); resolved from task results after
  the entire run completes
```

- [ ] **Step 9: Commit**

```bash
git add cmd/tkn-act/agentguide_data.md AGENTS.md docs/test-coverage.md docs/short-term-goals.md docs/feature-parity.md README.md
git commit -m "docs: document Pipeline.spec.results; flip Track 1 #6 to shipped"
```

---

### Task 9: Final verification and PR

- [ ] **Step 1: Full local verification**

```bash
go vet ./... && go vet -tags integration ./... && go vet -tags cluster ./...
go build ./...
go test -race -count=1 ./...
bash .github/scripts/parity-check.sh
.github/scripts/tests-required.sh main HEAD
```

Expected: all exit 0; `parity-check` reports the docs and tree are consistent; every test package OK or no-test-files.

- [ ] **Step 2: Push branch and open PR**

```bash
git push -u origin feat/pipeline-results
gh pr create --title "feat: honor Pipeline.spec.results (Track 1 #6)" --body "$(cat <<'EOF'
## Summary

Closes Track 1 #6 of `docs/short-term-goals.md`. Honors Tekton's
`Pipeline.spec.results` on both backends:

- New `engine.resolvePipelineResults` evaluates each declared result
  after every task (including finally) terminates. Drops are
  non-fatal: one `EvtError` per dropped result, run status and exit
  code unchanged.
- New `Results map[string]any` on `RunResult` and on the `run-end`
  event. Values typed as string / `[]string` / `map[string]string`,
  matching `ParamValue`.
- Cluster backend reads `pr.status.results` (Tekton v1) /
  `status.pipelineResults` (legacy) and forwards the resolved values
  through `PipelineRunResult.Results`. No double resolution: cluster
  mode trusts the controller's verdict.
- Validator catches `$(tasks.X.results.Y)` refs whose `X` isn't in
  `spec.tasks` ∪ `spec.finally`. Result-name existence is left to
  resolution-time (drops non-fatally) since some Tasks compute
  results dynamically.
- Pretty output appends one line per resolved result (alphabetical,
  truncated to 80 chars) after the run-summary line.
- One cross-backend fixture (`pipeline-results`) exercises a string
  result + an array-of-task-result-refs that includes a finally task.

Implements `docs/superpowers/plans/2026-05-03-pipeline-results.md`.

## Test plan

- [x] `go vet ./...` × {default, integration, cluster}
- [x] `go build ./...`
- [x] `go test -race -count=1 ./...`
- [x] `bash .github/scripts/parity-check.sh`
- [x] tests-required script
- [ ] docker-integration CI — runs the new fixture
- [ ] cluster-integration CI — same fixture against real Tekton

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 3: Wait for CI green, then merge per project default**

```bash
gh pr merge <num> --squash --delete-branch
```

---

## Self-review notes

- **Spec coverage:** Type addition? `tektontypes.PipelineResultSpec` and `PipelineSpec.Results` already exist (verified at `internal/tektontypes/types.go:177-203`); no new type tasks. Resolution helper → Task 2. Engine wiring (docker path) → Task 3. Cluster pass-through (extract `pr.status.results`) + engine forwarding → Task 4. Validator → Task 5. Reporter shape (event + pretty) → Tasks 1 + 6. Cross-backend fixture → Task 7. Docs convergence → Task 8. Final ship → Task 9. Every "Out of scope" line in the prompt has a matching omission in the plan.
- **No placeholders:** every step has actual Go code or shell commands. The `<NUM>` PR-number placeholders in Task 8 step 5/6 follow the same convention as the v1.3 timeouts plan; they get filled in after Task 9 step 2 runs.
- **Type consistency:** `resolvePipelineResults(pl, results) (map[string]any, []error)` is the single new symbol introduced in Task 2 and called in Task 3. `extractPipelineResults(*unstructured.Unstructured) map[string]any` is the cluster-side dual, introduced in Task 4. `Results map[string]any` is the field name on `Event`, `RunResult`, and `PipelineRunResult` — same name everywhere so a future grep reveals all three together.
- **Backward compatibility:** All three new fields are optional / zero-value-safe; pipelines without `spec.results` produce empty maps, which serialize to nothing thanks to `,omitempty`. Existing tests are not expected to need changes; if any do (e.g. an exhaustive `RunResult` deepequal check), the test failure is expected and the fix is to add the empty `Results` field to the `want`.
- **Cluster failure mode:** if Tekton's controller fails to populate `status.results` (older controller version, schema bug), `extractPipelineResults` returns `nil` and the engine surfaces an empty `Results` map. We do **not** re-resolve locally as a fallback — that would create the very docker-vs-cluster divergence the prompt warns against.
- **Open questions for the reviewer to resolve before implementation runs:**
  1. **Pretty output truncation length.** The plan picks 80 chars. Should this be the terminal width minus the prefix, or stay fixed? Fixed-80 is the simplest and matches typical CI log readability; flagged for confirmation.
  2. **Where exactly to put the new pretty section visually.** Currently appended *after* the `PipelineRun <status> in <dur>` line, on subsequent indented lines. An alternative is a separator (`results:` heading + bullets). The plan picks the indented lines approach because it's two lines of code; flagged in case the reviewer prefers the heading.
  3. **`status.pipelineResults` legacy fallback.** I included it as a defense against running the cluster integration against an older Tekton install, but every Tekton release the project's `cluster up` installs uses v1's `status.results`. If the reviewer wants to drop the fallback entirely (one less code path), the plan can be simplified — flagged.
