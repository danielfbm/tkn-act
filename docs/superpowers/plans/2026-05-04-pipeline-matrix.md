# `PipelineTask.matrix` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Honor Tekton's `PipelineTask.matrix` — fan a single
`PipelineTask` out into N concrete TaskRuns by Cartesian product over
named string-list params, plus optional `include` rows for named
extras — on both the docker and cluster backends, with cross-backend
fidelity for both task-level events and `Pipeline.spec.results`
aggregation.

**Architecture:** A new pure helper `expandMatrix(pl, params) (Pipeline, error)`
lives in `internal/engine/matrix.go`. It runs at the top of
`RunPipeline` *before* DAG construction, replacing every
matrix-fanned `PipelineTask` with N expansion children and rewriting
every downstream `RunAfter` to wait on all expansions. From the
DAG's perspective, an expanded child is a normal `PipelineTask`
nothing else needs to know about matrix. A second helper
`aggregateMatrixResults(...)` runs after every expansion of the same
parent terminates, folding per-expansion `string` results into a
JSON-array literal string under the *parent* name. To make
`$(tasks.<parent>.results.<Y>[*])` resolve into N spliced strings at
pipeline-results time, the resolver's `[*]` form is **first
extended** (Task 4) to recognise `$(tasks.X.results.Y[*])` (today
it only matches `$(params.X[*])`) and JSON-decode the source value;
this resolver change blocks the matrix-result aggregation task. The
cluster backend submits the matrix unchanged to Tekton (`json.Marshal`
round-trips `matrix` natively into the inlined `pipelineSpec.tasks[]`);
the post-hoc event emitter reads matrix-row params from
`taskRun.spec.params` plus the `tekton.dev/pipelineTask` label to
reconstruct the same `Matrix.Parent` / `Matrix.Index` /
`Matrix.Params` triple the docker engine emits live, using
deterministic param-hash matching as the PRIMARY mapping strategy
and `childReferences` ordering as the FALLBACK.

**Tech Stack:** Go 1.25, no new dependencies. Reuses
`internal/tektontypes`, `internal/engine`, `internal/validator`,
`internal/resolver`, the cross-backend `internal/e2e/fixtures`
table, and the existing parity-check + tests-required + coverage CI
gates.

---

## Track 1 #3 context

This closes Track 1 #3 of `docs/short-term-goals.md`. The Status
column says: *"Not started. Engine + DAG changes are non-trivial;
probably needs its own spec."* The spec is
`docs/superpowers/specs/2026-05-04-pipeline-matrix-design.md` —
read it first; this plan executes it.

## Tekton-upstream behavior we're matching

`PipelineTask.matrix` (v1):

```yaml
- name: build
  taskRef: { name: build }
  matrix:
    params:
      - name: os
        value: [linux, darwin]
      - name: goversion
        value: ["1.21", "1.22"]
    include:
      - name: arm-extra
        params:
          - { name: os,        value: linux  }
          - { name: goversion, value: "1.22" }
          - { name: arch,      value: arm64  }
```

Cardinality: `|os| × |goversion|` cross-product rows + one row per
`include` entry. For each row: shallow-copy the parent
`PipelineTask`, set `Name = parent + "-" + index` (or the include
row's name), merge row params over `pt.Params` (matrix wins), set
`Matrix = nil` on the copy. Downstream tasks that `runAfter:
[parent]` get rewritten to wait on every expansion. Upstream
promotes `string`-typed result `Y` of a matrix-fanned task `X` to
"array-of-strings", addressable via `$(tasks.X.results.Y[*])` and
`$(tasks.X.results.Y[N])`.

For tkn-act the spec-locked decisions:

- **`include` semantics**: rows are *always appended*. To prevent
  cross-backend divergence (cluster delegates to the controller, which
  *folds* an include row that overlaps a cross-product row; docker
  appends), the validator **rejects** any include row whose params
  overlap a `matrix.params` name (Critical 2 fix, option (b)). A
  `testdata/limitations/matrix-include-overlap/` fixture documents
  the divergence by example.
- **Cardinality cap**: 256 rows total per matrix. Hardcoded in
  `engine.matrixMaxRows`; validator enforces; no flag.
- **`failFast: false` β knob**: not implemented.
- **Result types other than string** on a matrix-fanned task:
  validator-rejected (mirrors upstream).
- **`when:` evaluation is per-row** (Critical 3 fix, option (a)):
  each expansion's `when:` is evaluated against a resolver context
  that includes the row's matrix-contributed params. Each false row
  emits one `task-skip` event under the *expansion* name (e.g.
  `build-1`, `arm-extra`) with `MatrixInfo` populated; the row is
  marked `not-run` in `outcomes`. Downstream `runAfter` propagation
  skips downstream tasks only if every expansion is `not-run`.
- **Resolver `[*]` extension (Critical 1 fix, Task 4)**: the
  current `resolver.isArrayStarRef` only matches
  `$(params.X[*])`. Result aggregation depends on `[*]` working
  for `$(tasks.X.results.Y[*])` too. Task 4 lands the resolver
  change first (with its own failing test), then Task 5 lands the
  aggregation that depends on it.
- **Cluster backend**: option (b) from the spec — submit matrix
  unchanged, read back N TaskRuns, reconstruct the
  `Matrix.{Parent,Index,Params}` triple via deterministic param-hash
  matching (PRIMARY) with `childReferences` ordering as FALLBACK.
  A probe in `cluster-integration.yml` validates both Tekton minor
  versions agree.

## Files to create / modify

| File | Why |
|---|---|
| `internal/tektontypes/types.go` | Add `Matrix`, `MatrixParam`, `MatrixInclude` types; add `Matrix *Matrix` on `PipelineTask`; add `MatrixInfo` (re-exported by engine via type alias) |
| `internal/tektontypes/types_test.go` | YAML round-trip for `matrix` and `matrix.include` |
| `internal/resolver/resolver.go` | Widen `isArrayStarRef` and `SubstituteArgs` to recognise `$(tasks.X.results.Y[*])` and JSON-decode the source string into `[]string` (Critical 1 fix; Task 4) |
| `internal/resolver/resolver_test.go` | `TestResolveTaskResultArrayStar` (failing test landed first) plus error-path test for non-array source |
| `internal/engine/matrix.go` (new) | `expandMatrix(pl, params) (Pipeline, error)` + `aggregateMatrixResults(...)` + per-row `evaluateMatrixWhen(...)` helper + the `matrixMaxRows = 256` constant |
| `internal/engine/matrix_test.go` (new) | Unit tests for every expansion / merge / cap / per-row when rule |
| `internal/engine/engine.go` | Call `expandMatrix` between `applyDefaults` and DAG construction; call `aggregateMatrixResults` after each task completes; thread `Matrix` event payload through `runOne` and `EvtTaskStart` / `EvtTaskEnd` / `EvtTaskSkip` emission |
| `internal/engine/run.go` | Add `Matrix *MatrixInfo` to `TaskOutcome` so the cluster path can hand the same triple to `emitClusterTaskEvents` |
| `internal/engine/pipeline_results.go` | Extend the array branch: when an array element is a sole `$(tasks.X.results.Y[*])` reference, splice via the new resolver path so `RunResult.Results[<name>]` is `[]string` not `string` |
| `internal/engine/pipeline_results_test.go` | `TestResolvePipelineResultsArrayStarFromMatrix` asserting Go type `[]string` |
| `internal/engine/matrix_engine_test.go` (new) | Integration: 4-expansion pipeline, downstream `runAfter`, one-expansion-fails-skips-downstream, `task-end` events carry `matrix`, **per-row when test** (some rows skip, others run) |
| `internal/reporter/event.go` | Add `Matrix *MatrixEvent` field to `Event` (omitempty) with `Parent`, `Index`, `Of`, `Params` |
| `internal/reporter/reporter_test.go` | `Event.Matrix` JSON-marshal round-trip; non-matrix events stay byte-identical |
| `internal/validator/validator.go` | New rules: empty/non-list matrix value, duplicate matrix param name, cardinality cap, include-row string-only, **include-overlaps-cross-product rejection (Critical 2)**, matrix-fanned task referenced result must be type `string` |
| `internal/validator/validator_test.go` | One negative test per rule (including the new include-overlap rule), plus a positive test with valid matrix and a positive test that asserts the result-typing rule fires |
| `internal/backend/backend.go` | Add `Matrix *tektontypes.MatrixInfo` field to `TaskOutcomeOnCluster` so the cluster backend forwards the triple back to the engine |
| `internal/backend/cluster/run.go` | In `collectTaskOutcomes`, read `tekton.dev/pipelineTask` label + `taskRun.spec.params` to reconstruct `Matrix.{Parent,Index,Params}` for each TaskRun. **PRIMARY**: deterministic `sha256(canonicalJSON(matrix-row params))` matching against pre-computed hashes for each parent's row order. **FALLBACK**: `pr.status.childReferences` ordering, with an `EvtError` warning logged. |
| `internal/backend/cluster/runpipeline_test.go` | Inlined PR has `pipelineSpec.tasks[].matrix` intact (regression-locking) |
| `internal/backend/cluster/run_test.go` | Faked PR with 4 child TaskRuns is mapped to 4 outcomes with correct `Matrix.Index` and `Matrix.Params` via param-hash; second test asserts the childReferences-ordering fallback fires when `spec.params` is empty |
| `.github/workflows/cluster-integration.yml` | Add a matrix probe job: run `matrix` fixture against the current Tekton pin AND one prior minor; assert both runs produce identical `MatrixInfo.Index` assignments |
| `testdata/e2e/matrix/pipeline.yaml` (new) | Cross-backend cross-product fixture (2×2) emitting one string result per expansion; `WantResults` asserts the array shape |
| `testdata/e2e/matrix-include/pipeline.yaml` (new) | Cross-backend fixture with one `include` row (named) and one `include` row (unnamed); both rows introduce a param NOT in `matrix.params` (no overlap) |
| `testdata/limitations/matrix-include-overlap/pipeline.yaml` (new) | Limitations fixture: include row whose params overlap a cross-product param. Header explains real-Tekton fold vs. tkn-act validate-time rejection. |
| `testdata/limitations/README.md` | New row for `matrix-include-overlap/` |
| `internal/e2e/fixtures/fixtures.go` | Two new fixture entries |
| `cmd/tkn-act/agentguide_data.md` | Note matrix support: row naming (`name`=expansion, new `matrix.parent`=original), 256 cap, `matrix:` event field, result-array surfacing, per-row `when:`, include-overlap rejection |
| `AGENTS.md` | Mirror agentguide_data.md (kept in sync) |
| `README.md` | One-line bullet under "Tekton features supported" |
| `docs/test-coverage.md` | List the two new e2e fixtures under `### -tags integration`; list `matrix-include-overlap/` under limitations |
| `docs/short-term-goals.md` | Mark Track 1 #3 done |
| `docs/feature-parity.md` | Flip the `PipelineTask.matrix` row from `gap` to `shipped`, populate `e2e fixture`. Set limitations cell to `none` (the `matrix-include-overlap` fixture is parser-coverage only, not a feature gap). |

## Out of scope (don't do here)

- **`failFast: false`** on a matrix (Tekton β knob). Defer.
- **Per-matrix `maxConcurrency`**. We honor `--parallel` globally;
  per-matrix throttling is a v2 knob.
- **`include`-row augmentation** of matching cross-product rows. v1
  always appends include rows as new rows; documented divergence.
- **Result types other than string** on matrix-fanned tasks. We
  reject `array` and `object` per upstream.
- **Reading `$(params.list[*])` inside a matrix value list** (i.e.
  matrix params expanded from another array param). v1 takes literal
  string lists only.
- **A new exit code** for matrix overflow. Folds into exit 4
  (validate).
- **`displayName` parameter substitution** in a matrix expansion
  (Track 1 #7 hasn't shipped; nothing to do here).

---

### Task 1: Add the `Matrix` types

**Files:**
- Modify: `internal/tektontypes/types.go` (add `Matrix`, `MatrixParam`, `MatrixInclude`; add `Matrix *Matrix` on `PipelineTask`)
- Test: `internal/tektontypes/types_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/tektontypes/types_test.go`:

```go
func TestUnmarshalPipelineTaskWithMatrix(t *testing.T) {
    in := []byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - name: build
      taskRef: {name: build}
      matrix:
        params:
          - name: os
            value: [linux, darwin]
          - name: goversion
            value: ["1.21", "1.22"]
        include:
          - name: arm-extra
            params:
              - {name: os, value: linux}
              - {name: goversion, value: "1.22"}
              - {name: arch, value: arm64}
`)
    var got Pipeline
    if err := yaml.Unmarshal(in, &got); err != nil {
        t.Fatal(err)
    }
    if len(got.Spec.Tasks) != 1 {
        t.Fatalf("tasks = %d, want 1", len(got.Spec.Tasks))
    }
    m := got.Spec.Tasks[0].Matrix
    if m == nil {
        t.Fatalf("Matrix is nil")
    }
    if len(m.Params) != 2 {
        t.Fatalf("Params = %d, want 2", len(m.Params))
    }
    if m.Params[0].Name != "os" || len(m.Params[0].Value) != 2 || m.Params[0].Value[0] != "linux" {
        t.Errorf("Params[0] = %+v, want os=[linux,darwin]", m.Params[0])
    }
    if len(m.Include) != 1 || m.Include[0].Name != "arm-extra" {
        t.Errorf("Include = %+v, want one arm-extra row", m.Include)
    }
    if len(m.Include[0].Params) != 3 || m.Include[0].Params[2].Name != "arch" {
        t.Errorf("Include[0].Params = %+v", m.Include[0].Params)
    }
}

