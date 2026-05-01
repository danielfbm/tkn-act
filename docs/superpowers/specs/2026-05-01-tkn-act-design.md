# tkn-act — design spec (v1)

**Date:** 2026-05-01
**Status:** Approved for implementation
**One-liner:** A CLI that runs Tekton Pipelines locally — Docker-by-default with an opt-in ephemeral k3d backend — so developers can debug and iterate on `pipeline.yaml` without owning a Kubernetes cluster.

---

## 1. Goals, non-goals, personas

### Goals
- Run real Tekton `Task` / `Pipeline` YAML locally with **no Kubernetes cluster required** (Docker default), with an opt-in ephemeral k3d backend for cluster-fidelity runs.
- Make the inner loop fast enough that "edit → re-run → inspect logs" feels as snappy as `nektos/act`.
- Behave faithfully enough that a pipeline that passes locally has very high odds of passing on a real Tekton cluster.
- Ship as a single static Go binary, installable via `brew`, `go install`, or as a `tkn` plugin (because the binary is named `tkn-act`, `tkn` discovers it on `$PATH` automatically).

### Non-goals (v1, explicitly punted)
- Sidecar containers
- `StepActions`
- Step retries
- `tekton-results` / `tekton-chains` integration
- Custom tasks / custom-task controllers
- `Resolvers` (git / hub / cluster / bundles) — `taskRef` only resolves names from the YAML files passed in
- Workspace `volumeClaimTemplate`
- Signed pipelines
- Replacing `tkn` for cluster-side operations
- Authoring UI / TUI — terminal output only in v1
- Windows support in v1 (Linux + macOS only)

### Personas
- **A — App developer.** Has a `pipeline.yaml` in their repo, no cluster, wants `tkn-act` to "just work" with zero args. Optimizes for: zero-setup, fast feedback, friendly errors.
- **B — Catalog / platform engineer.** Maintains shared Tasks/Pipelines, wants to test changes locally with high fidelity before promoting to a real cluster; reaches for `--cluster` when fidelity matters.

