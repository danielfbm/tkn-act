# `displayName` and `description` for Task / Pipeline / Step — Design

**Date:** 2026-05-04
**Track item:** Track 1 #7 of `docs/short-term-goals.md`
**Parity row:** "Task structure → `displayName` / `description` on Task / Pipeline / Step" in `docs/feature-parity.md`
**Status when written:** `gap` → target `shipped` on both backends in v1.5.

## 1. Goal

Honor Tekton's optional human-readable metadata fields — `displayName` and
`description` — on `Task`, `Pipeline`, `PipelineTask` (entries under
`Pipeline.spec.tasks` and `Pipeline.spec.finally`), and `Step`. Surface
them through tkn-act's two stable consumer surfaces:

1. The JSON event stream (`tkn-act run -o json`).
2. The pretty (human) renderer.

These are pure UX fields. They never affect scheduling, exit codes, or
status. The user-visible win is twofold:

- Pretty output becomes more informative — a Task named `t1` whose
  `displayName` is `"Build & test"` renders as the latter.
- Agents reading the JSON event stream can attribute events to a
  human-named pipeline / task / step without round-tripping the original
  YAML.

## 2. Non-goals

- **Substitution into `displayName` / `description`.** Tekton allows
  `$(params.X)` inside `Pipeline.spec.tasks[].displayName`. tkn-act will
  pass the literal string through unmodified for v1.5. (The `resolver`
  package isn't reached for these fields and the engine won't call it.)
  Documented as a follow-up gap in this spec; not in scope here.
- **`displayName` on a single Step's `result` (Tekton has
  `Step.results[].name` only).** Out of scope.
- **A new exit code or status for missing `displayName`.** None — empty
  is fine; agents fall back to `name`.
- **Pretty-output reformatting.** We retain the existing layout; only
  the *label* of each Task/Step/Pipeline line changes when a
  `displayName` is set. No new bracketing, no new lines.
- **`description` on `Pipeline.spec.tasks[]` / `Pipeline.spec.finally[]`.**
  Tekton's `PipelineTask` schema does **not** define a `description`
  field — only `displayName`. We mirror Tekton: PipelineTask gets
  `displayName` only.
- **`description` on `Step`.** Tekton 0.50+ added `Step.description`.
  We DO support it (in scope) — see §3 — because it's part of the
  newer Tekton schema and round-trips for free.

## 3. Exact list of fields added

Mirror Tekton v1 verbatim (JSON tag = upstream field name in camelCase;
the JSON event field is snake_case — see §4):

| Type | Field added | YAML JSON tag | Notes |
|---|---|---|---|
| `tektontypes.PipelineSpec` | `DisplayName string` | `displayName,omitempty` | Top-level human label for the Pipeline. |
| `tektontypes.PipelineSpec` | (`Description` already present) | — | Existing field; surfaced now. |
| `tektontypes.PipelineTask` | `DisplayName string` | `displayName,omitempty` | Per-task human label inside `spec.tasks` and `spec.finally`. Tekton v1 schema; **no** `Description` field on PipelineTask. |
| `tektontypes.TaskSpec` | `DisplayName string` | `displayName,omitempty` | Top-level human label for the referenced Task. (`Description` already present.) |
| `tektontypes.Step` | `DisplayName string` | `displayName,omitempty` | Per-step human label. |
| `tektontypes.Step` | `Description string` | `description,omitempty` | Per-step description (Tekton 0.50+). |
| `tektontypes.StepTemplate` | (none) | — | `displayName` / `description` on the template don't make sense — Step `displayName` is intrinsically per-step (one Task's `stepTemplate` can't supply a meaningful name for every Step). Out of scope. |

Existing `Description` fields on `TaskSpec`, `PipelineSpec`, `ParamSpec`,
`ResultSpec`, `WorkspaceDecl`, `PipelineWorkspaceDecl`, and
`PipelineResultSpec` are unchanged in shape. They become observable on
the JSON event stream where the `pipeline` and `task` carry top-level
metadata; the per-param / per-result / per-workspace `description`
fields stay on the YAML and aren't surfaced as event fields (out of
scope; not user-blocking).

