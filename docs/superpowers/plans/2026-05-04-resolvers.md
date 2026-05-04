# Resolvers (Track 1 #9) — phased implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Honor Tekton's `taskRef.resolver` and `pipelineRef.resolver` blocks across two operating modes (direct: tkn-act fetches; remote: tkn-act submits a Tekton `ResolutionRequest` to a real cluster), with lazy resolution at task-dispatch time so `resolver.params` can reference upstream-task results. Implements `docs/superpowers/specs/2026-05-04-resolvers-design.md`.

**Architecture:** A new `internal/refresolver` package holds the resolver registry, per-resolver implementations, the `ResolutionRequest` driver, and the on-disk cache. The engine grows a lazy-dispatch path: at task-dispatch time it substitutes `resolver.params` against the current resolver context, computes a content-addressed cache key (computed *after* substitution — see spec §3 cache-key invariant), calls into `refresolver.Registry`, validates the returned bytes via `validator.ValidateTaskSpec`, and feeds the inlined `TaskSpec` into the existing run-one path unchanged. **Both backends share this path:** the docker backend's `runOne` and the cluster backend's `runViaPipelineBackend` each call `lookupTaskSpecLazy` and then proceed with their respective dispatch shapes (per-task containers vs. one PipelineRun per dispatch level). The cluster backend specifically inlines the resolved `TaskSpec` into the submitted PipelineRun via the existing `inlineTaskSpec` path, because local k3d has no resolver credentials and can't fetch on its own. **One Pipeline-level exception:** the top-level `pipelineRef.resolver` is resolved *eagerly at load time* before DAG build, since a top-level reference cannot legally substitute upstream task results.

**Tech Stack:** Go 1.25. New deps: `github.com/go-git/go-git/v5` (git resolver), `github.com/google/go-containerregistry` (bundles resolver). The `cluster` and `remote` resolvers reuse the existing `k8s.io/client-go` dynamic client already used by the cluster backend. Reuses `internal/loader`, `internal/validator`, the existing `internal/resolver` (substitution — note the name collision; the new package is **`refresolver`** to avoid renaming), `internal/e2e/fixtures`, and the existing `parity-check` + `tests-required` + `coverage` CI gates.

**Why this is multi-PR:** Each phase is independently mergeable, ships a coherent slice of user-visible behavior, and exits CI green on its own. Each phase ends with a "ship as a separate PR" checkpoint. Phases 2-4 (the per-resolver implementations) are *parallelizable* — see the "Parallelism" note in §0 below.

---

## 0. Track 1 #9 context, naming, and parallelism

This closes Track 1 #9 of `docs/short-term-goals.md`. The Status column says: *"Not started. v1 spec explicitly punted; needs its own spec."* The spec lives at `docs/superpowers/specs/2026-05-04-resolvers-design.md`. Read it before starting any phase.

### Naming

`internal/resolver/` already exists for `$(...)` variable substitution. The new package is `internal/refresolver/` (for "resource-reference resolver"). The naming choice and rationale are documented in:

- `internal/refresolver/doc.go` (created in Phase 1)
- A new "Resolvers" section in `AGENTS.md` (added in Phase 6)

Future readers should not conflate the two packages.

### Parallelism

After Phase 1 ships, the per-resolver phases (2, 3, 4) operate on independent files (`internal/refresolver/git.go`, `hub.go`, `http.go`, `bundles.go`, `cluster.go`) and only touch the `Registry` constructor. They can be implemented and reviewed in parallel by separate agents/PRs. Phase 5 (remote) is also independent of 2-4. Phase 6 (offline + caching polish) consolidates everything and must land last.

Recommended sequencing:

```
Phase 1 ───┬─→ Phase 2 (git) ──┐
           ├─→ Phase 3 (hub+http) ┐
           ├─→ Phase 4 (bundles+cluster)
           ├─→ Phase 5 (remote)  ┘
           └────────────────────→ Phase 6 (offline + polish)
```

### Out of scope for the entire plan

