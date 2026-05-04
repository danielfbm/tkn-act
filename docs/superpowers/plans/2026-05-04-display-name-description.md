# `displayName` / `description` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Honor Tekton's optional `displayName` and `description` fields on `Task`, `Pipeline`, `PipelineTask` (under `spec.tasks` and `spec.finally`), and `Step`. Surface them on the JSON event stream as snake_case `display_name` and `description` keys, and prefer `displayName` over `name` in pretty output. Same shape on both docker and cluster backends.

**Architecture:** Pure type addition + event-field plumbing. Add `DisplayName` to `PipelineSpec`, `PipelineTask`, `TaskSpec`, and `Step`; add `Description` to `Step` (the only struct missing it). Extend `reporter.Event` with `DisplayName` / `Description`. Wire the engine: `run-start`/`run-end` carry pipeline-level fields; `task-*` events carry `PipelineTask.DisplayName` and the resolved `TaskSpec.Description`; `step-*` events carry the Step's. Cluster backend round-trips for free via `json.Marshal` of the inlined `pipelineSpec`; lock that in with a `BuildPipelineRunObject` regression test, plus an event-shape assertion on the cross-backend fixture.

**Tech Stack:** Go 1.25, no new dependencies. Reuses `internal/tektontypes`, `internal/engine`, `internal/reporter`, the cross-backend `internal/e2e/fixtures` table, and the existing `parity-check`, `tests-required`, and `coverage` CI gates.

---

## Track 1 #7 context

This closes Track 1 #7 of `docs/short-term-goals.md` and Track 2 #5 (`displayName` parity). The Status column says: *"Not started. Type addition + reporter change. Half a day."*

## Tekton-upstream behavior we're matching

Tekton v1 fields — taken verbatim from upstream:

```yaml
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: build-test}
spec:
  displayName: "Build & test pipeline"
  description: "Build the binary, run unit tests, then publish."
  tasks:
    - name: build
      displayName: "Compile binary"
      taskRef: {name: build-task}
    - name: test
      displayName: "Run unit tests"
      taskRef: {name: test-task}
      runAfter: [build]
  finally:
    - name: notify
      displayName: "Send notification"
      taskRef: {name: notify-task}
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: test-task}
spec:
  displayName: "Unit-test runner"
  description: "Runs `go test ./...` against the workspace."
  steps:
    - name: compile
      displayName: "Compile"
      description: "Compile the test binary so vet / lint can re-use the artefact."
      image: golang:1.25
      script: 'go test -c ./...'
```

Plain-string fields. No substitution. No type mutation across PRs.

## Field placement summary (mirrors the spec §3)

| Type | New field | YAML key |
|---|---|---|
| `PipelineSpec` | `DisplayName string` | `displayName` |
| `PipelineTask` | `DisplayName string` | `displayName` |
| `TaskSpec` | `DisplayName string` | `displayName` |
| `Step` | `DisplayName string` | `displayName` |
| `Step` | `Description string` | `description` |

`PipelineSpec.Description` and `TaskSpec.Description` already exist —
no struct change for those, just surfacing.

## Event field summary (mirrors the spec §4)

JSON keys are snake_case for the new fields:

| Event kind | `display_name` source | `description` source |
|---|---|---|
| `run-start` | `Pipeline.Spec.DisplayName` | `Pipeline.Spec.Description` |
| `run-end` | `Pipeline.Spec.DisplayName` | — |
| `task-start` | `PipelineTask.DisplayName` | resolved `TaskSpec.Description` |
| `task-end` | `PipelineTask.DisplayName` | — |
| `task-skip` | `PipelineTask.DisplayName` | — |
| `task-retry` | `PipelineTask.DisplayName` | — |
| `step-log` | `Step.DisplayName` | — |
| `error` | — | — |

**Not emitted today:** `EvtStepStart` and `EvtStepEnd` are defined as
enum values and rendered by `internal/reporter/pretty.go`, but no
production code emits them (verified:
`grep -rn 'EvtStepStart\|EvtStepEnd' internal/ | grep -v _test.go`
matches only the enum declaration and the pretty switch). Adding
emission is **out of scope for this plan** — it would expand the
public JSON contract beyond the UX scope of Track 1 #7 and requires a
separate AGENTS.md call-out. Step `displayName` / `description` are
surfaced in v1.5 only via:
- `step-log` (carries `display_name`, but not `description`).
- Pretty output (`prefixOf` uses the step's `displayName`).

## Files to create / modify

| File | Why |
|---|---|
| `internal/tektontypes/types.go` | Add `DisplayName` to `PipelineSpec`, `PipelineTask`, `TaskSpec`; add `DisplayName` + `Description` to `Step`. |
| `internal/tektontypes/types_test.go` | YAML round-trip tests for every new field. |
| `internal/reporter/event.go` | Add `DisplayName string \`json:"display_name,omitempty"\`` and `Description string \`json:"description,omitempty"\`` to `Event`. |
| `internal/reporter/reporter_test.go` | Assert snake_case keys on JSON output for every event kind that carries them. |
| `internal/reporter/pretty.go` | New helper `labelOf(name, displayName) string` returning `displayName` when non-empty, otherwise `name`. Use it in every place that prints a Pipeline / Task / Step name. |
| `internal/reporter/pretty_test.go` | Two-Step / two-Task pipeline; verify rendered output uses the displayName when set. |
| `internal/reporter/event.go` (`LogSink` adapter) | Extend the `LogSink.StepLog` adapter to accept the step's `displayName` and wire it into `Event.DisplayName`. |
| `internal/backend/backend.go` | Update the `LogSink` interface signature to match (`StepLog(taskName, stepName, stepDisplayName, stream, line string)`). The interface is internal — only the two backends implement it. |
| `internal/backend/docker/docker.go:386` | The docker backend's `StepLog` callsite — pass the resolved Step's `DisplayName`. |
| `internal/backend/cluster/run.go:540` | The **cluster** backend's `StepLog` callsite — also pass the resolved Step's `DisplayName`. (Both backends must be updated in lockstep with the interface change.) |
| `internal/engine/engine.go` | Populate `DisplayName` / `Description` on every event the engine emits: `run-start`, `run-end`, `task-start`, `task-end`, `task-skip`, `task-retry`. Also populate on synthesised cluster events in `emitClusterTaskEvents` (read from the input bundle, not from controller verdict). |
| `internal/engine/display_name_test.go` (new) | Recording-Reporter integration tests for both docker and cluster paths. |
| `internal/reporter/reporter_test.go` | Fake-`LogSink` unit test asserting `StepLog` emits an `EvtStepLog` event with the step's `display_name`. |
| `internal/backend/cluster/runpipeline_test.go` | New test `TestBuildPipelineRunRoundTripsDisplayName` asserting every new field round-trips through the inlined `spec.pipelineSpec`. |
| `internal/e2e/fixtures/fixtures.go` | (a) Add `WantEventFields map[string]map[string]string` to `Fixture`. (b) Add the `display-name-description` fixture entry. |
| `internal/e2e/e2e_test.go` and `internal/clustere2e/cluster_e2e_test.go` | Extend the existing `assertEventShape` helper in **both** harnesses to honor `WantEventFields`. |
| `testdata/e2e/display-name-description/pipeline.yaml` | New cross-backend fixture. |
| `cmd/tkn-act/agentguide_data.md` | Document the new event fields. |
| `AGENTS.md` | Mirror agentguide_data.md. |
| `README.md` | One bullet under "Tekton features supported." |
| `docs/test-coverage.md` | List the new fixture under both `### -tags integration` and `### -tags cluster`. |
| `docs/short-term-goals.md` | Mark Track 1 #7 done; flip Track 2 #5. |
| `docs/feature-parity.md` | Flip the `displayName / description` row from `gap` → `shipped`, populate `e2e fixture`. |

## Out of scope (don't do here)

- Substitution inside `displayName` / `description` (`$(params.X)` etc.). Tekton supports it; we pass strings through literally for v1.5.
- Surfacing `description` on per-`Param`, per-`Result`, or per-`Workspace`. The structs already carry it (`tektontypes`); adding event fields for them is a separate ask.
- Adding a `tkn-act describe -f pipeline.yaml` introspection command.
- StepTemplate-level `displayName` (each Step is its own thing — see spec §3).
- Renaming any existing camelCase event field. Public-contract rule.
- Adding length limits or sanitisation; Tekton doesn't either.

---

### Task 1: Add the type fields

**Files:**
- Modify: `internal/tektontypes/types.go`
- Test: `internal/tektontypes/types_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/tektontypes/types_test.go`:

```go
func TestUnmarshalPipelineWithDisplayNameAndDescription(t *testing.T) {
	in := []byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  displayName: "Build & test"
  description: "Build then test."
  tasks:
    - name: t
      displayName: "Compile binary"
      taskRef: {name: t}
  finally:
    - name: f
      displayName: "Notify"
      taskRef: {name: t}
`)
	var got Pipeline
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.DisplayName != "Build & test" {
		t.Errorf("Pipeline.Spec.DisplayName = %q", got.Spec.DisplayName)
	}
	if got.Spec.Description != "Build then test." {
		t.Errorf("Pipeline.Spec.Description = %q", got.Spec.Description)
	}
	if got.Spec.Tasks[0].DisplayName != "Compile binary" {
		t.Errorf("Tasks[0].DisplayName = %q", got.Spec.Tasks[0].DisplayName)
	}
	if got.Spec.Finally[0].DisplayName != "Notify" {
		t.Errorf("Finally[0].DisplayName = %q", got.Spec.Finally[0].DisplayName)
	}
}

