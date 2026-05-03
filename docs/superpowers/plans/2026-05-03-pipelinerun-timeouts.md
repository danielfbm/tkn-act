# PipelineRun-level timeouts (`spec.timeouts`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Honor Tekton's `Pipeline.spec.timeouts.{pipeline,tasks,finally}` so a wall-clock budget set at the pipeline level reliably terminates the run on both backends with the existing `timeout` status (exit 6).

**Architecture:** Three nested deadlines wrap the existing engine loops — `pipeline` is the outer cap on the whole `RunPipeline` invocation, `tasks` bounds the DAG loop, and `finally` bounds the finally-tasks loop. When a deadline fires, in-flight tasks get cancelled (their per-task policy already returns `timeout`), not-yet-started tasks are emitted as `not-run`, and the run ends `timeout`. The cluster backend just passes the field through to Tekton's `PipelineRun.spec.timeouts`; the existing `mapPipelineRunStatus` already converts `Reason: PipelineRunTimeout` to status `timeout`, so cluster parity falls out for free.

**Tech Stack:** Go 1.25, no new dependencies. Reuses `context.WithTimeout`, the `internal/engine/policy.go` per-task primitive, and the cross-backend `internal/e2e/fixtures` table.

---

## Track 1 #2 context

This closes Track 1 #2 of `docs/short-term-goals.md`. The "Status" column says: *"Engine has the per-Task primitive; needs an outer policy loop and one new exit-code disambiguation."* Exit code 6 (`timeout`) already exists, so no new code is needed; the disambiguation lives in pretty output and the agent-guide.

## Tekton-upstream behavior we're matching

Tekton's `PipelineSpec.Timeouts` (v1):

```yaml
spec:
  timeouts:
    pipeline: "1h"   # whole-run wall clock (default 1h in upstream)
    tasks:    "55m"  # tasks DAG only
    finally:  "5m"   # finally tasks only
```

Constraints:
- All three are Go-style `time.Duration` strings (`"30s"`, `"5m"`, `"2h"`).
- If `tasks` and `finally` are both set, `tasks + finally` must be `≤ pipeline`.
- Setting `pipeline: "0"` means "no timeout" (we will reject `"0"` for v1 and require omission instead — simpler invariant; we can lift this later).
- A task that takes longer than its containing budget ends with status `timeout` and message describing which budget fired (`"pipeline timeout 1h exceeded"` etc.).

## Files to create / modify

| File | Why |
|---|---|
| `internal/tektontypes/types.go` | Add `Timeouts` struct + `Timeouts *Timeouts` on `PipelineSpec` |
| `internal/tektontypes/types_test.go` | JSON round-trip for `Timeouts` |
| `internal/validator/validator.go` | Parse durations, enforce `tasks + finally ≤ pipeline` |
| `internal/validator/validator_test.go` | Negative cases: malformed, zero, sum-too-big |
| `internal/engine/pipeline_timeouts.go` (new) | `pipelineTimeouts` parser + `applyToContext` helper |
| `internal/engine/pipeline_timeouts_test.go` (new) | Unit tests for parser + budget computation |
| `internal/engine/engine.go` | Wrap tasks loop and finally loop in their respective timeout contexts; rewrite `not-run` for tasks not started by the deadline |
| `internal/engine/engine_test.go` (or new `pipeline_timeouts_engine_test.go`) | Integration: pipeline that hits each of the three budgets |
| `internal/backend/cluster/run.go` | Inline `spec.timeouts` map onto the unstructured PipelineRun |
| `internal/backend/cluster/runpipeline_test.go` | Assert `spec.timeouts` is on the submitted PipelineRun |
| `internal/e2e/fixtures/fixtures.go` | Add three fixtures: `pipeline-timeout`, `tasks-timeout`, `finally-timeout` |
| `testdata/e2e/pipeline-timeout/pipeline.yaml` | New fixture (YAML) |
| `testdata/e2e/tasks-timeout/pipeline.yaml` | New fixture |
| `testdata/e2e/finally-timeout/pipeline.yaml` | New fixture |
| `cmd/tkn-act/agentguide_data.md` | Document the difference between Task-level and Pipeline-level timeout |
| `AGENTS.md` | Same change (kept in sync — already drifts and we want this PR to converge them on the new section) |
| `docs/test-coverage.md` | List the three new fixtures |
| `docs/short-term-goals.md` | Mark Track 1 #2 done (status column) |
| `docs/feature-parity.md` | Flip the `Pipeline.spec.timeouts...` row from `in-progress` to `shipped`, populate `e2e fixture` for each of the three new fixtures, link the PR. CI's `parity-check` job will block the PR if this is missing. |

## Out of scope (don't do here)

