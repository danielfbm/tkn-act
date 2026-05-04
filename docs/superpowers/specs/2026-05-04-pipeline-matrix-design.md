# `PipelineTask.matrix` design

> Status: design draft. Implementation plan: `docs/superpowers/plans/2026-05-04-pipeline-matrix.md`.
>
> Closes Track 1 #3 of `docs/short-term-goals.md`. Adds Tekton's
> `PipelineTask.matrix` to `tkn-act` on both the docker and cluster
> backends. Cross-backend fidelity is required.

## 1. Goal

Honor Tekton v1's `PipelineTask.matrix`: fan a single `PipelineTask`
out into N concrete TaskRuns by Cartesian product over named param
lists, plus the optional `include` rows that add extra named
combinations. The fan-out is invisible to the rest of the pipeline
DAG (downstream tasks wait for *all* expansions before they run); the
fan-out is fully visible to JSON consumers (one `task-start` /
`task-end` event per expansion, each tagged with a stable identifier).

This is the v1 of matrix support. We deliberately ship the high-value
part (`matrix.params` cross-product, plus `include` for named extras)
in this PR and defer the niche knobs (`maxConcurrency`, complex
include-row interactions with the cross-product, params-of-arrays in
matrix values) to a follow-up — see § 11.

## 2. Non-goals

- **`PipelineSpec.matrix`** at the pipeline level — not a Tekton concept.
- **Substituting matrix values back into pipeline-level `params` /
  `results`** — matrix values are scoped to one expansion's TaskRun
  only.
- **A new exit code for matrix cardinality overflow** — that's a
  validation error (exit 4), no new code needed.
- **`maxConcurrency`** on a matrix (Tekton v1.x β feature) — defer.
  We honor `--parallel` globally; per-matrix throttling is a v2 knob.
- **Auto-expanding nested arrays** (`$(params.list[*])` inside a
  matrix value list) — supported only as literal string lists in v1.
- **Element-wise reduction of matrix-fanned results** (computing a
  scalar from N expansions) — Tekton itself doesn't do this.
- **Result objects from matrix-fanned tasks** — Tekton itself
  promotes only `string` results to `array-of-strings`; `array` and
  `object` typed results from a matrix-fanned Task are an error in
  upstream and we mirror that as a validation error.

## 3. Tekton-upstream behavior we're matching

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
`include` entry = `2*2 + 1 = 5` expansions.

Per upstream, simplified to what tkn-act actually reads:

| Aspect | Behavior |
|---|---|
| **When expansion happens** | At PipelineRun start, before any task-level scheduling. Engine-side for the docker backend; controller-side for the cluster backend. |
| **Param merge** | For each expansion: pipeline-level `params` (defaults + caller-provided) ∪ `PipelineTask.params` ∪ matrix-row params. **Matrix wins on conflicts**, so `matrix.params.os` overrides any same-named `PipelineTask.params.os`. |
| **DAG edges** | Downstream `runAfter: [build]` waits on every expansion; one expansion failing makes the *original* pipeline-task fail (see Failure). |
| **Result aggregation** | Each expansion writes its own `Results` map. Tekton promotes a string-typed result `Y` from a matrix-fanned task `X` to an **array-of-strings**, indexed by expansion order, addressable as `$(tasks.X.results.Y[*])` or `$(tasks.X.results.Y[0])`. |
| **Naming** | Each expansion is a child PipelineTask whose name is `<original>-<index>` (e.g. `build-0`, `build-1`, `build-2`, `build-3`). `include` rows that set `name:` get that name (e.g. `arm-extra`); unnamed include rows fall back to `<original>-<crossSize+i>`. |
| **Failure** | Default: any expansion failing makes the *original* pipeline-task fail; downstream tasks that `runAfter` it skip. The Tekton β `failFast: false` knob is **out of scope** for v1 (see § 11). |
| **Concurrency** | Expansions are independent siblings in the DAG; the engine runs up to `--parallel` of them in flight, same rule as ordinary parallel tasks. |
| **`when` evaluation** | A `when:` block on a matrix-fanned task is evaluated **per row** after expansion. Each row sees its own matrix-row params in `$(params.<name>)` references. Rows whose `when:` is false emit one `task-skip` event under the *expansion* name (e.g. `build-1`); their `MatrixInfo` is preserved on the skip event so consumers can correlate. This matches Tekton's controller-side per-row evaluation, so the cluster backend (which submits unchanged) is byte-aligned with docker. |