func TestUnmarshalTaskWithDisplayNameAndStepFields(t *testing.T) {
	in := []byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  displayName: "Unit-test runner"
  description: "Runs go test."
  steps:
    - name: s
      displayName: "Compile"
      description: "Compile the test binary."
      image: golang:1.25
      script: 'true'
`)
	var got Task
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.DisplayName != "Unit-test runner" {
		t.Errorf("Task.Spec.DisplayName = %q", got.Spec.DisplayName)
	}
	if got.Spec.Steps[0].DisplayName != "Compile" {
		t.Errorf("Step.DisplayName = %q", got.Spec.Steps[0].DisplayName)
	}
	if got.Spec.Steps[0].Description != "Compile the test binary." {
		t.Errorf("Step.Description = %q", got.Spec.Steps[0].Description)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -count=1 -run 'TestUnmarshalPipelineWithDisplayNameAndDescription|TestUnmarshalTaskWithDisplayNameAndStepFields' ./internal/tektontypes/...
```

Expected: FAIL with `DisplayName undefined`.

- [ ] **Step 3: Add the fields**

In `internal/tektontypes/types.go`:

`PipelineSpec` — add `DisplayName` after `Description`:
```go
type PipelineSpec struct {
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	// ... existing fields unchanged ...
}
```

`PipelineTask` — add `DisplayName` after `Name`:
```go
type PipelineTask struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	// ... existing fields unchanged ...
}
```

`TaskSpec` — add `DisplayName` next to `Description`:
```go
type TaskSpec struct {
	// ... existing fields ...
	Description  string        `json:"description,omitempty"`
	DisplayName  string        `json:"displayName,omitempty"`
	// ... existing fields ...
}
```

`Step` — add `DisplayName` and `Description` after `Name`:
```go
type Step struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	// ... existing fields unchanged ...
}
```

- [ ] **Step 4: Run tests**

```bash
go test -count=1 ./internal/tektontypes/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tektontypes/types.go internal/tektontypes/types_test.go
git commit -m "feat(types): add displayName/description fields (Tekton v1)"
```

---

### Task 2: Add event-struct fields

**Files:**
- Modify: `internal/reporter/event.go`
- Test: `internal/reporter/reporter_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/reporter/reporter_test.go`:

```go
func TestJSONEventCarriesDisplayNameAndDescription(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewJSON(&buf)
	r.Emit(reporter.Event{
		Kind:        reporter.EvtRunStart,
		Pipeline:    "p",
		DisplayName: "Build & test",
		Description: "Build then test.",
	})
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatal(err)
	}
	if got["display_name"] != "Build & test" {
		t.Errorf("display_name = %v", got["display_name"])
	}
	if got["description"] != "Build then test." {
		t.Errorf("description = %v", got["description"])
	}
}

func TestJSONEventOmitsEmptyDisplayName(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewJSON(&buf)
	r.Emit(reporter.Event{Kind: reporter.EvtRunStart, Pipeline: "p"})
	if bytes.Contains(buf.Bytes(), []byte("display_name")) {
		t.Errorf("expected display_name to be omitted, got: %s", buf.Bytes())
	}
	if bytes.Contains(buf.Bytes(), []byte(`"description"`)) {
		t.Errorf("expected description to be omitted, got: %s", buf.Bytes())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -count=1 -run 'TestJSONEventCarriesDisplayName|TestJSONEventOmitsEmpty' ./internal/reporter/...
```

Expected: FAIL with `DisplayName undefined`.

- [ ] **Step 3: Add the fields to `Event`**

In `internal/reporter/event.go`, append to the `Event` struct (after the existing `Results` field):

```go
	// DisplayName is the human-readable label for the entity this event
	// describes. Carried on:
	//   - run-start, run-end:    Pipeline.spec.displayName
	//   - task-start/end/skip/retry: PipelineTask.displayName
	//   - step-log:              Step.displayName
	// Empty when the source YAML didn't set a displayName. Agents
	// should fall back to the corresponding `pipeline` / `task` / `step`
	// name field.
	//
	// Note: step-start / step-end are defined as event kinds but are
	// not emitted by production code today (v1.5). If they are added
	// later, they will carry DisplayName the same way step-log does.
	DisplayName string `json:"display_name,omitempty"`

	// Description carries:
	//   - run-start: Pipeline.spec.description
	//   - task-start: TaskSpec.description (the resolved Task)
	// Omitted from terminal events to keep line size down — the start
	// event already carried it. Also omitted from step-log to avoid
	// ballooning every line of streamed output.
	Description string `json:"description,omitempty"`
```

- [ ] **Step 4: Run tests**

```bash
go test -count=1 ./internal/reporter/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/reporter/event.go internal/reporter/reporter_test.go
git commit -m "feat(reporter): add display_name/description fields to Event"
```

---

### Task 3: Pretty renderer prefers `displayName` over `name`

**Files:**
- Modify: `internal/reporter/pretty.go`
- Test: `internal/reporter/pretty_test.go` (create if absent)

- [ ] **Step 1: Write the failing test**

If `internal/reporter/pretty_test.go` doesn't exist, create it; otherwise append:

```go
package reporter_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/reporter"
)

// labelOf is the unit under test (lowercase / package-internal). To
// keep these tests in the external _test package, we assert against
// the rendered output instead of calling labelOf directly. The
// assertion shape is structured: rather than substring-checking
// `" p "`, we explicitly assert that the pipeline's chosen label
// substring is present and the raw `name` substring is absent from
// the relevant lines.

func TestPrettyPrefersDisplayNameOverName(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewPretty(&buf, reporter.PrettyOptions{Color: false, Verbosity: reporter.Normal})

	r.Emit(reporter.Event{Kind: reporter.EvtRunStart, Pipeline: "p-raw", DisplayName: "Build & test"})
	r.Emit(reporter.Event{Kind: reporter.EvtTaskEnd, Task: "t-raw", DisplayName: "Compile binary", Status: "succeeded"})
	r.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Status: "succeeded"})

	out := buf.String()
	if !strings.Contains(out, "Build & test") {
		t.Errorf("missing pipeline displayName in pretty output:\n%s", out)
	}
	if !strings.Contains(out, "Compile binary") {
		t.Errorf("missing task displayName in pretty output:\n%s", out)
	}
	// Structured assertion: when displayName is set, the raw name
	// MUST NOT appear anywhere in the pretty output. We pick distinct
	// raw-name strings ("p-raw", "t-raw") that cannot collide with
	// any other substring in the pretty output.
	if strings.Contains(out, "p-raw") {
		t.Errorf("pretty output leaked raw pipeline name 'p-raw' even though displayName is set; got:\n%s", out)
	}
	if strings.Contains(out, "t-raw") {
		t.Errorf("pretty output leaked raw task name 't-raw' even though displayName is set; got:\n%s", out)
	}
}

// Fallback: empty displayName MUST use the raw name. One assertion per
// label site (run-start, task-start, task-end, task-skip, step-log),
// so a future refactor can't drop labelOf at one site silently. (Task 5
// adds the step-log fallback case once StepLog plumbs displayName.)
func TestPrettyFallsBackToNameWhenDisplayNameEmpty(t *testing.T) {
	cases := []struct {
		name string
		evt  reporter.Event
		want string
	}{
		{"run-start", reporter.Event{Kind: reporter.EvtRunStart, Pipeline: "p-only"}, "p-only"},
		{"task-start", reporter.Event{Kind: reporter.EvtTaskStart, Task: "t-only"}, "t-only"},
		{"task-end", reporter.Event{Kind: reporter.EvtTaskEnd, Task: "t-end-only", Status: "succeeded"}, "t-end-only"},
		{"task-skip", reporter.Event{Kind: reporter.EvtTaskSkip, Task: "t-skip-only", Message: "when=false"}, "t-skip-only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			r := reporter.NewPretty(&buf, reporter.PrettyOptions{Color: false, Verbosity: reporter.Verbose})
			r.Emit(tc.evt)
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("expected raw name %q in pretty output (no displayName); got:\n%s", tc.want, buf.String())
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -count=1 -run TestPretty ./internal/reporter/...
```

Expected: FAIL — current renderer doesn't read `DisplayName`.

- [ ] **Step 3: Implement `labelOf`**

In `internal/reporter/pretty.go`, add (next to `or` near the bottom):

```go
// labelOf returns displayName when non-empty, otherwise name. Used
// everywhere the pretty renderer prints a Pipeline / Task / Step
// identifier; agents reading -o json get both the raw name and the
// displayName separately.
func labelOf(name, displayName string) string {
	if displayName != "" {
		return displayName
	}
	return name
}
```

Then update each rendering site in `pretty.go::Emit`:

- `EvtRunStart` line: replace `or(e.Pipeline, "pipeline")` with
  `labelOf(or(e.Pipeline, "pipeline"), e.DisplayName)`.
- `EvtTaskStart` line: replace `e.Task` with `labelOf(e.Task, e.DisplayName)`.
- `EvtTaskSkip` and `EvtTaskEnd`: replace `e.Task` with `labelOf(e.Task, e.DisplayName)`.
- `EvtStepLog` prefix: `prefixOf(e.Task, labelOf(e.Step, e.DisplayName))`
  — `e.DisplayName` here is the step's displayName plumbed via the
  extended `LogSink.StepLog` (Task 5).
- `EvtStepStart` / `EvtStepEnd` rendering sites: these branches exist
  in the pretty switch but are not hit by production code today (no
  emission). We still update them for consistency — same rule as
  EvtStepLog: `prefixOf(e.Task, labelOf(e.Step, e.DisplayName))` —
  so that if emission is added later the renderer already does the
  right thing. No new pretty-test for these branches; their behavior
  is exercised via the EvtStepLog path.

(If a future enhancement wants both task and step displayNames in step
event prefixes, the engine can populate a richer struct — out of scope.)

- [ ] **Step 4: Run tests**

```bash
go test -count=1 ./internal/reporter/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/reporter/pretty.go internal/reporter/pretty_test.go
git commit -m "feat(reporter): pretty renderer prefers displayName over name"
```

---

### Task 4: Engine emits `display_name` and `description` on run + task events

**Scope:** populate `DisplayName` / `Description` on the events the
engine itself emits — `run-start`, `run-end`, `task-start`, `task-end`,
`task-skip`, `task-retry` — across both the docker path
(`engine.RunPipeline`) and the cluster path (`runViaPipelineBackend`
+ `emitClusterTaskEvents`). Step-level plumbing lives in Task 5.

**Files:**
- Modify: `internal/engine/engine.go`
- Test: `internal/engine/display_name_test.go` (new)

**Decision (resolved from spec §6.1):** Step-event emission is **not**
expanded in this plan (no production code emits `EvtStepStart` /
`EvtStepEnd` today; out of scope per the "Event field summary" note).
The `LogSink.StepLog` signature change for `step-log` is in Task 5.

- [ ] **Step 1: Locate every existing engine emission site**

```bash
grep -rn 'EvtRunStart\|EvtRunEnd\|EvtTaskStart\|EvtTaskEnd\|EvtTaskSkip\|EvtTaskRetry' /workspaces/tkn-act/internal/engine/ | grep -v _test.go
```

Note every file:line; Steps 4-6 modify each. Confirm all sites are in
`internal/engine/engine.go` (no other emitter under `internal/`).

- [ ] **Step 2: Write the failing engine test**

Create `internal/engine/display_name_test.go`:

```go
package engine_test

import (
	"context"
	"testing"

	"github.com/danielfbm/tkn-act/internal/engine"
	"github.com/danielfbm/tkn-act/internal/loader"
	"github.com/danielfbm/tkn-act/internal/reporter"
)

// recordingReporter captures every Emit() for assertions.
type recordingReporter struct{ events []reporter.Event }

func (r *recordingReporter) Emit(e reporter.Event) { r.events = append(r.events, e) }
func (r *recordingReporter) Close() error          { return nil }

func TestEngineEmitsDisplayNameOnRunAndTaskEvents(t *testing.T) {
	b, err := loader.LoadBytes([]byte(`
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: tk}
spec:
  displayName: "Unit-test runner"
  description: "Runs go test."
  steps:
    - name: s
      displayName: "Compile"
      description: "Compile."
      image: alpine:3
      script: 'true'
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  displayName: "Build & test"
  description: "Build then test."
  tasks:
    - name: t1
      displayName: "Compile binary"
      taskRef: {name: tk}