- Sidecars (Track 1 #1, separate spec).
- Matrix (Track 1 #3).
- StepActions / Resolvers (Track 1 #8/#9).
- "Default `pipeline: 1h` if unset" — upstream Tekton does this; we will *not* default for v1 (an unbounded run is the existing behavior and changing the default is a separate decision). Document the gap.
- `TaskRun.spec.timeout` from inside a `pipelineSpec.tasks[].taskRef` — covered by the v1.2 per-Task timeout we already ship.
- Renaming the v1.2 per-Task `TaskSpec.Timeout` field. It coexists.

---

### Task 1: Add `Timeouts` to the type model

**Files:**
- Modify: `internal/tektontypes/types.go` (PipelineSpec around line 155-162)
- Test: `internal/tektontypes/types_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/tektontypes/types_test.go`:

```go
func TestUnmarshalPipelineWithTimeouts(t *testing.T) {
	yaml := []byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  timeouts:
    pipeline: "1h"
    tasks: "55m"
    finally: "5m"
  tasks:
    - {name: a, taskRef: {name: t}}
`)
	var p tektontypes.Pipeline
	if err := sigsyaml.Unmarshal(yaml, &p); err != nil {
		t.Fatal(err)
	}
	if p.Spec.Timeouts == nil {
		t.Fatalf("Timeouts is nil")
	}
	if got, want := p.Spec.Timeouts.Pipeline, "1h"; got != want {
		t.Errorf("Pipeline = %q, want %q", got, want)
	}
	if got, want := p.Spec.Timeouts.Tasks, "55m"; got != want {
		t.Errorf("Tasks = %q, want %q", got, want)
	}
	if got, want := p.Spec.Timeouts.Finally, "5m"; got != want {
		t.Errorf("Finally = %q, want %q", got, want)
	}
}

func TestUnmarshalPipelineWithoutTimeouts(t *testing.T) {
	yaml := []byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: a, taskRef: {name: t}}
`)
	var p tektontypes.Pipeline
	if err := sigsyaml.Unmarshal(yaml, &p); err != nil {
		t.Fatal(err)
	}
	if p.Spec.Timeouts != nil {
		t.Errorf("Timeouts = %+v, want nil", p.Spec.Timeouts)
	}
}
```

If `sigsyaml` isn't already imported in `types_test.go`, add `sigsyaml "sigs.k8s.io/yaml"` to the import block.

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -run TestUnmarshalPipelineWith ./internal/tektontypes/...
```

Expected: FAIL with `Spec.Timeouts undefined`.

- [ ] **Step 3: Add the type and field**

In `internal/tektontypes/types.go`, append after the existing `PipelineSpec` struct:

```go
// Timeouts mirrors Tekton's PipelineSpec.Timeouts (tekton.dev/v1).
//
// Each field is a Go-style time.Duration string (e.g. "30s", "5m", "2h").
// Unset fields mean "no budget at this level". Validator enforces:
// durations parseable, none equals zero, and tasks+finally ≤ pipeline
// when all three are set.
type Timeouts struct {
	Pipeline string `json:"pipeline,omitempty"`
	Tasks    string `json:"tasks,omitempty"`
	Finally  string `json:"finally,omitempty"`
}
```

Then add the field to `PipelineSpec` (in the struct body, after `Results`):

```go
	Timeouts *Timeouts `json:"timeouts,omitempty"`
```

- [ ] **Step 4: Run tests**

```bash
go test -run TestUnmarshalPipelineWith ./internal/tektontypes/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tektontypes/types.go internal/tektontypes/types_test.go
git commit -m "feat(types): add PipelineSpec.Timeouts (Tekton v1)"
```

---

### Task 2: Validator rejects malformed and contradictory timeouts

**Files:**
- Modify: `internal/validator/validator.go`
- Test: `internal/validator/validator_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/validator/validator_test.go`:

```go
func TestValidateTimeoutsMalformed(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  timeouts: {pipeline: "1zz"}
  tasks:
    - {name: a, taskRef: {name: t}}
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: s, image: alpine, script: "true"}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for malformed pipeline timeout")
	}
}

func TestValidateTimeoutsZeroRejected(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  timeouts: {pipeline: "0"}
  tasks:
    - {name: a, taskRef: {name: t}}
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: s, image: alpine, script: "true"}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for zero timeout (use omission to mean no budget)")
	}
}

func TestValidateTimeoutsTasksPlusFinallyExceedsPipeline(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  timeouts: {pipeline: "10m", tasks: "8m", finally: "5m"}
  tasks:
    - {name: a, taskRef: {name: t}}
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: s, image: alpine, script: "true"}]
`)
	errs := validator.Validate(b, "p", nil)
	if len(errs) == 0 {
		t.Fatalf("expected error for tasks+finally > pipeline")
	}
}

func TestValidateTimeoutsAllValid(t *testing.T) {
	b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  timeouts: {pipeline: "10m", tasks: "8m", finally: "2m"}
  tasks:
    - {name: a, taskRef: {name: t}}
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: s, image: alpine, script: "true"}]
`)
	if errs := validator.Validate(b, "p", nil); len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}
