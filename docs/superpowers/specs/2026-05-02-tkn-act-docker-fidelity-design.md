# tkn-act docker-backend fidelity (v1.2) — design spec

**Date:** 2026-05-02
**Status:** Approved (extends v1 spec, walks back v1 non-goals)
**One-liner:** Add the most-asked-for Tekton features that the v1 design intentionally punted, so persona A can stay in fast Docker mode for many more real-world pipelines.

This spec extends `2026-05-01-tkn-act-design.md`. Read that first.

---

## 1. Goals & non-goals

### Goals
- Six of the seven docker-backend limitations documented in `testdata/limitations/` become supported under `tkn-act` (default Docker mode), so the corresponding fixtures graduate from `testdata/limitations/` into `testdata/e2e/`.
- The agent contract — `--output json`, exit codes, `tkn-act help-json` — gains *additive* changes only. No existing field is renamed or has its type changed.
- Cluster mode (`--cluster`) is unaffected; it already supported these features by virtue of running real Tekton.

### Non-goals (v1.2)
- **Sidecars.** Shared network namespaces are fundamentally Kubernetes-shaped; users who need them stay on `--cluster`.
- **Step-state isolation foot-gun.** Each step is a separate container by design; the fixture stays in `testdata/limitations/` as documentation.
- **Kubernetes-y volume kinds:** `csi`, `projected`, `persistentVolumeClaim`, `downwardAPI`, etc. The validator now rejects them with a clear error (exit 4) instead of silently dropping.
- **Loading data from `kind: ConfigMap` / `kind: Secret` YAML manifests passed to `tkn-act -f`.** Deferred to v1.3.
- **Catalog-Hub StepActions, custom tasks, resolvers.** Still out of scope.

### Personas
Same as v1. Persona A (app developer in Docker mode) is the primary beneficiary of this work.

---

## 2. Features in scope

### 2.1 Engine policy (Phase 1)
| Field | Modeled on | Behavior |
|---|---|---|
| `Step.onError` | `tekton.dev/v1` | `continue` lets a non-zero step exit be recorded as `failed` but not fail the surrounding Task. Unset or `stopAndFail` (default) preserves today's short-circuit. |
| `PipelineTask.retries` | `tekton.dev/v1` | Integer `>=0`. Engine retries the Task that many times after a `failed`/`infrafailed` outcome. `skipped`/`not-run` are not retried. |
| `Task.timeout` (under `spec:`) | `tekton.dev/v1` | Go `time.Duration` parseable string (e.g. `30s`, `5m`). Engine wraps the Task in a `context.WithTimeout`; on expiry the Task ends `timeout`. |