- StepActions (`Step.ref`) — Track 1 #8, separate plan.
- Sigstore/cosign verification of bundles — follow-up spec.
- A `tkn-act resolve` standalone subcommand — deferred to v1.7 per spec §17.
- Custom resolver names in *direct* mode — only the in-scope five are dispatchable; remote mode forwards arbitrary names. (The validator-side allowance for arbitrary names lands in Phase 1 Task 8 so the validator's behavior is correct from day one; Phase 5 just flips the `RemoteResolverEnabled` Option to `true` when `--remote-resolver-context` is set.)
- Pipeline-result feedback into resolver params — Tekton itself doesn't do this and `Pipeline.spec.results` is computed only at run-end.

---

## Phase 1 — Types, loader, engine lazy-dispatch scaffolding

**Goal:** Land all type and engine plumbing for resolver-backed `taskRef` / `pipelineRef`, including the lazy-dispatch wiring, but without any actual resolver implementation. After this phase, a Pipeline that uses `resolver: inline` (a magic name we register first as a no-op resolver returning bytes pre-loaded into the bundle by the test harness) runs end-to-end. Any other `resolver:` block fails with a clear error: `refresolver: resolver "git" not registered`.

This phase is the engine work. It must merge first because every later phase depends on the `Registry` interface and the lazy-dispatch hook in `runOne`.

**Ship as:** one PR, titled `feat(resolvers): types + lazy dispatch scaffolding (Track 1 #9, Phase 1)`.

### Phase 1 task ordering (revised)

1. Types — `Resolver` / `ResolverParams` on `TaskRef` / `PipelineRef`.
2. Loader — surface `HasUnresolvedRefs` for diagnostics.
3. `refresolver` package — `Registry` + `Resolver` interface scaffolding.
4. **Implicit DAG edges from result references** (NEW PREREQUISITE — see Critical 1). Baseline implicit-edge inference for normal `pt.Params`, then the same walker extended to `resolver.params`.
5. Lazy-dispatch hook in the engine (`runOne` docker path).
6. **Eager top-level `pipelineRef.resolver` resolution at load time** (NEW — see Critical 3).
7. **Cluster backend: lazy-resolve + inline before submission** (NEW — see Critical 4).
8. Validator handles unresolved refs (now also includes "any resolver name valid in remote mode" — moved up from Phase 5).
9. New `resolver-start` / `resolver-end` event kinds (with `omitempty` discipline verified).
10. CLI flag scaffolding.
11. Phase-1 doc convergence.
12. Phase-1 verification & PR.

### Task 1: Add `Resolver` / `ResolverParams` to `TaskRef` and `PipelineRef`

**Files:** `internal/tektontypes/types.go`, `internal/tektontypes/types_test.go`.

- [ ] Write `TestUnmarshalTaskRefWithResolver` and `TestUnmarshalPipelineRefWithResolver` covering YAML round-trip for the new fields.
- [ ] Run tests; expect compile failure.
- [ ] Add `Resolver string` and `ResolverParams []ResolverParam` to `TaskRef` and `PipelineRef`. Add the `ResolverParam` struct mirroring `Param`.
- [ ] Re-run tests; expect PASS.
- [ ] Commit: `feat(types): TaskRef/PipelineRef.Resolver + ResolverParams`.

### Task 2: Loader keeps the new fields

**Files:** `internal/loader/loader.go` (no changes expected — generic JSON round-trip), `internal/loader/loader_test.go`.

- [ ] Write `TestLoadPipelineWithResolverTaskRef`: load a Pipeline whose first PipelineTask has `taskRef: {resolver: git, params: [...]}`; assert `Resolver == "git"`, `len(ResolverParams) > 0`.
- [ ] Run tests. If they pass, no production change needed (yaml→struct round-trip already handles it). If they fail, debug.
- [ ] Add a `loader.HasUnresolvedRefs(b *Bundle) []UnresolvedRef` helper for diagnostics; unit-test the listing.
- [ ] Commit: `feat(loader): surface resolver-backed refs via HasUnresolvedRefs`.

### Task 3: Define the `refresolver` package and its `Registry` interface

**Files:** `internal/refresolver/doc.go` (new), `internal/refresolver/refresolver.go` (new), `internal/refresolver/refresolver_test.go` (new).

- [ ] Write `TestRegistryDispatchesByResolverName`: creates a Registry with one stub resolver named `"stub"`; calls `Resolve` with `Resolver: "stub"`; asserts the stub's `Resolve` ran exactly once and bytes flowed through.
- [ ] Write `TestRegistryRejectsUnknownResolver`: same setup; calls with `Resolver: "git"`; expects `ErrResolverNotRegistered`.
- [ ] Write `TestRegistryAllowList`: configure a Registry with `Allow: []string{"stub"}` and a registered `"git"`; expect the call with `Resolver: "git"` to fail with `ErrResolverNotAllowed`.
- [ ] Run tests; expect compile failure.
- [ ] Implement `doc.go` (package comment), `refresolver.go` (the `Resolver` interface, `Request`, `Resolved`, `Registry` struct, `Resolve` method, the two `Err...` sentinels, the `Cache` interface stub).
- [ ] Re-run tests; expect PASS.
- [ ] Commit: `feat(refresolver): registry + Resolver interface scaffolding`.

### Task 4: Implicit DAG edges from result references (NEW — Critical 1 baseline)

**Why this exists.** The current engine builds DAG edges only from `pt.RunAfter` (`engine.go:85`). There is no code that walks `pt.Params` for `$(tasks.X.results.Y)` substrings to add an implicit edge `X → pt.Name`. Upstream Tekton's controller does add these implicit edges; tkn-act must too, otherwise lazy resolution is unschedulable (a task whose `resolver.params: pathInRepo: $(tasks.discover.results.path)` would be placed on dispatch level 0 and run before `discover` ever fires its result). This task installs the baseline walker for normal `pt.Params`; Task 5 (lazy-dispatch hook) extends the same walker to `resolver.params` once that field exists on the type. **This task MUST land before Task 5 in the same PR; the two are co-dependent in CI but logically Task 4 is the prerequisite.**

**Files:** `internal/engine/dag/dag.go` (or new `internal/engine/dag/implicit.go`), `internal/engine/dag/dag_test.go` (or `internal/engine/engine_dag_test.go`), `internal/engine/engine.go`.

- [ ] Write `TestImplicitEdgeFromParamResultRef`: build a Pipeline with two tasks `checkout` and `build`; `build.params` contains `[{name: rev, value: "$(tasks.checkout.results.commit)"}]`; no `runAfter`. Assert that `dag.Levels()` returns `[[checkout], [build]]` (not `[[checkout, build]]`). Assert that running the Pipeline through the engine dispatches `build` strictly after `checkout` completes.
- [ ] Write `TestImplicitEdgeFromResolverParamResultRef` (added in same task; the field doesn't exist yet but the test imports the type — it stays red until Task 1's types land, which they have by Task 4 since Tasks 1-3 have already merged in the same PR): build a Pipeline whose `build.taskRef.resolver = "stub"` and `build.taskRef.resolverParams = [{name: pathInRepo, value: "$(tasks.discover.results.path)"}]`; assert the same `[[discover], [build]]` level layout. Tagging resolver-backed tasks as `Unresolved=true` does not exempt them from DAG construction; their `resolver.params` walker is the same code path.
- [ ] Write `TestImplicitEdgeIsAdditive`: a task with both an explicit `runAfter: [a]` and an implicit reference to `$(tasks.b.results.x)`. Assert two predecessors `{a, b}` (not just `a` and not double-counted).
- [ ] Write `TestImplicitEdgeFromArrayAndObjectParams`: a task with `params` of array shape (`["foo", "$(tasks.x.results.y)"]`) and object shape (`{key: "$(tasks.x.results.y)"}`). Assert the walker scans both and emits the edge. Mirrors how `internal/resolver` substitution already iterates through array/object param shapes.
- [ ] Run tests; expect compile / fail.
- [ ] Implement: in `internal/engine/dag/` (or wherever the engine builds the graph), after the `RunAfter` loop in `engine.go:85`, iterate every PipelineTask's `Params` (and, after Task 5's types exist, `TaskRef.ResolverParams` and `PipelineRef.ResolverParams`); for each string value matching a `$(tasks.<name>.results.<key>)` regex (reuse the regex from `internal/resolver` if exposed, else duplicate as a small helper), call `g.AddEdge(<name>, pt.Name)`. Edges to nonexistent tasks emit a validate-time error (`unknown task in result reference`) — handled by the validator, not silently dropped.
- [ ] Re-run tests; expect PASS.
- [ ] Commit: `feat(engine): implicit DAG edges from $(tasks.X.results.Y) references`.

### Task 5: Lazy-dispatch hook in the engine (was Task 4)

