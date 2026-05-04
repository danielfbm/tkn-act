# Tekton feature parity

Last updated: 2026-05-03.

This document is the **canonical scoreboard** for what `tkn-act` does and
doesn't support from upstream Tekton, and what proves it. It's read by
humans (to decide what to pick up) and by CI (`.github/scripts/parity-check.sh`,
run as the `parity-check` job in `.github/workflows/ci.yml`).

If you change a feature's status, you change this table. The CI check
ensures the table doesn't lie:

1. Every row marked `shipped` MUST have a fixture under `testdata/e2e/`
   listed in the `e2e fixture` column, AND that fixture MUST appear in
   `internal/e2e/fixtures.All()` so it runs on **both** backends.
2. Every row marked `shipped` MUST NOT have a `testdata/limitations/`
   fixture (graduation rule — when a feature is supported, the
   limitation example is removed in the same PR).
3. Every row marked `gap` or `in-progress` whose `limitations fixture`
   column names a directory MUST have that directory exist on disk.
4. Every directory under `testdata/limitations/` MUST be referenced by
   at least one `gap` / `in-progress` row (no orphan limitations).

A failing `parity-check` blocks the PR the same way `tests-required` does.

## Status legend

| Status | Meaning |
|---|---|
| `shipped` | Implemented and tested on the listed backend(s); cross-backend fixture present. |
| `in-progress` | A plan exists under `docs/superpowers/plans/` and a PR is in flight. |
| `gap` | Not yet implemented. Either a `testdata/limitations/` fixture documents the gap, or the feature has no fixture at all (TBD). |
| `out-of-scope` | Intentionally not supported (e.g. macOS-only behaviors); see notes. |

The `backends` column tells you whether the feature works under
`--docker`, `--cluster`, or both. Unless otherwise noted, `shipped` means
**both** backends behave identically (verified by
`internal/e2e/fixtures.All()` running each fixture through both
harnesses).

## Per-feature workflow (every contributor reads this)

Picking a feature off the `gap` list:

1. Open a worktree and a feature branch (per `superpowers:using-git-worktrees`).
2. Write/update the spec under `docs/superpowers/specs/` if the feature
   is non-trivial (anything beyond a one-file change).
3. Write the plan under `docs/superpowers/plans/`. Every plan ends with
   a "Doc convergence" task that touches:
   - This file (`docs/feature-parity.md`) — flip the row's status,
     populate `e2e fixture`, clear `limitations fixture`, link the PR.
   - `docs/short-term-goals.md` — move the row out of Track 1 / Track 2.
   - `docs/test-coverage.md` — list the new fixture under the right tag.
   - `AGENTS.md` and `cmd/tkn-act/agentguide_data.md` — note the new
     capability (keep these two in sync).
4. Implement using TDD; add the e2e fixture under `testdata/e2e/<name>/`
   and the descriptor entry in `internal/e2e/fixtures.All()` so it runs
   on **both** backends.
5. **Delete** the corresponding `testdata/limitations/<name>/` directory
   in the same PR. The `parity-check` enforces it.
6. Run the local verification:
   ```sh
   go vet ./... && go vet -tags integration ./... && go vet -tags cluster ./...
   go build ./...
   go test -race -count=1 ./...
   bash .github/scripts/parity-check.sh
   .github/scripts/tests-required.sh main HEAD
   ```
7. Open the PR, link the plan + spec in the description, merge per
   project default (`gh pr merge <num> --squash --delete-branch`).

---

## Parity table

The columns:

- **Feature** — the Tekton concept name. Where multiple sub-features
  exist (e.g. four volume kinds), one row per sub-feature.
- **Spec field** — the `Pipeline` / `Task` / `PipelineRun` field that
  expresses it.
- **Status** — `shipped` / `in-progress` / `gap` / `out-of-scope`.
- **Backends** — `docker` / `cluster` / `both`.
- **e2e fixture** — directory under `testdata/e2e/`, or `none`.
- **Limitations fixture** — directory under `testdata/limitations/`, or
  `none`. Populated only when status is `gap` / `in-progress` AND we
  ship an example pipeline that documents the gap.
- **Plan / Spec / PR** — link to the design doc, plan, or merged PR.

### Core types & parsing