### 2.2 Per-Step results (Phase 2)
| Field | Modeled on | Behavior |
|---|---|---|
| `Step.results` | `tekton.dev/v1` | Per-step results, mounted at `/tekton/steps/<step>/results/<name>` inside that step's container, **and the same dir mounted read-only into all later steps in the same Task**. Resolver substitutes `$(step.results.<name>.path)` (current step's own path) and `$(steps.<earlier-step>.results.<name>)` (literal value of a previously-finished step's result). Cross-Task `$(steps...)` references are still resolved through the existing Task-level `results:` block. |

### 2.3 Local-friendly volumes (Phase 3)
`TaskSpec.volumes` and `Step.volumeMounts` are parsed and honored for four kinds:

| Kind | Source on the host |
|---|---|
| `emptyDir` | Fresh per-Task tmpdir (under workspace cache). `medium: Memory` is honored on Linux as `tmpfs`; ignored on macOS. |
| `hostPath` | Literal `path` on host. Validator warns if path doesn't exist (still mounts). |
| `configMap` | Bytes resolved at run-time: see CLI flags below. Mounted as a directory; one file per key. |
| `secret` | Same as `configMap`, just under a separate flag namespace. |

CLI surface:

| Flag | Repeatable | Meaning |
|---|---|---|
| `--configmap-dir <path>` | no | Directory layout: `<path>/<name>/<key>` is the file used for ConfigMap `name`'s `key`. Default `$XDG_CACHE_HOME/tkn-act/configmaps`. |
| `--secret-dir <path>` | no | Same shape; default `$XDG_CACHE_HOME/tkn-act/secrets`. |
| `--configmap <name>=<k1>=<v1>,<k2>=<v2>` | yes | Inline override. Beats `--configmap-dir` for that `name`. |
| `--secret <name>=<k1>=<v1>,…` | yes | Same shape. |

Any volume kind not in the table above is rejected by the validator with a Validate error (exit 4) and a message naming the unsupported kind.

---

## 3. Agent-contract additions (additive only)

| Surface | Change |
|---|---|
| Event kinds | New `task-retry` event between attempts. Payload: `task`, `attempt: N` (1-based, the attempt that just failed), `exitCode`, `durationMs`, `message`. The terminal `task-end` carries the final outcome and a new `attempts: N` field. |
| Task statuses | New `timeout` status on `task-end`, distinct from `failed`. Existing `succeeded`/`failed`/`skipped`/`not-run`/`infrafailed` unchanged. |
| Exit codes | New code `6` named `timeout` — emitted when at least one Task ended `timeout`. Code `5` (pipeline) still covers ordinary `failed`. The doctor / validate / list / version / help-json shapes are unchanged. |
| `help-json` | Surfaces the new flags (`--configmap-dir`, `--secret-dir`, `--configmap`, `--secret`) and the new exit code automatically. |
| `agent-guide` (AGENTS.md) | New section "Supported Tekton features (v1.2)" listing the in-scope features and explicitly flagging sidecars + step-state as "use --cluster" or "by design." |

---

## 4. Architecture

The v1 unit boundaries stay intact. Three engine extensions and one CLI extension:

```
internal/tektontypes/      types.go: + Step.OnError, Step.Results,
                                       PipelineTask.Retries,
                                       TaskSpec.Timeout,
                                       TaskSpec.Volumes, Step.VolumeMounts

internal/engine/           engine.go: retry/timeout loop wrapping runOne
                           policy.go (new): onError → status, retries → loop,
                                            timeout → context wrapping

internal/resolver/         resolver.go: + $(steps.<name>.results.<name>),
                                          $(step.results.<name>.path)

internal/backend/docker/   docker.go: per-step results dir mounting,
                                       volumes resolution + per-volumeMount
                                       binds, onError → step status only

internal/volumes/          new package: ResolveTaskVolumes(task, taskRunDir,
                                       cmStore, secStore) → map[name]hostPath
                           store.go:    file-tree-backed lookup + inline override

internal/validator/        validator.go: + reject unsupported volume kinds,
                                          + parse-check timeout strings,
                                          + reject negative retries

internal/reporter/         event.go: + EvtTaskRetry, status "timeout"
internal/exitcode/         exitcode.go: + Timeout = 6
```

### Data flow — engine policy (Phase 1)

For each PipelineTask, the engine now wraps the existing `runOne` call in a small policy loop:

```
for attempt := 1; attempt <= 1+retries; attempt++ {
    ctx2, cancel := withTaskTimeout(ctx, taskTimeout)
    outcome := runOne(ctx2)
    cancel()
    if outcome.Status == TaskTimedOut          { break }     // do not retry
    if outcome.Status == TaskSucceeded         { break }
    if outcome.Status == TaskSkipped           { break }
    if attempt <= retries {
        emit(EvtTaskRetry, attempt, outcome)
        continue
    }
    break
}
emit(EvtTaskEnd, ..., attempts: attempt)
```

`onError` is handled inside `runOne`, not in the policy loop: a continue-step that exits non-zero records `step-end{status: failed, exitCode: N}` but does not return early; the Task continues to the next step. The Task's final status is `succeeded` iff every non-continue step succeeded.

### Data flow — per-step results (Phase 2)

The docker backend was already mounting one Task-level `/tekton/results/` dir into every step. We add a per-step subdir:

```
<resultsHost>/                 <- Task-level (existing)
  <result-name>                <- written by any step
  steps/
    <step-name-1>/
       <result-name>           <- step-1's own result file
    <step-name-2>/
       <result-name>           <- step-2's own
```

Each step container gets:
- `/tekton/results` -> `<resultsHost>` (Task-level, RW; existing).
- `/tekton/steps/<this-step>/results` -> `<resultsHost>/steps/<this-step>` (RW, only this step's results).
- `/tekton/steps/<earlier-step>/results` -> `<resultsHost>/steps/<earlier-step>` (RO) for every earlier step in the same Task.

Resolver gets two new substitution sources, scoped to one Task:
- `$(step.results.<name>.path)` -> `/tekton/steps/<current-step>/results/<name>`. Requires knowing which step is being resolved; the engine substitutes step-by-step, just before container creation.
- `$(steps.<step>.results.<name>)` -> the contents of `<resultsHost>/steps/<step>/<name>`, read after that step finishes and before the next step starts.

### Data flow — volumes (Phase 3)

A new `internal/volumes` package owns volume materialization:

```
type Store struct { dir string; inline map[string]map[string][]byte }
func (Store) Materialize(name string) (hostPath string, err error)
```

Two `Store`s are constructed by the CLI: one for ConfigMaps, one for Secrets. Each takes the `--*-dir` path and an inline override map built from repeated `--configmap` / `--secret` flags.

For each Task, the engine calls `ResolveTaskVolumes(task, runDir, cmStore, secStore) -> map[name]hostPath`. The docker backend then iterates each step's `volumeMounts` and adds a bind mount per `(volumeName -> hostPath, mountPath, readOnly, subPath)`.

Validator rejects unsupported kinds at parse time so the user sees the error before any container is created.

---

## 5. Error handling

| Scenario | Exit code | Visible in JSON |
|---|---|---|
| Bad timeout duration string | 4 (validate) | `validate -o json` errors array |
| Negative retries | 4 (validate) | same |
| Unknown volume kind on a Task | 4 (validate) | same |
| ConfigMap/Secret name referenced but not findable | 4 (validate, before run) — emitted as a synthetic check during run setup before any container starts | error message names the missing source, suggests `--configmap` / `--configmap-dir` |
| Step container times out at the Task's wall-clock limit | 6 (timeout) | `task-end{status: timeout, exitCode: 137 (SIGKILL) or 0}` |
| Non-continue step exits non-zero on retry attempt 2/3 | 5 (pipeline) only if final attempt fails too; intermediate emitted as `task-retry` | `task-retry{attempt, exitCode, message}` events between attempts |

---

## 6. Testing

Each phase ships:

1. **Unit tests** at the package nearest the change (resolver, volumes, engine policy). Run by default; no docker.
2. **A graduated fixture** under `testdata/e2e/<feature>/` that exercises the feature end-to-end via the docker backend (gated by `-tags integration`). The corresponding entry in `testdata/limitations/` is removed in the same commit.
3. **A help-json assertion** when new flags are introduced.

Test fixtures graduating in this work:

| From `testdata/limitations/` | To `testdata/e2e/` |
|---|---|
| `onerror-continue/` | `onerror/` |
| `retries/` | `retries/` |
| `timeout/` | `timeout/` |
| `step-results/` | `step-results/` |
| `step-volumes/` | `volumes/` |
| `sidecars/` | (stays — non-goal) |
| `step-state/` | (stays — by design) |

---

## 7. CI

A new `.github/workflows/ci.yml`:
- Runs on `push` to `main` and on every PR.
- Steps: `go build ./...`, `go vet ./...`, `go test -race -count=1 ./...` on Linux + macOS.
- A separate `tests-required` job runs against the PR diff: any change touching `**/*.go` outside `_test.go` files must be accompanied by a matching `_test.go` change in the same PR. Override with the literal string `[skip-test-check]` in any commit message in the PR.

The "tests required" rule is also documented in AGENTS.md so contributors and AI agents working on this repo know to add a test for every code change.

---

## 8. Phasing

One PR, one branch, three commits ordered:

1. `feat(engine): onError, retries, timeout` — phase 1 + agent-contract additions + new exit code 6 + `task-retry` event + AGENTS.md update + graduated fixtures.
2. `feat(steps): per-step results` — phase 2 + resolver + docker backend mounts + graduated fixture.
3. `feat(volumes): emptyDir, hostPath, configMap, secret` — phase 3 + new CLI flags + validator rejection of other kinds + graduated fixture.
4. `ci: add GitHub Actions workflow + tests-required rule` — final commit.

Each commit's tests pass on its own (the PR is bisectable).