**Files:** `internal/engine/engine.go`, `internal/engine/lazy_resolve.go` (new), `internal/engine/lazy_resolve_test.go` (new).

- [ ] Write `TestRunOneResolvesLazily`: registers an `inline` stub resolver that returns hard-coded Task bytes; defines a Pipeline with a `taskRef: {resolver: inline, params: [...]}`; runs the engine with a fake backend; asserts that the resolver fired once and the resolved task ran on the backend with the inlined steps.
- [ ] Write `TestRunOneResolverFailureMarksTaskFailed`: registers a resolver that returns `errors.New("boom")`; asserts the task ends with `status: "failed"` and `message: "resolver: boom"`; asserts overall run status `failed`, exit code 5 path.
- [ ] Write `TestRunOneCachesPerRun`: registers a resolver that increments a counter; defines two PipelineTasks with identical `(resolver, SUBSTITUTED-params)`; asserts the resolver fired exactly once.
- [ ] Write **`TestRunOneDoesNotCacheAcrossDifferentSubstitutedParams`** (Critical 2): registers a counter-incrementing resolver; defines two PipelineTasks pointing at the same Pipeline ref via the same resolver, but whose `resolver.params` substitute (after running an upstream `discover` Task) to *different* values; assert the resolver fires *twice* (once per unique post-substitution cache key) and the per-run cache contains two entries.
- [ ] Write **`TestRunOneRejectsResolvedTaskWithBadSpec`** (Important 6): registers a resolver that returns syntactically valid YAML but a Task with no `steps:`; assert the run-end status is `failed` and the failing `task-end.message` contains a clear validator error (e.g., `resolver: validate: spec.steps: must have at least one step`); assert the engine called `validator.ValidateTaskSpec` *after* resolution and *before* dispatch.
- [ ] Write **`TestRunOneFinallyTaskWithResolverRef`**: a Pipeline with one main task that fails and one finally task whose `taskRef.resolver = "stub"`; assert the finally task still resolves and runs even though the main DAG failed; assert the resolver fires exactly once for the finally task.
- [ ] Run tests; expect compile failure.
- [ ] Add `Engine.Options.Refresolver *refresolver.Registry` (nil-safe). Implement `lazy_resolve.go` with `lookupTaskSpecLazy(ctx, in, pt, rctx, registry, perRunCache) (TaskSpec, []byte, error)` — substitutes `pt.TaskRef.ResolverParams` against `rctx`, computes `cacheKey = sha256(resolver-name + "\x00" + sortedKVs(SUBSTITUTED-params))` (per spec §3 cache-key invariant), looks up per-run cache, calls `registry.Resolve` on miss, validates the resolved spec via `validator.ValidateTaskSpec`, and returns the inlined `TaskSpec` plus raw bytes (for the on-disk cache).
- [ ] Wire `lookupTaskSpecLazy` into `runOne` between the `evaluateWhen` block and the existing `lookupTaskSpec` call. If `pt.TaskRef != nil && pt.TaskRef.Resolver != ""`, route through lazy-resolve; else fall through to the existing path. **The same dispatch path is used for finally-task resolver refs.**
- [ ] Update `uniqueImages` to skip resolver-backed tasks (they have no spec at pre-pull time).
- [ ] Extend the implicit-edge walker added in Task 4 to also iterate `pt.TaskRef.ResolverParams` and `pt.PipelineRef.ResolverParams` (the field types now exist).
- [ ] Re-run tests; expect PASS.
- [ ] Commit: `feat(engine): lazy resolution at task-dispatch time`.

### Task 6: Eager top-level `pipelineRef.resolver` resolution at load time (NEW — Critical 3)

**Why this exists.** Spec §3 says all resolution is lazy at dispatch time; spec §7 says the top-level Pipeline ref is resolved before DAG build. These don't conflict — top-level `pipelineRef.resolver` cannot legally substitute `$(tasks.X.results.Y)` (no task has run yet, the DAG it would define hasn't been parsed), so eager resolution at load time is correct. This task makes that explicit and wires `--offline` cache lookup to fire at load time for top-level refs.

**Files:** `internal/engine/engine.go` (the top of `RunPipeline`), `internal/engine/eager_pipelineref_test.go` (new), `internal/loader/loader.go` (a small helper to detect a top-level `pipelineRef.resolver`).

- [ ] Write `TestEagerTopLevelPipelineRefResolves`: a `PipelineRun`-style input whose top-level `pipelineRef = {resolver: stub, params: [...]}`; the stub returns a Pipeline with two tasks; assert the engine substitutes the resolved Pipeline as `pl` before DAG build, and the `resolver-start` / `resolver-end` events emitted carry an empty `task` field (per spec §12).
- [ ] Write `TestEagerTopLevelPipelineRefRejectsResultRef`: a top-level `pipelineRef.params` containing `$(tasks.X.results.Y)`; assert validate-time error (exit 4) — there is no `tasks.X` to reference at load time.
- [ ] Write `TestEagerTopLevelPipelineRefOfflineCacheMiss`: top-level `pipelineRef.resolver: git` with `--offline` and an empty cache; assert exit 4 *before* any task starts (i.e., no `task-start` event fires).
- [ ] Implement: at the top of `Engine.RunPipeline`, before the `Prepare` call, check `pl.Spec.PipelineRef` (or wherever the loader stashes the top-level ref). If non-nil with a `Resolver` field, call `registry.Resolve` synchronously, validate via `validator.ValidatePipelineSpec`, replace `pl.Spec` with the resolved Pipeline's Spec, and emit `resolver-start` / `resolver-end` events with empty `task`.
- [ ] Commit: `feat(engine): eager top-level pipelineRef.resolver resolution`.

### Task 7: Cluster backend lazy-resolve + inline before submission (NEW — Critical 4)

**Why this exists.** The cluster path goes through `runViaPipelineBackend` (`engine.go:51-52, 483`), which serializes the Pipeline directly to a `PipelineRun` for the local k3d. Local k3d's Tekton has no hub credentials, no `--resolver-config`, and no access to `--resolver-cache-dir`; an unresolved `taskRef: {resolver: git, ...}` submitted to it would fail. We must resolve in tkn-act and inline `taskSpec` into the PipelineRun *before* submission. The existing `inlineTaskSpec` (`internal/backend/cluster/run.go:165-172`) already inlines `taskRef.name` references the same way — this task extends it to resolver-backed refs.