`))
	if err != nil {
		t.Fatal(err)
	}
	rep := &recordingReporter{}
	be := &captureBackend{} // reuse from step_template_engine_test.go (same _test package)
	if _, err := engine.New(be, rep, engine.Options{}).RunPipeline(
		context.Background(),
		engine.PipelineInput{Bundle: b, Name: "p"},
	); err != nil {
		t.Fatal(err)
	}

	got := map[reporter.EventKind]reporter.Event{}
	for _, e := range rep.events {
		// keep the first of each kind
		if _, ok := got[e.Kind]; !ok {
			got[e.Kind] = e
		}
	}

	if e := got[reporter.EvtRunStart]; e.DisplayName != "Build & test" || e.Description != "Build then test." {
		t.Errorf("run-start display_name=%q description=%q", e.DisplayName, e.Description)
	}
	if e := got[reporter.EvtRunEnd]; e.DisplayName != "Build & test" {
		t.Errorf("run-end display_name=%q", e.DisplayName)
	}
	if e := got[reporter.EvtTaskStart]; e.DisplayName != "Compile binary" || e.Description != "Runs go test." {
		t.Errorf("task-start display_name=%q description=%q", e.DisplayName, e.Description)
	}
	if e := got[reporter.EvtTaskEnd]; e.DisplayName != "Compile binary" {
		t.Errorf("task-end display_name=%q", e.DisplayName)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

```bash
go test -count=1 -run TestEngineEmitsDisplayNameOnRunAndTaskEvents ./internal/engine/...
```

Expected: FAIL — engine doesn't populate the fields.

- [ ] **Step 4: Wire pipeline-level displayName into run-start / run-end (docker path)**

In `internal/engine/engine.go::RunPipeline` (around line 67), update the `run-start` emission:

```go
e.rep.Emit(reporter.Event{
	Kind: reporter.EvtRunStart, Time: time.Now(),
	RunID: runID, Pipeline: pl.Metadata.Name,
	DisplayName: pl.Spec.DisplayName,
	Description: pl.Spec.Description,
})
```

And every `run-end` emission in the same function (lines 72, 91, 100, 242):

```go
e.rep.Emit(reporter.Event{
	Kind: reporter.EvtRunEnd, Time: time.Now(),
	Status: overall, Duration: time.Since(overallStart),
	Results: pipelineResults,
	DisplayName: pl.Spec.DisplayName, // safe — pl is in scope at every run-end site
})
```

For run-end emissions on the early-return error paths where `pl.Spec.DisplayName` is also in scope, populate likewise.

- [ ] **Step 5: Wire PipelineTask.DisplayName into task-* events (docker path)**

Same file — every `task-start`, `task-end`, `task-skip` emission needs `DisplayName: pt.DisplayName`. For `task-start` only, also resolve the Task spec to grab `Description`:

For the main-loop `task-start`:
```go
spec, _ := lookupTaskSpec(in.Bundle, pt) // ignore err here; runOne already handles it
e.rep.Emit(reporter.Event{
	Kind: reporter.EvtTaskStart, Time: time.Now(), Task: tname,
	DisplayName: pt.DisplayName,
	Description: spec.Description,
})
```

For `task-end` and `task-skip`, only `DisplayName: pt.DisplayName` is needed.

For finally tasks (around line 188-191), do the same with the finally `pt`.

- [ ] **Step 6: Wire PipelineTask.DisplayName into the cluster path (`runViaPipelineBackend` + `emitClusterTaskEvents`)**

In `runViaPipelineBackend`, the `run-start` / `run-end` emissions (lines 497, 501, 525, 556) need `DisplayName: pl.Spec.DisplayName` (and Description on run-start).

In `emitClusterTaskEvents` (line 576), the per-task event synthesis loses access to `pl` and the input bundle — it iterates `tasks map[string]TaskOutcomeOnCluster`. **Refactor:** pass `pl tektontypes.Pipeline` and `bundle *loader.Bundle` as arguments so the synthesised `task-start` / `task-end` events can look up `PipelineTask.DisplayName` and the resolved `TaskSpec.Description` from the input bundle (NOT the controller verdict — same source-of-truth as docker mode).

```go
func (e *Engine) emitClusterTaskEvents(pl tektontypes.Pipeline, bundle *loader.Bundle, tasks map[string]backend.TaskOutcomeOnCluster) {
	ptByName := map[string]tektontypes.PipelineTask{}
	for _, pt := range append([]tektontypes.PipelineTask(nil), pl.Spec.Tasks...) {
		ptByName[pt.Name] = pt
	}
	for _, pt := range pl.Spec.Finally {
		ptByName[pt.Name] = pt
	}
	for n, oc := range tasks {
		pt := ptByName[n]
		spec, _ := lookupTaskSpec(bundle, pt)
		now := time.Now()
		e.rep.Emit(reporter.Event{
			Kind: reporter.EvtTaskStart, Time: now, Task: n,
			DisplayName: pt.DisplayName,
			Description: spec.Description,
		})
		for _, r := range oc.RetryAttempts {
			t := r.Time
			if t.IsZero() {
				t = now
			}
			e.rep.Emit(reporter.Event{
				Kind: reporter.EvtTaskRetry, Time: t, Task: n,
				Status: r.Status, Message: r.Message, Attempt: r.Attempt,
				DisplayName: pt.DisplayName,
			})
		}
		attempt := oc.Attempts
		if attempt == 0 {
			attempt = 1
		}
		e.rep.Emit(reporter.Event{
			Kind: reporter.EvtTaskEnd, Time: time.Now(), Task: n,
			Status: oc.Status, Message: oc.Message, Attempt: attempt,
			DisplayName: pt.DisplayName,
		})
	}
}
```

Update the caller (line 528) to pass `pl, in.Bundle`.

- [ ] **Step 7: Run the engine test**

```bash
go test -count=1 -run TestEngineEmitsDisplayNameOnRunAndTaskEvents ./internal/engine/...
```

Expected: PASS.

- [ ] **Step 8: Run the full engine suite**

```bash
go test -race -count=1 ./internal/engine/...
```

Expected: every test OK — including v1.3 timeout, v1.4 stepTemplate, and v1.5 pipeline-results suites.

- [ ] **Step 9: Commit**

```bash
git add internal/engine/engine.go internal/engine/display_name_test.go
git commit -m "feat(engine): emit display_name/description on run + task events"
```

---

### Task 5: `step-log` carries displayName (both backends)

**Scope:** ONLY `step-log` gets the new `display_name` field. Production
code does not emit `EvtStepStart` / `EvtStepEnd` today, and adding
emission is explicitly out of scope (see "Event field summary"). This
task plumbs the step's `displayName` through the `LogSink.StepLog`
interface; both the docker and the cluster backend must be updated in
lockstep.

**Files:**
- Modify: `internal/backend/backend.go` (`LogSink` interface signature)
- Modify: `internal/reporter/event.go` (`LogSink` adapter)
- Modify: `internal/backend/docker/docker.go:386` (docker `StepLog` callsite)
- Modify: `internal/backend/cluster/run.go:540` (cluster `StepLog` callsite — Critical 2 from review)
- Test: append to the existing `internal/reporter/reporter_test.go` — fake-`LogSink` unit test (no need for a new file)

- [ ] **Step 1: Verify exhaustive callsite list**

```bash
grep -rn 'LogSink.StepLog\|\.StepLog(' /workspaces/tkn-act/internal/ | grep -v _test.go
```

Expected output (exactly these three lines):
```
internal/backend/backend.go:60:		StepLog(taskName, stepName, stream string, line string)
internal/backend/docker/docker.go:386:		sink.StepLog(taskName, stepName, stream, s.Text())
internal/backend/cluster/run.go:540:				in.LogSink.StepLog(taskName, stepName, "stdout", s.Text())
```

If a fourth callsite shows up (e.g. somebody added another backend),
this task's file list must be widened to cover it before proceeding.

Also confirm step-start/step-end are not emitted in production:

```bash
grep -rn 'EvtStepStart\|EvtStepEnd' /workspaces/tkn-act/internal/ | grep -v _test.go
```

Expected: only the enum declarations in `internal/reporter/event.go`
and the pretty-renderer switch in `internal/reporter/pretty.go`. **No
`reporter.Event{Kind: reporter.EvtStepStart, ...}` emission anywhere.**
If that ever changes, the spec's §4 note and this task's scope are
both out of date.

- [ ] **Step 2: Write the failing fake-`LogSink` unit test**

Replaces the previous `t.Skip`-based "real coverage comes from the e2e
fixture" placeholder. We can directly test the `LogSink` adapter
because it lives in `internal/reporter` and the new behavior is purely
about parameter-to-field mapping — no backend needed.

Append to `internal/reporter/reporter_test.go`:

```go
func TestLogSinkStepLogPropagatesDisplayName(t *testing.T) {
	cap := &captureSink{} // small in-package recorder; same shape as the e2e harness
	ls := reporter.NewLogSink(cap)
	ls.StepLog("t1", "s1", "Compile binary", "stdout", "hello\n")

	got := cap.events
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	e := got[0]
	if e.Kind != reporter.EvtStepLog {
		t.Errorf("Kind = %q", e.Kind)
	}
	if e.Task != "t1" || e.Step != "s1" {
		t.Errorf("Task/Step = %q/%q", e.Task, e.Step)
	}
	if e.DisplayName != "Compile binary" {
		t.Errorf("DisplayName = %q (want %q)", e.DisplayName, "Compile binary")
	}
	if e.Stream != "stdout" || e.Line != "hello\n" {
		t.Errorf("Stream/Line = %q/%q", e.Stream, e.Line)
	}
}