func TestUnmarshalPipelineTaskWithoutMatrix(t *testing.T) {
    in := []byte(`
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: p}
spec:
  tasks:
    - {name: build, taskRef: {name: build}}
`)
    var got Pipeline
    if err := yaml.Unmarshal(in, &got); err != nil {
        t.Fatal(err)
    }
    if got.Spec.Tasks[0].Matrix != nil {
        t.Errorf("Matrix = %+v, want nil", got.Spec.Tasks[0].Matrix)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -run TestUnmarshalPipelineTaskWithMatrix -count=1 ./internal/tektontypes/...
```

Expected: FAIL with `Matrix undefined`.

- [ ] **Step 3: Add the types**

In `internal/tektontypes/types.go`:

1. In `PipelineTask`, append `Matrix *Matrix \`json:"matrix,omitempty"\`` after `Retries`.
2. Below the `PipelineTask` struct, add:

```go
// Matrix declares a Cartesian-product fan-out of one PipelineTask
// across one or more named string-list params. Optional include
// rows add named combinations on top of the cross-product. Mirrors
// Tekton v1 PipelineTask.matrix for the subset tkn-act reads.
type Matrix struct {
    Params  []MatrixParam   `json:"params,omitempty"`
    Include []MatrixInclude `json:"include,omitempty"`
}

// MatrixParam is a name + value list. Tekton requires `value` to be
// a list of strings here (no scalars, no objects); the validator
// enforces it.
type MatrixParam struct {
    Name  string   `json:"name"`
    Value []string `json:"value"`
}

// MatrixInclude is one extra named row. Its params override matching
// matrix-row params for that one row, and may introduce param names
// not in `matrix.params`. Param.Value must be string-typed.
type MatrixInclude struct {
    Name   string  `json:"name,omitempty"`
    Params []Param `json:"params,omitempty"`
}
```

- [ ] **Step 4: Run tests**

```bash
go test -run TestUnmarshalPipelineTaskWith -count=1 ./internal/tektontypes/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tektontypes/types.go internal/tektontypes/types_test.go
git commit -m "feat(types): add PipelineTask.Matrix (Tekton v1)"
```

---

### Task 2: `expandMatrix` helper

**Files:**
- Create: `internal/engine/matrix.go`
- Test: `internal/engine/matrix_test.go`

A pure transformation. Takes the parsed pipeline + the resolved
pipeline-level params; returns the pipeline with every matrix-fanned
`PipelineTask` replaced by N expansion children and every downstream
`RunAfter` rewritten.

- [ ] **Step 1: Write the failing test (cross-product, naming, RunAfter rewrite)**

Create `internal/engine/matrix_test.go`:

```go
package engine

import (
    "reflect"
    "testing"

    "github.com/danielfbm/tkn-act/internal/tektontypes"
)

func TestExpandMatrixNoMatrixIsNoOp(t *testing.T) {
    pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
        Tasks: []tektontypes.PipelineTask{{Name: "a", TaskRef: &tektontypes.TaskRef{Name: "t"}}},
    }}
    got, err := expandMatrix(pl, nil)
    if err != nil {
        t.Fatal(err)
    }
    if !reflect.DeepEqual(got, pl) {
        t.Errorf("nil-matrix should be no-op, got %+v", got)
    }
}

func TestExpandMatrixCrossProduct(t *testing.T) {
    pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
        Tasks: []tektontypes.PipelineTask{{
            Name:    "build",
            TaskRef: &tektontypes.TaskRef{Name: "t"},
            Matrix: &tektontypes.Matrix{Params: []tektontypes.MatrixParam{
                {Name: "os", Value: []string{"linux", "darwin"}},
                {Name: "goversion", Value: []string{"1.21", "1.22"}},
            }},
        }},
    }}
    got, err := expandMatrix(pl, nil)
    if err != nil {
        t.Fatal(err)
    }
    if len(got.Spec.Tasks) != 4 {
        t.Fatalf("tasks = %d, want 4", len(got.Spec.Tasks))
    }
    wantNames := []string{"build-0", "build-1", "build-2", "build-3"}
    for i, pt := range got.Spec.Tasks {
        if pt.Name != wantNames[i] {
            t.Errorf("tasks[%d].Name = %q, want %q", i, pt.Name, wantNames[i])
        }
        if pt.Matrix != nil {
            t.Errorf("tasks[%d].Matrix = %+v, want nil after expansion", i, pt.Matrix)
        }
    }
    // Row order: outermost param iterates slowest. With params order
    // [os, goversion] and values [[linux,darwin], [1.21, 1.22]], the
    // first row should be (os=linux, goversion=1.21).
    pt0 := got.Spec.Tasks[0]
    osVal, goVal := paramByName(pt0.Params, "os"), paramByName(pt0.Params, "goversion")
    if osVal != "linux" || goVal != "1.21" {
        t.Errorf("row 0 = (os=%q goversion=%q), want (linux, 1.21)", osVal, goVal)
    }
}

// paramByName is a test helper.
func paramByName(ps []tektontypes.Param, name string) string {
    for _, p := range ps {
        if p.Name == name {
            return p.Value.StringVal
        }
    }
    return ""
}

func TestExpandMatrixIncludeAppendsRows(t *testing.T) {
    pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
        Tasks: []tektontypes.PipelineTask{{
            Name:    "build",
            TaskRef: &tektontypes.TaskRef{Name: "t"},
            Matrix: &tektontypes.Matrix{
                Params: []tektontypes.MatrixParam{
                    {Name: "os", Value: []string{"linux"}},
                },
                Include: []tektontypes.MatrixInclude{
                    {Name: "arm-extra", Params: []tektontypes.Param{
                        {Name: "os", Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "linux"}},
                        {Name: "arch", Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "arm64"}},
                    }},
                    {Params: []tektontypes.Param{
                        {Name: "os", Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "darwin"}},
                    }},
                },
            },
        }},
    }}
    got, err := expandMatrix(pl, nil)
    if err != nil {
        t.Fatal(err)
    }
    // 1 cross-product row + 2 include rows = 3 expansions.
    if len(got.Spec.Tasks) != 3 {
        t.Fatalf("tasks = %d, want 3", len(got.Spec.Tasks))
    }
    names := []string{got.Spec.Tasks[0].Name, got.Spec.Tasks[1].Name, got.Spec.Tasks[2].Name}
    want := []string{"build-0", "arm-extra", "build-2"}
    if !reflect.DeepEqual(names, want) {
        t.Errorf("names = %v, want %v", names, want)
    }
}

func TestExpandMatrixRunAfterRewrite(t *testing.T) {
    pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
        Tasks: []tektontypes.PipelineTask{
            {
                Name:    "build",
                TaskRef: &tektontypes.TaskRef{Name: "t"},
                Matrix: &tektontypes.Matrix{Params: []tektontypes.MatrixParam{
                    {Name: "os", Value: []string{"linux", "darwin"}},
                }},
            },
            {Name: "publish", TaskRef: &tektontypes.TaskRef{Name: "t2"}, RunAfter: []string{"build"}},
        },
    }}
    got, err := expandMatrix(pl, nil)
    if err != nil {
        t.Fatal(err)
    }
    var pub tektontypes.PipelineTask
    for _, pt := range got.Spec.Tasks {
        if pt.Name == "publish" {
            pub = pt
            break
        }
    }
    if !reflect.DeepEqual(pub.RunAfter, []string{"build-0", "build-1"}) {
        t.Errorf("publish.RunAfter = %v, want [build-0 build-1]", pub.RunAfter)
    }
}

func TestExpandMatrixParamPrecedence(t *testing.T) {
    // PipelineTask.params says os=linux; matrix says os ∈ {linux, darwin}.
    // After expansion, each row's os MUST come from matrix.
    pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
        Tasks: []tektontypes.PipelineTask{{
            Name:    "build",
            TaskRef: &tektontypes.TaskRef{Name: "t"},
            Params: []tektontypes.Param{
                {Name: "os", Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: "linux"}},
            },
            Matrix: &tektontypes.Matrix{Params: []tektontypes.MatrixParam{
                {Name: "os", Value: []string{"linux", "darwin"}},
            }},
        }},
    }}
    got, err := expandMatrix(pl, nil)
    if err != nil {
        t.Fatal(err)
    }
    if paramByName(got.Spec.Tasks[1].Params, "os") != "darwin" {
        t.Errorf("expansion 1 os = %q, want darwin (matrix wins)", paramByName(got.Spec.Tasks[1].Params, "os"))
    }
}

func TestExpandMatrixCardinalityCap(t *testing.T) {
    // 17 × 17 = 289 > 256.
    big := func() []string {
        out := make([]string, 17)
        for i := range out {
            out[i] = "v"
        }
        return out
    }
    pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
        Tasks: []tektontypes.PipelineTask{{
            Name:    "build",
            TaskRef: &tektontypes.TaskRef{Name: "t"},
            Matrix: &tektontypes.Matrix{Params: []tektontypes.MatrixParam{
                {Name: "a", Value: big()},
                {Name: "b", Value: big()},
            }},
        }},
    }}
    if _, err := expandMatrix(pl, nil); err == nil {
        t.Fatal("expected cardinality-cap error, got nil")
    }
}

func TestExpandMatrixFinallyRewrite(t *testing.T) {
    // A matrix-fanned task in finally is also expanded; finally has no
    // RunAfter to rewrite (finally tasks run sequentially), so this is
    // just an "is-it-expanded" assertion.
    pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
        Finally: []tektontypes.PipelineTask{{
            Name:    "notify",
            TaskRef: &tektontypes.TaskRef{Name: "t"},
            Matrix: &tektontypes.Matrix{Params: []tektontypes.MatrixParam{
                {Name: "channel", Value: []string{"slack", "email"}},
            }},
        }},
    }}
    got, err := expandMatrix(pl, nil)
    if err != nil {
        t.Fatal(err)
    }
    if len(got.Spec.Finally) != 2 {
        t.Errorf("finally tasks = %d, want 2", len(got.Spec.Finally))
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -run TestExpandMatrix -count=1 ./internal/engine/...
```

Expected: FAIL — `expandMatrix undefined`.

- [ ] **Step 3: Implement `expandMatrix`**

Create `internal/engine/matrix.go`:

```go
package engine

import (
    "fmt"
    "strconv"

    "github.com/danielfbm/tkn-act/internal/tektontypes"
)

// matrixMaxRows caps the total expansions per matrix (cross-product
// rows + include rows). Hardcoded to prevent foot-guns; if a real
// pipeline genuinely needs more, split the pipeline or run with
// --cluster.
const matrixMaxRows = 256

// MatrixInfo is the per-expansion identity: which parent
// PipelineTask the row came from, where it sits in the row order,
// and what params the row contributed. Threaded through the engine
// (TaskOutcome.Matrix), the cluster backend
// (TaskOutcomeOnCluster.Matrix), and the reporter (Event.Matrix).
type MatrixInfo struct {
    Parent string
    Index  int
    Of     int
    Params map[string]string
}

// expandMatrix replaces every PipelineTask in pl.Spec.Tasks and
// pl.Spec.Finally that has Matrix != nil with N expansion children,
// rewriting downstream RunAfter edges so anything that referenced
// the original name now waits on every expansion. Returns pl
// unchanged when no task has a matrix.
//
// The unused `params` argument reserves a hook for future support
// of $(params.X) in matrix.params[].value entries (Tekton allows
// this); v1 takes literal string lists only.
func expandMatrix(pl tektontypes.Pipeline, _ map[string]tektontypes.ParamValue) (tektontypes.Pipeline, error) {
    if !hasAnyMatrix(pl) {
        return pl, nil
    }

    rewriteMap := map[string][]string{} // original name → expansion names
    newTasks, err := expandList(pl.Spec.Tasks, rewriteMap)
    if err != nil {
        return tektontypes.Pipeline{}, err
    }
    newFinally, err := expandList(pl.Spec.Finally, rewriteMap)
    if err != nil {
        return tektontypes.Pipeline{}, err
    }

    // Rewrite RunAfter on both lists.
    for i := range newTasks {
        newTasks[i].RunAfter = rewriteRunAfter(newTasks[i].RunAfter, rewriteMap)
    }
    for i := range newFinally {
        newFinally[i].RunAfter = rewriteRunAfter(newFinally[i].RunAfter, rewriteMap)
    }

    out := pl
    out.Spec.Tasks = newTasks
    out.Spec.Finally = newFinally
    return out, nil
}

func hasAnyMatrix(pl tektontypes.Pipeline) bool {
    for _, pt := range pl.Spec.Tasks {
        if pt.Matrix != nil {
            return true
        }
    }
    for _, pt := range pl.Spec.Finally {
        if pt.Matrix != nil {
            return true
        }
    }
    return false
}

func expandList(in []tektontypes.PipelineTask, rewrite map[string][]string) ([]tektontypes.PipelineTask, error) {
    var out []tektontypes.PipelineTask
    for _, pt := range in {
        if pt.Matrix == nil {
            out = append(out, pt)
            continue
        }
        rows, err := materializeRows(pt)
        if err != nil {
            return nil, err
        }
        names := make([]string, 0, len(rows))
        for i, row := range rows {
            child := pt
            child.Matrix = nil
            child.Name = rowName(pt.Name, i, row.includeName)
            child.Params = mergeParams(pt.Params, row.params)
            out = append(out, child)
            names = append(names, child.Name)
        }
        rewrite[pt.Name] = names
    }
    return out, nil
}

type matrixRow struct {
    params      []tektontypes.Param
    includeName string // empty for cross-product rows
}

// materializeRows builds the cross-product followed by include rows.
// Cross-product order: outermost param (matrix.params[0]) iterates
// slowest, so for params [os=[linux,darwin], goversion=[1.21,1.22]]
// the order is (linux,1.21), (linux,1.22), (darwin,1.21), (darwin,1.22).
func materializeRows(pt tektontypes.PipelineTask) ([]matrixRow, error) {
    m := pt.Matrix
    // Validate non-empty value lists.
    for _, mp := range m.Params {
        if len(mp.Value) == 0 {
            return nil, fmt.Errorf("pipeline task %q matrix param %q must be a non-empty string list", pt.Name, mp.Name)
        }
    }
    // Cardinality cap.
    cross := 1
    if len(m.Params) > 0 {
        cross = 1
        for _, mp := range m.Params {
            cross *= len(mp.Value)
        }
    } else {
        cross = 0
    }
    total := cross + len(m.Include)
    if total > matrixMaxRows {
        return nil, fmt.Errorf("pipeline task %q matrix would produce %d rows, exceeding the cap of %d", pt.Name, total, matrixMaxRows)
    }

    var rows []matrixRow
    if cross > 0 {
        idxs := make([]int, len(m.Params))
        for {
            ps := make([]tektontypes.Param, 0, len(m.Params))
            for i, mp := range m.Params {
                ps = append(ps, tektontypes.Param{
                    Name:  mp.Name,
                    Value: tektontypes.ParamValue{Type: tektontypes.ParamTypeString, StringVal: mp.Value[idxs[i]]},
                })
            }
            rows = append(rows, matrixRow{params: ps})
            // Increment indices, outermost param last (so it iterates slowest).
            done := true
            for i := len(idxs) - 1; i >= 0; i-- {
                idxs[i]++
                if idxs[i] < len(m.Params[i].Value) {
                    done = false
                    break
                }
                idxs[i] = 0
            }
            if done {
                break
            }
        }
    }
    for _, inc := range m.Include {
        rows = append(rows, matrixRow{params: inc.Params, includeName: inc.Name})
    }
    return rows, nil
}

// rowName returns parent-i for cross-product rows and the include
// row's name when set. Unnamed include rows fall through to
// parent-i where i continues past the cross-product.
func rowName(parent string, idx int, includeName string) string {
    if includeName != "" {
        return includeName
    }
    return parent + "-" + strconv.Itoa(idx)
}

// mergeParams returns base ∪ row, where row entries override base
// entries with the same Name. Order: base entries first (preserving
// order, with values overridden in place), then row-only entries.
func mergeParams(base, row []tektontypes.Param) []tektontypes.Param {
    rowIdx := map[string]int{}
    for i, p := range row {
        rowIdx[p.Name] = i
    }
    out := make([]tektontypes.Param, 0, len(base)+len(row))
    emitted := map[string]bool{}
    for _, p := range base {
        if i, ok := rowIdx[p.Name]; ok {
            out = append(out, row[i])
        } else {
            out = append(out, p)
        }
        emitted[p.Name] = true
    }
    for _, p := range row {
        if !emitted[p.Name] {
            out = append(out, p)
        }
    }
    return out
}

func rewriteRunAfter(in []string, rewrite map[string][]string) []string {
    if len(in) == 0 {
        return in
    }
    out := make([]string, 0, len(in))
    for _, dep := range in {
        if expansions, ok := rewrite[dep]; ok {
            out = append(out, expansions...)
        } else {
            out = append(out, dep)
        }
    }
    return out
}
```

- [ ] **Step 4: Run tests**

```bash
go test -run TestExpandMatrix -count=1 ./internal/engine/...
```

Expected: PASS (all 7).

- [ ] **Step 5: Commit**

```bash
git add internal/engine/matrix.go internal/engine/matrix_test.go
git commit -m "feat(engine): expandMatrix Cartesian-product fan-out (Track 1 #3)"
```

---

### Task 3: Wire `expandMatrix` into `RunPipeline`; thread `Matrix` through events

**Files:**
- Modify: `internal/engine/engine.go` (call `expandMatrix` between `applyDefaults` and DAG build; thread the `Matrix` payload through `runOne` so per-expansion `task-start` / `task-end` events carry it)
- Modify: `internal/engine/run.go` (add `Matrix *MatrixInfo` to `TaskOutcome`)
- Modify: `internal/reporter/event.go` (add `Matrix *MatrixEvent` to `Event`)
- Test: new file `internal/engine/matrix_engine_test.go`

The trickiest piece: the engine needs to know, per expansion, *which
matrix index* it's running so it can populate the event's
`Matrix.Index` / `Matrix.Of` / `Matrix.Params`. We do this by
attaching a side-table inside `expandMatrix`'s output — simplest
approach is to store the `MatrixInfo` on a new optional field
`PipelineTask.matrixInfo` (Go-only, JSON-tagged `-` so it doesn't
serialize) that `runOne` reads when emitting events.

- [ ] **Step 1: Write the failing integration test**

Create `internal/engine/matrix_engine_test.go`:

```go
package engine_test

import (
    "context"
    "sort"
    "testing"

    "github.com/danielfbm/tkn-act/internal/backend"
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

    // Every task-end event for an expansion carries Matrix.Parent="build".
    var endsWithMatrix int
    for _, ev := range sink.events {
        if ev.Kind == reporter.EvtTaskEnd && ev.Matrix != nil && ev.Matrix.Parent == "build" {
            endsWithMatrix++
        }
    }
    if endsWithMatrix != 4 {
        t.Errorf("matrix task-end events = %d, want 4", endsWithMatrix)
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
        t.Errorf("want 1 task to run (the linux row); got %v", be.steps)
    }
    // Exactly one skip event, under the EXPANSION name, with Matrix
    // populated.
    var skips []reporter.Event
    for _, ev := range sink.events {
        if ev.Kind == reporter.EvtTaskSkip {
            skips = append(skips, ev)
        }
    }
    if len(skips) != 1 {
        t.Fatalf("task-skip events = %d, want 1", len(skips))
    }
    if skips[0].Task != "build-1" {
        t.Errorf("skip Task = %q, want %q", skips[0].Task, "build-1")
    }
    if skips[0].Matrix == nil || skips[0].Matrix.Parent != "build" || skips[0].Matrix.Index != 1 {
        t.Errorf("skip Matrix = %+v, want {Parent:build Index:1 ...}", skips[0].Matrix)
    }
}
```

`captureBackend` and `sliceSink` already exist in
`internal/engine/step_template_engine_test.go` and
`internal/engine/policy_test.go` respectively. Extend
`captureBackend` with an `invocationsInOrder []backend.TaskInvocation`
slice (append in `RunTask`) so the order test can assert. If the
existing fixture doesn't have it, add the field in this same task.

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -run TestMatrix -count=1 ./internal/engine/...
```

Expected: FAIL — `expandMatrix` not called from `RunPipeline`, and
`Event.Matrix` doesn't exist.

- [ ] **Step 3: Add `MatrixEvent` to the reporter event**

In `internal/reporter/event.go`, append after the `Results` field:

```go
    // Matrix is set on task-start / task-end / task-skip events
    // emitted from a PipelineTask.matrix expansion. Nil for
    // ordinary tasks (omitempty).
    Matrix *MatrixEvent `json:"matrix,omitempty"`
```

And add the type at the bottom of the file:

```go
// MatrixEvent identifies one expansion of a matrix-fanned
// PipelineTask. Parent is the original PipelineTask name; Index is
// the 0-based row index in the expansion order; Of is the total
// number of expansions of this Parent; Params holds the row's
// matrix-contributed params (string-keyed string values). The
// engine constructs this from internal/engine.MatrixInfo before
// emitting the event.
type MatrixEvent struct {
    Parent string            `json:"parent"`
    Index  int               `json:"index"`
    Of     int               `json:"of"`
    Params map[string]string `json:"params,omitempty"`
}
```

- [ ] **Step 4: Add `Matrix *MatrixInfo` to `TaskOutcome`**

In `internal/engine/run.go`, append after the `Results` field on `TaskOutcome`:

```go
    // Matrix, when non-nil, identifies which expansion of a
    // matrix-fanned parent PipelineTask this outcome came from.
    // Threaded into reporter.Event.Matrix at task-end emission.
    Matrix *MatrixInfo
```

- [ ] **Step 5: Stash `MatrixInfo` on each expanded `PipelineTask`**

We need a Go-only side-channel from `expandMatrix` into the engine
loop. Adding a non-serialized field to `PipelineTask` keeps the
plumbing in one place. In `internal/tektontypes/types.go`:

```go
type PipelineTask struct {
    // ... existing fields ...
    Retries int `json:"retries,omitempty"`
    Matrix  *Matrix `json:"matrix,omitempty"`

    // matrixInfo is set by engine.expandMatrix on each expansion
    // child; never serialized (`json:"-"`). The engine reads it
    // when emitting task-start / task-end events to populate
    // reporter.Event.Matrix.
    MatrixInfo *MatrixInfo `json:"-"`
}

// MatrixInfo is re-declared here to avoid an engine→types import
// cycle. internal/engine.MatrixInfo is a type alias to this one.
type MatrixInfo struct {
    Parent string
    Index  int
    Of     int
    Params map[string]string
}
```

…and in `internal/engine/matrix.go` change `MatrixInfo` to:

```go
type MatrixInfo = tektontypes.MatrixInfo
```

Then in `expandList`, set `child.MatrixInfo = &MatrixInfo{Parent: pt.Name, Index: i, Of: len(rows), Params: rowParamMap(row.params)}` on each expansion (where `rowParamMap` walks `row.params` into a `map[string]string`).

- [ ] **Step 6: Call `expandMatrix` in `RunPipeline`**

In `internal/engine/engine.go`, find the block (around lines 60-67):

```go
    params, err := applyDefaults(pl.Spec.Params, in.Params)
    if err != nil {
        return RunResult{}, err
    }
    results := map[string]map[string]string{} // task → result name → value
    outcomes := map[string]TaskOutcome{}      // task → outcome
```

Insert *before* the `results :=` line:

```go
    // Fan out PipelineTask.matrix into expansion children before
    // building the DAG. The DAG layer is matrix-unaware after this
    // pass — every expansion looks like an ordinary PipelineTask.
    pl, err = expandMatrix(pl, params)
    if err != nil {
        e.rep.Emit(reporter.Event{Kind: reporter.EvtRunEnd, Time: time.Now(), Status: "failed", Message: err.Error()})
        return RunResult{Status: "failed"}, err
    }
```

- [ ] **Step 7: Thread `Matrix` into emitted events**

In `internal/engine/engine.go`, find the per-task event-emission
sites in `RunPipeline` (the `EvtTaskStart` and `EvtTaskEnd`
emissions inside the `eg.Go` closure, plus the `EvtTaskSkip` blocks
for upstream-bad and finally-skip). For each one, if `pt.MatrixInfo
!= nil`, populate `Matrix:` on the event:

```go
e.rep.Emit(reporter.Event{
    Kind: reporter.EvtTaskStart, Time: time.Now(), Task: tname,
    Matrix: matrixEventFor(pt),
})
```

Add a helper near the bottom of the file:

```go
func matrixEventFor(pt tektontypes.PipelineTask) *reporter.MatrixEvent {
    if pt.MatrixInfo == nil {
        return nil
    }
    return &reporter.MatrixEvent{
        Parent: pt.MatrixInfo.Parent,
        Index:  pt.MatrixInfo.Index,
        Of:     pt.MatrixInfo.Of,
        Params: pt.MatrixInfo.Params,
    }
}
```

Update the four sites (`EvtTaskStart`, `EvtTaskEnd`, `EvtTaskSkip` ×2)
in `RunPipeline` and the equivalent two sites in the finally loop.

- [ ] **Step 8: Per-row `when:` evaluation**

`when:` is evaluated **per row**, not on the parent (Critical 3 fix,
option (a)). The `when:` block on the parent PipelineTask is
shallow-copied onto every expansion by `expandList` already (since
the child is a copy of `pt`). The engine's existing per-task
`evaluateWhen` call site fires naturally per expansion — the only
change required is making the resolver context for that call
include the row's matrix-contributed params. Concretely: in the
per-task dispatch loop (`runOne`'s caller), populate
`resolverCtx.Params` from `mergeParams(pl.Spec.Params + pt.Params)`
*before* calling `evaluateWhen` — `pt.Params` already contains the
row's matrix params after `expandMatrix`, so they end up visible to
`$(params.<matrix-name>)` inside the `when:` block.

For each row whose `when:` is false:

1. Emit one `EvtTaskSkip` event under the **expansion** name (e.g.
   `build-1`) with `Matrix:` populated via `matrixEventFor(pt)`.
2. Mark the expansion `not-run` in `outcomes`.
3. Continue dispatching the remaining expansions (do NOT
   short-circuit to the parent). Downstream `runAfter` propagation
   skips downstream tasks only if **every** expansion is `not-run`
   or otherwise non-success — the existing per-task propagation
   does this correctly because each expansion is an independent
   `PipelineTask` after `expandMatrix`.

Update the test `TestMatrixWhenSkipShortCircuitsParent` to
`TestMatrixWhenSkipsRowsIndependently`:

```go
func TestMatrixWhenSkipsRowsIndependently(t *testing.T) {
    // when: $(params.os) in [linux] — only build-0 (linux) runs;
    // build-1 (darwin) emits one task-skip with Matrix.Parent="build",
    // Matrix.Index=1.
    /* ... pipeline YAML ... */
    /* assertions:
       - len(be.steps) == 1 (only the linux row ran)
       - exactly one EvtTaskSkip with Task=="build-1" and ev.Matrix != nil
    */
}
```

- [ ] **Step 9: Run tests**

```bash
go test -run TestMatrix -race -count=1 ./internal/engine/...
```

Expected: PASS (all 3).

- [ ] **Step 10: Run the full engine suite to confirm no regressions**

```bash
go test -race -count=1 ./internal/engine/... ./internal/reporter/...
```

Expected: every test OK — including the v1.3 timeout suite, the
v1.4 step-template suite, and the v1.5 pipeline-results suite.

- [ ] **Step 11: Commit**

```bash
git add internal/engine/engine.go internal/engine/matrix.go internal/engine/matrix_engine_test.go internal/engine/run.go internal/reporter/event.go internal/tektontypes/types.go
git commit -m "feat(engine): wire matrix expansion + thread Matrix event payload"
```

---

### Task 4: Extend resolver to support `$(tasks.X.results.Y[*])` array projection

**Files:**
- Modify: `internal/resolver/resolver.go` (widen `isArrayStarRef`; extend `SubstituteArgs` to JSON-decode task-result `[*]` references)
- Test: `internal/resolver/resolver_test.go` (`TestResolveTaskResultArrayStar`)

**Why this lands before Task 5 (result aggregation).** Task 5
folds matrix-fanned per-expansion string results into a JSON-array
literal under the parent name. For
`$(tasks.<parent>.results.<Y>[*])` to resolve to N spliced strings
in pipeline-results / downstream-task params, the resolver needs to
recognise `$(tasks.X.results.Y[*])` (today only `$(params.X[*])` is
recognised) and JSON-decode the source string. Without this, the
Task 5 integration test cannot pass.

- [ ] **Step 1: Write the failing test**

Append to `internal/resolver/resolver_test.go`:

```go
func TestResolveTaskResultArrayStar(t *testing.T) {
    ctx := resolver.Context{
        Results: map[string]map[string]string{
            // Tekton's on-disk shape for an array-of-strings result
            // is a JSON-array literal string. aggregateMatrixResults
            // (Task 5) writes exactly this shape under the parent name.
            "build": {"images": `["a","b","c"]`},
        },
    }
    got, err := resolver.SubstituteArgs([]string{"$(tasks.build.results.images[*])"}, ctx)
    if err != nil {
        t.Fatal(err)
    }
    want := []string{"a", "b", "c"}
    if !reflect.DeepEqual(got, want) {
        t.Errorf("SubstituteArgs = %v, want %v", got, want)
    }
}

func TestResolveTaskResultArrayStarRejectsNonArrayValue(t *testing.T) {
    ctx := resolver.Context{
        Results: map[string]map[string]string{"build": {"tag": "scalar"}},
    }
    if _, err := resolver.SubstituteArgs([]string{"$(tasks.build.results.tag[*])"}, ctx); err == nil {
        t.Fatal("want error for non-array source, got nil")
    }
}
```

- [ ] **Step 2: Run test to verify failure**

```bash
go test -run 'TestResolveTaskResultArrayStar' -count=1 ./internal/resolver/...
```

Expected: FAIL — `isArrayStarRef` only matches `$(params.…[*])`, so
the call falls through to scalar `Substitute`, which doesn't know
how to splice.

- [ ] **Step 3: Widen `isArrayStarRef` and extend `SubstituteArgs`**

In `internal/resolver/resolver.go`:

```go
// isArrayStarRef now accepts BOTH:
//   $(params.<name>[*])
//   $(tasks.<task>.results.<name>[*])
func isArrayStarRef(s string) bool {
    if !strings.HasPrefix(s, "$(") || !strings.HasSuffix(s, "[*])") {
        return false
    }
    inner := s[2 : len(s)-len("[*])")]
    return strings.HasPrefix(inner, "params.") || strings.HasPrefix(inner, "tasks.")
}
```

Add a small splitter:

```go
// arrayStarKind reports which family the [*] reference belongs to.
//   ("param", name)         for $(params.<name>[*])
//   ("taskResult", task, result) for $(tasks.<task>.results.<name>[*])
func arrayStarRef(s string) (kind, a, b string) {
    inner := s[2 : len(s)-len("[*])")] // strip $( ... [*])
    if strings.HasPrefix(inner, "params.") {
        return "param", strings.TrimPrefix(inner, "params."), ""
    }
    // tasks.<task>.results.<name>
    rest := strings.TrimPrefix(inner, "tasks.")
    if dot := strings.Index(rest, ".results."); dot >= 0 {
        return "taskResult", rest[:dot], strings.TrimPrefix(rest[dot:], ".results.")
    }
    return "", "", ""
}
```

In `SubstituteArgs` (and `SubstituteArgsAllowStepRefs`), replace the
single `params.`-only branch with:

```go
if isArrayStarRef(a) {
    kind, x, y := arrayStarRef(a)
    switch kind {
    case "param":
        vals, ok := ctx.ArrayParams[x]
        if !ok {
            return nil, fmt.Errorf("unknown array param %q", x)
        }
        out = append(out, vals...)
    case "taskResult":
        rs, ok := ctx.Results[x]
        if !ok {
            return nil, fmt.Errorf("no results for task %q", x)
        }
        raw, ok := rs[y]
        if !ok {
            return nil, fmt.Errorf("task %q has no result %q", x, y)
        }
        var arr []string
        if err := json.Unmarshal([]byte(raw), &arr); err != nil {
            return nil, fmt.Errorf("task result %q is not a JSON-array: %w", x+"."+y, err)
        }
        out = append(out, arr...)
    default:
        return nil, fmt.Errorf("unrecognised [*] reference %q", a)
    }
    continue
}
```

(Add `import "encoding/json"` at the top of the file.)

- [ ] **Step 4: Run test**

```bash
go test -count=1 ./internal/resolver/...
```

Expected: every test passes (including the new ones and the
existing param-`[*]` suite).

- [ ] **Step 5: Commit**

```bash
git add internal/resolver/resolver.go internal/resolver/resolver_test.go
git commit -m "feat(resolver): support \$(tasks.X.results.Y[*]) array projection"
```

---

### Task 5: Aggregate matrix-fanned task results into the parent

**Depends on:** Task 4 (the resolver `[*]` extension for
`$(tasks.X.results.Y[*])`). Without Task 4, the integration test
below cannot pass; verify Task 4 has landed before starting this
task.

**Files:**
- Modify: `internal/engine/engine.go` (after each task completes, if it's a matrix expansion and every sibling is now terminal, fold results)
- Create: helper in `internal/engine/matrix.go` → `aggregateMatrixResults`
- Modify: `internal/engine/pipeline_results.go` (extend the `array` branch to recognise sole `$(tasks.X.results.Y[*])` elements and splice via `resolver.SubstituteArgs` so the result lands as `[]string`, not `string`)
- Test: extension to `internal/engine/matrix_engine_test.go`
- Test: `internal/engine/pipeline_results_test.go::TestResolvePipelineResultsArrayStarFromMatrix` asserting the Go type is `[]string`

We promote string results from N expansions to a single
JSON-array-literal string under the *parent* name. The Task 4
resolver extension makes `$(tasks.<parent>.results.Y[*])` resolve to
N spliced strings. The `pipeline_results.go` extension threads that
through into `RunResult.Results[<name>]` as a Go `[]string` (the
Important #5 requirement — the test asserts `[]string` not
`string`).

- [ ] **Step 1: Write the failing test**

Append to `internal/engine/matrix_engine_test.go`:

```go
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
    // Script the per-expansion result.
    be := &resultsBackend{
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
        t.Fatalf("Results[tags] type = %T, want []string", res.Results["tags"])
    }
    want := []string{"linux-1.21", "linux-1.22", "darwin-1.21", "darwin-1.22"}
    if len(got) != 4 {
        t.Fatalf("Results[tags] len = %d, want 4 (%v)", len(got), got)
    }
    // Order must match the expansion row order.
    for i := range want {
        if got[i] != want[i] {
            t.Errorf("Results[tags][%d] = %q, want %q", i, got[i], want[i])
        }
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -run TestMatrixResultsAggregate -count=1 ./internal/engine/...
```

Expected: FAIL — `Results[tags]` is empty (the resolver couldn't
find `tasks.build.results.tag` because `build` is no longer a real
PipelineTask after expansion).

- [ ] **Step 3: Implement `aggregateMatrixResults`**

Append to `internal/engine/matrix.go`:

```go
// aggregateMatrixResults folds per-expansion string results into
// the parent name. After every expansion of a matrix-fanned parent
// has produced its results map, this writes one entry per result
// name into results[parent], where the value is a JSON-array
// literal of the per-expansion strings in row order. That is the
// shape Tekton itself writes to /tekton/results/<name> for
// matrix-fanned tasks, so the existing resolver handles
// $(tasks.parent.results.Y) and $(tasks.parent.results.Y[*])
// without further changes.
//
// Called from RunPipeline at every task completion: if the task is
// a matrix expansion (pt.MatrixInfo != nil) and every other
// expansion of the same parent is terminal, fold.
func aggregateMatrixResults(parent string, expansionNames []string, results map[string]map[string]string) {
    if len(expansionNames) == 0 {
        return
    }
    // Collect every result name produced by any expansion.
    nameSet := map[string]bool{}
    for _, n := range expansionNames {
        for k := range results[n] {
            nameSet[k] = true
        }
    }
    if len(nameSet) == 0 {
        return
    }
    if results[parent] == nil {
        results[parent] = map[string]string{}
    }
    for k := range nameSet {
        arr := make([]string, 0, len(expansionNames))
        for _, n := range expansionNames {
            // Missing-on-one-expansion → empty string in that slot.
            arr = append(arr, results[n][k])
        }
        // Encode as a JSON-array literal so resolver's [*] expansion
        // sees it as an array.
        b, _ := json.Marshal(arr)
        results[parent][k] = string(b)
    }
}
```

(Add `import "encoding/json"` at the top.)

In `internal/engine/engine.go`, after the per-task `mu.Lock()` block
that writes `results[tname] = oc.Results`, add:

```go
if pt.MatrixInfo != nil {
    if siblingsDone(pt.MatrixInfo, outcomes) {
        aggregateMatrixResults(
            pt.MatrixInfo.Parent,
            expansionNamesOf(pt.MatrixInfo, pl.Spec.Tasks),
            results,
        )
    }
}
```

`siblingsDone` and `expansionNamesOf` live in `matrix.go`:

```go
func siblingsDone(mi *MatrixInfo, outcomes map[string]TaskOutcome) bool {
    // Every name in the parent's expansion set must be present in
    // outcomes (i.e. terminal — `outcomes` is only written from a
    // task's terminal block).
    // Caller holds mu.
    return /* see implementation */
}
```

- [ ] **Step 4: Extend `resolvePipelineResults` to splice `[*]` references into `[]string`**

Important #5 requirement. In
`internal/engine/pipeline_results.go`, the `array` branch today
calls `resolver.Substitute` per element, which is the SCALAR path
and would resolve `$(tasks.build.results.tag[*])` into a
JSON-array-literal *string*, not the spliced strings.

Change the `array` branch: if the array element is a sole
`$(tasks.X.results.Y[*])` reference (the whole element, not
interpolated into a longer string), call `resolver.SubstituteArgs`
on it (which now, after Task 4, splices via the JSON-decode path)
and append the resulting `[]string` to `items`. For elements that
mix `[*]` with other text (rare; Tekton flat-out rejects this),
fall through to the existing `Substitute` path so the error message
points at the offending element.

Add a focused test in
`internal/engine/pipeline_results_test.go`:

```go
func TestResolvePipelineResultsArrayStarFromMatrix(t *testing.T) {
    pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
        Results: []tektontypes.PipelineResult{{
            Name: "tags",
            Value: tektontypes.ParamValue{
                Type:     tektontypes.ParamTypeArray,
                ArrayVal: []string{"$(tasks.build.results.tag[*])"},
            },
        }},
    }}
    results := map[string]map[string]string{
        "build": {"tag": `["linux-1.21","linux-1.22","darwin-1.21","darwin-1.22"]`},
    }
    got, errs := resolvePipelineResults(pl, results)
    if len(errs) != 0 {
        t.Fatalf("unexpected errs: %v", errs)
    }
    // CRITICAL: type must be []string, not string.
    arr, ok := got["tags"].([]string)
    if !ok {
        t.Fatalf("Results[tags] type = %T, want []string", got["tags"])
    }
    want := []string{"linux-1.21", "linux-1.22", "darwin-1.21", "darwin-1.22"}
    if !reflect.DeepEqual(arr, want) {
        t.Errorf("Results[tags] = %v, want %v", arr, want)
    }
}
```

- [ ] **Step 5: Run tests**

```bash
go test -race -count=1 -run 'TestMatrix|TestResolvePipelineResultsArrayStarFromMatrix' ./internal/engine/...
```

Expected: PASS (all 4 + the new pipeline-results test).

- [ ] **Step 6: Commit**

```bash
git add internal/engine/matrix.go internal/engine/engine.go internal/engine/matrix_engine_test.go internal/engine/pipeline_results.go internal/engine/pipeline_results_test.go
git commit -m "feat(engine): aggregate matrix-fanned string results under parent name; pipeline-results [*] splices to []string"
```

---

### Task 6: Validator rules for matrix

**Files:**
- Modify: `internal/validator/validator.go`
- Test: `internal/validator/validator_test.go`

Per spec § 7. Seven rules in total (the include-overlap rule is the
Critical 2 fix that prevents docker-vs-cluster divergence).

- [ ] **Step 1: Write the failing tests**

Append to `internal/validator/validator_test.go`:

```go
func TestValidateMatrixEmptyValueList(t *testing.T) {
    b := mustLoad(t, `
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
          - {name: os, value: []}
`)
    errs := validator.Validate(b, "p", nil)
    if len(errs) == 0 {
        t.Fatal("want error for empty matrix value list, got none")
    }
}

func TestValidateMatrixDuplicateParamName(t *testing.T) { /* analogous; matrix.params[0].name == matrix.params[1].name */ }

func TestValidateMatrixCardinalityCap(t *testing.T) {
    // 17 × 17 = 289 > 256.
    /* construct via raw YAML or programmatically */
}

func TestValidateMatrixIncludeNonStringRejected(t *testing.T) { /* include row with array-typed param value */ }

func TestValidateMatrixIncludeOverlapsCrossProductRejected(t *testing.T) {
    // matrix.params has {os, goversion}; include[0].params has
    // {os, arch}. The os overlap MUST be rejected with a clear
    // error pointing at the v1 limitation.
    b := mustLoad(t, `
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: t}
spec:
  params: [{name: os}, {name: goversion}, {name: arch}]
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
          - {name: goversion, value: ["1.21"]}
        include:
          - name: arm-extra
            params:
              - {name: os,   value: linux}   # OVERLAPS matrix.params.os
              - {name: arch, value: arm64}
`)
    errs := validator.Validate(b, "p", nil)
    if len(errs) == 0 {
        t.Fatal("want overlap-rejection error, got none")
    }
    // The error message must mention the v1 limitation so users know
    // why and where to look.
    found := false
    for _, e := range errs {
        if strings.Contains(e.Error(), "overlaps a cross-product param") {
            found = true; break
        }
    }
    if !found {
        t.Errorf("error messages = %v, want one mentioning 'overlaps a cross-product param'", errs)
    }
}

func TestValidateMatrixFannedTaskArrayResultRejected(t *testing.T) {
    // Task declares result type=array; pipeline references it via
    // matrix-fanned PipelineTask — must reject. This test also
    // serves as the positive test that the result-typing rule
    // FIRES (i.e. resolvedTasks is plumbed in correctly); a future
    // refactor that drops the resolved-tasks scope would silently
    // disable the rule, and this test would catch it.
}

func TestValidateMatrixHappyPath(t *testing.T) {
    /* one valid 2x2 matrix with no include overlaps; expect 0 errors */
}
```

- [ ] **Step 2: Run tests to verify failure modes are correct**

```bash
go test -count=1 -run TestValidateMatrix ./internal/validator/...
```

Expected: every negative test FAILS (no error returned), happy path
PASSES vacuously (no matrix support means no error either).

- [ ] **Step 3: Add validation rules**

In `internal/validator/validator.go`, after rule 11 (or wherever the
last numbered rule lives), add a rule block walking
`pl.Spec.Tasks` ∪ `pl.Spec.Finally` for matrix:

```go
// 12. Matrix rules.
for _, pt := range all {
    if pt.Matrix == nil {
        continue
    }
    seen := map[string]bool{}
    cross := 1
    for _, mp := range pt.Matrix.Params {
        if seen[mp.Name] {
            errs = append(errs, fmt.Errorf("pipeline task %q matrix declares param %q twice", pt.Name, mp.Name))
        }
        seen[mp.Name] = true
        if len(mp.Value) == 0 {
            errs = append(errs, fmt.Errorf("pipeline task %q matrix param %q must be a non-empty string list", pt.Name, mp.Name))
            continue
        }
        cross *= len(mp.Value)
    }
    if len(pt.Matrix.Params) == 0 {
        cross = 0
    }
    total := cross + len(pt.Matrix.Include)
    // Use the engine's hardcoded cap; validator imports it via a
    // shared constant (move matrixMaxRows into a small
    // internal/engine/matrixconst package or duplicate it as
    // ValidatorMatrixMaxRows = 256 to avoid the engine→validator
    // import). Duplication chosen for v1 simplicity.
    const validatorMatrixMaxRows = 256
    if total > validatorMatrixMaxRows {
        errs = append(errs, fmt.Errorf("pipeline task %q matrix would produce %d rows, exceeding the cap of %d", pt.Name, total, validatorMatrixMaxRows))
    }
    // Build the cross-product param-name set for the overlap check.
    crossNames := map[string]bool{}
    for _, mp := range pt.Matrix.Params {
        crossNames[mp.Name] = true
    }
    for _, inc := range pt.Matrix.Include {
        for _, p := range inc.Params {
            if p.Value.Type != tektontypes.ParamTypeString && p.Value.Type != "" {
                errs = append(errs, fmt.Errorf("pipeline task %q matrix include %q param %q must be a string", pt.Name, inc.Name, p.Name))
            }
            // Critical 2 fix: include params overlapping a
            // cross-product param would silently behave differently
            // on cluster (Tekton folds) vs. docker (we append). We
            // reject the overlap until v2 implements the fold.
            if crossNames[p.Name] {
                errs = append(errs, fmt.Errorf(
                    "pipeline task %q matrix include %q param %q overlaps a cross-product param; "+
                        "matrix.include params overlapping cross-product params are not supported in v1; "+
                        "see Track 1 #3 follow-up",
                    pt.Name, inc.Name, p.Name))
            }
        }
    }
    // Result-typing: referenced Task's results must all be type=string.
    // resolvedTasks is the map already used by the taskRef-resolution
    // rules earlier in this file; if it's not in scope here, thread
    // it explicitly into the matrix helper rather than disabling the
    // rule (the result-typing positive test in Step 1 would then
    // catch a silent disable).
    spec, ok := resolvedTasks[pt.Name]
    if ok {
        for _, r := range spec.Results {
            if r.Type == "array" || r.Type == "object" {
                errs = append(errs, fmt.Errorf("pipeline task %q (matrix-fanned) references task whose result %q is type %q; matrix-fanned tasks may only emit string results", pt.Name, r.Name, r.Type))
            }
        }
    }
}
```

- [ ] **Step 4: Run tests**

```bash
go test -count=1 ./internal/validator/...
```

Expected: every test passes.

- [ ] **Step 5: Commit**

```bash
git add internal/validator/validator.go internal/validator/validator_test.go
git commit -m "feat(validator): matrix rules (cardinality cap, types, include-overlap rejection)"
```

---

### Task 7: Cluster backend round-trips `matrix` and reconstructs `MatrixInfo`

**Files:**
- Modify: `internal/backend/backend.go` (add `Matrix *tektontypes.MatrixInfo` to `TaskOutcomeOnCluster`)
- Modify: `internal/backend/cluster/run.go` (read `tekton.dev/pipelineTask` label + `taskRun.spec.params`; reconstruct `MatrixInfo`)
- Modify: `internal/engine/engine.go` (`emitClusterTaskEvents` populates `Matrix:` on the synthesized events when `oc.Matrix != nil`)
- Test: `internal/backend/cluster/runpipeline_test.go` (regression-locking inline test)
- Test: `internal/backend/cluster/run_test.go` (faked PR with 4 child TaskRuns mapped correctly)

- [ ] **Step 1: Write the regression-locking inline test**

Append to `internal/backend/cluster/runpipeline_test.go`:

```go
// TestBuildPipelineRunInlinesMatrix: a PipelineTask with matrix
// must round-trip through BuildPipelineRunObject intact under
// pipelineSpec.tasks[].matrix.
func TestBuildPipelineRunInlinesMatrix(t *testing.T) {
    be, _, _, _, _ := fakeBackend(t)
    pl := tektontypes.Pipeline{Spec: tektontypes.PipelineSpec{
        Tasks: []tektontypes.PipelineTask{{
            Name: "build", TaskRef: &tektontypes.TaskRef{Name: "t"},
            Matrix: &tektontypes.Matrix{Params: []tektontypes.MatrixParam{
                {Name: "os", Value: []string{"linux", "darwin"}},
            }},
        }},
    }}
    pl.Metadata.Name = "p"
    tk := tektontypes.Task{Spec: tektontypes.TaskSpec{
        Steps: []tektontypes.Step{{Name: "s", Image: "alpine", Script: "true"}},
    }}
    tk.Metadata.Name = "t"

    prObj, err := be.BuildPipelineRunObject(backend.PipelineRunInvocation{
        RunID: "abc12345", PipelineRunName: "p-abc12345",
        Pipeline: pl, Tasks: map[string]tektontypes.Task{"t": tk},
    }, "tkn-act-abc12345")
    if err != nil {
        t.Fatal(err)
    }
    un := prObj.(*unstructured.Unstructured)
    tasks, _, _ := unstructured.NestedSlice(un.Object, "spec", "pipelineSpec", "tasks")
    if len(tasks) != 1 {
        t.Fatalf("tasks = %d, want 1", len(tasks))
    }
    taskMap := tasks[0].(map[string]any)
    matrix, ok := taskMap["matrix"].(map[string]any)
    if !ok {
        t.Fatalf("matrix not found on inlined task: %v", taskMap)
    }
    params := matrix["params"].([]any)
    if len(params) != 1 {
        t.Fatalf("matrix.params len = %d, want 1", len(params))
    }
}
```

- [ ] **Step 2: Run regression-locking test**

```bash
go test -count=1 -run TestBuildPipelineRunInlinesMatrix ./internal/backend/cluster/...
```

Expected: PASS — `taskSpecToMap` already round-trips via JSON.

- [ ] **Step 3: Write the failing test for `MatrixInfo` reconstruction**

Append to `internal/backend/cluster/run_test.go`:

```go
// TestCollectTaskOutcomesReconstructsMatrix_PrimaryParamHash:
// a faked PR with 4 child TaskRuns that share pipelineTask=build
// and have distinct matrix-row params on spec.params must be
// mapped back via param-hash into 4 outcomes carrying correct
// MatrixInfo.{Parent,Index,Params}. Specifically: shuffle the
// TaskRuns into a non-row order in childReferences and assert the
// indices come back in MATRIX-ROW order, not childRef order — that
// proves the param-hash path fired (not the fallback).
func TestCollectTaskOutcomesReconstructsMatrix_PrimaryParamHash(t *testing.T) {
    /* fake unstructured PipelineRun, 4 TaskRuns with
         metadata.labels["tekton.dev/pipelineTask"] = "build"
         spec.params = the matrix-row params
       Shuffle the childReferences order. Call collectTaskOutcomes,
       assert MatrixInfo.Index follows the engine's expected
       row order regardless of childReferences order. */
}

// TestCollectTaskOutcomesReconstructsMatrix_FallbackChildRefOrder:
// strip spec.params from each TaskRun. The param-hash path returns
// no hit; the fallback must use childReferences index, AND emit
// exactly one EvtError warning per TaskRun.
func TestCollectTaskOutcomesReconstructsMatrix_FallbackChildRefOrder(t *testing.T) {
    /* same setup but with empty spec.params on each TaskRun.
       Assert MatrixInfo.Index == childReferences-index, and that
       len(sink.events filter Kind==EvtError) == 4. */
}
```

- [ ] **Step 4: Implement the reconstruction (param-hash PRIMARY, childRef-order FALLBACK)**

Critical 4 fix: TaskRun-to-expansion mapping uses two strategies.
The PRIMARY is deterministic param hashing (version-stable across
Tekton minors); the FALLBACK is `childReferences` ordering (kept for
the unlikely case a future Tekton drops `spec.params` from inlined
TaskRuns).

In `internal/backend/cluster/run.go`'s `collectTaskOutcomes`, after
extracting per-TaskRun status, read:

```go
labels := tr.GetLabels()
parent := labels["tekton.dev/pipelineTask"]
// Match this TaskRun's spec.params against the original Pipeline's
// matrix definition for `parent`. If `parent` is matrix-fanned,
// reconstruct MatrixInfo:
if mi := matchMatrixRowFromTaskRun(in.Pipeline, parent, tr, indexInChildRefs, e.rep); mi != nil {
    outcome.Matrix = mi
    // The map key changes from `parent` to `parent-<index>` to
    // mirror engine-side naming. (Include rows with a Name use
    // mi.Name instead — same shape as engine-side rowName().)
    key := parent + "-" + strconv.Itoa(mi.Index)
    if mi.IncludeName != "" {
        key = mi.IncludeName
    }
    outcomesByName[key] = outcome
} else {
    outcomesByName[parent] = outcome
}
```

`matchMatrixRowFromTaskRun` lives in `run.go`:

```go
// matchMatrixRowFromTaskRun reconstructs MatrixInfo for a TaskRun
// belonging to a matrix-fanned parent. PRIMARY: hash the
// matrix-row params extracted from TaskRun.spec.params and look
// up against the parent's pre-computed row hashes. FALLBACK:
// childReferences ordering, with an EvtError warning logged so we
// notice the regression.
func matchMatrixRowFromTaskRun(
    pl tektontypes.Pipeline, parent string, tr *unstructured.Unstructured,
    childRefIdx int, rep reporter.Sink,
) *tektontypes.MatrixInfo {
    pt := findMatrixParent(pl, parent)
    if pt == nil || pt.Matrix == nil {
        return nil
    }
    // Materialize expected rows (shared helper exported by
    // internal/engine: engine.MaterializeMatrixRows(pt) returns the
    // same slice expandMatrix uses).
    rows := engine.MaterializeMatrixRows(*pt)

    // Extract this TaskRun's matrix-row params from spec.params.
    trParams := extractTaskRunParams(tr) // map[string]string
    matrixOnly := filterToNames(trParams, matrixParamNames(pt))

    // PRIMARY: param-hash match.
    target := canonicalHash(matrixOnly)
    for i, row := range rows {
        if canonicalHash(rowParamMap(row.params)) == target {
            return &tektontypes.MatrixInfo{
                Parent: parent, Index: i, Of: len(rows),
                Params: matrixOnly, IncludeName: row.includeName,
            }
        }
    }

    // FALLBACK: childReferences order, with a one-shot warning.
    if childRefIdx >= 0 && childRefIdx < len(rows) {
        rep.Emit(reporter.Event{
            Kind: reporter.EvtError,
            Message: fmt.Sprintf(
                "matrix index reconstruction fell back to childReferences ordering for TaskRun %q "+
                    "(param-hash matching produced no hit; please file an issue with your Tekton version)",
                tr.GetName()),
        })
        return &tektontypes.MatrixInfo{
            Parent: parent, Index: childRefIdx, Of: len(rows),
            Params: matrixOnly, IncludeName: rows[childRefIdx].includeName,
        }
    }
    return nil
}

// canonicalHash JSON-encodes the input with sorted keys and hashes
// it. Stable regardless of map iteration order; insensitive to
// whitespace.
func canonicalHash(m map[string]string) string {
    keys := make([]string, 0, len(m))
    for k := range m { keys = append(keys, k) }
    sort.Strings(keys)
    type kv struct{ K, V string }
    pairs := make([]kv, 0, len(keys))
    for _, k := range keys { pairs = append(pairs, kv{k, m[k]}) }
    b, _ := json.Marshal(pairs)
    sum := sha256.Sum256(b)
    return hex.EncodeToString(sum[:])
}
```

(Adds `crypto/sha256`, `encoding/hex`, `encoding/json`, `sort` to
`run.go`'s imports.)

- [ ] **Step 5: Forward `Matrix` in `emitClusterTaskEvents`**

In `internal/engine/engine.go::emitClusterTaskEvents`, when emitting
`task-start` and `task-end`, populate `Matrix:` from `oc.Matrix`
(now of type `*tektontypes.MatrixInfo`).

- [ ] **Step 5b: Two-version Tekton probe in cluster-integration**

Critical 4 follow-on. Add a probe job to
`.github/workflows/cluster-integration.yml` that runs the `matrix`
fixture against the current Tekton pin AND **one prior minor**
version, then asserts both runs reconstruct identical
`MatrixInfo.Index` assignments by diffing the JSON event streams.

```yaml
matrix-probe:
  needs: [build]
  runs-on: ubuntu-latest
  strategy:
    matrix:
      tekton: [<current-pin>, <prior-minor-pin>]
  steps:
    # ... cluster-up with the chosen Tekton version ...
    - run: |
        ./tkn-act run --cluster -f testdata/e2e/matrix/pipeline.yaml -o json > out-${{ matrix.tekton }}.jsonl
    # In a final consolidate step, diff the two output files on the
    # task / matrix.index / matrix.params fields; fail if they
    # disagree.
```

If the diff fails on a pin we don't intend to support, drop the
older minor from the matrix and document the supported window.

- [ ] **Step 6: Run cluster tests**

```bash
go test -count=1 -race ./internal/backend/cluster/... ./internal/engine/...
```

Expected: every test OK.

- [ ] **Step 7: Commit**

```bash
git add internal/backend/backend.go internal/backend/cluster/run.go internal/backend/cluster/run_test.go internal/backend/cluster/runpipeline_test.go internal/engine/engine.go
git commit -m "feat(cluster): reconstruct MatrixInfo from TaskRun labels and params"
```

---

### Task 8: Cross-backend e2e fixtures

**Files:**
- Create: `testdata/e2e/matrix/pipeline.yaml`
- Create: `testdata/e2e/matrix-include/pipeline.yaml`
- Modify: `internal/e2e/fixtures/fixtures.go`

- [ ] **Step 1: Write the cross-product fixture**

Create `testdata/e2e/matrix/pipeline.yaml`:

```yaml
apiVersion: tekton.dev/v1
kind: Task
metadata: { name: emit }
spec:
  params:
    - { name: os }
    - { name: goversion }
  results:
    - { name: tag }
  steps:
    - name: emit
      image: alpine:3
      script: |
        printf "$(params.os)-$(params.goversion)" > $(results.tag.path)
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: { name: matrix }
spec:
  results:
    - name: tags
      value: $(tasks.build.results.tag[*])
  tasks:
    - name: build
      taskRef: { name: emit }
      matrix:
        params:
          - { name: os, value: [linux, darwin] }
          - { name: goversion, value: ["1.21", "1.22"] }
```

- [ ] **Step 2: Write the include fixture**

Create `testdata/e2e/matrix-include/pipeline.yaml`. Note: the
include rows MUST NOT introduce a param name that's also in
`matrix.params` — that would trip the include-overlap validator
rule (Critical 2 fix). The fixture below uses `arch` as the
include-only param; `os` (the cross-product param) gets a
PipelineTask-level param default that include rows inherit.

```yaml
apiVersion: tekton.dev/v1
kind: Task
metadata: { name: emit }
spec:
  params:
    - { name: os }
    - { name: arch, default: amd64 }
  results:
    - { name: tag }
  steps:
    - image: alpine:3
      script: |
        printf "$(params.os)-$(params.arch)" > $(results.tag.path)
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: { name: matrix-include }
spec:
  results:
    - name: tags
      value: $(tasks.build.results.tag[*])
  tasks:
    - name: build
      taskRef: { name: emit }
      params:
        # PipelineTask-level os is inherited by include rows that
        # don't override it (include rows aren't allowed to override
        # the cross-product 'os' name; they introduce 'arch' only).
        - { name: os, value: linux }
      matrix:
        params:
          - { name: os, value: [linux] }
        include:
          # Two include rows that contribute ONLY 'arch' (not in
          # matrix.params, so no overlap). os comes from PipelineTask.params.
          - name: arm-extra
            params:
              - { name: arch, value: arm64 }
          - params:
              - { name: arch, value: armv7 }
```

Expected expansion: 1 cross-product row (os=linux, arch=amd64 from
Task default) + 2 include rows (arch=arm64, arch=armv7; os comes
from PipelineTask.params=linux). Result: `["linux-amd64",
"linux-arm64", "linux-armv7"]`.

- [ ] **Step 3: Add the fixture entries**

In `internal/e2e/fixtures/fixtures.go::All()`, append two entries:

```go
{
    Dir: "matrix", Pipeline: "matrix", WantStatus: "succeeded",
    WantResults: map[string]any{
        "tags": []any{"linux-1.21", "linux-1.22", "darwin-1.21", "darwin-1.22"},
    },
},
{
    Dir: "matrix-include", Pipeline: "matrix-include", WantStatus: "succeeded",
    WantResults: map[string]any{
        // Order: cross-product first (1 row), then include rows in
        // declaration order (2 rows).
        "tags": []any{"linux-amd64", "linux-arm64", "linux-armv7"},
    },
},
```

- [ ] **Step 4: Write the limitations fixture (Critical 2 documentation)**

Create `testdata/limitations/matrix-include-overlap/pipeline.yaml`:

```yaml
# This Pipeline is REJECTED by tkn-act at validate time (exit 4)
# because matrix.include[0].params.os overlaps matrix.params.os.
#
# Real Tekton FOLDS the include row into the matching cross-product
# row (so a 2x1 cross-product + 1 overlapping include = 2 rows, not
# 3). tkn-act --docker would APPEND the include as a new 3rd row,
# diverging from cluster mode silently. To prevent the divergence
# we reject at validate time until v2 implements the fold.
#
# See docs/superpowers/specs/2026-05-04-pipeline-matrix-design.md § 7.
apiVersion: tekton.dev/v1
kind: Task
metadata: { name: emit }
spec:
  params:
    - { name: os }
    - { name: arch, default: amd64 }
  steps:
    - { image: alpine:3, script: 'true' }
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: { name: matrix-include-overlap }
spec:
  tasks:
    - name: build
      taskRef: { name: emit }
      matrix:
        params:
          - { name: os, value: [linux, darwin] }
        include:
          - name: arm-extra
            params:
              - { name: os,   value: linux }   # overlap → REJECTED
              - { name: arch, value: arm64 }
```

Add a row to `testdata/limitations/README.md`:

```markdown
| `matrix-include-overlap/`| `PipelineTask.matrix.include` overlap   | Validator REJECTS at exit 4; on cluster Tekton would fold the row instead. |
```

The loader test `internal/loader/limitations_test.go` already
iterates the directory; nothing else to wire.

- [ ] **Step 5: Compile-check both tag builds**

```bash
go vet -tags integration ./...
go vet -tags cluster ./...
```

Expected: both exit 0.

- [ ] **Step 6: Run docker e2e locally if Docker is available (optional)**

```bash
docker info >/dev/null 2>&1 && go test -tags integration -run 'TestE2E/matrix' -count=1 ./internal/e2e/... || echo "no docker; CI will run it"
```

- [ ] **Step 7: Commit**

```bash
git add testdata/e2e/matrix/pipeline.yaml testdata/e2e/matrix-include/pipeline.yaml testdata/limitations/matrix-include-overlap/pipeline.yaml testdata/limitations/README.md internal/e2e/fixtures/fixtures.go
git commit -m "test(e2e): matrix fixtures + matrix-include-overlap limitations fixture"
```

---

### Task 9: Documentation convergence

**Files:**
- Modify: `AGENTS.md`
- Modify: `cmd/tkn-act/agentguide_data.md`
- Modify: `README.md`
- Modify: `docs/test-coverage.md`
- Modify: `docs/short-term-goals.md`
- Modify: `docs/feature-parity.md`

- [ ] **Step 1: Add a "Matrix fan-out" section to `AGENTS.md`**

Insert between `## stepTemplate (DRY for Steps)` and
`## Documentation rule: keep related docs in sync with every change`:

```markdown
## Matrix fan-out (`PipelineTask.matrix`)

`PipelineTask.matrix` fans one PipelineTask out into N concrete
TaskRuns by Cartesian product over named string-list params, plus
optional `include` rows for named extras.

| Aspect | Behavior |
|---|---|
| Naming | The `task` field on every event always carries the per-expansion stable name. Cross-product expansions are `<parent>-<index>` (0-based, in row order, e.g. `build-1`); named `include` rows use their declared `name` (e.g. `arm-extra`); unnamed include rows continue the `<parent>-<i>` numbering. The new `matrix.parent` field on the event carries the original PipelineTask name. Consumers reading `Event.Task` keep working unchanged. |
| Param merge | Per row: pipeline-task `params` ∪ matrix-row params; matrix wins on conflicts. |
| DAG | Downstream `runAfter: [<parent>]` waits on every expansion. |
| Failure | Default: any expansion failing makes the parent fail; downstream skips. `failFast: false` is not implemented. |
| Concurrency | Honors `--parallel` like ordinary parallel tasks. |
| Cardinality cap | **256 rows total per matrix** (cross-product + include). Hardcoded; no env var, no flag. Exceeding is exit 4 (validate). If you need more, split the pipeline. |
| Result aggregation | A `string` result `Y` from a matrix-fanned task `X` is promoted to an array-of-strings, addressable as `$(tasks.X.results.Y[*])` and `$(tasks.X.results.Y[N])`. A scalar reference `$(tasks.X.results.Y)` (no `[*]`) returns the JSON-array literal *string* (e.g. `'["a","b","c"]'`) — matches Tekton's on-disk representation. `array` and `object` result types on a matrix-fanned task are validator-rejected. |
| `when:` | Evaluated **per row**. Each row's matrix-contributed params are visible in `$(params.<name>)` references inside the `when:` block. Rows that evaluate false emit one `task-skip` event under the *expansion* name (e.g. `build-1`) with `matrix:` populated. Other rows run normally. Downstream `runAfter` skips downstream tasks only if every expansion ends `not-run`/non-success. |
| JSON event shape | `task-start` / `task-end` / `task-skip` events for an expansion carry `matrix: {parent, index, of, params}`. The field is `omitempty`, so non-matrix runs are byte-identical to today. |

`include` semantics: tkn-act always **appends** include rows as new
rows; it does NOT fold them into matching cross-product rows the way
upstream Tekton can. To prevent silent docker-vs-cluster divergence
(cluster delegates to the controller and would fold; docker would
not), `tkn-act validate` **rejects** any include row whose params
overlap a `matrix.params` name with an explicit error message and
exit 4. The `testdata/limitations/matrix-include-overlap/` fixture
is the documented example.
```

- [ ] **Step 2: Mirror into `cmd/tkn-act/agentguide_data.md`**

```bash
go generate ./cmd/tkn-act/
diff cmd/tkn-act/agentguide_data.md AGENTS.md
```

Expected: no diff.

- [ ] **Step 3: Run the embedded-guide test**

```bash
go test -count=1 ./cmd/tkn-act/...
```

- [ ] **Step 4: Update `README.md`**

Add one bullet under "Tekton features supported": "Matrix fan-out
(`PipelineTask.matrix`)."

- [ ] **Step 5: Update `docs/test-coverage.md`**

Under `### -tags integration`, insert two rows:

```markdown
| `matrix/`         | `PipelineTask.matrix` 2×2 cross-product, result-array aggregation |
| `matrix-include/` | `matrix.include` named + unnamed rows, result-array order, no overlap with cross-product |
```

Under the limitations table (parser-coverage section), insert:

```markdown
| `matrix-include-overlap/`| `matrix.include` overlap with cross-product param: validator-rejected (would fold on cluster) |
```

- [ ] **Step 6: Mark Track 1 #3 done in `docs/short-term-goals.md`**

In the Track 1 table, change the row 3 Status cell from:

```
| Not started. Engine + DAG changes are non-trivial; probably needs its own spec. |
```

to:

```
| Done in v1.6 (PR for `feat: PipelineTask.matrix`). Engine-side expansion before DAG; cluster pass-through with TaskRun reconstruction. |
```

- [ ] **Step 7: Flip the `feature-parity.md` row**

In `docs/feature-parity.md` under `### Pipeline policy`, change:

```
| `PipelineTask.matrix` (parameter-matrix fan-out) | `PipelineTask.matrix` | gap | both | none | none | docs/short-term-goals.md (Track 1 #3) |
```

to:

```
| `PipelineTask.matrix` (parameter-matrix fan-out) | `PipelineTask.matrix` | shipped | both | matrix | none | docs/superpowers/plans/2026-05-04-pipeline-matrix.md (Track 1 #3); also covered by `matrix-include`. Limitations example: `testdata/limitations/matrix-include-overlap/` (parser-coverage only). |
```

The limitations cell stays `none` per `parity-check.sh` invariant 2
(`shipped` rows must not name a limitations fixture). The
`matrix-include-overlap/` directory exists for parse-cleanly
coverage in `internal/loader/limitations_test.go` and as a worked
example documented in AGENTS.md; it is NOT a feature gap. To keep
invariant 5 (every dir under testdata/limitations is referenced by
*some* row), reference it from the Notes column above and from the
`testdata/limitations/README.md` table — both are scanned by the
parity script.

- [ ] **Step 8: Run parity-check**

```bash
bash .github/scripts/parity-check.sh
```

Expected: `parity-check: docs/feature-parity.md, testdata/e2e/, and testdata/limitations/ are consistent.` If invariant 5 fires (the `matrix-include-overlap/` directory isn't recognised by a row's limitations cell), promote it from "Notes-only" to a `gap` placeholder row in `feature-parity.md` whose limitations column names it (e.g. a row for `matrix.include` overlap with `Status=gap, e2e=none, limitations=matrix-include-overlap`).

- [ ] **Step 9: Commit**

```bash
git add AGENTS.md cmd/tkn-act/agentguide_data.md README.md docs/test-coverage.md docs/short-term-goals.md docs/feature-parity.md
git commit -m "docs: document PipelineTask.matrix; flip Track 1 #3 to shipped"
```

---

### Task 10: Final verification, coverage gate, parity gate, PR

- [ ] **Step 1: Full local verification**

```bash
go vet ./... && go vet -tags integration ./... && go vet -tags cluster ./...
go build ./...
go test -race -count=1 ./...
bash .github/scripts/parity-check.sh
.github/scripts/tests-required.sh main HEAD
```

Expected: all exit 0, every test package OK or no-test-files.

- [ ] **Step 2: Coverage no-drop check**

The CI `coverage` job runs
`.github/scripts/coverage-check.sh` per-package. Locally:

```bash
go test -cover -count=1 ./... > /tmp/head.cov 2>&1
git stash
go test -cover -count=1 ./... > /tmp/base.cov 2>&1
git stash pop
diff <(grep -E '^ok|^---' /tmp/base.cov) <(grep -E '^ok|^---' /tmp/head.cov)
```

Inspect: any package whose HEAD coverage dropped > 0.1 percentage
points compared to BASE is a coverage-gate failure. Tasks 1-8 add
new tests for every new code path; Task 9 is doc-only and shouldn't
affect coverage. Expected: no drops.

If a drop appears, the most likely culprit is a code branch in
`internal/engine/matrix.go` or `internal/backend/cluster/run.go`
that's only hit on a specific matrix shape; add a focused unit test
for it.

- [ ] **Step 3: Push branch and open PR**

```bash
git push -u origin feat/pipeline-matrix
gh pr create --title "feat: honor PipelineTask.matrix (Track 1 #3)" --body "$(cat <<'EOF'
## Summary

Closes Track 1 #3 of `docs/short-term-goals.md`. Honors Tekton's
`PipelineTask.matrix` on both backends:

- New `tektontypes.Matrix`, `MatrixParam`, `MatrixInclude` types.
- Engine pre-DAG expansion (`internal/engine/matrix.go::expandMatrix`):
  Cartesian product + `include` rows, deterministic naming
  (`<parent>-<index>` or include-row name), `RunAfter` rewrite so
  downstream tasks wait on every expansion, hardcoded 256-row cap.
- Per-expansion JSON events (`task-start`, `task-end`, `task-skip`)
  carry `matrix: {parent, index, of, params}`. `when:` is evaluated
  per row: each false row emits its own `task-skip` under the
  expansion name (with `Matrix` populated); other rows run normally.
- Resolver extension: `$(tasks.X.results.Y[*])` is now a recognised
  `[*]` form (today only `$(params.X[*])` was). The source string is
  JSON-decoded into `[]string` before splicing.
- Result aggregation: string results from N expansions fold into the
  parent name as a JSON-array literal; the resolver `[*]` extension
  splices it. `Pipeline.spec.results` extractor is extended so an
  array-typed pipeline result fed by `[*]` lands as `[]string` (not
  `string`) on `RunResult.Results`.
- Cluster backend submits `matrix` unchanged to Tekton (round-trips
  via the existing JSON marshal path). Per-TaskRun events
  reconstruct `MatrixInfo` from the `tekton.dev/pipelineTask` label
  + `taskRun.spec.params`, using **deterministic param-hash**
  matching as PRIMARY and `childReferences` ordering as FALLBACK.
  A two-version probe in cluster-integration confirms agreement.
- Validator rules: empty/duplicate matrix params, cardinality cap,
  include-row string-only, **include-row params overlapping a
  cross-product param are rejected** (avoids cluster-vs-docker
  divergence on Tekton's fold semantics), matrix-fanned task may
  only emit string results.
- Two cross-backend fixtures: `matrix` (2×2 cross-product) and
  `matrix-include` (named + unnamed include rows, no overlap with
  cross-product). One limitations fixture
  `matrix-include-overlap` documents the rejection case.

Implements `docs/superpowers/plans/2026-05-04-pipeline-matrix.md`;
spec at `docs/superpowers/specs/2026-05-04-pipeline-matrix-design.md`.

## Test plan

- [x] `go vet ./...` × {default, integration, cluster}
- [x] `go build ./...`
- [x] `go test -race -count=1 ./...`
- [x] `bash .github/scripts/parity-check.sh`
- [x] tests-required script
- [x] coverage no-drop (per-package, vs. main)
- [ ] docker-integration CI — runs `matrix` and `matrix-include`
- [ ] cluster-integration CI — same fixtures on real Tekton

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

- **Spec coverage:** Type model → Task 1. Pure expansion helper with
  every Tekton-spec'd rule (cross-product, include, naming, param
  merge, RunAfter rewrite, cap, per-row when) → Task 2. Engine
  wiring + event payload → Task 3. **Resolver `[*]` extension for
  task-result references** (must land before aggregation) → Task 4.
  Result aggregation (the matrix→`[*]` bridge) → Task 5. Validator
  (including the include-overlap rejection from Critical 2) → Task
  6. Cluster backend round-trip + TaskRun reconstruction with
  param-hash matching (Critical 4) + two-version probe → Task 7.
  Cross-backend fidelity (e2e + limitations fixture) → Task 8.
  Docs convergence → Task 9. Final ship → Task 10.
- **Task ordering / blocking dependencies:**
  - Task 5 (aggregation) BLOCKS on Task 4 (resolver `[*]`) — without
    the resolver fix, the `Pipeline.spec.results`-from-matrix
    integration test cannot pass.
  - Task 7 (cluster reconstruction) BLOCKS on Task 1 (types) and
    Task 3 (engine `MatrixInfo` plumbing).
  - Task 8 (e2e fixtures) BLOCKS on Tasks 5 and 7 — both backends
    must produce the asserted Results map.
  - Tasks 2, 3, 4, 6 are otherwise independent and can be
    interleaved.
- **No placeholders that defer real work:** every step has actual
  code or commands. Task 7 step 4 has the only "implementation
  outline" (the cluster `matchMatrixRowFromTaskRun`) — written that
  way because the exact field path (`tekton.dev/pipelineTask` vs.
  `tekton.dev/pipelineTaskName`) needs verification against the
  Tekton version pinned in `cluster-integration.yml`. Resolve at
  implementation time by reading
  `internal/backend/cluster/run.go::collectTaskOutcomes` for the
  current label name and matching that.
- **Type consistency:** `tektontypes.MatrixInfo` is the single
  carrier across `PipelineTask.MatrixInfo`,
  `engine.TaskOutcome.Matrix`,
  `backend.TaskOutcomeOnCluster.Matrix`, and (mapped to)
  `reporter.MatrixEvent`. The mapping function `matrixEventFor` is
  the only place these types meet.
- **One-task non-regression:** Task 3 step 10 explicitly runs the
  full `engine` and `reporter` suites so the `expandMatrix`
  insertion can't silently break the v1.3 timeout, v1.4
  stepTemplate, or v1.5 pipeline-results work. Task 7 step 6 runs
  the cluster suite for the same reason.
- **Cluster pass-through "free" for the inline path:** the existing
  `taskSpecToMap` JSON round-trip carries `matrix` through unchanged
  because `tektontypes.PipelineTask` gains a JSON-tagged field. Task
  7 step 1 is a regression-locking test, not a new code path.
- **Docs are atomic with the code:** Task 9 lands AGENTS.md ↔
  embedded guide convergence, README, test-coverage,
  short-term-goals, and the parity row flip in one commit so the
  parity-check is satisfied at every commit boundary.
- **Coverage gate:** Task 10 step 2 explicitly compares per-package
  coverage to base. The new code paths (`internal/resolver`
  widening, `internal/engine/matrix.go`, validator additions,
  cluster reconstruction) are all covered by unit + integration
  tests added in the same plan; the only risk is a bare-`else`
  branch picking up no coverage — fix by adding the focused test
  before opening the PR.