### Supported Tekton features (v1)
- API: `tekton.dev/v1` (`Task`, `TaskRun`, `Pipeline`, `PipelineRun`)
- **Steps**: `image`, `command`, `args`, `script`, `env`, `workingDir`
- **Params**: `string`, `array`, `object` types; default values; type validation; `$(params.x)` substitution (including `$(params.x[*])` array expansion and `$(params.obj.key)`)
- **Results**: file-based at `/tekton/results/` inside step containers; `$(tasks.<task>.results.<result>)` substitution between pipeline tasks (creates implicit DAG edges)
- **Workspaces**: declared on Task/Pipeline; bound on PipelineRun; shared across tasks; bind-mounted on the Docker backend
- **Pipeline ordering**: `runAfter` + implicit edges from result-data dependencies
- **`when` expressions**: input/operator/values; CEL not supported in v1 (Tekton's classic when expressions only)
- **`finally` tasks**: always run after main DAG, can read `$(tasks.<task>.status)`

---

## 2. Architecture

The system splits into focused units with one job each, communicating across narrow interfaces.

```
                ┌──────────────────────────────┐
                │            cmd/              │  thin Cobra commands
                │ (root, run, list, version,   │  → call into engine
                │  cluster, validate)          │
                └──────────────┬───────────────┘
                               │
   ┌───────────────────────────┴───────────────────────────┐
   │                       internal/                        │
   │                                                        │
   │  loader/   discovery/     resolver/   validator/       │
   │   parse     find files     param/      schema +        │
   │   YAML →    in repo,       result      semantic        │
   │   typed     pick default   substitution checks         │
   │   structs                                              │
   │                                                        │
   │           ┌──────────────────────────────┐            │
   │           │           engine/            │            │
   │           │   orchestrates a PipelineRun │            │
   │           │   - DAG build + topo sort    │            │
   │           │   - when / finally semantics │            │
   │           │   - workspace lifecycle      │            │
   │           │   - delegates Step exec to   │            │
   │           │     a Backend                │            │
   │           └──────────────┬───────────────┘            │
   │                          │ Backend interface          │
   │           ┌──────────────┴───────────────┐            │
   │           │                              │            │
   │     backend/docker/             backend/k3d/          │
   │     each Step → container       lazy-start k3d,       │
   │     workspaces → bind mount     install Tekton,       │
   │     results → host tmpdir       kubectl apply,        │
   │                                  watch PipelineRun    │
   │                                                        │
   │     reporter/                                          │
   │     pretty step/task tree, log streaming,             │
   │     final summary, JSON mode for scripting            │
   └────────────────────────────────────────────────────────┘
```

### Key boundaries

- **`Backend` interface is the critical seam.** The engine knows nothing about Docker or k3d; it asks a `Backend` to "run this Task with these inputs and give me back results + exit code." Adding new backends later (Podman, embedded controller) is a matter of implementing one interface.
- **Loader / validator / engine separation.** Parsing and validation never run containers; the engine never parses YAML. This is the boundary that makes unit-testing the orchestration logic trivial — feed in typed structs, assert on backend calls.
- **Reporter is sink-only.** Engine emits structured events; reporter formats them. JSON mode (`--output json`) falls out for free.
- **`tektoncd/pipeline` upstream types are vendored** in `loader/` for parsing. We get spec compliance for the schema without owning it.

### File layout

```
cmd/tkn-act/main.go
cmd/tkn-act/root.go              cobra root, global flags
cmd/tkn-act/run.go                run / list / validate / version / cluster
internal/loader/                  YAML → typed structs (vendored Tekton v1 types)
internal/discovery/               find pipeline files in repo, default selection
internal/resolver/                $(params.x), $(tasks.X.results.Y) substitution
internal/validator/               schema + semantic checks (DAG cycles, missing refs)
internal/engine/                  PipelineRun orchestration, when/finally
internal/engine/dag/              graph build + topo sort + cycle detect
internal/backend/                 Backend interface + shared types
internal/backend/docker/          Docker backend
internal/backend/k3d/             k3d backend
internal/reporter/                event sink: pretty + json
internal/workspace/               workspace lifecycle (tmpdirs, cleanup)
testdata/                         golden pipelines for integration tests
```

### Backend interface (sketch)

```go
type Backend interface {
    Prepare(ctx context.Context, run RunSpec) error      // pull images, create network
    RunTask(ctx context.Context, task TaskSpec, ws Workspaces) (TaskResult, error)
    Cleanup(ctx context.Context) error
}
```

The engine drives one `Backend` per run. The Docker backend runs each Step as `docker run`; the k3d backend submits a real `PipelineRun` and watches it. From the engine's perspective they're indistinguishable.

---

## 3. Data flow: a single `tkn-act` run

### 3.1 Discovery & loading
1. `cmd/run` resolves inputs:
   - If `-f <file>` given → use it directly.
   - Else `discovery/` walks the repo for `pipeline.yaml`, `pipelinerun.yaml`, `.tekton/*.yaml`, `tekton/*.yaml` (in priority order). Multiple Pipelines → error: `use -p <name> or tkn-act list`.
   - If a `PipelineRun` is found, it carries params/workspaces — use it. Otherwise we synthesize a `PipelineRun` from the `Pipeline` + CLI flags (`-p key=val`, `-w name=./path`).
2. `loader/` decodes YAML into vendored upstream types. Multi-doc YAML supported. External `taskRef.name` resolved by name from the same set of loaded files. Unresolved refs → loud error.
3. `validator/` runs schema + semantic checks: param presence/type, workspace declarations match bindings, DAG has no cycles, `runAfter` refs exist, `when` expressions parse, `finally` task names don't collide with main DAG.

### 3.2 Planning
4. `engine/dag/` builds the DAG: nodes = `PipelineTask`s, edges = explicit `runAfter` + implicit edges from result-data dependencies. Cycle detect → fail. Topo sort produces execution levels (parallelizable batches).
5. `workspace/` materializes declared workspaces: each becomes a host tmpdir under `$XDG_CACHE_HOME/tkn-act/<run-id>/workspaces/<name>`, overridable with `-w name=./path` for persistent dirs the user wants to inspect.

### 3.3 Execution (Docker backend)
6. `engine/` walks the DAG level by level. For each `PipelineTask`:
   - Evaluate `when` expressions with current params + accumulated results. Skipped → emit "skipped" event, mark results as missing for downstream `when` evaluation.
   - Resolve params: `resolver/` substitutes `$(params.x)` and `$(tasks.X.results.Y)` against the typed-struct context, returning a fully-baked `TaskRun` spec.
   - Hand the `TaskRun` to the `Backend`. For Docker:
     - Allocate a per-Task results host tmpdir; bind-mount as `/tekton/results/` into every Step container of that Task.
     - For each Step, `docker run` with: image, command/args (or script written to a tmp file and executed), env, working dir, workspace bind mounts (`-v /host/ws:/workspace/<name>`), and the results bind mount.
     - Stream stdout/stderr to the reporter, tagged `(task=foo, step=bar)`.
     - On Step exit non-zero: stop the TaskRun (don't run later Steps), record failure.
     - On TaskRun completion: read result files from `/tekton/results/`, attach to run state.
   - Emit task-completed event.
7. Tasks at the same DAG level run in parallel; concurrency capped by `--max-parallel` (default `min(4, NumCPU)`).
8. After main DAG completes (or any task fails), run `finally` tasks. They always run, get access to `$(tasks.X.status)`. A `finally` failure fails the overall run.

### 3.4 Reporting & exit
9. `reporter/` renders a tree:
   ```
   ✓ fetch-source         (3.2s)
   ✓ run-tests            (12.4s)
   ✗ build-image          (1.1s)  step 'docker-build' exited 1
   ⊘ deploy               skipped (when: results.tests.passed == "true")
     finally:
   ✓ cleanup              (0.4s)

   PipelineRun failed in 16.7s (1/4 tasks failed, 1 skipped)
   ```
10. `--output json` emits one structured event per line.
11. Exit code: 0 on success, non-zero on any task failure. Workspace tmpdirs preserved on failure (path printed); `--cleanup` wipes them anyway.

### 3.5 The `--cluster` (k3d) variant
- Same loading / validation / DAG. Backend swap: instead of running Steps, the k3d backend writes the resolved `PipelineRun` YAML to the ephemeral cluster and watches it via the k8s API. Logs streamed back via `kubectl logs -f`.
- Cluster started lazily on first use (~10–30s cold start), kept alive between runs (named `tkn-act`), torn down with `tkn-act cluster down`. `tkn-act cluster status` shows state.
- k3d and `kubectl` are not bundled — we shell out and surface "not found" with install hints. `kubectl` is preferred but the k8s Go client is used for watching to avoid polling.

### 3.6 Tekton-fidelity decisions in the Docker backend
A few spots where Docker has to make choices that diverge from real Tekton — these are the bug-prone areas, and they're called out so they're explicit, not accidental:
- **`$(workspaces.x.path)`** evaluates to `/workspace/<name>`. Bind-mount target inside the container matches.
- **Context vars** (`$(context.taskRun.name)`, `$(context.pipelineRun.name)`, `$(context.taskRun.uid)`, `$(context.pipeline.name)`) are synthesized to stable, predictable values: `<pipelinerun>-<task>` etc. UIDs are random v4s.
- **Step image entrypoint.** Real Tekton injects an entrypoint binary that handles step ordering and result-file lifecycle inside one pod. In Docker we run each Step as its own container, so the entrypoint shim isn't needed — but the per-Task results dir must survive across Steps (shared host tmpdir, bind-mounted into every Step container of that Task).
- **Script blocks** are written to a host tmp file (`mode 0755`) and bind-mounted as the executable, with `command: ["/path/to/script"]`. Shebang preserved; default `#!/bin/sh` if missing.
- **Env merging.** Tekton merges Task-level + Step-level env, with Step winning. We do the same.
- **Pulling images.** Honor `imagePullPolicy: IfNotPresent` (default), `Always`, `Never`. Default IfNotPresent.
- **Step resource limits** (`resources.limits.cpu/memory`) translated to `docker run --cpus --memory`. Requests ignored.
- **Service account / secrets / Tekton volumes from secrets** — out of scope for v1; ignored with a warning when present.

---

## 4. Error handling

Errors fall into four classes, each with a different handling strategy. This is what the user sees when something goes wrong.

### 4.1 User-input errors (loading, validation)
Caught before any container starts. Surfaced as a single, actionable error message that names the file and the field.
- **Examples**: malformed YAML, missing required param, unknown `taskRef`, DAG cycle, workspace declared but not bound.
- **Output shape**: `error: pipeline.yaml: PipelineTask "build" references taskRef "buld" — did you mean "build"?` (Levenshtein-suggest for typos.)
- **Exit code**: 2 (distinguishes from runtime failures).

### 4.2 Backend / environment errors (preconditions)
Detected during `Backend.Prepare`. The user can't fix these by editing YAML; they need to install/start something.
- **Examples**: Docker daemon not reachable, image pull denied, k3d binary missing, port conflict.
- **Output shape**: `error: cannot reach docker daemon at unix:///var/run/docker.sock — is docker running?` plus install/setup hints.
- **Exit code**: 3.

### 4.3 Pipeline runtime failures (the expected case)
A Step exits non-zero, a `when` expression goes false, a TaskRun's pre-flight resource resolution fails. These are *normal* outcomes a CI tool must report, not bugs.
- **Behavior**: stop the failing Task at the failing Step (don't run later Steps of the same Task). Other tasks at the same DAG level continue. Downstream tasks that depend on the failed task are skipped (recorded as `not-run`). `finally` always runs.
- **Output shape**: pretty tree with `✗`, last 20 lines of failed Step's stderr inlined under it. Full logs in `$XDG_CACHE_HOME/tkn-act/<run-id>/logs/<task>/<step>.log`.
- **Exit code**: 1.

### 4.4 Internal errors (bugs in tkn-act)
Panics, unexpected impossible states. Caught at the top level.
- **Behavior**: print stack trace (only in `--debug`; otherwise a short message), tell the user to file an issue with the run-id, preserve the workspace dir.
- **Exit code**: 70.

### 4.5 Cleanup discipline
- Containers from a failed/cancelled run are removed on exit (named `tkn-act-<run-id>-<task>-<step>` so leaks are auditable: `docker ps -a --filter name=tkn-act-`).
- SIGINT / SIGTERM cancels the context, waits up to 10s for graceful Step container stop (`docker stop`), then `docker kill`. Workspace tmpdirs preserved on cancel for inspection.
- Background image pulls are cancelled on context done.

---

## 5. Testing strategy

Three layers, scoped to where each catches the most bugs cheaply.

### 5.1 Unit tests (the bulk)
- **`loader/`, `validator/`, `resolver/`, `engine/dag/`** — pure-data, no I/O. Table-driven Go tests with golden YAML fixtures in `testdata/`. Target: ≥85% coverage on these packages.
- **`engine/`** — drives a fake `Backend` (records calls, returns scripted results). Verifies orchestration logic without running containers: DAG ordering, `when` evaluation, `finally` semantics, parallel level execution, failure propagation, parameter substitution timing.
- **`reporter/`** — golden-file tests on the formatted output for known event sequences.

### 5.2 Backend integration tests (the medium tier)
- **Docker backend**: requires Docker locally. Gated behind a build tag (`-tags=integration`) so unit `go test` stays Docker-free. Uses `alpine:3` images and trivial Step scripts to keep tests fast (<30s for the whole suite). Verifies image pull policies, workspace bind mounts, results extraction, env merging, exit codes, signal handling.
- **k3d backend**: gated behind `-tags=k3d`, even slower (cluster cold start). One smoke test per CI run: a 2-Task pipeline with workspace + result passing. Local devs run on demand.

### 5.3 End-to-end golden pipelines
A small library of representative `Pipeline` YAMLs in `testdata/e2e/`:
- `hello/` — single Task, single Step, prints to stdout.
- `params-and-results/` — two Tasks, second consumes first's result via `$(tasks.x.results.y)`.
- `workspaces/` — three Tasks share a workspace, each appends to a file.
- `when-and-finally/` — branching `when`, `finally` always runs.
- `failure-propagation/` — middle task fails, downstream skipped, `finally` runs.

Each fixture has a `expected.json` of structured events; the e2e test runs `tkn-act -o json` and diffs. Both backends run the same fixtures (Docker always; k3d on `-tags=k3d`).

### 5.4 What we deliberately don't test
- Real Tekton catalog tasks pulled from the Hub (out-of-scope: no Resolver in v1).
- Performance (no benchmark suite in v1; `act` didn't have one in its first year either).
- Snapshot tests for the pretty-print output (only the JSON mode is golden-tested; pretty output is allowed to evolve freely).

### 5.5 CI
- GitHub Actions: lint (`golangci-lint`), unit tests, Docker integration tests on `ubuntu-latest`, build on `ubuntu-latest` + `macos-latest`. k3d job is `workflow_dispatch`-triggered (slow) and runs nightly.

---

## 6. Distribution & packaging

- **Language**: Go 1.22+. Single static binary (`CGO_ENABLED=0`).
- **Module path**: `github.com/<owner>/tkn-act` (placeholder until repo is public).
- **Binary name**: `tkn-act`. Because of the `tkn-<name>` prefix, `tkn act ...` works automatically when both binaries are on `$PATH` — free `tkn` plugin.
- **Channels**:
  - GitHub Releases — `goreleaser` produces tarballs for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`.
  - Homebrew tap (`brew install <owner>/tap/tkn-act`).
  - `go install github.com/<owner>/tkn-act/cmd/tkn-act@latest`.
- **Versioning**: SemVer. v0.x while iterating; v1.0 when feature scope above is stable and no breaking flag changes are pending.

---

## 7. Out-of-scope reminders (so v1.x can pick them up)

These came up during design but are explicitly punted to keep v1 shippable. Listed here so we don't forget the shape of v1.x:

- Sidecars (need lifecycle: start before Steps, terminate after).
- Resolvers — `git`, `bundles`, `hub`, `cluster`. The Hub one in particular makes `tkn-act` immediately useful for catalog consumers.
- `StepActions`.
- Step retries.
- TUI mode (`--tui`) with steps/logs panes.
- `tekton-results` writer for keeping a local history of runs.
- Windows support (likely needs WSL-only path handling).
- Podman backend (mostly a matter of swapping the docker client; preconditions docs).

---

## 8. Open questions / risks

- **Upstream API drift.** Vendoring `tektoncd/pipeline` types pins us to a specific minor version. Bumping requires a smoke test against fixtures. Acceptable risk.
- **Docker-vs-Tekton fidelity surprises.** The 3.6 list is what we know about; there will be more. Mitigation: every divergence we discover gets a named fixture in `testdata/e2e/` and a comment in the relevant Docker backend file pointing at the spec section.
- **k3d availability.** Not pre-installed on most macOS dev boxes. We don't bundle it. A "first run" hint that points at `brew install k3d` is the best we can do.
- **Image pull rate-limits.** Docker Hub anonymous pulls are throttled. The `IfNotPresent` default helps. Consider documenting `--registry-mirror` later.