```

If `mustLoad` doesn't exist in `validator_test.go` already, add it next to the test (loader returns a `*loader.Bundle`):

```go
func mustLoad(t *testing.T, yaml string) *loader.Bundle {
	t.Helper()
	b, err := loader.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
```

(If a helper with the same purpose exists under a different name, reuse it.)

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -run TestValidateTimeouts ./internal/validator/...
```

Expected: 3 of 4 FAIL (`expected error ..., got nil`); the all-valid one may already pass if Validate ignores the field.

- [ ] **Step 3: Add the validation rule**

Add to `internal/validator/validator.go`, inside the `Validate` function, after the existing pipeline-level checks (after `resolvedTasks` is built is fine):

```go
	if t := pl.Spec.Timeouts; t != nil {
		var pdur, tdur, fdur time.Duration
		var perr, terr, ferr error
		if t.Pipeline != "" {
			pdur, perr = parseTimeout("timeouts.pipeline", t.Pipeline)
			if perr != nil {
				out = append(out, perr)
			}
		}
		if t.Tasks != "" {
			tdur, terr = parseTimeout("timeouts.tasks", t.Tasks)
			if terr != nil {
				out = append(out, terr)
			}
		}
		if t.Finally != "" {
			fdur, ferr = parseTimeout("timeouts.finally", t.Finally)
			if ferr != nil {
				out = append(out, ferr)
			}
		}
		if perr == nil && terr == nil && ferr == nil &&
			pdur > 0 && tdur > 0 && fdur > 0 && tdur+fdur > pdur {
			out = append(out, fmt.Errorf(
				"timeouts.tasks (%s) + timeouts.finally (%s) > timeouts.pipeline (%s)",
				tdur, fdur, pdur))
		}
	}
```

Then add the helper at the bottom of the file:

```go
// parseTimeout parses a Tekton-style duration string and returns a
// non-zero positive duration. Empty strings should not reach this
// function. The error message includes the field name so users see
// "timeouts.pipeline: invalid duration" rather than just "invalid".
func parseTimeout(field, s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q: %w", field, s, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: must be positive (use omission to mean no budget), got %q", field, s)
	}
	return d, nil
}
```

Add `"time"` to the import block if not already present.

The variable used inside `Validate` to collect errors is currently called `out`. If yours is named differently, substitute the name; the rest of the structure is unchanged.

- [ ] **Step 4: Run tests**

```bash
go test -run TestValidateTimeouts ./internal/validator/...
```

Expected: PASS (all 4).

- [ ] **Step 5: Commit**

```bash
git add internal/validator/validator.go internal/validator/validator_test.go
git commit -m "feat(validator): reject malformed/contradictory PipelineSpec.Timeouts"
```

---

### Task 3: Engine helper to compute and apply the three deadlines

**Files:**
- Create: `internal/engine/pipeline_timeouts.go`
- Test: `internal/engine/pipeline_timeouts_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/engine/pipeline_timeouts_test.go`:

```go
package engine

import (
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

func TestPipelineTimeoutsParse(t *testing.T) {
	cases := []struct {
		name     string
		spec     *tektontypes.Timeouts
		wantPipe time.Duration
		wantTask time.Duration
		wantFin  time.Duration
		wantErr  bool
	}{
		{"nil", nil, 0, 0, 0, false},
		{"empty", &tektontypes.Timeouts{}, 0, 0, 0, false},
		{"pipeline only", &tektontypes.Timeouts{Pipeline: "10m"}, 10 * time.Minute, 0, 0, false},
		{"all three", &tektontypes.Timeouts{Pipeline: "10m", Tasks: "8m", Finally: "2m"}, 10 * time.Minute, 8 * time.Minute, 2 * time.Minute, false},
		{"malformed pipeline", &tektontypes.Timeouts{Pipeline: "x"}, 0, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePipelineTimeouts(tc.spec)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if got.Pipeline != tc.wantPipe || got.Tasks != tc.wantTask || got.Finally != tc.wantFin {
				t.Errorf("got %+v, want %v/%v/%v", got, tc.wantPipe, tc.wantTask, tc.wantFin)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -run TestPipelineTimeoutsParse ./internal/engine/...
```

Expected: FAIL with `parsePipelineTimeouts undefined`.

- [ ] **Step 3: Implement the helper**

Create `internal/engine/pipeline_timeouts.go`:

```go
package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

// pipelineTimeouts is the parsed form of tektontypes.Timeouts. Zero
// values mean "no budget at this level" — the engine treats them as
// pass-through.
type pipelineTimeouts struct {
	Pipeline time.Duration
	Tasks    time.Duration
	Finally  time.Duration
}

// parsePipelineTimeouts converts the raw spec into durations. Returns
// (zero, nil) when t is nil or all fields are empty — the caller is
// expected to skip wrapping in that case.
func parsePipelineTimeouts(t *tektontypes.Timeouts) (pipelineTimeouts, error) {
	out := pipelineTimeouts{}
	if t == nil {
		return out, nil
	}
	parse := func(field, s string) (time.Duration, error) {
		if s == "" {
			return 0, nil
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("timeouts.%s: %w", field, err)
		}
		return d, nil
	}
	var err error
	if out.Pipeline, err = parse("pipeline", t.Pipeline); err != nil {
		return pipelineTimeouts{}, err
	}
	if out.Tasks, err = parse("tasks", t.Tasks); err != nil {
		return pipelineTimeouts{}, err
	}
	if out.Finally, err = parse("finally", t.Finally); err != nil {
		return pipelineTimeouts{}, err
	}
	return out, nil
}

// tasksBudget returns the wall-clock available for the tasks DAG given
// the parsed timeouts. Falls through `pipeline - finally` when only
// `pipeline` is set; returns 0 (no budget) when neither is set.
func (p pipelineTimeouts) tasksBudget() time.Duration {
	if p.Tasks > 0 {
		return p.Tasks
	}
	if p.Pipeline > 0 && p.Finally > 0 {
		return p.Pipeline - p.Finally
	}
	if p.Pipeline > 0 {
		return p.Pipeline
	}
	return 0
}

// finallyBudget returns the wall-clock available for finally tasks.
func (p pipelineTimeouts) finallyBudget() time.Duration {
	if p.Finally > 0 {
		return p.Finally
	}
	if p.Pipeline > 0 && p.Tasks > 0 {
		return p.Pipeline - p.Tasks
	}
	if p.Pipeline > 0 {
		return p.Pipeline
	}
	return 0
}

// withMaybeBudget wraps ctx in a deadline if d > 0; otherwise returns
// the parent untouched with a no-op cancel.
func withMaybeBudget(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, d)
}
```

- [ ] **Step 4: Run tests**

```bash
go test -run TestPipelineTimeoutsParse ./internal/engine/...
```

Expected: PASS.

- [ ] **Step 5: Add a budget-computation test**

Append to `internal/engine/pipeline_timeouts_test.go`:

```go
func TestPipelineTimeoutsBudgets(t *testing.T) {
	cases := []struct {
		name        string
		p           pipelineTimeouts
		wantTasks   time.Duration
		wantFinally time.Duration
	}{
		{"all zero", pipelineTimeouts{}, 0, 0},
		{"only pipeline", pipelineTimeouts{Pipeline: 10 * time.Minute}, 10 * time.Minute, 10 * time.Minute},
		{"pipeline + finally", pipelineTimeouts{Pipeline: 10 * time.Minute, Finally: 2 * time.Minute}, 8 * time.Minute, 2 * time.Minute},
		{"pipeline + tasks", pipelineTimeouts{Pipeline: 10 * time.Minute, Tasks: 7 * time.Minute}, 7 * time.Minute, 3 * time.Minute},
		{"all three", pipelineTimeouts{Pipeline: 10 * time.Minute, Tasks: 7 * time.Minute, Finally: 2 * time.Minute}, 7 * time.Minute, 2 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.tasksBudget(); got != tc.wantTasks {
				t.Errorf("tasks = %v, want %v", got, tc.wantTasks)
			}
			if got := tc.p.finallyBudget(); got != tc.wantFinally {
				t.Errorf("finally = %v, want %v", got, tc.wantFinally)
			}
		})
	}
}
```

- [ ] **Step 6: Run tests**

```bash
go test -run TestPipelineTimeouts ./internal/engine/...
```

Expected: PASS (both tests).

- [ ] **Step 7: Commit**

```bash
git add internal/engine/pipeline_timeouts.go internal/engine/pipeline_timeouts_test.go
git commit -m "feat(engine): parse PipelineSpec.Timeouts and compute per-phase budgets"
```

---

### Task 4: Wire the budgets into the engine's tasks and finally loops

**Files:**
- Modify: `internal/engine/engine.go` (the `RunPipeline` body, around lines 95–171 in current `main`)
- Test: `internal/engine/pipeline_timeouts_engine_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `internal/engine/pipeline_timeouts_engine_test.go`:

```go
package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

// hangBackend blocks every RunTask until ctx is cancelled, so that any
// per-phase deadline triggers immediately.
type hangBackend struct{}

func (hangBackend) Prepare(_ context.Context, _ backend.RunSpec) error { return nil }
func (hangBackend) Cleanup(_ context.Context) error                    { return nil }
func (hangBackend) RunTask(ctx context.Context, _ backend.TaskInvocation) (backend.TaskResult, error) {
	<-ctx.Done()
	return backend.TaskResult{Status: backend.TaskInfraFailed, Err: ctx.Err()}, nil
}

func TestPipelineLevelTimeoutTriggers(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  steps: [{name: s, image: alpine, script: 'sleep 60'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  timeouts: {pipeline: "200ms"}
  tasks:
    - {name: a, taskRef: {name: t}}
`))
	if err != nil {
		t.Fatal(err)
	}
	sink := &sliceSink{}
	res, err := engine.New(hangBackend{}, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "timeout" {
		t.Errorf("status = %q, want timeout", res.Status)
	}
}

func TestTasksTimeoutDoesNotKillFinally(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: hang}
spec:
  steps: [{name: s, image: alpine, script: 'sleep 60'}]
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: cleanup}
spec:
  steps: [{name: s, image: alpine, script: 'true'}]
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  timeouts: {tasks: "100ms", finally: "5s"}
  tasks:
    - {name: a, taskRef: {name: hang}}
  finally:
    - {name: c, taskRef: {name: cleanup}}
`))
	if err != nil {
		t.Fatal(err)
	}
	// A backend that succeeds quickly for `cleanup` but hangs for `hang`.
	be := &recBackend{
		hangFor: map[string]time.Duration{"a": 60 * time.Second},
	}
	sink := &sliceSink{}
	start := time.Now()
	res, err := engine.New(be, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "timeout" {
		t.Errorf("status = %q, want timeout", res.Status)
	}
	if oc, ok := res.Tasks["c"]; !ok || oc.Status != "succeeded" {
		t.Errorf("finally cleanup = %+v, want succeeded", res.Tasks["c"])
	}
	if elapsed > 4*time.Second {
		t.Errorf("elapsed %v, want under 4s (tasks budget should not block finally)", elapsed)
	}
}

func TestNoTimeoutsBackwardCompatible(t *testing.T) {
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
    - {name: a, taskRef: {name: t}}
`))
	if err != nil {
		t.Fatal(err)
	}
	sink := &sliceSink{}
	res, err := engine.New(&recBackend{}, sink, engine.Options{}).RunPipeline(context.Background(), engine.PipelineInput{Bundle: b, Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "succeeded" {
		t.Errorf("status = %q, want succeeded", res.Status)
	}
}
```

`recBackend` and `sliceSink` already exist in `internal/engine/policy_test.go` (same package `engine_test`); just reuse them.

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -run 'TestPipelineLevelTimeoutTriggers|TestTasksTimeoutDoesNotKillFinally|TestNoTimeoutsBackwardCompatible' ./internal/engine/...
```

Expected: 2 of 3 FAIL (`status = succeeded, want timeout` and finally test). Backward-compat passes.

- [ ] **Step 3: Wire the budgets into the engine**

In `internal/engine/engine.go`, find the section that begins:

```go
	overallStart := time.Now()
	overall := "succeeded"

	// Execute levels.
	var mu sync.Mutex
	for _, level := range levels {
```

Replace it down to (and including) the `_ = eg.Wait()` end of the level loop with this block:

```go
	overallStart := time.Now()
	overall := "succeeded"

	timeouts, err := parsePipelineTimeouts(pl.Spec.Timeouts)
	if err != nil {
		e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: err.Error()})
		return RunResult{Status: "failed"}, err
	}
	pipeCtx, pipeCancel := withMaybeBudget(ctx, timeouts.Pipeline)
	defer pipeCancel()

	// Execute levels under the tasks budget.
	tasksCtx, tasksCancel := withMaybeBudget(pipeCtx, timeouts.tasksBudget())
	var mu sync.Mutex
levelLoop:
	for _, level := range levels {
		// If the tasks budget already fired, mark anything not yet
		// started as not-run and stop scheduling.
		if tasksCtx.Err() != nil {
			for _, taskName := range level {
				if _, alreadyDone := outcomes[taskName]; alreadyDone {
					continue
				}
				outcomes[taskName] = TaskOutcome{Status: "not-run"}
				e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskSkip, Time: time.Now(), Task: taskName, Message: "tasks timeout fired"})
			}
			continue
		}
		eg, gctx := errgroup.WithContext(tasksCtx)
		eg.SetLimit(e.opts.MaxParallel)
		for _, taskName := range level {
			tname := taskName
			pt := main[tname]

			mu.Lock()
			anyAncestorBad := false
			for _, ancestor := range upstream(g, tname) {
				if oc, ok := outcomes[ancestor]; ok {
					if oc.Status == "failed" || oc.Status == "not-run" || oc.Status == "skipped" {
						anyAncestorBad = true
						break
					}
				}
			}
			mu.Unlock()
			if anyAncestorBad {
				mu.Lock()
				outcomes[tname] = TaskOutcome{Status: "not-run"}
				mu.Unlock()
				e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskSkip, Time: time.Now(), Task: tname, Message: "upstream failure"})
				continue
			}

			eg.Go(func() error {
				e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskStart, Time: time.Now(), Task: tname})
				oc := e.runOneWithPolicy(gctx, in, pl, pt, params, results, runID, pipelineRunName)
				e.rep.Emit(reporter.Event{
					Kind: reporter.EvtTaskEnd, Time: time.Now(), Task: tname,
					Status: oc.Status, Duration: oc.Duration, Message: oc.Message, Attempt: oc.Attempt,
				})
				mu.Lock()
				outcomes[tname] = oc
				if oc.Results != nil {
					results[tname] = oc.Results
				}
				switch oc.Status {
				case "failed", "infrafailed":
					if overall != "timeout" {
						overall = "failed"
					}
				case "timeout":
					overall = "timeout"
				}
				mu.Unlock()
				return nil
			})
		}
		_ = eg.Wait()
		if tasksCtx.Err() != nil {
			overall = "timeout"
			tasksCancel()
			break levelLoop
		}
	}
	tasksCancel()