**Files:** `internal/engine/engine.go` (`runViaPipelineBackend`), `internal/engine/cluster_lazy_resolve_test.go` (new), `internal/backend/cluster/run.go` (extend `inlineTaskSpec` to accept already-resolved `TaskSpec` overrides).

- [ ] Write `TestClusterBackendInlinesResolverBackedRefs`: a Pipeline with one task whose `taskRef.resolver = "stub"`; the stub returns a Task with one step. Use a cluster-backend test double (or the existing `runViaPipelineBackend` test harness, e.g. `cluster_events_test.go`'s patterns) and assert the submitted PipelineRun contains `taskSpec: {steps: [...]}` for that task — *not* an unresolved `taskRef.resolver` block.
- [ ] Write `TestClusterBackendResolverParamsSubstitutionFromPipelineParams`: a Pipeline whose `resolver.params` reference `$(params.url)`; assert the substituted value appears in the registry call and the result is inlined before submission.
- [ ] Write `TestClusterBackendResolverParamsSubstitutionFromUpstreamResults`: a Pipeline with `discover → build` where `build.taskRef.resolver = "stub"` and `build.resolver.params.path = $(tasks.discover.results.path)`; assert the cluster backend submits one PipelineRun for the `discover` level, waits for it to complete, then submits the `build` level after substituting `discover`'s actual result. (Implementation: the cluster backend already uses level-by-level submission for some flows; this test pins the per-level rhythm for resolver-backed deps. If the existing cluster backend submits the entire Pipeline in one shot, see the implementation note below for the per-level fallback path.)
- [ ] Write `TestClusterBackendResolverFailureSurfacesAsTaskFailed`: stub returns an error; assert the task ends with `status: "failed"` and `message: "resolver: ..."`, and overall run status `failed`. Mirrors the docker-path behavior.
- [ ] Run tests; expect compile failure.
- [ ] Implement: in `runViaPipelineBackend`, walk `pl.Spec.Tasks ∪ pl.Spec.Finally` and identify resolver-backed `taskRef`s. For each, build the same `resolver.Context` accumulator the docker `runOne` path uses, call `lookupTaskSpecLazy`, and pass the resolved `TaskSpec` map into `inlineTaskSpec` so the submitted PipelineRun contains `taskSpec` instead of an unresolved `taskRef`. For Pipelines whose `resolver.params` reference upstream task results, submit one PipelineRun per dispatch level so each level's substitution sees prior-level results; for Pipelines whose `resolver.params` reference run-scope only (no upstream-result deps), inline the whole Pipeline and submit once.
- [ ] Re-run tests; expect PASS.
- [ ] Commit: `feat(engine/cluster): lazy-resolve and inline resolver refs before PipelineRun submission`.

### Task 8: Validator handles unresolved refs (was Task 5)

**Files:** `internal/validator/validator.go`, `internal/validator/validator_test.go`.

- [ ] Write `TestValidateAcceptsResolverBackedTaskRef`: a Pipeline with `taskRef: {resolver: git, params: [...]}` should validate cleanly when no `--offline` flag is in play.
- [ ] Write `TestValidateRejectsResolverBackedTaskRefOffline`: same Pipeline; with `Offline: true` and an empty cache; expect a validate error.
- [ ] Write `TestValidateRejectsUnknownResolverInDirectMode`: with `Resolver: "made-up"` and `Allow: ["git"]`; expect a validate error mentioning "made-up".
- [ ] Write **`TestValidateAcceptsAnyResolverNameInRemoteMode`** (moved up from Phase 5 — Minor 14, Important 10): with `Resolver: "made-up-custom-resolver"` and `Options.RemoteResolverEnabled = true`; assert validation passes. The `RemoteResolverEnabled` Option short-circuits the direct-mode allow-list check; the `--remote-resolver-context` plumbing in Phase 5 simply flips this Option to `true`.
- [ ] Write `TestValidateRejectsResolverParamWithUnknownTaskResultRef`: a `resolver.params` containing `$(tasks.does-not-exist.results.foo)`; expect a validate error mentioning the missing task name. Reuses the implicit-edge walker logic from Task 4 in reverse — references to nonexistent tasks become validate errors here, not silent edge drops in the DAG.
- [ ] Implement: extend `validator.Options` with `Offline bool`, `RegisteredResolvers []string`, `RemoteResolverEnabled bool`. Add a new check that runs over `pl.Spec.Tasks ∪ pl.Spec.Finally` looking for resolver-backed refs and applying the rules above. The unknown-resolver check skips when `RemoteResolverEnabled` is true. Any cache-presence check is delegated to a `validator.Options.CacheCheck func(refresolver.Request) bool` callback (nil-safe; nil means "skip the offline check").
- [ ] Commit: `feat(validator): resolver-ref pre-flight checks`.

### Task 9: New `resolver-start` / `resolver-end` event kinds (was Task 6)

**Files:** `internal/reporter/event.go`, `internal/reporter/reporter_test.go`.

- [ ] Write `TestJSONResolverEvents`: emit one of each kind through the JSON reporter and assert the marshalled shape (per spec §12).
- [ ] Write **`TestJSONResolverEventOmitsZeroValues`** (Important 7): emit a `resolver-end` Event with `Resolver=""`, `Cached=false`, `SHA256=""`, `Source=""` and assert none of those four keys appear in the JSON output. Confirms the new fields follow the `,omitempty` precedent every other optional `Event` field already uses (`runId`, `pipeline`, `task`, `step`, `status`, `exitCode`, `durationMs`, `message`, `attempt`, `results`).
- [ ] Write `TestJSONResolverEventEmptyTaskForTopLevelPipelineRef`: emit a `resolver-end` for the top-level pipelineRef path with `Task=""`; assert the JSON does NOT include a `"task"` key (existing `omitempty` on `Task`); the consumer disambiguates via the empty task field per spec §12.
- [ ] Implement: add `EvtResolverStart`, `EvtResolverEnd` to the event-kind enum. Add the four fields to `Event` with `,omitempty` JSON tags: `Resolver string \`json:"resolver,omitempty"\``, `Cached bool \`json:"cached,omitempty"\``, `SHA256 string \`json:"sha256,omitempty"\``, `Source string \`json:"source,omitempty"\``. Update the JSON encoder docs.
- [ ] Wire the engine's `lazy_resolve.go` to emit `resolver-start` before the call and `resolver-end` after, including failure cases. Wire the eager top-level path (Task 6) to emit the same events with empty `Task`.
- [ ] Update `cmd/tkn-act/agentguide_data.md` and `AGENTS.md`'s event-kind list (line 175 area in `feature-parity.md`'s machine-interface table).
- [ ] Commit: `feat(reporter): resolver-start / resolver-end events`.