## 4. JSON event field names — **snake_case**

The existing `Event` struct already mixes camelCase JSON tags
(`runId`, `exitCode`, `durationMs`) and lower-case-no-separator
(`pipeline`, `task`, `step`). For new fields we pick **snake_case** for
two reasons:

1. The user's request explicitly asks for snake_case ("snake_case to
   match existing event JSON" — for the *new* fields).
2. We've been doing this consistently for the most-recent v1.3/v1.4/v1.5
   additions (e.g. `results` on `run-end`, which is single-word so
   case is moot, but the engine-side `RunResult.Results` uses Go-style
   names; the `task-retry` event added in v1.3 used a single-word
   `attempt` likewise). Adopting snake_case for the new multi-word
   fields keeps a defensible rule going forward: "new event fields are
   snake_case." Existing camelCase fields (`runId`, `exitCode`,
   `durationMs`) are frozen by the public-contract rule and are NOT
   renamed.

This rule is documented explicitly in `AGENTS.md` as part of the
docs-update task in the plan: *"JSON event field naming: existing
fields are camelCase (`runId`, `exitCode`, `durationMs`) and remain so
for backward compatibility. New event fields added from v1.5 onward
use snake_case (`display_name`, `description`)."*

New `reporter.Event` fields:

```go
type Event struct {
    // ... existing fields unchanged ...

    // DisplayName is the human-readable label for the entity this event
    // describes. Set on:
    //   - run-start, run-end:    Pipeline.spec.displayName
    //   - task-start, task-end,
    //     task-skip, task-retry: PipelineTask.displayName
    //   - step-log:              Step.displayName
    // Empty when the source YAML didn't set a displayName. Agents
    // should fall back to the corresponding `pipeline` / `task` / `step`
    // name field when this is empty.
    DisplayName string `json:"display_name,omitempty"`

    // Description carries:
    //   - run-start: Pipeline.spec.description
    //   - task-start: TaskSpec.description (the Task referenced by
    //     PipelineTask.taskRef, OR the inline taskSpec if PipelineTask.taskSpec is set)
    // Omitted from terminal events (run-end / task-end) to keep the
    // line size down — the start event already carried it.
    Description string `json:"description,omitempty"`
}
```

Per-event coverage (which fields each event kind carries):

| Event kind     | `display_name` | `description` |
|---|---|---|
| `run-start`    | Pipeline-level | Pipeline-level |
| `run-end`      | Pipeline-level | — (already on run-start) |
| `task-start`   | PipelineTask-level | TaskSpec-level (resolved Task) |
| `task-end`     | PipelineTask-level | — |
| `task-skip`    | PipelineTask-level | — |
| `task-retry`   | PipelineTask-level | — |
| `step-log`     | Step-level | — |
| `error`        | — | — |

**Note on `step-start` / `step-end`:** these event kinds are *defined*
in `internal/reporter/event.go` (`EvtStepStart`, `EvtStepEnd`) and
*rendered* by `internal/reporter/pretty.go`, but **no production code
emits them today** (verified via
`grep -rn 'EvtStepStart\|EvtStepEnd' internal/ | grep -v _test.go` —
only the enum declaration and the pretty switch are matched). Adding
emission would expand the public JSON contract beyond the UX scope of
Track 1 #7. We therefore deliberately limit step-level event surfacing
to `step-log` for v1.5. The Step's `displayName` and `description` are
still consumed in two places:

- **Pretty output** — `prefixOf(task, step)` uses the step's
  `displayName` when rendering log-line prefixes (see §5).
- **`step-log` event** — carries the Step's `displayName` (but not
  `description`, which would balloon every line of streamed output).

If a future change adds emission of `EvtStepStart` / `EvtStepEnd`, that
is a public-contract addition that **must** be called out explicitly in
`AGENTS.md` and is **out of scope for this plan**. Those events would
then be expected to carry `display_name` (both) and `description`
(start only), per the same rule as `task-start`.

## 5. Pretty output rules