func TestLogSinkStepLogEmptyDisplayNameOmitsField(t *testing.T) {
	// Coverage: empty displayName must produce an event whose JSON
	// omits display_name (so agents fall back to e.Step). Asserts the
	// snake_case + omitempty contract for the new field at the
	// step-log site.
	cap := &captureSink{}
	ls := reporter.NewLogSink(cap)
	ls.StepLog("t1", "s1", "", "stdout", "hello\n")

	if len(cap.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(cap.events))
	}
	if cap.events[0].DisplayName != "" {
		t.Errorf("DisplayName = %q, want empty", cap.events[0].DisplayName)
	}
	// Encode and assert omitempty.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(cap.events[0]); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("display_name")) {
		t.Errorf("expected display_name to be omitted; got: %s", buf.Bytes())
	}
}
```

(`captureSink` here is a tiny in-test recorder that implements
`reporter.Reporter`; if `internal/reporter` already has one, reuse;
otherwise inline three lines.)

- [ ] **Step 3: Run the test to verify it fails**

```bash
go test -count=1 -run TestLogSinkStepLog ./internal/reporter/...
```

Expected: FAIL — the adapter doesn't take a `stepDisplayName` parameter
yet.

- [ ] **Step 4: Update `LogSink.StepLog` signature**

In `internal/backend/backend.go`:

```go
type LogSink interface {
	StepLog(taskName, stepName, stepDisplayName, stream, line string)
}
```

In `internal/reporter/event.go`, update the adapter:

```go
func (s *LogSink) StepLog(taskName, stepName, stepDisplayName, stream, line string) {
	s.r.Emit(Event{
		Kind:        EvtStepLog,
		Time:        time.Now(),
		Task:        taskName,
		Step:        stepName,
		DisplayName: stepDisplayName,
		Stream:      stream,
		Line:        line,
	})
}
```

- [ ] **Step 5: Update the docker callsite**

In `internal/backend/docker/docker.go:386`:

```go
sink.StepLog(taskName, stepName, step.DisplayName, stream, s.Text())
```

(The `step` value must be in scope at the callsite; verify before
editing — if it isn't, the docker scanner-loop wraps `taskName` and
`stepName` already, so plumbing `step.DisplayName` may need an extra
parameter on whatever helper kicks off the scan.)

- [ ] **Step 6: Update the cluster callsite (Critical 2 from review)**

In `internal/backend/cluster/run.go:540`:

```go
in.LogSink.StepLog(taskName, stepName, step.DisplayName, "stdout", s.Text())
```

Same scope check applies. The cluster log-streaming path receives
TaskRun-level container logs — to know the step's `DisplayName` it
must look it up from the input bundle (the resolved Task spec by
name), NOT from the controller verdict (consistent with the
source-of-truth rule in spec §6.2). If `step` isn't in scope, build a
small `byContainerName` lookup from the resolved TaskSpec at the top
of the streaming loop.

- [ ] **Step 7: Compile + run all tests**

```bash
go build ./...
go test -race -count=1 ./...
```

Expected: every test OK — including the new fake-`LogSink` test, plus
all existing reporter / backend / engine tests.

- [ ] **Step 8: Commit**

```bash
git add internal/backend/backend.go internal/reporter/event.go internal/reporter/reporter_test.go internal/backend/docker/docker.go internal/backend/cluster/run.go
git commit -m "feat(backend): step-log carries display_name (docker + cluster)"
```

---

### Task 6: Cluster backend round-trips `displayName` / `description` intact

**Files:**
- Test: `internal/backend/cluster/runpipeline_test.go`

The existing `taskSpecToMap` is `json.Marshal`/`json.Unmarshal`, so the new fields pass through. We add a regression test to lock that in.

- [ ] **Step 1: Write the test**

Append to `internal/backend/cluster/runpipeline_test.go`:

```go
// TestBuildPipelineRunRoundTripsDisplayName: every displayName /
// description on the input Pipeline + Task must appear under the
// inlined spec.pipelineSpec.* on the resulting PipelineRun object.
// Locks in that a future hand-written conversion can't silently drop
// these fields.
func TestBuildPipelineRunRoundTripsDisplayName(t *testing.T) {
	be, _, _, _, _ := fakeBackend(t)

	pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
		DisplayName: "Build & test",
		Description: "Build then test.",
		Tasks: []tektontypes.PipelineTask{{
			Name:        "t1",
			DisplayName: "Compile binary",
			TaskRef:     &tektontypes.TaskRef{Name: "tk"},
		}},
	}}
	pl.Metadata.Name = "p"
	tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
		DisplayName: "Unit-test runner",
		Description: "Runs go test.",
		Steps: []tektontypes.Step{{
			Name:        "s",
			DisplayName: "Compile",
			Description: "Compile the binary.",
			Image:       "alpine:3",
			Script:      "true",
		}},
	}}
	tk.Metadata.Name = "tk"

	prObj, err := be.BuildPipelineRunObject(backend.PipelineRunInvocation{
		RunID: "12345678", PipelineRunName: "p-12345678",
		Pipeline: pl, Tasks: map[string]tektontypes.Task{"tk": tk},
	}, "tkn-act-12345678")
	if err != nil {
		t.Fatal(err)
	}
	un := prObj.(*unstructured.Unstructured)

	plSpec, _, _ := unstructured.NestedMap(un.Object, "spec", "pipelineSpec")
	if plSpec["displayName"] != "Build & test" {
		t.Errorf("pipelineSpec.displayName = %v", plSpec["displayName"])
	}
	if plSpec["description"] != "Build then test." {
		t.Errorf("pipelineSpec.description = %v", plSpec["description"])
	}
	tasks, _, _ := unstructured.NestedSlice(un.Object, "spec", "pipelineSpec", "tasks")
	taskMap := tasks[0].(map[string]any)
	if taskMap["displayName"] != "Compile binary" {
		t.Errorf("tasks[0].displayName = %v", taskMap["displayName"])
	}
	taskSpec := taskMap["taskSpec"].(map[string]any)
	if taskSpec["displayName"] != "Unit-test runner" {
		t.Errorf("taskSpec.displayName = %v", taskSpec["displayName"])
	}
	if taskSpec["description"] != "Runs go test." {
		t.Errorf("taskSpec.description = %v", taskSpec["description"])
	}
	steps := taskSpec["steps"].([]any)
	stepMap := steps[0].(map[string]any)
	if stepMap["displayName"] != "Compile" {
		t.Errorf("step.displayName = %v", stepMap["displayName"])
	}
	if stepMap["description"] != "Compile the binary." {
		t.Errorf("step.description = %v", stepMap["description"])
	}
}
```

- [ ] **Step 2: Run the test**

```bash
go test -count=1 -run TestBuildPipelineRunRoundTripsDisplayName ./internal/backend/cluster/...
```

Expected: PASS (no production change required — `taskSpecToMap` already round-trips via `json.Marshal`/`json.Unmarshal`).

- [ ] **Step 3: Commit**

```bash
git add internal/backend/cluster/runpipeline_test.go
git commit -m "test(cluster): assert displayName/description survive PipelineRun inlining"
```

---

### Task 7: Add `WantEventFields` to `Fixture` and extend both event-shape harnesses

**Why this task exists (Critical 3 from review):** the cross-backend
e2e fixture in Task 8 needs to assert specific JSON event-field values
on both backends. Today the `Fixture` struct
(`internal/e2e/fixtures/fixtures.go:103+`) only carries `WantStatus`
and `WantResults`; the two harnesses
(`internal/e2e/e2e_test.go::assertEventShape` and
`internal/clustere2e/cluster_e2e_test.go::assertEventShape`) already
record the event stream via `captureSink` + `reporter.NewTee`, so the
infrastructure for "look at events" exists — but no fixture-driven
event-field assertion does. This task adds it, in isolation from the
new fixture, so Task 8 can wire the new fixture in cleanly.

**Files:**
- Modify: `internal/e2e/fixtures/fixtures.go` — add `WantEventFields` field.
- Modify: `internal/e2e/e2e_test.go` — extend `assertEventShape` to honor it.
- Modify: `internal/clustere2e/cluster_e2e_test.go` — same extension.
- Test: a small in-tree assertion via an existing fixture (any one — pick `pipeline-results` since it already has a per-fixture extra check) to validate the new path on green-field events.

- [ ] **Step 1: Add the field**

In `internal/e2e/fixtures/fixtures.go`, inside the `Fixture` struct
(after `WantResults`):

```go
	// WantEventFields, if non-nil, asserts that for each named event
	// kind the first matching event in the captured stream carries
	// each named JSON-key/value pair. Shape:
	//   kind -> { jsonKey -> expectedValue }
	// Only the first event of each kind is inspected (run-start /
	// run-end always have exactly one; task-start / step-log are
	// asserted on the first emission). Skipped if the map is nil.
	WantEventFields map[string]map[string]string