## 4. Type additions

### 4.1 `tektontypes`

```go
// In internal/tektontypes/types.go.

// Matrix declares a Cartesian-product fan-out of one PipelineTask
// across one or more named string-list params. Optional `include`
// rows add named combinations on top of the cross-product.
type Matrix struct {
    Params  []MatrixParam   `json:"params,omitempty"`
    Include []MatrixInclude `json:"include,omitempty"`
}

// MatrixParam is a name + value list. Tekton requires `value` to be
// a list of strings here (no scalars, no objects).
type MatrixParam struct {
    Name  string   `json:"name"`
    Value []string `json:"value"`
}

// MatrixInclude is one extra named row. Its params override matching
// matrix-row params for that one row, and may introduce param names
// not in `matrix.params`.
type MatrixInclude struct {
    Name   string   `json:"name,omitempty"`
    Params []Param  `json:"params,omitempty"` // Param.Value is string-typed
}
```

`PipelineTask` gains `Matrix *Matrix \`json:"matrix,omitempty"\``.

### 4.2 No new types in `engine`

Expansion produces `tektontypes.PipelineTask` values reusing the
existing struct; matrix is a *transformation* on the pipeline at
DAG-build time, not a new runtime concept.

## 5. Engine algorithm (docker backend)

### 5.1 Where it slots in

In `internal/engine/engine.go::RunPipeline`, between `applyDefaults`
and the existing DAG construction:

```go
// expandMatrix replaces every PipelineTask with matrix != nil by N
// expansion children, rewriting RunAfter edges so any task that
// referenced the original waits on every expansion. No-op when no
// task in the pipeline declares matrix.
expanded, err := expandMatrix(pl, params)
if err != nil { ... }   // exit 4 (validate) — cardinality cap, type error
pl = expanded
```

`expandMatrix` is a pure function: takes the parsed `Pipeline` plus
the pipeline-level resolved `params` map, returns a transformed
`Pipeline` (or an error). It lives in
`internal/engine/matrix.go`.

### 5.2 Algorithm

1. **Walk** `pl.Spec.Tasks` and `pl.Spec.Finally`. For each `pt`
   without `Matrix`, copy unchanged. For each `pt` with `Matrix`:
   1. **Cardinality check**: `len(rows) = product(len(p.Value) for p in matrix.params) + len(matrix.include)`. Reject if `> matrixMaxRows` (see § 7).
   2. **Materialize cross-product rows**: deterministic order — same
      order params are declared in `matrix.params`, lexicographic on
      ties; row index `i` matches the order
      `pt.Name + "-" + strconv.Itoa(i)` for stable naming.
   3. **Materialize include rows**: append in declaration order.
      Their names override the `<orig>-<i>` default.
   4. **Per-row PipelineTask synthesis**: shallow-copy `pt`, set
      `pt.Name = rowName(orig, idx, includeName)`, set `pt.Matrix =
      nil`, set `pt.Params = mergeParams(pt.Params, rowParams)` (row
      params win on conflict). Append to the rewritten task list.
3. **Rewrite RunAfter edges**: any other PipelineTask whose
   `RunAfter` references the *original* name `X` is rewritten to
   reference every expansion of `X`. Same for `pl.Spec.Finally`.
4. **`when:` is preserved on every expansion** and evaluated
   per-row at task-dispatch time, not in `expandMatrix`. The
   `when:` block on the parent is shallow-copied onto each child;
   when the engine reaches that child, it evaluates `when:` against
   a resolver context that includes the row's matrix-contributed
   params. Rows whose `when:` is false emit one `task-skip` event
   per skipped expansion (under the expansion name, with
   `MatrixInfo` populated) and are marked `not-run` in `outcomes`.
   If *every* expansion of a parent is `not-run`, downstream
   `runAfter` propagation skips downstream tasks as today. See
   § 6.3.

### 5.3 Param substitution flow

Within each expansion:

- `PipelineTask.params` substitution runs as today against the
  pipeline-level resolver context (`$(params.X)`,
  `$(tasks.Y.results.Z)`, `$(context.*)`). Matrix values can
  reference these — rare but Tekton allows it; we do the substitution
  *before* expansion so the cross-product sees concrete strings.
- After expansion, the merged params (pipeline-task ∪ matrix-row) flow
  into `runOne` exactly as today. From `runOne`'s perspective, an
  expanded child looks identical to a hand-written PipelineTask.

### 5.4 Result aggregation

A new helper `aggregateMatrixResults(originalName string, expansionNames []string, results map[string]map[string]string) map[string]string`:

- For every result key `Y` produced by *any* expansion, the helper
  writes a single key `Y` into `results[originalName]` whose value is
  a **JSON-encoded array string** of the per-expansion values, in row
  order. That is the on-disk shape Tekton itself writes for
  matrix-fanned results, so the same string flows through
  `Pipeline.spec.results` extraction below.
- Aggregation runs at the moment the *last* expansion of a task
  completes — same place per-expansion `results[expansionName]` is
  written today, plus a check "is every expansion of `<original>`
  now terminal? if so, fold into `results[<original>]`."

#### 5.4.1 Resolver gap (must fix before aggregation works)

The current `internal/resolver/resolver.go::isArrayStarRef` only
recognises `$(params.<name>[*])` — it does **not** handle
`$(tasks.X.results.Y[*])`. The plan therefore lands a resolver
change first, as its own task with its own failing test:

- Failing test `TestResolveTaskResultArrayStar` in
  `internal/resolver/resolver_test.go`: input string
  `"$(tasks.build.results.images[*])"` with
  `tasks.build.results.images = ["a","b","c"]` (the JSON-array literal
  the engine writes) returns the array `["a","b","c"]` from
  `SubstituteArgs`.
- `isArrayStarRef` is widened to also match
  `$(tasks.<task>.results.<name>[*])`.
- `SubstituteArgs` (the only `[*]`-aware entry point) gains a branch
  that, when the matched ref is a task-result `[*]`, reads
  `ctx.Results[task][name]` and JSON-decodes it into `[]string`
  before splicing into the output args. Non-array values fail with a
  clear `"task result %q is not a JSON-array"` error.

#### 5.4.2 Pipeline-results path