When both `name` and `displayName` are present, **`displayName` wins**.
Format: existing line, but the leading identifier is `displayName` (or
`name` when `displayName` is empty). The original `name` is not shown
parenthetically — that doubles the line width for no agent-visible
benefit; users who want the YAML name can inspect the JSON.

Concretely (using `internal/reporter/pretty.go` glyph conventions):

```
▶ Build & test pipeline                                # was: ▶ build-test
▸ Run unit tests                                       # was: ▸ unit-tests   (verbose only)
  · Run unit tests/Compile started                     # was: · unit-tests/compile started   (verbose only)
  Run unit tests/Compile │ ...                         # log line prefix
✓ Run unit tests  (12s)                                # was: ✓ unit-tests  (12s)
```

The `prefixOf(task, step)` helper used for log-line prefixes
(`<task>/<step>`) consults `displayName` first for both halves; this
keeps logs readable and lets a user grep for the same identifier they
saw in the run-end summary.

`description` is **not** rendered in pretty output by default. It's
informational metadata for agents; surfacing it inline would push step
logs off-screen. (We may add a `tkn-act describe -f pipeline.yaml`
introspection command later — out of scope for this plan.)

Quiet (`-q`) mode is unchanged: the only renderable output is task /
run summaries, both of which respect `displayName` per the rule above.

## 6. Backend strategy

### 6.1 Docker backend (default)

Pure type-and-plumb: read the new fields from the loaded YAML, propagate
them through `engine.runOne` / `RunPipeline` to the reporter. No
behavior change to scheduling, substitution, retries, timeouts, or
volumes.

The engine and backends emit events — `run-start`, `run-end`,
`task-start`, `task-end`, `task-skip`, `task-retry`, `step-log`. Each
of these call sites already has access to:

- The Pipeline (`pl tektontypes.Pipeline`) — for `pl.Spec.DisplayName` /
  `pl.Spec.Description` on `run-start`/`run-end`.
- The PipelineTask (`pt tektontypes.PipelineTask`) — for
  `pt.DisplayName` on `task-*` events. The resolved `TaskSpec` is in
  scope via `lookupTaskSpec(in.Bundle, pt)` (already called per task);
  re-use to grab `TaskSpec.Description`.
- Each Step — only `step-log` is emitted today (verified above). The
  `LogSink.StepLog` interface signature must be extended to accept the
  step's `displayName`. The **only** two implementation-side callers
  are:
  - `internal/backend/docker/docker.go:386` (docker scanner-loop callsite)
  - `internal/backend/cluster/run.go:540` (cluster log-streaming callsite)

  Both must be updated in lockstep with the interface change. There is
  one consumer (the `reporter.LogSink` adapter at
  `internal/reporter/event.go:71`) that maps `StepLog` to an
  `EvtStepLog` event; it gets the new parameter and wires it into
  `Event.DisplayName`. Step `description` is **not** plumbed through
  `step-log` (would balloon every log line; see §4).

### 6.2 Cluster backend

Tekton's controller preserves these fields in two places:

1. **Inlined `pipelineSpec`** on the PipelineRun: when tkn-act builds
   the PipelineRun via `BuildPipelineRunObject` (cluster mode inlines
   the pipeline + tasks under `spec.pipelineSpec`), the entire
   `displayName` / `description` round-trips through the existing JSON
   marshal path with no code change. (Same as `stepTemplate` —
   verified in v1.4.)
2. **`PipelineRun.status`**: Tekton's status subresource preserves
   `pipelineSpec.displayName`, `pipelineSpec.tasks[].displayName`, and
   `taskSpec.displayName` because the status is materialised from the
   spec the controller observed. The `task-start` / `task-end`
   (and `task-retry`) events tkn-act synthesises in cluster mode (via
   `emitClusterTaskEvents`) need their `display_name` from the same
   source the docker engine uses — i.e., the **input bundle**, not the
   controller's verdict. This avoids a second source-of-truth bug.
   `step-log` events on the cluster backend are produced by
   `internal/backend/cluster/run.go:540` and pick up the step's
   `displayName` via the same extended `LogSink.StepLog` signature
   used by the docker backend.