```

- [ ] **Step 2: Extend `assertEventShape` (docker harness)**

In `internal/e2e/e2e_test.go::assertEventShape`, after the existing
retry / status block, append:

```go
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
```

(Add `encoding/json` and `fmt` imports if not already present.)

- [ ] **Step 3: Mirror the same block in `internal/clustere2e/cluster_e2e_test.go::assertEventShape`**

Identical change — paste the same block in the same relative position.
Both harnesses must read the same `WantEventFields` field with the
same semantics so the cross-backend fidelity invariant holds.

- [ ] **Step 4: Run the existing fixture suite to verify the empty path is a no-op**

```bash
go test -count=1 ./internal/e2e/...
```

Expected: PASS — no fixture sets `WantEventFields` yet, so the new
block is a no-op for every existing entry. This is the safety check
that the new field doesn't break the existing harness.

- [ ] **Step 5: Sanity-check the new path with a synthetic test (optional but recommended)**

Add a small unit test in `internal/e2e/fixtures/fixtures_test.go` (or
a new file in the same package) that constructs a `Fixture{WantEventFields: ...}`
and feeds a hand-built `[]reporter.Event` slice through the
`assertEventShape` helper — the test asserts the helper fails when the
event stream is missing a kind, and passes when it matches. This locks
in that the new code path actually does something.

- [ ] **Step 6: Commit**

```bash
git add internal/e2e/fixtures/fixtures.go internal/e2e/e2e_test.go internal/clustere2e/cluster_e2e_test.go internal/e2e/fixtures/fixtures_test.go
git commit -m "test(e2e): add WantEventFields to Fixture; extend both harnesses"
```

---

### Task 8: Cross-backend e2e fixture

**Files:**
- Create: `testdata/e2e/display-name-description/pipeline.yaml`
- Modify: `internal/e2e/fixtures/fixtures.go` (add the entry; the `WantEventFields` field landed in Task 7)

- [ ] **Step 1: Write the fixture YAML**

Create `testdata/e2e/display-name-description/pipeline.yaml`:

```yaml
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: tk
spec:
  displayName: "Unit-test runner"
  description: "Runs `go test ./...`."
  steps:
    - name: compile
      displayName: "Compile"
      description: "Compile the test binary."
      image: alpine:3
      script: 'true'
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: display-name-description
spec:
  displayName: "Build & test"
  description: "Build then test."
  tasks:
    - name: t1
      displayName: "Compile binary"
      taskRef: { name: tk }
  finally:
    - name: notify
      displayName: "Send notification"
      taskRef: { name: tk }