`Pipeline.spec.results` resolution lives in
`internal/engine/pipeline_results.go::resolvePipelineResults`. For
the common case `value: $(tasks.<parent>.results.Y[*])` declared as a
`type: array` pipeline-result (the spec's example below), the
existing array branch already iterates `spec.Value.ArrayVal` calling
`resolver.Substitute` on each element — but `Substitute` is the
scalar path, not `SubstituteArgs`, so a `[*]` reference there
resolves to the JSON-array *literal string* (e.g. `'["a","b","c"]'`)
rather than three discrete strings.

To cover that, `resolvePipelineResults` is extended: when an array
pipeline-result element matches a single `$(tasks.X.results.Y[*])`
reference (the whole element, not interpolated into a larger string),
it is resolved through the new task-result `[*]` branch and its
elements are spliced into the result array in order. This keeps the
shape `RunResult.Results[<name>]` is `[]string` (per § 5.4.3 below),
NOT `string`. A unit test asserts the type is `[]string` not
`string`.

For scalar pipeline-result expressions like `value:
$(tasks.<parent>.results.Y)` (no `[*]`), the JSON-array literal is
returned as a string — matches Tekton's own on-disk shape, document
this in AGENTS.md.

#### 5.4.3 Encoding contract for `RunResult.Results`

`aggregateMatrixResults` writes a JSON-array-encoded *string* into
the per-task `results` map (`map[string]map[string]string`). The
*pipeline-results extractor* (`resolvePipelineResults`, Track 1 #6's
existing code, extended per § 5.4.2) JSON-decodes array-typed values
before populating `RunResult.Results`. The contract is therefore:

| Layer | Type |
|---|---|
| `results[parent][Y]` (engine-internal) | JSON-array literal `string` (e.g. `'["a","b","c"]'`) |
| `RunResult.Results[<pipeline-result-name>]` for an `array`-typed pipeline result fed by `[*]` | `[]string` |
| `RunResult.Results[<pipeline-result-name>]` for a scalar pipeline result whose value is a non-`[*]` reference to a matrix-fanned result | `string` (the JSON-array literal) |

A dedicated unit test
(`TestResolvePipelineResultsArrayStarFromMatrix`) asserts the
extracted value is `[]string` not `string`.

### 5.5 DAG construction

Unchanged. After step 1's rewrite, `dag.New()` sees the same flat
`PipelineTask` slice it always has — the matrix is invisible to the
DAG layer.

## 6. JSON event shape

### 6.1 Per-expansion events

Each `task-start` / `task-end` event carries:

```jsonc
{
  "kind": "task-start",
  "task": "build-1",            // expansion name (stable)
  "matrix": {
    "parent": "build",          // original PipelineTask name
    "index": 1,                 // 0-based; matches name suffix for cross-product rows
    "of":    4,                 // total expansions (cross + include)
    "params": {                 // resolved row params (string keys, string values)
      "os":        "darwin",
      "goversion": "1.21"
    }
  },
  ...
}
```

`matrix` is omitted (`omitempty`) on tasks that don't come from a
matrix expansion — events for non-matrix tasks are byte-identical to
what they are today.

### 6.2 `run-end` `results` shape

The pipeline-level `results` map (Track 1 #6) is unchanged in its
top-level shape. Matrix-fanned task results show up as the
already-supported array form:

```jsonc
{
  "kind": "run-end",
  "status": "succeeded",
  "results": {
    "binaries": ["build-linux-1.21", "build-linux-1.22",
                 "build-darwin-1.21", "build-darwin-1.22"]
  }
}
```

The `Pipeline.spec.results` declaration would be e.g.
`value: $(tasks.build.results.binary[*])` — unchanged from § 5.4.

### 6.3 `task-skip` per row

`when:` is evaluated **per row** (per § 3 and § 5.2). When an
expansion's `when:` is false, emit:

```jsonc
{
  "kind": "task-skip",
  "task": "build-1",            // EXPANSION name (e.g. build-0, build-1, arm-extra)
  "matrix": {                   // MatrixInfo preserved so consumers can correlate
    "parent": "build",
    "index": 1,
    "of":    4,
    "params": {"os": "darwin", "goversion": "1.21"}
  },
  "message": "when expression false: os != 'linux'"
}
```

Each skipped expansion produces its own `task-skip` event; the
expansion is marked `not-run` in `outcomes`. Downstream
`runAfter` propagation skips downstream tasks only if **every**
expansion is `not-run` (or otherwise non-success), matching the
existing per-task semantics. If some rows ran and others skipped,
downstream tasks see a mix of outcomes under the parent — and
`aggregateMatrixResults` writes empty strings into the parent
result array for rows that skipped.

### 6.4 Rationale for the chosen shape

We considered four options:

1. **Suffix-only**: rely on the `task` field naming convention
   (`build-1`) and document it. — Rejected: agents would have to
   parse the suffix; ambiguous when the original task name itself
   ends in `-N`.
2. **Top-level `expansion: {index, of}` field** — Rejected: doesn't
   tell the agent what `index` *means*. It would also collide
   semantically with future per-step expansion features.
3. **Nested `matrix: {parent, index, of, params}` object** —
   Selected. Self-describing. Adding fields later (e.g. `name:` for
   include-row names, or `failFast` from § 11) doesn't widen the
   `Event` struct.
4. **Per-expansion events under a single virtual `task-end` for the
   original** — Rejected: breaks the 1:1 correspondence between
   `task-start` and `task-end` events that the JSON stream contract
   guarantees. Agents that count tasks would silently miscount.

The `matrix` field is `omitempty`, so non-matrix runs are
byte-identical to today. Adding it doesn't affect the existing
parity-check fixture set.

## 7. Validation rules

In `internal/validator/validator.go`:

| Rule | Failure |
|---|---|
| `matrix.params[i].value` MUST be a string list with `len ≥ 1` | exit 4: `pipeline task %q matrix param %q must be a non-empty string list` |
| `matrix.params[i].name` MUST be unique within the matrix | exit 4: `pipeline task %q matrix declares param %q twice` |
| The Cartesian product cardinality + `len(include)` MUST be `≤ matrixMaxRows` (default 256) | exit 4: `pipeline task %q matrix would produce %d rows, exceeding the cap of %d` |
| `matrix.include[i].params[j].value.Type` MUST be `string` (not array, not object) | exit 4: `pipeline task %q matrix include %q param %q must be a string` |
| **No `include` row's params may overlap any cross-product (`matrix.params`) param name.** Upstream Tekton folds matching `include` rows into the cross-product; tkn-act always appends. To prevent silent cross-backend divergence (cluster delegates to the controller and would fold; docker would not), we reject the overlap at validate time. | exit 4: `pipeline task %q matrix include %q param %q overlaps a cross-product param; matrix.include params overlapping cross-product params are not supported in v1; see Track 1 #3 follow-up` |
| Every `include` row's `params` MUST reference a name not in `matrix.params` (extra param introduced for that row). The override flavor is gated by the rule above. | (no failure, covered by the overlap rule) |
| **Result-typing**: any Task referenced by a matrix-fanned PipelineTask whose `spec.results[i].type` is `array` or `object` is rejected — Tekton requires `string` to promote to array-of-strings. | exit 4: `pipeline task %q (matrix-fanned) references task %q whose result %q is type %q; matrix-fanned tasks may only emit string results` |
| `when:` is allowed on a matrix-fanned task; we don't reject it | (informational) |
| `Pipeline.spec.results` referencing `$(tasks.X.results.Y)` where X is matrix-fanned and the expression is **not** the array form `[*]` or `[N]` — accepted (the JSON-array-literal flows through), but a CLI warning is logged. Validator does not reject. | (no failure) |

`matrixMaxRows` lives as a package constant in the engine
(`engine.matrixMaxRows = 256`) and is referenced by the validator. We
make it a constant rather than a flag because the cap exists to
prevent foot-guns, not to gate intentionally large matrices — if a
user genuinely needs >256, the right answer is "split your pipeline,
or run on real Tekton with `--cluster`" until we add a flag.

**Validator imports.** The result-typing rule needs the resolved
referenced Task (to read its `spec.results[i].type`). The validator
already accepts a `resolvedTasks map[string]tektontypes.Task`
parameter for the `taskRef`-resolution rules; the matrix rule
reuses it. If the implementer finds the matrix block runs in a code
path that doesn't have `resolvedTasks` in scope, the fix is to
thread it explicitly into the helper rather than leaving the
type-check silently disabled. The validator test plan in § 10
includes a positive test that asserts the rule actually fires (not
just that the negative path errors).

## 8. Pretty output

A matrix-fanned task contributes one log-prefix per expansion (today's
`<task>/<step>: line` becomes `build-0/run: line`). The run summary
adds one rolled-up line *before* the per-expansion lines:

```
matrix build × 4 (os, goversion)
  build-0  os=linux  goversion=1.21    succeeded  3.2s
  build-1  os=linux  goversion=1.22    succeeded  3.4s
  build-2  os=darwin goversion=1.21    succeeded  3.1s
  build-3  os=darwin goversion=1.22    succeeded  3.5s
```

Pretty output is for humans and may change at any time; agents must
parse JSON.

## 9. Cluster backend mapping

**Strategy: option (b) — submit the matrix unchanged to Tekton; map
expanded TaskRun names back.**

In `internal/backend/cluster/run.go`:

- `BuildPipelineRunObject` already serializes `PipelineTask` via
  `json.Marshal`. Adding `Matrix` to the type means it round-trips
  natively into `pipelineSpec.tasks[].matrix` — Tekton's
  `EmbeddedTask` schema accepts the field. **No code change needed**
  in the inliner. (Regression test in Task 6 of the plan.)
- `watchPipelineRun` currently maps each `pr.status.childReferences`
  TaskRun back into `res.Tasks[<originalPipelineTaskName>]`. With
  matrix, Tekton produces N TaskRuns whose
  `metadata.labels["tekton.dev/pipelineTask"]` is the *original*
  PipelineTask name and whose `taskRun.spec.params` record the
  matrix-row params (an `tekton.dev/matrixParams` annotation also
  exists on some Tekton versions; `spec.params` is the
  version-stable carrier).
- **Index reconstruction (PRIMARY: param-hash matching).** For each
  TaskRun whose `pipelineTask` label names a matrix-fanned parent,
  compute `sha256(canonicalJSON(matrix-row params extracted from
  TaskRun.spec.params))` and match against pre-computed hashes for
  the same parent's expansion rows in the engine-side row order.
  The matching row's index becomes `MatrixInfo.Index`. The hash
  inputs are: (a) the subset of `TaskRun.spec.params` whose names
  appear in the parent's `Matrix.Params` (cross-product) or in any
  `Matrix.Include[*].Params` (include extras), (b) sorted by name,
  (c) JSON-encoded with sorted keys. This is version-independent.
- **Index reconstruction (FALLBACK: childReferences order).** If
  param-hash matching produces zero hits (e.g. a future Tekton
  version drops `spec.params` from the inlined TaskRun), fall back
  to `pr.status.childReferences[i].name` order — TaskRuns appear in
  the controller's row-emission order. Log a one-line warning at
  `EvtError` severity ("matrix index reconstruction fell back to
  childReferences ordering — please file an issue with your Tekton
  version") so we notice the regression.
- **Probe in cluster-integration.** The cluster-integration workflow
  pins one Tekton version today; Plan Task 6 step 5 adds a matrix
  probe job that runs the `matrix` fixture against **two pinned
  Tekton minor versions** (the current pin and one older minor) and
  asserts both runs reconstruct identical `MatrixInfo.Index`
  assignments. This guards against a future Tekton release breaking
  the param-hash assumption silently.
- The cluster backend then emits per-expansion `task-start` /
  `task-end` events with the same `matrix: {parent, index, of,
  params}` payload the docker backend produces — see
  `emitClusterTaskEvents` in `engine.go`. The `BackendOnCluster`
  type gets a new `Matrix *MatrixOutcome` field carrying parent +
  index + params; the engine forwards it onto the event.
- Result aggregation: Tekton's controller already populates
  `pr.status.results` array-of-strings for matrix-fanned tasks. The
  existing `extractPipelineResults` in `run.go` (added in v1.5)
  reads these as-is. **Cross-backend parity for `Pipeline.spec.results`
  is automatic.**

The naming map (engine's `build-0` ↔ Tekton's
`<pipelineRun>-build-<random8>`) is internal to the cluster backend's
event emitter; agents only see the *engine-side* names in the JSON
stream, just like today's non-matrix tasks.

### Why not (a) submit N PipelineRuns, or (c) expand client-side and
submit one PipelineRun?

- **(a)** is wrong: matrix is a PipelineRun-internal concept; Tekton
  expects a single PipelineRun with N TaskRuns under it. Submitting
  N PipelineRuns would lose the DAG-level shared-workspace and
  `runAfter` semantics across expansions.
- **(c)** would work, but doubles the source of truth: the docker
  backend already expands client-side, and re-expanding for the
  cluster backend means two different code paths could disagree on
  cardinality / row order / param merge. The whole point of (b) is
  to delegate to Tekton's controller, which has its own well-tested
  expansion logic, and just translate the *result* back into our
  event shape.

## 10. Test plan

### 10.1 Unit tests

| File | Test cases |
|---|---|
| `internal/resolver/resolver_test.go` | `TestResolveTaskResultArrayStar`: input `"$(tasks.build.results.images[*])"` with `tasks.build.results.images = '["a","b","c"]'` (JSON-array literal) returns `["a","b","c"]` from `SubstituteArgs`; non-array result string returns a clear error. |
| `internal/tektontypes/types_test.go` | YAML round-trip for `matrix`, `matrix.include`. |
| `internal/engine/matrix_test.go` (new) | `expandMatrix` cross-product (1×N, M×N, M×N×P), `include` with name and without, param-merge precedence (matrix > pt.Params > task default), cardinality cap, deterministic row ordering, `RunAfter` rewrite for downstream tasks, finally-task rewrite, **per-row `when:` evaluation (one skip per skipped row, others run)**, nil-matrix is no-op. |
| `internal/engine/matrix_test.go` | `aggregateMatrixResults`: string promotion to JSON-array literal, missing result on one expansion (empty string in slot), all-failed expansion. |
| `internal/engine/pipeline_results_test.go` | `TestResolvePipelineResultsArrayStarFromMatrix`: a `Pipeline.spec.results` of `type: array` with element `$(tasks.build.results.tag[*])` resolves to a value of Go type `[]string` (NOT `string`) on `RunResult.Results[<name>]`. |
| `internal/engine/engine_test.go` (extension) | End-to-end through `RunPipeline` with a fake backend: 4 expansions, downstream task waits on all 4, one expansion fails ⇒ downstream skips, `task-end` events carry `matrix:{parent,index,of,params}`. |
| `internal/validator/validator_test.go` | Every rejection path in § 7 including the **include-overlaps-cross-product rule** (Critical 2 fix); the cap; the result-type rejection; the include-row string-only rule; positive test that result-typing rule fires (so a future refactor that drops `resolvedTasks` from scope doesn't silently disable it). |
| `internal/reporter/reporter_test.go` | `Event.Matrix` round-trips through JSON marshalling; `omitempty` keeps non-matrix events byte-identical to today. |
| `internal/backend/cluster/runpipeline_test.go` | Inlined PR has `pipelineSpec.tasks[].matrix` intact (regression-locking test). |
| `internal/backend/cluster/run_test.go` | A faked PR with 4 child TaskRuns (same `pipelineTaskName=build`, distinct `matrixParams`) is mapped to 4 `BackendOnCluster` entries with correct `Matrix.Parent` / `Matrix.Index` / `Matrix.Params` via param-hash matching; a second test asserts the childReferences-order fallback fires when `spec.params` is empty and emits the `EvtError` warning. |

### 10.2 e2e fixture (cross-backend)

`testdata/e2e/matrix/pipeline.yaml`:

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
          - { name: os,        value: [linux, darwin] }
          - { name: goversion, value: ["1.21", "1.22"] }
```

Fixture descriptor in `internal/e2e/fixtures/fixtures.go`:

```go
{
    Dir: "matrix", Pipeline: "matrix", WantStatus: "succeeded",
    WantResults: map[string]any{
        "tags": []any{"linux-1.21", "linux-1.22", "darwin-1.21", "darwin-1.22"},
    },
},
```

Both harnesses iterate `fixtures.All()`, so this runs on docker AND
cluster automatically. The cross-backend assertion is "same Status,
same Results map (under `ResultsEqual`'s normalization)."

A second fixture `testdata/e2e/matrix-include/pipeline.yaml` exercises
`include` with one named row and one unnamed row, asserts the
unnamed row gets the default `<parent>-<idx>` name and the named row
gets its declared name in `task-start` events.

### 10.2.1 Limitations fixture (Critical 2 documentation)

`testdata/limitations/matrix-include-overlap/pipeline.yaml`: a
matrix whose `include[0].params` overlaps a `matrix.params` name
(e.g. `matrix.params.os = [linux, darwin]` plus `include[0].params:
[{name: os, value: linux}, {name: arch, value: arm64}]`). The
fixture's header comment explains: real Tekton would *fold* the
include row into the matching cross-product row (making it a 2-row,
not 3-row, expansion); tkn-act rejects this at validate time
(exit 4) until v2 implements the fold. Loaded by
`internal/loader/limitations_test.go` to confirm parse-cleanly; the
validator-rejection test lives in
`internal/validator/validator_test.go`.

### 10.3 Parity scoreboard

`docs/feature-parity.md` row update:

```diff
-| `PipelineTask.matrix` (parameter-matrix fan-out) | `PipelineTask.matrix` | gap | both | none | none | docs/short-term-goals.md (Track 1 #3) |
+| `PipelineTask.matrix` (parameter-matrix fan-out) | `PipelineTask.matrix` | shipped | both | matrix | matrix-include-overlap | docs/superpowers/plans/2026-05-04-pipeline-matrix.md (Track 1 #3); also covered by `matrix-include`. Limitation: include rows overlapping cross-product params are validator-rejected (cluster would fold; docker would not). |
```

Per `parity-check.sh` invariant 2, a `shipped` row's limitations
column should be `none`. The exception here: the *feature ships*,
but a documented sub-corner (include-overlap) is rejected at
validation rather than supported. The limitations fixture exists
to keep the example loadable so the loader test catches YAML
breakage; the parity-check rule is satisfied by setting the
limitations cell to `none` and pointing to the loader/validator
tests instead. Implementer: confirm the script's exact behavior
before merging — if invariant 2 is strict, drop the limitations
cell to `none` and move the explanation to the AGENTS.md matrix
section.

`bash .github/scripts/parity-check.sh` MUST pass at every commit
boundary in the implementing PR.

## 11. Documentation updates required

Atomic with the implementing PR:

- `AGENTS.md` — new section "Matrix fan-out" between
  `stepTemplate` and `Timeout disambiguation`. Must document, at
  minimum:
  - **Row naming.** The `name` field on `task-start` / `task-end` /
    `task-skip` events always carries the per-expansion stable name
    (e.g. `build-1`, `arm-extra`); consumers reading `Event.Task`
    today keep working unchanged. The new field is `matrix.parent`
    (the original PipelineTask name).
  - **The 256-row cardinality cap** (so users don't hunt for a
    flag); the cap is hardcoded, exit 4 on overflow, no env var.
  - **The `matrix:` event payload shape** (`{parent, index, of,
    params}`).
  - **Per-row `when:` evaluation** (one `task-skip` per skipped
    row, others run).
  - **Result-array surfacing** via `$(tasks.X.results.Y[*])`.
  - **`include` overlap is rejected** (Critical 2) with a pointer
    to the limitations fixture.
- `cmd/tkn-act/agentguide_data.md` — mirror via `go generate`.
- `README.md` — one-line bullet under "Tekton features supported."
- `docs/feature-parity.md` — flip the row.
- `docs/short-term-goals.md` — mark Track 1 #3 done.
- `docs/test-coverage.md` — list `matrix/` and `matrix-include/`
  under `### -tags integration`; list
  `matrix-include-overlap/` under the limitations table.

## 12. Open questions

- **Cardinality cap default.** 256 is a starting point, not a
  measured ceiling. If real-world pipelines want >256 we'll add
  `--matrix-max-rows` (CLI flag) and document an env var. Not
  shipping that flag in v1 keeps the surface area small.
- **`failFast: false` (don't cancel sibling expansions when one
  fails).** Tekton β feature. Not commonly used. Defer.
- **Substituting matrix params in a Task's `displayName` (when we
  add Track 1 #7).** Today nothing reads `displayName`; revisit
  when that lands.
- **`matrix.include` augmentation flavor.** Upstream Tekton folds
  include rows whose params match an existing cross-product row into
  that row (instead of appending). v1 of tkn-act **rejects the
  overlap at validate time** to keep cross-backend fidelity (option
  (b) from the reviewer matrix); a `testdata/limitations/matrix-include-overlap/`
  fixture documents the divergence by example. If real pipelines
  need the augmentation flavor, the follow-up is to implement the
  fold in `expandMatrix` (option (a)) and graduate the limitations
  fixture; tracked here for v2.

### 12.1 Resolved during review

- **`matrix.include` interaction with the cross-product.** RESOLVED
  (option (b)): validator rejects overlap, limitations fixture
  documents it. See § 7 and § 11.
- **`when:` per-row vs per-parent.** RESOLVED (option (a)): docker
  evaluates `when:` per row, emitting one `task-skip` per
  skipped expansion. Cluster mode delegates to Tekton, which is also
  per-row. See § 6.3.
- **Cluster expansion-index mapping.** RESOLVED: PRIMARY strategy is
  deterministic `sha256(canonicalJSON(matrix-row params))` matched
  against each `TaskRun.spec.params`; FALLBACK is
  `pr.status.childReferences[i].name` ordering. Plan Task 6 includes
  a probe in `cluster-integration.yml` against two Tekton minor
  versions to confirm both strategies agree. See § 9.

## 13. Out of scope (don't do here)

- Sidecars (Track 1 #1).
- `displayName` / `description` (Track 1 #7).
- StepActions (Track 1 #8).
- Resolvers (Track 1 #9).
- A new exit code for matrix expansion failure — folded into
  exit 4 (validate) and exit 5 (pipeline) per existing semantics.