### Task 10: CLI flag scaffolding (Phase 1 subset) (was Task 7)

**Files:** `cmd/tkn-act/run.go`, `cmd/tkn-act/run_test.go`.

In Phase 1 only, the user-visible flags are:

- `--resolver-cache-dir <path>` (default `$XDG_CACHE_HOME/tkn-act/resolved/`)
- `--offline` (the validator already wired it; CLI plumbs it through)
- `--resolver-allow <csv>` (default `git,hub,http,bundles`)

The remote-mode flags land in Phase 5.

- [ ] Write `TestRunFlagsResolverDefaults`: assert the default cache dir resolves under `XDG_CACHE_HOME` or `$HOME/.cache/`; assert default allow-list matches.
- [ ] Implement the flags. Construct the `refresolver.Registry` once per run; in Phase 1 it has zero registered direct resolvers (only the `inline` stub registered by the test harness). Pass through to `engine.Options`.
- [ ] Update `tkn-act help-json` golden test if there is one.
- [ ] Commit: `feat(cli): --resolver-cache-dir / --offline / --resolver-allow`.

### Task 11: Phase-1 doc convergence (was Task 8)

**Files:** `AGENTS.md`, `cmd/tkn-act/agentguide_data.md`, `docs/feature-parity.md`, `docs/short-term-goals.md`, `docs/test-coverage.md`.

- [ ] Add a "Resolvers (in progress)" section to `AGENTS.md` introducing the lazy-dispatch concept and the new event kinds. Note that no concrete resolver is implemented yet.
- [ ] Mirror to `agentguide_data.md` via `go generate ./cmd/tkn-act/`.
- [ ] In `docs/feature-parity.md`, change the `Resolvers — git / hub / cluster / bundles` row from `gap` → `in-progress`, link `docs/superpowers/plans/2026-05-04-resolvers.md`.
- [ ] In `docs/short-term-goals.md`, update Track 1 #9 status to "Phase 1 done in v1.6.x; remaining phases tracked in plan."
- [ ] In `docs/test-coverage.md`, list the new `internal/refresolver/` package.
- [ ] Commit: `docs: introduce resolver work; flip Track 1 #9 to in-progress`.

### Task 12: Phase-1 verification & PR (was Task 9)

- [ ] Run the local battery: `go vet ./... && go vet -tags integration ./... && go vet -tags cluster ./...; go build ./...; go test -race -count=1 ./...; bash .github/scripts/parity-check.sh; .github/scripts/tests-required.sh main HEAD`. Expect all green.
- [ ] Push branch and open PR (title above). Wait for CI (incl. coverage gate). The coverage gate must pass per-package; the new `internal/refresolver/` and `internal/engine/lazy_resolve_test.go` keep coverage at-or-above baseline. If a per-package drop appears, add tests or add `[skip-coverage-check]` only with a clear, one-sentence rationale in the PR body.
- [ ] Merge per project default: `gh pr merge <num> --squash --delete-branch`.

**Phase 1 done.** Subsequent phases each ship their own PR.

---

## Phase 2 — Direct git resolver

**Goal:** Concrete `git` resolver wired into the Phase 1 registry. After this phase, a Pipeline with `taskRef: {resolver: git, params: [{name: url, value: file://...}, {name: revision, value: HEAD}, {name: pathInRepo, value: task.yaml}]}` runs end-to-end on both backends.

**Ship as:** `feat(resolvers): direct git resolver (Track 1 #9, Phase 2)`.

### Task 1: Add `go-git` dependency

- [ ] Add `github.com/go-git/go-git/v5` to `go.mod`. Run `go mod tidy`. **Measure the `tkn-act` binary size before and after the dependency addition** (`go build -o /tmp/tkn-act-before ./cmd/tkn-act && du -h /tmp/tkn-act-before`, then add the dep and rebuild). Document the delta in the PR body. The combined Phase-2 + Phase-4 dependency budget is **+10MB total** (per spec §17 Q7); if go-git alone consumes more than ~5MB, pause and request review before continuing — leaving zero headroom for `go-containerregistry` in Phase 4 forces a deferral decision.

### Task 2: Implement the git resolver (TDD)

**Files:** `internal/refresolver/git.go` (new), `internal/refresolver/git_test.go` (new).