```

- [ ] **Step 2: Add the fixture entry**

In `internal/e2e/fixtures/fixtures.go::All()`, append after the `pipeline-results` row. Note: we assert on `step-log` (NOT `step-start` — that event kind isn't emitted by production code today; see plan header):

```go
		{
			Dir:        "display-name-description",
			Pipeline:   "display-name-description",
			WantStatus: "succeeded",
			// WantEventFields (added in Task 7) asserts that specific
			// event kinds carry the documented display_name /
			// description fields. Mirrors how pipeline-results checks
			// Results, but at the event-stream layer.
			WantEventFields: map[string]map[string]string{
				"run-start":  {"display_name": "Build & test", "description": "Build then test."},
				"task-start": {"display_name": "Compile binary", "description": "Runs `go test ./...`."},
				"step-log":   {"display_name": "Compile"},
			},
		},
```

- [ ] **Step 3: Compile + vet for both build tags**

```bash
go vet ./... && go vet -tags integration ./... && go vet -tags cluster ./...
```

Expected: clean.

- [ ] **Step 4: Run docker e2e locally if Docker is available (optional)**

```bash
docker info >/dev/null 2>&1 && go test -tags integration -run TestE2E/display-name-description -count=1 ./internal/e2e/... || echo "no docker; CI will run it"
```

- [ ] **Step 5: Commit**

```bash
git add testdata/e2e/display-name-description/pipeline.yaml internal/e2e/fixtures/fixtures.go
git commit -m "test(e2e): display-name-description fixture (cross-backend)"
```

---

### Task 9: Documentation convergence

**Files:**
- Modify: `AGENTS.md`
- Modify: `cmd/tkn-act/agentguide_data.md` (regenerated)
- Modify: `README.md`
- Modify: `docs/test-coverage.md`
- Modify: `docs/short-term-goals.md`
- Modify: `docs/feature-parity.md`

- [ ] **Step 1: Add a new section to `AGENTS.md`**

Insert a new section just above "Timeout disambiguation":

```markdown
## `displayName` / `description`