**Verification step (cluster pass-through):** the plan adds a unit
test (`internal/backend/cluster/runpipeline_test.go::TestBuildPipelineRunRoundTripsDisplayName`)
that constructs a Pipeline + Task with all four `displayName` /
`description` field placements, calls `BuildPipelineRunObject`, parses
the resulting unstructured object, and asserts every field appears
under the inlined `spec.pipelineSpec.*` paths. This locks in that a
future hand-written conversion can't silently drop them. The
cluster-integration e2e fixture additionally asserts the **event
stream** shape on cluster mode matches docker mode — which is the
acceptance criterion for "cross-backend fidelity."

If a future Tekton release changes how it preserves these fields in
the inlined spec, the unit test catches the regression at PR time, not
at user-runtime.

### 6.3 Cross-backend invariant

For every event kind in the table in §4, the JSON output of `tkn-act
run -o json` MUST be identical between docker and cluster modes for
the `display_name` and `description` fields. This is enforced by the
existing `internal/e2e/fixtures.All()` table — the new
`display-name-description` fixture has both backends running the same
pipeline and the harness runs an event-stream-shape assertion.

Both harnesses (`internal/e2e/e2e_test.go` and
`internal/clustere2e/cluster_e2e_test.go`) already wire a recording
`captureSink` reporter via `reporter.NewTee(jsonRep, cap)` and an
`assertEventShape(t, fixture, events)` helper. To assert the new
event fields per fixture, the `Fixture` struct needs a new
`WantEventFields` map (shape: `kind -> {key -> expected}`); the two
existing `assertEventShape` helpers each get one extra block that
walks the captured event list, looks up the first event of each named
kind, and asserts each named JSON key equals the expected value. This
is a small, surgical extension of an existing path — no new harness
infrastructure. See plan Task 6 (the new task that lands BEFORE the
display-name fixture).

## 7. Validation

`internal/validator/validator.go` does not need a new rule. `displayName`
and `description` are free-form strings; an empty string is the same as
absent. The only thing worth checking is that they parse as strings
(not arrays / objects) in YAML — but that's enforced by the Go
struct's `string` typing during unmarshal; a wrong shape fails with a
parse error (exit 4) before validation even runs.

If a future user requests length limits, that's a follow-up; Tekton
itself doesn't impose any.

## 8. Test plan

**Unit tests** (Go, no Docker / no k3d):
- `internal/tektontypes/types_test.go` — YAML round-trip for every new
  field, individually and combined.
- `internal/engine/engine_test.go` (or new `display_name_test.go`) —
  inject a recording Reporter, run a pipeline whose Pipeline / Tasks /
  Steps all carry `displayName`, assert the right events received the
  right `DisplayName` / `Description` strings.
- `internal/backend/cluster/runpipeline_test.go` — fake-dynamic-client
  test asserts every `displayName` / `description` field round-trips
  through `BuildPipelineRunObject`.
- `internal/reporter/reporter_test.go` — JSON encoding assertion: an
  Event with `DisplayName` / `Description` set marshals to the
  documented snake_case keys.
- `internal/reporter/pretty_test.go` — pretty renderer prefers
  `displayName` over `name`.

**Cross-backend e2e fixture:** `testdata/e2e/display-name-description/`.
- A Pipeline with `displayName` + `description`.
- One main task with `displayName` whose `taskRef` resolves to a Task
  with its own `displayName` + `description`, and whose Steps have
  `displayName` + `description`.
- Pipeline runs to `succeeded` (the test isn't about behavior — it's
  about field surfacing).
- Added to `internal/e2e/fixtures.All()` with `WantStatus: "succeeded"`
  and a new `WantEventFields` map.
- Per-fixture extra assertion (extending the *existing* `assertEventShape`
  helpers in both `internal/e2e/e2e_test.go` and
  `internal/clustere2e/cluster_e2e_test.go`, parallel to how the
  per-fixture `Retries` check is wired): assert the `run-start` event
  carries `display_name=...` and `description=...`, the `task-start`
  carries `display_name=...` (and a `description` from the resolved
  TaskSpec), and a `step-log` event carries `display_name=...`. We do
  NOT assert on `step-start` / `step-end` — those events are not
  emitted today (see §4 note).