- [ ] Write `TestGitResolverHappyPath`: build a local bare repo in a `t.TempDir()` containing one task YAML; call the resolver with `url: file://<tmpdir>`, `revision: HEAD`, `pathInRepo: task.yaml`; assert bytes match.
- [ ] Write `TestGitResolverMissingPath`: same repo; `pathInRepo: nope.yaml`; expect a typed error mentioning "pathInRepo".
- [ ] Write `TestGitResolverRevisionMismatch`: clone fails for `revision: nonexistent-branch`; assert the error mentions the revision.
- [ ] Write `TestGitResolverHonorsCacheDir`: call twice; assert the second call hits the on-disk cache (no second clone).
- [ ] Write `TestGitResolverSSHURLParse`: an `ssh://...` URL is accepted (we don't actually clone in the unit test; just check the URL parses and the resolver delegates to the underlying client).
- [ ] Implement `git.go`. Public constructor: `func NewGit(cacheDir string) Resolver`.
- [ ] Register in `Registry` constructor (`refresolver.NewDefault(opts) *Registry`).
- [ ] Re-run tests; PASS.
- [ ] Commit: `feat(refresolver): direct git resolver`.

### Task 3: Cross-backend e2e fixture

**Files:** `testdata/e2e/resolver-git/pipeline.yaml`, `testdata/e2e/resolver-git/repo.git/` (bare repo, committed), `internal/e2e/fixtures/fixtures.go`.

- [ ] Build a tiny bare repo on a developer's box (`git init --bare`; `git push` a single Task YAML); commit the resulting `objects/`, `HEAD`, `refs/` into the fixture dir. The fixture's pipeline uses `url: file://$WORKSPACE/testdata/e2e/resolver-git/repo.git`.
- [ ] Add `{Dir: "resolver-git", Pipeline: "resolver-git", WantStatus: "succeeded"}` to `fixtures.All()`.
- [ ] Run docker e2e locally if Docker is available.
- [ ] Commit: `test(e2e): resolver-git cross-backend fixture`.

### Task 4: Phase-2 doc convergence + verification + PR

- [ ] Update the `AGENTS.md` "Resolvers" section to note `git` is supported. Mirror to `agentguide_data.md`.
- [ ] Add the fixture row to `docs/test-coverage.md`.
- [ ] Run the local verification battery (per Phase 1 Task 12). Expect all green; per-package coverage stable.
- [ ] Open PR, merge per project default.

---

## Phase 3 — Direct `hub` and `http` resolvers

**Goal:** Two more concrete resolvers. Both share the same HTTP-client testing pattern (`httptest.NewServer`).

**Ship as:** `feat(resolvers): direct hub + http resolvers (Track 1 #9, Phase 3)`.

### Task 1: hub resolver

**Files:** `internal/refresolver/hub.go`, `internal/refresolver/hub_test.go`.

- [ ] Write `TestHubResolverHappyPath`: `httptest.NewServer` returns a canned YAML for `/v1/resource/<catalog>/<kind>/<name>/<version>/yaml`; resolver constructed with `BaseURL: server.URL`; assert bytes match.
- [ ] Write `TestHubResolver404HelpfulHint`: server returns 404; expect an error message that includes "did you mean" or at least "not found at".
- [ ] Write `TestHubResolver5xxRetries`: server fails the first request, succeeds the second; assert the resolver retried exactly once.
- [ ] Implement; honor `--resolver-config hub.token` for `Authorization: Bearer ...`.
- [ ] Register in `NewDefault`.
- [ ] Commit.

### Task 2: http resolver

**Files:** `internal/refresolver/http.go`, `internal/refresolver/http_test.go`.

- [ ] Write `TestHTTPResolverHappyPath`, `TestHTTPResolverSHA256Verify` (both pass and mismatch), `TestHTTPResolverBearerToken` (asserts `Authorization` header), `TestHTTPResolverRejectsHTTPByDefault` (expects an error unless `AllowInsecure: true`).
- [ ] Implement; expose **`--resolver-allow-insecure-http`** on the run command. The `--resolver-` prefix matches the rest of the resolver flag family (`--resolver-cache-dir`, `--resolver-allow`, `--resolver-config`, `--resolver-cache-mode`, etc.). The flag is documented as CI-only in `--help`.
- [ ] Register.
- [ ] Commit.

### Task 3: Cross-backend e2e fixture

**Files:** `testdata/e2e/resolver-http/pipeline.yaml`, `internal/e2e/fixtures/fixtures.go`, `internal/e2e/harness_http_server.go` (new helper that spins up an `httptest.NewServer` for the duration of the run and substitutes its URL into a Pipeline param).

- [ ] Add the fixture; the Pipeline references `url: $(params.fixture-server-url)/task.yaml`. The harness sets the param when invoking the run.
- [ ] Run docker e2e locally if available.
- [ ] Commit.

### Task 4: Doc convergence + verification + PR

- [ ] Update `AGENTS.md` / `agentguide_data.md`. Add fixtures to `docs/test-coverage.md`.
- [ ] Verification battery; coverage gate.
- [ ] PR + merge.

---

## Phase 4 — Direct `bundles` and `cluster` resolvers

**Goal:** OCI bundles + the kubeconfig-driven `cluster` resolver. Bundles is the heavier one (extra dependency); we batch them together because both are lower-volume than git/hub.

**Ship as:** `feat(resolvers): direct bundles + cluster resolvers (Track 1 #9, Phase 4)`.

### Task 1: bundles resolver

**Files:** `internal/refresolver/bundles.go`, `internal/refresolver/bundles_test.go`.

- [ ] Add `github.com/google/go-containerregistry`. **Measure the `tkn-act` binary size before and after** (`go build -o /tmp/tkn-act-before ./cmd/tkn-act && du -h /tmp/tkn-act-before`, then add the dep and rebuild). The **combined Phase-2 + Phase-4 dependency budget is +10MB total** measured against the `main` branch's binary at the time Phase 2 starts (cumulative go-git + go-containerregistry growth). Document the delta in the PR body. **If the cumulative growth exceeds +10MB, pause and request human review before continuing** — the options at that point are (a) ship `bundles` behind a build tag, (b) defer `bundles` to a separate v1.7 release, or (c) explicitly raise the budget. Don't silently exceed it.
- [ ] Write `TestBundlesResolverHappyPath`: build a bundle in-memory with `crane.Append`; serve it via `registry.New()` (the test registry from `go-containerregistry`); resolver pulls and finds the named resource.
- [ ] Write `TestBundlesResolverMissingResource`: same bundle; ask for a name not in it; expect typed error.
- [ ] Implement; honor `~/.docker/config.json` via the lib's default keychain.
- [ ] Register.
- [ ] Commit.

### Task 2: cluster resolver

**Files:** `internal/refresolver/cluster.go`, `internal/refresolver/cluster_test.go`.

- [ ] Write `TestClusterResolverRequiresExplicitContext`: with no `--cluster-resolver-context` and `KUBECONFIG` unset; assert refusal with `ErrClusterContextRequired`.
- [ ] Write `TestClusterResolverHappyPath`: fake dynamic client returns an unstructured Task; resolver serializes to YAML.
- [ ] Implement; the `cluster` resolver is **not** registered by default in the allow-list — `NewDefault` adds it only if `Opts.AllowCluster` is set.
- [ ] Update CLI: add `--cluster-resolver-context <ctx>` flag. Document the security stance in `--help` text.
- [ ] Commit.

### Task 3: Cross-backend e2e fixture (`bundles` only — `cluster` is hard to fixture without a real cluster)

**Files:** `testdata/e2e/resolver-bundles/pipeline.yaml`, `testdata/e2e/resolver-bundles/bundle.tar` (built by a one-shot script committed alongside; the script is documented but not run in CI).

- [ ] Add the fixture; the harness sets up an in-memory OCI registry via the helper.
- [ ] Mark `cluster` resolver as covered by unit tests only — add a row to `docs/test-coverage.md` noting the gap.
- [ ] Commit.

### Task 4: Doc convergence + verification + PR

- [ ] Update `AGENTS.md` / `agentguide_data.md`. Add the new fixture to `docs/test-coverage.md`.
- [ ] Verification battery; coverage gate; parity-check.
- [ ] PR + merge.

---

## Phase 5 — Mode B (remote resolver via `ResolutionRequest`)

**Goal:** Submit `ResolutionRequest` CRDs to a user-configured remote cluster and use the response in place of direct-mode fetching. After this phase, every direct resolver can also be served by the remote, plus arbitrary custom resolver names.

**Ship as:** `feat(resolvers): remote resolver via ResolutionRequest CRD (Track 1 #9, Phase 5)`.

### Task 1: Remote resolver implementation

**Files:** `internal/refresolver/remote.go`, `internal/refresolver/remote_test.go`.

- [ ] Write `TestRemoteResolverHappyPath`: fake dynamic client receives a `ResolutionRequest`, sets `status.conditions=Succeeded` and `status.data=base64(yaml)`; remote resolver decodes and returns bytes.
- [ ] Write `TestRemoteResolverFailedCondition`: status condition `Succeeded=False` with a `reason` and `message`; expect a typed error containing both.
- [ ] Write `TestRemoteResolverTimeout`: dynamic client never updates status; remote resolver times out after the configured budget; expect typed error.
- [ ] Write `TestRemoteResolverDeletesAfterSuccess` and `TestRemoteResolverDeletesAfterFailure`: assert the `ResolutionRequest` is deleted regardless of outcome (cleanup discipline matches the cluster backend's namespace cleanup).
- [ ] Write **`TestRemoteResolverDeletesOnContextCancel`** (Critical 5): start the resolver with a `context.WithCancel`-derived context; cancel mid-resolution (after the `ResolutionRequest` has been Created but before the watch fires Succeeded); assert the resolver's `defer Delete(...)` *still* runs and the `ResolutionRequest` is deleted on the fake client. The implementation already uses `context.Background()` for the Delete call (per spec §6) — this test pins that behavior so a future refactor can't silently regress it into using the cancelled context.
- [ ] Write **`TestRemoteResolverFallsBackToV1Alpha1OnNoKindMatch`** (Minor 11): fake dynamic client returns `meta.NoKindMatchError` for v1beta1; assert the resolver retries the same request on v1alpha1 and emits a one-shot debug-level log. Both shapes are wire-compatible for `spec.params` / `status.conditions` / `status.data`.
- [ ] Write `TestRemoteResolverV1Alpha1OnlyCluster`: same as above but the v1alpha1 path returns Succeeded; assert the resolver returns the bytes without error.
- [ ] Implement, including `--remote-resolver-context`, `--remote-resolver-namespace`, `--remote-resolver-timeout` plumbing and an explicit `Registry.Remote(*RemoteResolver)` setter that takes precedence over direct resolvers when non-nil. The Delete call uses `context.Background()` so cancellation of the request context still triggers cleanup.
- [ ] Flip `validator.Options.RemoteResolverEnabled = true` when `--remote-resolver-context` is set. (The validator-side allowance for arbitrary resolver names already landed in Phase 1 Task 8 — this is just the CLI plumbing.)
- [ ] Commit.

### Task 2: CLI flags + kubeconfig wiring

**Files:** `cmd/tkn-act/run.go`, `cmd/tkn-act/run_test.go`.

- [ ] Write `TestRunRemoteResolverContextLoadsKubeconfig`: with `--remote-resolver-context=test`, expect the run command to load `KUBECONFIG`'s `test` context. Use a temp kubeconfig file in the test.
- [ ] Implement: load kubeconfig via `clientcmd.NewNonInteractiveDeferredLoadingClientConfig`; build a dynamic client; hand to the registry.
- [ ] Update `tkn-act help-json` golden if needed.
- [ ] Commit.

### Task 3: Cross-backend e2e fixture using local k3d as the remote target

**Files:** `testdata/e2e/resolver-remote/pipeline.yaml`, `internal/clustere2e/cluster_e2e_test.go` extension.

- [ ] In the cluster harness, add a sub-test that:
  1. Brings up the standard k3d (already done by the harness).
  2. Pre-loads a Task into the k3d via `kubectl apply -f`.
  3. Runs `tkn-act run --cluster --remote-resolver-context=tkn-act -f testdata/e2e/resolver-remote/pipeline.yaml`. The Pipeline's task uses `taskRef: {resolver: cluster, params: [...]}`. Tekton's built-in cluster resolver in the k3d picks up the request, returns the Task bytes, tkn-act inlines them, and the same k3d runs them.
- [ ] This fixture is `cluster`-tag only. Add a stub entry in the docker harness with `WantSkip=true, Reason: "remote resolver requires a cluster"`.
- [ ] Commit.

### Task 4: Doc convergence + verification + PR

- [ ] Update `AGENTS.md` / `agentguide_data.md` with the full dual-mode story.
- [ ] Add the new fixture to `docs/test-coverage.md`. Note the cluster-only restriction.
- [ ] Verification battery (coverage, parity-check, tests-required).
- [ ] PR + merge.

---

## Phase 6 — `--offline`, caching polish, cross-backend fidelity, ship Track 1 #9

**Goal:** Final polish + flip the parity row to `shipped`. After this phase, `tkn-act run --offline` behaves correctly; the on-disk cache has prune/refresh subcommands; `tkn-act doctor -o json` surfaces resolved-cache stats; and every resolver is exercised on both backends.

**Ship as:** `feat(resolvers): offline mode, cache management, parity ship (Track 1 #9, Phase 6)`.

### Task 1: `--offline` end-to-end

**Files:** `cmd/tkn-act/run.go`, `cmd/tkn-act/run_test.go`, `internal/refresolver/cache.go`, `internal/refresolver/cache_test.go`.

- [ ] Write `TestOfflineRejectsCacheMiss`: validate-step error path; confirm exit code 4.
- [ ] Write `TestOfflineAllowsCacheHit`: pre-populate the cache; run with `--offline`; expect success.
- [ ] Implement: Cache exposes `Has(req Request) bool`; the validator's `CacheCheck` callback (Phase 1 Task 8) wires to it.
- [ ] Commit.

### Task 2: Cache management subcommands

**Files:** `cmd/tkn-act/cache.go` (new — there isn't currently a cache subcommand; this phase introduces one for the resolver cache only).

- [ ] **Verify the `cache` noun is fresh** (Minor 13): `grep -rn '"cache"\|Use:.*cache' cmd/` should return zero hits before this task starts. The repo currently exposes `run`, `validate`, `list`, `doctor`, `cluster`, `version`, `agent-guide`, `help-json` — `cache` does not collide. If a future PR introduces a different `cache` subcommand before this phase lands, reconcile by namespacing as `tkn-act resolver cache prune` / `tkn-act resolver cache list` instead.
- [ ] Add `tkn-act cache prune --resolver` (rm-rf the cache dir under `--resolver-cache-dir`).
- [ ] Add `tkn-act cache list --resolver -o json` (lists cached resolved refs by resolver name + key + age).
- [ ] Add `--resolver-cache-mode={use,bypass,refresh}` on `run`. Default `use`.
- [ ] Tests for each subcommand.
- [ ] Commit.

### Task 3: `doctor -o json` reports cache state when `--debug`

**Files:** `cmd/tkn-act/doctor.go`, `cmd/tkn-act/doctor_test.go`.

- [ ] Add a new check `name: "resolver_cache"` reporting count + total bytes; only included when `--debug` is set (avoid polluting the default doctor output).
- [ ] Test.
- [ ] Commit.

### Task 4: Final cross-backend pass

- [ ] Confirm every fixture from Phases 2-5 appears in `internal/e2e/fixtures.All()` and runs on both backends (or has an explicit `WantSkip` with a reason).
- [ ] Run the cluster-integration suite locally (or in a draft PR) to catch any cluster-specific drift.

### Task 5: Ship Track 1 #9 — flip parity, finalize docs

**Files:** `docs/feature-parity.md`, `docs/short-term-goals.md`, `AGENTS.md`, `cmd/tkn-act/agentguide_data.md`, `README.md`, `docs/test-coverage.md`.

- [ ] In `docs/feature-parity.md`, change the `Resolvers — git / hub / cluster / bundles` row from `in-progress` → `shipped`. Populate the `e2e fixture` column with a comma-separated list (`resolver-git, resolver-http, resolver-bundles, resolver-remote`). Link to this plan.
- [ ] In `docs/short-term-goals.md`, mark Track 1 #9 done in v1.6.
- [ ] In `AGENTS.md`, finalize the Resolvers section to reflect the shipped state (drop "in progress", document the dual modes, list every flag, the new event kinds, the security stance, the offline mode, and the cache subcommands).
- [ ] Mirror `AGENTS.md` to `cmd/tkn-act/agentguide_data.md` via `go generate ./cmd/tkn-act/`. Confirm `diff cmd/tkn-act/agentguide_data.md AGENTS.md` is empty.
- [ ] Add a one-line bullet under "Tekton features supported" in `README.md`.
- [ ] Add the new fixtures to `docs/test-coverage.md`.
- [ ] Run `bash .github/scripts/parity-check.sh` — must pass.
- [ ] Commit: `docs: ship resolvers; flip Track 1 #9 to shipped`.

### Task 6: Final verification + PR

- [ ] Run the full local battery one more time: `go vet ./... && go vet -tags integration ./... && go vet -tags cluster ./...; go build ./...; go test -race -count=1 ./...; bash .github/scripts/parity-check.sh; .github/scripts/tests-required.sh main HEAD`. Expect all green.
- [ ] Push branch, open PR (title above).
- [ ] Wait for all CI jobs (incl. `docker-integration`, `cluster-integration`, `coverage`).
- [ ] Merge per project default.

---

## Self-review notes

- **Coverage gate per phase.** Every phase keeps per-package coverage at-or-above baseline. Phases 2-5 each add a new package file with a dedicated `*_test.go`; their coverage starts at 100% the day they land. Phase 1 grew from 9 tasks to 12 tasks (the three new tasks address blocking review feedback); the engine, validator, dag, reporter, and cluster-backend changes add roughly 400 lines across several functions — every change is unit-tested in `dag_test.go`, `lazy_resolve_test.go`, `eager_pipelineref_test.go`, `cluster_lazy_resolve_test.go`, `validator_test.go`, and `reporter_test.go` extensions, keeping each package on the right side of the gate.
- **Parity-check per phase.** Phase 1 flips the parity row to `in-progress` and adds no fixture (acceptable per the script's rules). Phases 2-5 each add at least one fixture under `testdata/e2e/` registered in `fixtures.All()`. Phase 6 flips to `shipped` with the e2e-fixture cell populated. The script's invariants (every shipped row has a fixture; no orphan limitations) hold at every commit boundary.
- **Tests-required gate per phase.** Every phase modifies at least one `*_test.go` (most modify several), so `tests-required.sh` is satisfied. No `[skip-test-check]` is anticipated; if a phase ends up doing pure documentation work (unlikely given how the phases are scoped), the docs-touching commits don't trigger the gate.
- **Backwards compatibility.** Phase 1 changes the public types (`TaskRef`, `PipelineRef`) by adding optional fields. JSON round-trips of existing inline-only YAML are unaffected (the new fields are `omitempty`). The new event kinds and the four new `Event` fields (`Resolver`, `Cached`, `SHA256`, `Source`) are additive with `,omitempty` JSON tags; agents that don't recognize them ignore them. No exit-code renumbering. No flag renames. **One existing-engine behavior change:** the new implicit-edge inference (Task 4) means a Pipeline that previously had a hidden race condition (two tasks at level 0, one referencing the other's results, depending on map-iteration order to "happen to work") will now correctly serialize them. This is a strict improvement but technically observable to a user whose Pipeline relied on the broken behavior; document in the Phase 1 PR description.
- **Failure recovery between phases.** If Phase 2 (git) ships and Phase 3 stalls, the parity row stays `in-progress`; users can use git-resolver pipelines and inline pipelines, with hub/http/bundles still erroring out cleanly. The system is incrementally useful at every boundary.
- **Risk concentration.** The four highest-risk pieces are (a) Phase 1 Task 4's implicit-edge inference (touches every engine run, not just resolver-backed ones — must be conservative and well-tested), (b) Phase 1 Task 5's lazy-dispatch wiring into the engine, (c) Phase 1 Task 7's cluster-backend lazy-resolve (changes how `runViaPipelineBackend` serializes — bug here breaks every cluster-mode resolver run), and (d) Phase 5's `ResolutionRequest` polling driver. Every one has a dedicated unit-test surface and the cluster path also has a real-cluster integration test in Phase 5. The bundles dependency-size question (spec §17 Q7) is the highest external risk; Phase 2 Task 1 and Phase 4 Task 1 each make a hard checkpoint against the +10MB cumulative budget.