`Pipeline.spec`, `PipelineTask` (entries under `spec.tasks` and
`spec.finally`), `Task.spec`, and `Step` all accept optional
`displayName` and `description` fields per Tekton v1. tkn-act surfaces
them on the JSON event stream as snake_case keys:

| Event kind | `display_name` source | `description` source |
|---|---|---|
| `run-start` | `Pipeline.spec.displayName` | `Pipeline.spec.description` |
| `run-end` | `Pipeline.spec.displayName` | — |
| `task-start` | `PipelineTask.displayName` | resolved `TaskSpec.description` |
| `task-end` / `task-skip` / `task-retry` | `PipelineTask.displayName` | — |
| `step-log` | `Step.displayName` | — |

`step-start` and `step-end` event kinds are defined in the API but no
production code emits them today (v1.5). Step `description` is parsed
and round-trips through cluster mode but is not surfaced on any event
(carrying it on every `step-log` line would balloon log output). The
Step's `displayName` is consumed in two places: the `step-log` JSON
event (above) and pretty output's log-line prefix.

Empty fields are omitted from the JSON object. Agents should fall
back to the corresponding `pipeline` / `task` / `step` (raw name) field
when `display_name` is empty. Pretty output prefers `displayName` over
`name` everywhere a label appears.

Substitution (`$(params.X)`) inside `displayName` / `description` is
NOT honored — strings are passed through literally.

### JSON event field naming

Existing event fields are camelCase (`runId`, `exitCode`, `durationMs`)
and remain so for backward compatibility — renaming them would break
the public-contract rule. **New event fields added from v1.5 onward
use snake_case** (`display_name`, `description`). This rule is
forward-going and applies to any future multi-word event field added
to the `Event` struct.

---

```

- [ ] **Step 2: Regenerate the embedded guide and verify (Important 6 from review)**

```bash
go generate ./cmd/tkn-act/
git diff --exit-code cmd/tkn-act/agentguide_data.md
```

Expected: exit 0 — `agentguide_data.md` is byte-identical to the
just-edited `AGENTS.md` (the `go generate` step regenerates it from
`AGENTS.md`). A non-zero exit means the embedded guide is out of sync
and CI will reject the PR.

- [ ] **Step 3: Run the embedded-guide test**

```bash
go test -count=1 ./cmd/tkn-act/...
```

Expected: PASS.

- [ ] **Step 4: README bullet**

In `README.md` under "Tekton features supported," add:

```markdown
- `displayName` / `description` on Task / Pipeline / PipelineTask / Step
  — surfaced on the JSON event stream as `display_name` / `description`,
  preferred over `name` in pretty output.
```

- [ ] **Step 5: Update `docs/test-coverage.md`**

Add a row under `### -tags integration` (after `pipeline-results/`):

```markdown
| `display-name-description/` | `displayName` + `description` on Pipeline / PipelineTask / Task / Step; asserts JSON event fields |
```

And the same row under `### -tags cluster`.

- [ ] **Step 6: Mark Track 1 #7 + Track 2 #5 done in `docs/short-term-goals.md`**

Change row 7's Status cell from:

```
| Not started. Type addition + reporter change. Half a day. |
```

to:

```
| Done in v1.5 (PR for `feat: displayName + description`). Type addition + event-field plumbing; cluster pass-through verified. |
```

And Track 2 row 5 from:

```
| Same shape on both backends. |
```

to:

```
| Done in v1.5; cross-backend assertion in the `display-name-description` fixture. |
```

- [ ] **Step 7: Flip the `feature-parity.md` row**

Change:

```
| `displayName` / `description` on Task / Pipeline / Step | various | gap | both | none | none | docs/short-term-goals.md (Track 1 #7) |
```

to:

```
| `displayName` / `description` on Task / Pipeline / Step | various | shipped | both | display-name-description | none | docs/superpowers/plans/2026-05-04-display-name-description.md (Track 1 #7) |
```

- [ ] **Step 8: Run parity-check**

```bash
bash .github/scripts/parity-check.sh
```

Expected: clean.

- [ ] **Step 9: Commit**

```bash
git add AGENTS.md cmd/tkn-act/agentguide_data.md README.md docs/test-coverage.md docs/short-term-goals.md docs/feature-parity.md
git commit -m "docs: document displayName/description; flip Track 1 #7 to shipped"
```

---

### Task 10: Coverage-no-drop and final verification

- [ ] **Step 1: Local coverage check**