**Parity row update** — flip `displayName / description` from `gap` to
`shipped`, point at the new fixture, update the plan link.

## 9. Documentation updates required (in the same PR)

Per the docs-sync rule in `CLAUDE.md`:

| Doc | Change |
|---|---|
| `AGENTS.md` | New section "`displayName` / `description`" before "Timeout disambiguation," documenting which event kinds carry which field, and the snake_case key names. Also add a one-paragraph "JSON event field naming" note codifying the **forward-going rule**: existing fields are camelCase (`runId`, `exitCode`, `durationMs`) and remain so for backward compatibility; new event fields added from v1.5 onward use snake_case (`display_name`, `description`). Re-run `go generate ./cmd/tkn-act/` so `cmd/tkn-act/agentguide_data.md` mirrors. |
| `cmd/tkn-act/agentguide_data.md` | Mirror of `AGENTS.md` (regenerated). |
| `README.md` | One bullet under "Tekton features supported": "`displayName` / `description` on Task / Pipeline / PipelineTask / Step — surfaced on the JSON event stream and the pretty renderer." |
| `docs/test-coverage.md` | Add `display-name-description/` row under `### -tags integration` AND `### -tags cluster`. |
| `docs/short-term-goals.md` | Track 1 #7 row Status flips to "Done in v1.5 (PR for `feat: displayName + description`)." Track 2 #5 (`displayName` parity) flips to done in the same edit. |
| `docs/feature-parity.md` | Row "displayName / description on Task / Pipeline / Step" flips `gap` → `shipped`, e2e fixture column gets `display-name-description`, plan column points at this spec + the plan. |

## 10. Out of scope (gotchas to call out explicitly so they don't leak in)

- Substitution inside `displayName` / `description`. Pass through
  literal. Document as a known limitation.
- `description` on a Pipeline `Result` / `Param` / `Workspace` — these
  fields already exist on the structs (per `internal/tektontypes/types.go`)
  but aren't surfaced on the event stream. Out of scope; not
  user-blocking.
- A `tkn-act describe` introspection command. Tempting once `description`
  is plumbed, but out of scope for this row.
- StepTemplate-level `displayName` (each Step still gets its own).
- A new event kind. We extend existing events; we do not add a
  `metadata` event.
- **Emitting `EvtStepStart` / `EvtStepEnd`.** These enum values exist
  but no production code emits them today. Adding emission would
  expand the public JSON contract beyond the UX scope of Track 1 #7
  and would require a separate AGENTS.md call-out. Out of scope.
- Renaming any existing camelCase event field (`runId`, `exitCode`,
  `durationMs`) — public-contract rule.

## 11. Risks

- **Test churn from new event fields.** Several existing tests inspect
  Event JSON; adding two new optional fields with `omitempty` is
  backwards-compatible, but tests that compare against a full
  serialised object need to be tolerant. Mitigation: keep new fields
  `omitempty`; add tests assert-by-key, not whole-object compare.
- **Cluster backend silently dropping `displayName`** if a future
  Tekton minor version changes how it preserves the spec. Mitigation:
  unit test on `BuildPipelineRunObject` round-trip + cross-backend e2e
  assertion.
- **Pretty output line-length regression** for users with very long
  `displayName` strings. Mitigation: leave as-is for v1.5; if a real
  pipeline emits one that overflows we can add ellipsis later.

## 12. Acceptance

- All new fields parse from YAML and round-trip through the cluster
  backend's PipelineRun inlining.
- Every event kind in §4 carries the right `display_name` / `description`
  on both backends (tested via the `display-name-description` fixture
  in `internal/e2e/fixtures.All()`).
- Pretty output prefers `displayName` over `name` everywhere a Task /
  Pipeline / Step name was previously printed.
- Parity row flipped to `shipped`; `parity-check` and `tests-required`
  CI gates pass.
- Coverage gate doesn't drop on any package.