| Feature | Spec field | Status | Backends | e2e fixture | Limitations fixture | Plan / Spec / PR |
|---|---|---|---|---|---|---|
| Type model: Task, Pipeline, PipelineRun (v1) | `tekton.dev/v1` | shipped | both | hello | none | docs/superpowers/specs/2026-05-01-tkn-act-design.md |
| Multi-doc YAML loader | `-f` | shipped | both | hello | none | docs/superpowers/specs/2026-05-01-tkn-act-design.md |
| Loading `kind: ConfigMap` / `kind: Secret` from `-f` | `-f` | shipped | both | configmap-from-yaml | none | docs/superpowers/plans/2026-05-03-cm-secret-from-yaml.md (Track 1 #5); also covered by `secret-from-yaml` |

### DAG, params, results

| Feature | Spec field | Status | Backends | e2e fixture | Limitations fixture | Plan / Spec / PR |
|---|---|---|---|---|---|---|
| `params` (string / array / object) | `Pipeline.spec.params`, `Task.spec.params` | shipped | both | params-and-results | none | docs/superpowers/specs/2026-05-01-tkn-act-design.md |
| `runAfter` DAG with topological ordering | `PipelineTask.runAfter` | shipped | both | params-and-results | none | docs/superpowers/specs/2026-05-01-tkn-act-design.md |
| `when` expressions | `PipelineTask.when` | shipped | both | when-and-finally | none | docs/superpowers/specs/2026-05-01-tkn-act-design.md |
| `finally` tasks | `Pipeline.spec.finally` | shipped | both | when-and-finally | none | docs/superpowers/specs/2026-05-01-tkn-act-design.md |
| Per-Task `results` | `Task.spec.results` | shipped | both | params-and-results | none | docs/superpowers/specs/2026-05-01-tkn-act-design.md |
| Per-Step `results` | `Step.results` | shipped | both | step-results | none | docs/superpowers/specs/2026-05-02-tkn-act-docker-fidelity-design.md |
| Pipeline-level `results` (surfaced on run-end) | `Pipeline.spec.results` | shipped | both | pipeline-results | none | docs/superpowers/plans/2026-05-03-pipeline-results.md (Track 1 #6) |
| Param substitution: `$(params.X)`, `$(tasks.X.results.Y)`, `$(context.*)`, `$(workspaces.X.path)`, `$(step.results.X.path)` | n/a | shipped | both | params-and-results | none | docs/superpowers/specs/2026-05-01-tkn-act-design.md |

### Workspaces & volumes

| Feature | Spec field | Status | Backends | e2e fixture | Limitations fixture | Plan / Spec / PR |
|---|---|---|---|---|---|---|
| Workspaces (PVC on cluster, bind-mount on docker) | `Pipeline.spec.workspaces`, `PipelineTask.workspaces` | shipped | both | workspaces | none | docs/superpowers/specs/2026-05-01-tkn-act-design.md |
| `volumes.emptyDir` | `Task.spec.volumes` | shipped | both | volumes | none | docs/superpowers/specs/2026-05-02-tkn-act-docker-fidelity-design.md |
| `volumes.hostPath` | `Task.spec.volumes` | shipped | both | volumes | none | docs/superpowers/specs/2026-05-02-tkn-act-docker-fidelity-design.md |
| `volumes.configMap` | `Task.spec.volumes` | shipped | both | volumes | none | docs/superpowers/specs/2026-05-02-tkn-act-docker-fidelity-design.md |
| `volumes.secret` | `Task.spec.volumes` | shipped | both | volumes | none | docs/superpowers/specs/2026-05-02-tkn-act-docker-fidelity-design.md |
| `volumes.csi` / `projected` / `persistentVolumeClaim` / `downwardAPI` | `Task.spec.volumes` | out-of-scope | n/a | none | none | Validator rejects with exit 4. |

### Task policy (per-Task)

| Feature | Spec field | Status | Backends | e2e fixture | Limitations fixture | Plan / Spec / PR |
|---|---|---|---|---|---|---|
| `Step.onError: continue` | `Step.onError` | shipped | both | onerror | none | docs/superpowers/specs/2026-05-02-tkn-act-docker-fidelity-design.md |
| `PipelineTask.retries` | `PipelineTask.retries` | shipped | both | retries | none | docs/superpowers/specs/2026-05-02-tkn-act-docker-fidelity-design.md |
| `Task.spec.timeout` (per-task wall clock) | `TaskSpec.timeout` | shipped | both | timeout | none | docs/superpowers/specs/2026-05-02-tkn-act-docker-fidelity-design.md |

### Pipeline policy

| Feature | Spec field | Status | Backends | e2e fixture | Limitations fixture | Plan / Spec / PR |
|---|---|---|---|---|---|---|
| `Pipeline.spec.timeouts.{pipeline,tasks,finally}` | `PipelineSpec.timeouts` | shipped | both | pipeline-timeout | none | docs/superpowers/plans/2026-05-03-pipelinerun-timeouts.md (Track 1 #2); also covered by `tasks-timeout`, `finally-timeout` |
| `PipelineTask.matrix` (parameter-matrix fan-out) | `PipelineTask.matrix` | gap | both | none | none | docs/short-term-goals.md (Track 1 #3) |

### Task structure

| Feature | Spec field | Status | Backends | e2e fixture | Limitations fixture | Plan / Spec / PR |
|---|---|---|---|---|---|---|
| Steps (one container per step) | `Task.spec.steps` | shipped | both | hello | none | docs/superpowers/specs/2026-05-01-tkn-act-design.md |
| Step state isolation (cwd / env / `/tmp`) | n/a — fundamental | by-design | both | none | step-state | This is correct Tekton behavior; the limitations example documents the foot-gun. |
| `Task.stepTemplate` | `TaskSpec.stepTemplate` | shipped | both | step-template | none | docs/superpowers/plans/2026-05-03-step-template.md (Track 1 #4) |
| `Task.sidecars` (long-lived helper containers) | `Task.spec.sidecars` | shipped | both | sidecars | none | docs/superpowers/plans/2026-05-04-task-sidecars.md (Track 1 #1) |
| `displayName` / `description` on Task / Pipeline / Step | various | shipped | both | display-name-description | none | docs/superpowers/plans/2026-05-04-display-name-description.md (Track 1 #7) |

### Resolution & catalogs

| Feature | Spec field | Status | Backends | e2e fixture | Limitations fixture | Plan / Spec / PR |
|---|---|---|---|---|---|---|
| `taskRef.name` (in-bundle reference) | `PipelineTask.taskRef` | shipped | both | hello | none | docs/superpowers/specs/2026-05-01-tkn-act-design.md |
| `StepActions` (`tekton.dev/v1beta1` referenceable steps) | `Step.ref` | gap | both | none | none | docs/short-term-goals.md (Track 1 #8) |
| Resolvers — git / hub / cluster / bundles | `taskRef.resolver` | in-progress | both | none | none | docs/superpowers/plans/2026-05-04-resolvers.md (Track 1 #9) — Phase 1 (types + lazy-dispatch + cluster inline + validator + events + CLI flags) shipped; concrete resolvers (Phase 2-4), remote ResolutionRequest driver (Phase 5), and offline cache polish (Phase 6) tracked in the plan |
| Custom Tasks (`taskRef.apiVersion != tekton.dev/v1`) | `PipelineTask.taskRef.apiVersion` | out-of-scope | n/a | none | none | v1 spec non-goal. |
| Tekton Chains (signing) | n/a | out-of-scope | n/a | none | none | v1 spec non-goal. |
| Tekton Results (long-term storage) | n/a | out-of-scope | n/a | none | none | v1 spec non-goal. |

### Backends

| Feature | Status | Backends | Notes |
|---|---|---|---|
| `--docker` backend | shipped | docker | Default. Fast; one container per step. |
| `--cluster` backend (k3d driver) | shipped | cluster | Real Tekton controller in an ephemeral k3d cluster. |
| `--cluster-driver=kind` | gap | cluster | Track 2 follow-up; `Driver` interface exists. |
| `podman` as docker-compatible alternative | gap | docker | Many CI runners ship podman by default. |
| Cross-backend fixture parity | shipped | both | Every fixture in `testdata/e2e/` runs on both backends via `internal/e2e/fixtures.All()`. |

### Agent contract (machine interfaces)

| Feature | Status | Notes |
|---|---|---|
| `tkn-act doctor -o json` | shipped | Stable shape. |
| `tkn-act help-json` | shipped | Stable shape. |
| `tkn-act agent-guide` | shipped | Embeds `cmd/tkn-act/agentguide_data.md`. |
| `tkn-act run -o json` event stream | shipped | Stable kinds: `run-start`, `run-end`, `task-start`, `task-end`, `task-skip`, `task-retry`, `step-start`, `step-end`, `step-log`, `error`, `resolver-start`, `resolver-end`. The two `resolver-*` kinds are additive (Track 1 #9 Phase 1); agents that don't recognize them ignore them. |
| Stable exit codes (0,1,2,3,4,5,6,130) | shipped | See `internal/exitcode/exitcode.go`. |
| `task-retry` events from cluster mode | shipped | Post-hoc from TaskRun.status.retriesStatus; PR #6. |

---

## How `parity-check` reads this file

The script greps each table row for the columns it needs. The format is a
strict GitHub-flavored markdown table; do not split a row across lines or
add inline HTML. Specifically:

- A feature row is recognized by having at least 7 pipe-separated columns
  with the column 3 cell matching `shipped`, `in-progress`, `gap`,
  `out-of-scope`, or `by-design`.
- Column 5 is the e2e fixture name (or `none`).
- Column 6 is the limitations fixture name (or `none`).
- Column 7 is the plan/spec/PR link (free-form, not validated by the
  check beyond "non-empty").

If you add a new feature row, follow the same column count and order. To
add an entirely new feature category section, add a `### <Category>` header
before the table; the script ignores headers.