```

Then immediately below, replace the existing finally loop with this version that wraps it in the finally budget:

```go
	finallyCtx, finallyCancel := withMaybeBudget(pipeCtx, timeouts.finallyBudget())
	defer finallyCancel()
	for _, pt := range pl.Spec.Finally {
		if finallyCtx.Err() != nil {
			outcomes[pt.Name] = TaskOutcome{Status: "not-run"}
			e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskSkip, Time: time.Now(), Task: pt.Name, Message: "finally timeout fired"})
			continue
		}
		e.rep.Emit(reporter.Event{Kind: reporter.EvtTaskStart, Time: time.Now(), Task: pt.Name})
		oc := e.runOneWithPolicy(finallyCtx, in, pl, pt, params, results, runID, pipelineRunName)
		e.rep.Emit(reporter.Event{
			Kind: reporter.EvtTaskEnd, Time: time.Now(), Task: pt.Name,
			Status: oc.Status, Duration: oc.Duration, Message: oc.Message, Attempt: oc.Attempt,
		})
		outcomes[pt.Name] = oc
		switch oc.Status {
		case "failed", "infrafailed":
			if overall != "timeout" {
				overall = "failed"
			}
		case "timeout":
			overall = "timeout"
		}
	}
```

Finally, after the finally loop and before the `EvtRunEnd` emission, add the outer pipeline-budget check:

```go
	if pipeCtx.Err() != nil && overall != "failed" {
		overall = "timeout"
	}