```bash
go test -cover -count=1 ./... | tee /tmp/cov-head.txt
```

Compare the per-package `coverage:` lines against `main` (e.g. via
`git stash && go test -cover -count=1 ./... > /tmp/cov-base.txt && git stash pop`).
The new code (event-field plumbing, type fields, pretty `labelOf`) is
fully unit-tested; coverage should not drop on any modified package by
more than the gate threshold (0.1pp).

If a package shows a drop, write the missing test in the `_test.go`
file for that package; do NOT use `[skip-coverage-check]` — this is a
fully-testable change.

- [ ] **Step 2: Full local verification**

```bash
go vet ./... && go vet -tags integration ./... && go vet -tags cluster ./...
go build ./...
go test -race -count=1 ./...
bash .github/scripts/parity-check.sh
.github/scripts/tests-required.sh main HEAD

# Important 6 from review: regenerate the embedded agent guide and
# fail loudly if AGENTS.md and the embedded copy have drifted. CI
# runs the same check; running it locally catches the drift before
# the push.
go generate ./cmd/tkn-act/
git diff --exit-code cmd/tkn-act/agentguide_data.md
```

Expected: all exit 0, every test package OK or no-test-files, and the
`git diff --exit-code` confirms `cmd/tkn-act/agentguide_data.md` is
in sync with `AGENTS.md`.

- [ ] **Step 3: Push branch and open PR**

```bash
git push -u origin plan/step-template   # if continuing in this branch
# or, if a fresh branch:
# git checkout -b feat/display-name-description
# git push -u origin feat/display-name-description

gh pr create --title "feat: honor displayName + description (Track 1 #7)" --body "$(cat <<'EOF'
## Summary

Closes Track 1 #7 of `docs/short-term-goals.md` (and Track 2 #5).
Surfaces Tekton's optional `displayName` and `description` fields on
both backends:

- New `tektontypes` fields: `PipelineSpec.DisplayName`,
  `PipelineTask.DisplayName`, `TaskSpec.DisplayName`,
  `Step.DisplayName`, `Step.Description`. YAML round-trip tested.
- New `reporter.Event` fields: `display_name` and `description`
  (snake_case JSON keys). Carried on every event kind that has a
  natural source (see AGENTS.md).
- Pretty output prefers `displayName` over `name` everywhere a label
  appears.
- Cluster backend round-trips every new field via the existing
  inlined `pipelineSpec` JSON marshal path; regression test asserts
  it. Cross-backend e2e fixture (`display-name-description`)
  asserts the JSON event-stream shape on both backends.

Implements `docs/superpowers/plans/2026-05-04-display-name-description.md`.
Spec: `docs/superpowers/specs/2026-05-04-display-name-description-design.md`.

## Test plan

- [x] `go vet ./...` × {default, integration, cluster}
- [x] `go build ./...`
- [x] `go test -race -count=1 ./...`
- [x] `bash .github/scripts/parity-check.sh`
- [x] `.github/scripts/tests-required.sh main HEAD`
- [x] coverage no-drop locally
- [ ] docker-integration CI — runs the new fixture
- [ ] cluster-integration CI — same fixture on real Tekton

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 4: Wait for CI green, then merge per project default**

```bash
gh pr merge <num> --squash --delete-branch
```

---

## Self-review notes

- **Spec coverage:** Type model → Task 1. Event struct + JSON shape → Task 2. Pretty renderer → Task 3. Engine event population (run + task) → Task 4. Backend / step-log plumbing → Task 5. Cluster round-trip lock-in → Task 6. Recording-reporter / `WantEventFields` infrastructure → Task 7 (NEW from review). Cross-backend fidelity fixture → Task 8 (was Task 7). Docs convergence → Task 9 (was Task 8). Coverage + ship → Task 10 (was Task 9).
- **Task 7 must land before Task 8.** Task 8's fixture entry sets `WantEventFields` — the field doesn't exist on the `Fixture` struct, and the harness assertion path doesn't either, until Task 7 lands. This ordering is non-negotiable.
- **Step events scope (Critical 1):** the only step event in scope for v1.5 is `step-log` carrying `display_name`. `EvtStepStart` / `EvtStepEnd` are defined enums but unemitted by production code; adding emission would expand the public JSON contract beyond Track 1 #7's UX scope. Out of scope and explicitly called out in spec §4 + the plan header.
- **Both backends update `LogSink.StepLog` (Critical 2):** Task 5 explicitly modifies `internal/backend/docker/docker.go:386` AND `internal/backend/cluster/run.go:540`. Verified via `grep -rn 'LogSink.StepLog'` — there are exactly two production callsites today.
- **No placeholders:** every step has actual code or commands. Task 4 Step 6 includes a small refactor of `emitClusterTaskEvents` (signature change to take `pl, bundle`) — explicit, not deferred. Task 5 replaces the previous `t.Skip` with a fake-`LogSink` unit test (Important 7 from review).
- **Type consistency:** `DisplayName` / `Description` are plain `string` everywhere — no pointer / no nullable. Empty string is the absent state, matching `omitempty` JSON behavior.
- **Cluster pass-through "free":** `taskSpecToMap` is a `json.Marshal` round-trip. Task 6 is a regression-locking test, not a new code path.
- **Public-contract preservation:** new event fields are `omitempty` and snake_case (a deliberate choice for new fields, documented in spec §4 AND in `AGENTS.md` per Task 9 Step 1). Existing camelCase fields (`runId`, `exitCode`, `durationMs`) are unchanged.
- **Empty-displayName fallback coverage (Important 8 from review):** Task 3's `TestPrettyFallsBackToNameWhenDisplayNameEmpty` table-tests every label site (run-start, task-start, task-end, task-skip). Task 5 adds an explicit `TestLogSinkStepLogEmptyDisplayNameOmitsField` for the step-log site. So every label site has an explicit empty-displayName test case, preventing a future refactor from silently dropping the fallback at one site.
- **Pretty fallback assertion is structured (Important 5 from review):** the failing test in Task 3 picks distinct raw-name strings (`p-raw`, `t-raw`) and asserts they MUST NOT appear in the output when `displayName` is set. No fragile substring like `" p "`.
- **One-task non-regression:** Task 4 Step 8 explicitly runs the full engine test suite so the new event-field plumbing can't silently break v1.3 (timeouts), v1.4 (stepTemplate), or v1.5 (pipeline-results).
- **Docs are atomic with the code:** Task 9 lands AGENTS.md ↔ embedded guide convergence (with `git diff --exit-code` per Important 6), README, test-coverage.md, short-term-goals.md, AND the feature-parity row flip in one commit so `parity-check` is satisfied at every commit boundary.
- **Coverage gate:** Task 10 explicitly checks per-package coverage doesn't drop. The change is fully testable; we do NOT use `[skip-coverage-check]`.
- **Cluster source-of-truth:** the synthesised cluster `task-start` events read `displayName` from the input bundle (NOT the controller verdict). Documented in spec §6.2 and implemented in Task 4 Step 6. The cluster `step-log` callsite (Task 5 Step 6) must do the same — look up the step's `displayName` from the input bundle, not from the controller's TaskRun status.
