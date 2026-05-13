# Short-term goals

Last updated: 2026-05-13 (Track 2 fully complete; cluster-CI now matrixes over Tekton LTS versions).

This document is the project's working priority list for the next few
weeks of feature work. It has two ranked tracks; both run in parallel
but track-2 items unblock track-1 items by giving us confidence that
each new feature behaves identically across backends.

For the canonical, machine-checked record of what's shipped vs. gapped
(and which fixture proves it), see `docs/feature-parity.md` — that's the
scoreboard, this file is the priority list. For "what is and isn't
tested today" see `docs/test-coverage.md`. For the long-form designs
that have already shipped see `docs/superpowers/specs/`.

---

## Track 1 — Tekton-upstream feature parity (most-used first)

The ranking is "how often the feature appears in real-world Tekton
catalogs and Tekton-using repos," not "how easy it is to build." Items
near the top are the ones whose absence forces the most users onto
`--cluster` today.

| # | Feature | Why it matters | Status |
|---|---|---|---|
| 1 | **`Task.sidecars`** | Catalog Tasks rely on database / mock-service sidecars; without it, a large fraction of community Tasks won't run on `--docker`. | Done in v1.6 (PR for `feat: Task.sidecars`). Per-Task pause container owns the netns; sidecars + steps join via `network_mode: container:<pause-id>`; cluster pass-through with `sidecar-start` / `sidecar-end` events from `status.sidecars[]`. |
| 2 | **PipelineRun-level timeouts** (`spec.timeouts.{pipeline,tasks,finally}`) | Wraps the per-Task timeout we shipped in v1.2. Common in CI pipelines that need a hard wall-clock cap. | Done in v1.3 (PR for `feat: PipelineSpec.Timeouts`). Outer policy + cluster pass-through; status `timeout`, exit code 6 unchanged. |
| 3 | **`PipelineTask.matrix`** | Fan a Task across a parameter matrix (build OS × Go version, etc.). Heavily used in language-toolchain pipelines. | Done in v1.6 (PR for `feat: PipelineTask.matrix`). Engine-side `expandMatrix` runs before DAG build (cross-product + include rows, per-row `when:`, 256-row cap); cluster delegates to the controller and reconstructs `MatrixInfo` per TaskRun via canonical param-hash matching with childReferences-order fallback. Resolver gained `$(tasks.X.results.Y[*])` so matrix-fanned string results promote to `[]string` on `Pipeline.spec.results`. Include rows whose params overlap a cross-product param are validator-rejected (Tekton folds them; tkn-act would always append, so we reject to keep cross-backend fidelity); see `testdata/limitations/matrix-include-overlap/`. |
| 4 | **`Task.stepTemplate`** | DRY for `image` / `env` shared across steps. Common in catalog Tasks. | Done in v1.4 (PR for `feat: TaskSpec.StepTemplate`). Engine-side merge before substitution; cluster pass-through verified. |
| 5 | **Load `kind: ConfigMap` / `kind: Secret` from `-f`** | Already in v1.2 as the deferred "B-3" path. Lets users pass the same YAML they would `kubectl apply` instead of `--configmap-dir`. | Done in v1.5 (PR for `feat: load ConfigMap/Secret from -f`). Loader accepts `kind: ConfigMap` / `kind: Secret` (apiVersion v1); `volumes.Store` gained a third lowest-precedence layer fed by the bundle. |
| 6 | **Pipeline results** (`Pipeline.spec.results`) | Surfaces a final value at the run level; consumers / agents read it from `run-end`. | Done in v1.5 (PR for `feat: Pipeline.spec.results`). Engine resolves after finally; cluster reads `pr.status.results`; `run-end` event carries `results`. |
| 7 | **`displayName` / `description`** on Task / Pipeline / Step | Pure UX; absent fields make pretty output less informative and AGENTS-mode output less self-describing. | Done in v1.5 (PR for `feat: displayName + description`). Type addition + event-field plumbing; cluster pass-through verified. |
| 8 | **StepActions** (`tekton.dev/v1beta1` referenceable steps) | Newer Tekton concept (0.50+). Less frequent in catalogs today but rising. | Done in v1.6 (PR for `feat: StepActions`). Engine-side expansion before stepTemplate; cluster backend receives the same inlined Steps; resolver-form refs (hub/git/cluster/bundles) deferred to Track 1 #9. |
| 9 | **Resolvers** (git / hub / cluster / bundles / remote) | Largest user-impact item — most catalog usage references `taskRef` from a hub. | Done in v1.6 (PR for `feat: ship Track 1 #9 Resolvers — offline + cache subcommands (Phase 6)`). All six phases shipped: Phase 1 (types + lazy-dispatch + cluster inline + validator + events + CLI flag scaffolding), Phase 2 (direct `git` resolver via go-git/v5; cross-backend `resolver-git` e2e fixture), Phase 3 (`hub` + `http` direct resolvers; cross-backend e2e fixtures `resolver-http`, `resolver-hub`), Phase 4 (direct `bundles` resolver via go-containerregistry + off-by-default direct `cluster` resolver via the kube dynamic client; cross-backend `resolver-bundles` e2e fixture; new `--cluster-resolver-context` / `--cluster-resolver-kubeconfig` flags), Phase 5 (Mode B remote resolver via `resolution.tekton.dev/v1beta1.ResolutionRequest` CRD with v1alpha1 fallback; new `--remote-resolver-context` / `--remote-resolver-namespace` / `--remote-resolver-timeout` flags; `resolver-remote` cluster e2e fixture; `defer Delete(...)` via `context.Background()` so SIGINT mid-resolution still triggers cleanup), and Phase 6 (`--offline` end-to-end via the validator's CacheCheck callback wired to a shared on-disk DiskCache; `tkn-act cache list / prune --older-than / clear -y` subcommands; cross-run disk-cache hits surfaced as `cached:true` on `resolver-end` and as `(cached)` in pretty output for every direct resolver). |

---

## Track 2 — Backend parity (current backends first)

> **Status (2026-05-13):** Complete. All five items below shipped
> between v1.2 and v1.6. As of PR #35 the `cluster-integration`
> workflow also runs a Tekton-LTS matrix (`v1.3.0`, `v1.12.0`), so a
> future Tekton bump cannot silently regress the prior LTS. New work
> proceeds on the natural follow-ups listed under the table.

Originally: the project has two backends — `docker` (default) and
`cluster` (k3d). The v1.2 work shipped six new docker-backend
features; the `cluster-integration` workflow only exercised the
`hello` fixture, so "the same pipeline behaves the same way on both
backends" was a claim, not a tested invariant. Closing that gap was
prioritized before adding new backends.

| # | Item | Why it matters |
|---|---|---|
| 1 | **Cross-execute every v1.2 fixture under `--cluster`.** Same fixture descriptor, same expected outcome, run twice (once per backend). | Done in v1.2 (PR #6 `feat(cluster): cross-backend fidelity for v1.2 features`). The `cluster-integration` workflow now drives the full `internal/e2e/fixtures.All()` table. As of 2026-05-13 the workflow also runs a Tekton-LTS matrix (`v1.3.0`, `v1.12.0`) per [`docs/superpowers/specs/2026-05-13-tekton-bump-and-matrix-design.md`](superpowers/specs/2026-05-13-tekton-bump-and-matrix-design.md). |
| 2 | **Map Tekton terminal conditions to our task statuses.** `internal/backend/cluster/run.go:watchPipelineRun` only checks `Succeeded={True,False}`. Tekton sets `Reason: PipelineRunTimeout` / `TaskRunTimeout` distinctly; we should map those to status `timeout` so the parity tests pass. | Done in v1.2 (PRs #9 `feat: honor Pipeline.spec.timeouts.{pipeline,tasks,finally}` introduced `mapPipelineRunStatus` / `mapTaskRunStatus`; PR #13 `fix(cluster): pipeline-timeout misclassified as failed via skippedTasks path` added the `anySkippedDueToTimeout` fallback for the case where Tekton folds the timeout into `reason: Failed`). |
| 3 | **Apply ConfigMap / Secret resources into the run namespace before submission.** When a Task uses `volumes: [configMap]`, the cluster backend currently does nothing — Tekton then fails because the named ConfigMap doesn't exist. Source the bytes from the same `volumes.Store` the docker backend uses, then `kubectl apply` an ephemeral ConfigMap into the per-run namespace. | Done in v1.2 (`internal/backend/cluster/volumes.go`, `applyVolumeSources` called from `RunPipeline`). The `volumes/` fixture now passes on both backends. |
| 4 | **Cluster-side `task-retry` events.** The cluster backend delegates to Tekton's retry impl; we currently emit nothing equivalent to the docker policy loop's `task-retry`. Watch TaskRun status and emit one `task-retry` per Tekton retry attempt. | Done in v1.2 (PR #6, `RetryAttempts` on `TaskOutcomeOnCluster` plus engine emission of `task-retry` events per attempt). |
| 5 | **`displayName` parity.** When we add it (Track 1 #7), surface it the same way in pretty + JSON regardless of backend. | Done in v1.5; cross-backend assertion in the `display-name-description` fixture. |

Track 2 is complete as of 2026-05-13. Natural follow-ups are *new*
backends (in priority order):

1. **`--cluster-driver=kind`** alongside k3d. The v1.1 spec already
   named this as v1.2 work; the `Driver` interface exists.
2. **`podman` as a docker-compatible alternative**. Many CI runners
   ship podman by default.

Both are out of scope for the current short-term cycle.

---

## How this interacts with the contribution rule

Every track item ships behind the existing tests-required rule
(`AGENTS.md`): each feature lands with unit tests, a fixture under
`testdata/e2e/` if it's user-visible, and (Track 2) a cross-backend
assertion. Track 2 #1 is the unlock for treating "this feature works"
as a checkable invariant rather than an aspiration.