```

(The existing `EvtRunEnd` line stays unchanged.)

- [ ] **Step 4: Run tests**

```bash
go test -count=1 -race -run 'TestPipelineLevelTimeoutTriggers|TestTasksTimeoutDoesNotKillFinally|TestNoTimeoutsBackwardCompatible|TestEngine' ./internal/engine/...
```

Expected: PASS — including all the existing engine tests (the change must be backward-compatible).

- [ ] **Step 5: Run the full unit test suite to confirm no regressions**

```bash
go test -race -count=1 ./...
```

Expected: every package OK.

- [ ] **Step 6: Commit**

```bash
git add internal/engine/engine.go internal/engine/pipeline_timeouts_engine_test.go
git commit -m "feat(engine): honor PipelineSpec.Timeouts.{pipeline,tasks,finally}"
```

---

### Task 5: Cluster backend passes `spec.timeouts` through to Tekton

**Files:**
- Modify: `internal/backend/cluster/run.go` (`buildPipelineRun`)
- Test: `internal/backend/cluster/runpipeline_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/backend/cluster/runpipeline_test.go`:

```go
// TestBuildPipelineRunInlinesTimeouts: when the Pipeline declares
// spec.timeouts, the cluster backend must copy that block onto the
// submitted PipelineRun's spec so the controller enforces it.
func TestBuildPipelineRunInlinesTimeouts(t *testing.T) {
	be, _, _, _, _ := fakeBackend(t)

	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		Timeouts: &tektontypes.Timeouts{Pipeline: "10m", Tasks: "8m", Finally: "2m"},
		Tasks:    []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{Steps: []tektontypes.Step{{Name: "s", Image: "alpine:3", Script: "true"}}}}
	tk.Metadata.Name = "t"

	prObj, err := be.BuildPipelineRunObject(backend.PipelineRunInvocation{
		RunID: "12345678", PipelineRunName: "p-12345678",
		Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
	}, "tkn-act-12345678")
	if err != nil {
		t.Fatal(err)
	}
	un := prObj.(*unstructured.Unstructured)
	got, found, err := unstructured.NestedMap(un.Object, "spec", "timeouts")
	if err != nil || !found {
		t.Fatalf("spec.timeouts missing on submitted PipelineRun")
	}
	if got["pipeline"] != "10m" || got["tasks"] != "8m" || got["finally"] != "2m" {
		t.Errorf("spec.timeouts = %v, want pipeline=10m tasks=8m finally=2m", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -run TestBuildPipelineRunInlinesTimeouts ./internal/backend/cluster/...
```

Expected: FAIL with `spec.timeouts missing on submitted PipelineRun`.

- [ ] **Step 3: Inline timeouts onto the PipelineRun**

In `internal/backend/cluster/run.go`, inside `buildPipelineRun`, find this block:

```go
	spec := map[string]any{
		"pipelineSpec": pipelineSpec,
	}
	if len(in.Params) > 0 {
```

And insert this just before the `if len(in.Params) > 0 {` line:

```go
	if t := in.Pipeline.Spec.Timeouts; t != nil {
		out := map[string]any{}
		if t.Pipeline != "" {
			out["pipeline"] = t.Pipeline
		}
		if t.Tasks != "" {
			out["tasks"] = t.Tasks
		}
		if t.Finally != "" {
			out["finally"] = t.Finally
		}
		if len(out) > 0 {
			spec["timeouts"] = out
		}
	}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test -count=1 -run TestBuildPipelineRunInlinesTimeouts ./internal/backend/cluster/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/cluster/run.go internal/backend/cluster/runpipeline_test.go
git commit -m "feat(cluster): pass PipelineSpec.Timeouts through to the submitted PipelineRun"
```

---

### Task 6: Cross-backend e2e fixture for pipeline-level timeout

**Files:**
- Create: `testdata/e2e/pipeline-timeout/pipeline.yaml`
- Modify: `internal/e2e/fixtures/fixtures.go`

- [ ] **Step 1: Write the fixture YAML**

Create `testdata/e2e/pipeline-timeout/pipeline.yaml`:

```yaml
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: hangs
spec:
  steps:
    - name: sleep
      image: alpine:3
      script: |
        sleep 30
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: pipeline-timeout
spec:
  timeouts:
    pipeline: "2s"
  tasks:
    - name: t
      taskRef: { name: hangs }
```

- [ ] **Step 2: Add the fixture to the shared table**

In `internal/e2e/fixtures/fixtures.go`, inside the `All()` table, add this entry just after the `timeout` fixture:

```go
		{Dir: "pipeline-timeout", Pipeline: "pipeline-timeout", WantStatus: "timeout"},
```

- [ ] **Step 3: Compile-check both tag builds**

```bash
go vet -tags integration ./...
go vet -tags cluster ./...
```

Expected: both exit 0.

- [ ] **Step 4: Run the fixture locally if Docker is available (optional)**

```bash
docker info >/dev/null 2>&1 && go test -tags integration -run TestE2E/pipeline-timeout -count=1 ./internal/e2e/... || echo "no docker; skip — CI will run it"
```

Expected: PASS if Docker is up.

- [ ] **Step 5: Commit**

```bash
git add testdata/e2e/pipeline-timeout/pipeline.yaml internal/e2e/fixtures/fixtures.go
git commit -m "test(e2e): pipeline-timeout fixture (cross-backend)"
```

---

### Task 7: Cross-backend e2e fixture for tasks-only and finally-only timeouts

**Files:**
- Create: `testdata/e2e/tasks-timeout/pipeline.yaml`
- Create: `testdata/e2e/finally-timeout/pipeline.yaml`
- Modify: `internal/e2e/fixtures/fixtures.go`

- [ ] **Step 1: Write the tasks-timeout fixture**

Create `testdata/e2e/tasks-timeout/pipeline.yaml`:

```yaml
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: hangs
spec:
  steps:
    - name: sleep
      image: alpine:3
      script: |
        sleep 30
---
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: cleanup
spec:
  steps:
    - name: ok
      image: alpine:3
      script: |
        echo "cleanup ran"
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: tasks-timeout
spec:
  timeouts:
    tasks: "2s"
    finally: "30s"
  tasks:
    - name: t
      taskRef: { name: hangs }
  finally:
    - name: c
      taskRef: { name: cleanup }
```

- [ ] **Step 2: Write the finally-timeout fixture**

Create `testdata/e2e/finally-timeout/pipeline.yaml`:

```yaml
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: trivial
spec:
  steps:
    - name: ok
      image: alpine:3
      script: |
        echo "ok"
---
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: hangs
spec:
  steps:
    - name: sleep
      image: alpine:3
      script: |
        sleep 30
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: finally-timeout
spec:
  timeouts:
    finally: "2s"
  tasks:
    - name: t
      taskRef: { name: trivial }
  finally:
    - name: c
      taskRef: { name: hangs }
```

- [ ] **Step 3: Add both to the shared fixture table**

In `internal/e2e/fixtures/fixtures.go`, after the `pipeline-timeout` entry from Task 6, add:

```go
		{Dir: "tasks-timeout", Pipeline: "tasks-timeout", WantStatus: "timeout"},
		{Dir: "finally-timeout", Pipeline: "finally-timeout", WantStatus: "timeout"},
```

- [ ] **Step 4: Compile-check both tag builds**

```bash
go vet -tags integration ./...
go vet -tags cluster ./...
```

Expected: both exit 0.

- [ ] **Step 5: Commit**

```bash
git add testdata/e2e/tasks-timeout/ testdata/e2e/finally-timeout/ internal/e2e/fixtures/fixtures.go
git commit -m "test(e2e): tasks-timeout and finally-timeout fixtures (cross-backend)"
```

---

### Task 8: Document the new behavior in the agent guide

**Files:**
- Modify: `cmd/tkn-act/agentguide_data.md` (the embedded copy `tkn-act agent-guide` prints)
- Modify: `AGENTS.md` (the human-facing copy)
- Modify: `docs/test-coverage.md`
- Modify: `docs/short-term-goals.md`

Note: the embedded copy currently drifts from `AGENTS.md`. This task converges them on the new content and adds the timeout disambiguation. Drift fixes outside the new section are in scope here so the embedded version is a true mirror after this PR.

- [ ] **Step 1: Update `cmd/tkn-act/agentguide_data.md`**

Replace the entire content of `cmd/tkn-act/agentguide_data.md` with the current `AGENTS.md`, then add a new section before `## Conventions`:

```markdown
## Timeout disambiguation

Two distinct timeout primitives can both end a task with `status: "timeout"`
and exit code 6:

| Field | Scope | Where |
|---|---|---|
| `Task.spec.timeout` (per-task) | Wall clock for one Task attempt; retries reset it. | v1.2 |
| `Pipeline.spec.timeouts.pipeline` | Whole-run wall clock (tasks + finally). | v1.3 |
| `Pipeline.spec.timeouts.tasks` | Wall clock for the tasks DAG only. | v1.3 |
| `Pipeline.spec.timeouts.finally` | Wall clock for finally tasks only. | v1.3 |

When a `pipeline` budget fires, in-flight tasks end `timeout` and unstarted
tasks end `not-run`. The terminal `task-end` event for a budget-killed task
reports `status: "timeout"` and a `message` such as `"pipeline timeout 2s
exceeded"`. The run-end status is `timeout`.

`tasks` and `finally` are independent budgets — exhausting `tasks` does not
shorten `finally`, and vice versa.

We do not default `timeouts.pipeline` to `1h` the way upstream Tekton does;
omission means "no budget at this level." This may change in a future
release.
```

(The exact heading position doesn't matter — anywhere between `## Pretty output` and `## Conventions` is fine.)

- [ ] **Step 2: Mirror the same changes into `AGENTS.md`**

Copy the new "Timeout disambiguation" section from `cmd/tkn-act/agentguide_data.md` into `AGENTS.md` at the same position.

- [ ] **Step 3: Verify the embedded guide test still passes**

```bash
go test -count=1 ./cmd/tkn-act/...
```

Expected: PASS. (The `agent-guide` command embeds the file via `//go:embed`; the test asserts a few canonical phrases are present.)

- [ ] **Step 4: Update `docs/test-coverage.md`**

In `docs/test-coverage.md`, find the `### -tags integration` table of fixtures and add three rows after the existing `volumes/` row:

```markdown
| `pipeline-timeout/` | `Pipeline.spec.timeouts.pipeline: 2s` triggers run-level timeout |
| `tasks-timeout/`    | `tasks` budget fires; `finally` block still runs to completion |
| `finally-timeout/`  | `finally` budget fires; `tasks` block already succeeded |
```

- [ ] **Step 5: Mark Track 1 #2 done in `docs/short-term-goals.md`**

In the Track 1 table, change the row for "PipelineRun-level timeouts" Status cell from:

```
| Not started. Engine has the per-Task primitive; needs an outer policy loop and one new exit-code disambiguation. |
```

to:

```
| Done in v1.3 (PR for `feat: PipelineSpec.Timeouts`). Outer policy + cluster pass-through; status `timeout`, exit code 6 unchanged. |
```

(Replace the PR number once the PR is open.)

- [ ] **Step 6: Update `docs/feature-parity.md`**

The `parity-check` CI job will fail the PR if this step is skipped — the
table is the canonical scoreboard and CI enforces five invariants on it
(see the doc's preamble).

In `docs/feature-parity.md`, find the row in the **Pipeline policy**
section that reads:

```
| `Pipeline.spec.timeouts.{pipeline,tasks,finally}` | `PipelineSpec.timeouts` | in-progress | both | none | none | docs/superpowers/plans/2026-05-03-pipelinerun-timeouts.md (Track 1 #2) |
```

Replace it with three rows — one per sub-feature — each marked
`shipped` and pointing at its e2e fixture:

```
| `Pipeline.spec.timeouts.pipeline` (whole-run wall clock) | `PipelineSpec.timeouts.pipeline` | shipped | both | pipeline-timeout | none | PR #<NUM> |
| `Pipeline.spec.timeouts.tasks` (tasks-DAG-only wall clock) | `PipelineSpec.timeouts.tasks` | shipped | both | tasks-timeout | none | PR #<NUM> |
| `Pipeline.spec.timeouts.finally` (finally-only wall clock) | `PipelineSpec.timeouts.finally` | shipped | both | finally-timeout | none | PR #<NUM> |
```

Replace `<NUM>` with this PR's number once it's open (Task 9 step 3).

Verify the table is consistent before committing:

```bash
bash .github/scripts/parity-check.sh
```

Expected: `parity-check: docs/feature-parity.md, testdata/e2e/, and testdata/limitations/ are consistent.`

- [ ] **Step 7: Commit**

```bash
git add cmd/tkn-act/agentguide_data.md AGENTS.md docs/test-coverage.md docs/short-term-goals.md docs/feature-parity.md
git commit -m "docs: document spec.timeouts, converge AGENTS.md with embedded guide, flip parity scoreboard"
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

Expected: all exit 0; `parity-check` reports the docs and tree are
consistent; every test package OK or no-test-files.

- [ ] **Step 2: Push branch and open PR**

```bash
git push -u origin feat/pipelinerun-timeouts
gh pr create --title "feat: honor Pipeline.spec.timeouts.{pipeline,tasks,finally} (Track 1 #2)" --body "$(cat <<'EOF'
## Summary

Closes Track 1 #2 of `docs/short-term-goals.md`. Honors Tekton's
`Pipeline.spec.timeouts` on both backends:

- Engine wraps the tasks loop in `tasksBudget()` and the finally loop
  in `finallyBudget()`; both nest inside a `pipeline` budget. Budget
  exhaustion ends in-flight tasks `timeout` and unstarted tasks
  `not-run`. Run-end status becomes `timeout` (exit 6).
- Cluster backend passes `spec.timeouts` straight through to the
  submitted PipelineRun. The existing `mapPipelineRunStatus` already
  converts `Reason: PipelineRunTimeout` → `timeout`, so cluster parity
  is automatic.
- Three cross-backend fixtures (`pipeline-timeout`, `tasks-timeout`,
  `finally-timeout`) exercise each budget; they run on both backends
  via `internal/e2e/fixtures`.

Implements `docs/superpowers/plans/2026-05-03-pipelinerun-timeouts.md`.

## Test plan

- [x] `go vet ./...` × {default, integration, cluster}
- [x] `go build ./...`
- [x] `go test -race -count=1 ./...`
- [x] tests-required script
- [ ] docker-integration CI — runs the three new fixtures
- [ ] cluster-integration CI — same fixtures on real Tekton

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 3: After the PR is open, update the short-term-goals link**

Update the `docs/short-term-goals.md` Status cell from Task 8 to reference the actual PR number, then `git commit --amend` the docs commit (or push a follow-up).

- [ ] **Step 4: Wait for CI green, then merge per project default**

```bash
gh pr merge <num> --squash --delete-branch
```

---

## Self-review notes

- **Spec coverage:** Three Tekton fields (`pipeline`, `tasks`, `finally`) → Tasks 3 (parser), 4 (engine wiring), 5 (cluster pass-through). Validator rules → Task 2. Type model → Task 1. Cross-backend invariant → Tasks 6+7 (fixtures hit `fixtures.All()`, both harnesses iterate). Docs → Task 8 (now 7 steps: agent-guide × 2, embedded-test, test-coverage, short-term-goals, **feature-parity scoreboard**, commit). The `parity-check` CI gate (added in PR #8 / commit 716b719) will block this PR if the scoreboard step is skipped.
- **No placeholders:** every step has the actual code or command. The "amend the docs commit" step in Task 9 is the only mild deferral; it's an after-PR-open polish, fine to skip. The PR-number placeholders in Task 8 step 5 and step 6 are deliberate (the number isn't known until Task 9 step 2 runs).
- **Type consistency:** `parsePipelineTimeouts` returns `pipelineTimeouts` (lowercase, package-internal); `tasksBudget`/`finallyBudget` are methods on it; `withMaybeBudget` is the wrapper. These names are stable across Tasks 3 and 4. The validator helper `parseTimeout` is unrelated and lives in `internal/validator`.
- **One-task non-regression:** Task 4 explicitly runs `TestEngine` to make sure the existing per-task timeout test (`TestEngineTaskTimeout`) still passes — important because the engine loop is rearranged.
