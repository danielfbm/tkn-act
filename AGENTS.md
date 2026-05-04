# AGENTS.md — `tkn-act` for AI agents

This file is the canonical guide for AI agents (and humans writing scripts)
using `tkn-act`. It is also embedded in the binary and printed by:

```sh
tkn-act agent-guide
```

If you are an AI agent and you can run only one command before doing anything
else, run:

```sh
tkn-act doctor -o json
```

That single command tells you whether the environment is ready, and which
optional features (cluster mode) are available.

---

## What `tkn-act` is

`tkn-act` runs [Tekton](https://tekton.dev) Pipelines and Tasks locally,
either:

- **Docker mode** (default): each Step is a container. Fast, no Kubernetes.
- **Cluster mode** (`--cluster`): an ephemeral `k3d` cluster with the Tekton
  controller installed. Higher fidelity to production Tekton.

It is intended for:

- Local development of Tekton pipelines.
- CI smoke-tests of pipelines without booting a cluster.
- AI agents that need to validate or run a pipeline end-to-end and report
  the result programmatically.

It does **not** run on Kubernetes you already have, deploy anything to
production, or modify your `~/.kube/config`.

---

## Machine-readable interfaces

Every command supports `--output json` (alias `-o json`) with a stable shape.
Three commands are designed specifically for agent use:

| Command                 | Purpose                                              |
| ----------------------- | ---------------------------------------------------- |
| `tkn-act doctor`        | Preflight: Docker, k3d, kubectl, cache dir, version. |
| `tkn-act help-json`     | Full command/flag tree as JSON for introspection.    |
| `tkn-act agent-guide`   | Prints this document.                                |

### `tkn-act doctor -o json`

```json
{
  "ok": true,
  "version": "dev",
  "os": "linux",
  "arch": "amd64",
  "cache_dir": "/home/u/.cache/tkn-act",
  "checks": [
    {"name": "cache_dir", "ok": true,  "detail": "/home/u/.cache/tkn-act", "required_for": "default"},
    {"name": "docker",    "ok": true,  "detail": "API 1.45",               "required_for": "default"},
    {"name": "k3d",       "ok": false, "detail": "not found on PATH",      "required_for": "cluster"},
    {"name": "kubectl",   "ok": false, "detail": "not found on PATH",      "required_for": "cluster"}
  ]
}
```

`ok` is `true` iff every check whose `required_for` is `"default"` passes.
Cluster checks failing is not fatal unless you intend to use `--cluster`.

Exit code: 0 if `ok` is true, 3 (environment) otherwise.

### `tkn-act help-json`

Emits the entire command tree:

```json
{
  "name": "tkn-act",
  "version": "dev",
  "exit_codes": [{"code": 0, "name": "success"}, ...],
  "global_flags": [
    {"name": "output", "short": "o", "type": "string",
     "default": "pretty", "usage": "output format: pretty | json"}
  ],
  "commands": [
    {"path": "tkn-act run",
     "short": "Run a pipeline on the local backend",
     "long":  "...",
     "examples": ["tkn-act run -f pipeline.yaml ..."],
     "flags": [{"name": "file", "short": "f", "type": "stringSlice", ...}]
    }
  ]
}
```

Use this to construct correct invocations without scraping `--help` text.

### `tkn-act run -o json`

Streams one JSON object per line on stdout, one event per line. Event kinds:
`run-start`, `run-end`, `task-start`, `task-end`, `task-skip`, `task-retry`,
`step-start`, `step-end`, `step-log`, `error`, `resolver-start`, `resolver-end`.
The two `resolver-*` kinds (Track 1 #9 Phase 1) are additive — agents that
don't recognize them ignore them. `task-retry` fires between
attempts of a retried task; the terminal `task-end` carries `attempt: N`.
Task statuses: `succeeded`, `failed`, `infrafailed`, `skipped`, `not-run`,
`timeout`. The exit code follows the table below.

### Other JSON outputs

- `tkn-act list -o json` →
  `{"pipelines": ["..."], "tasks": ["..."]}`
- `tkn-act validate -o json` →
  `{"ok": true, "pipeline": "...", "errors": []}`
- `tkn-act version -o json` →
  `{"name": "tkn-act", "version": "dev"}`
- `tkn-act cluster status -o json` →
  `{"name":"tkn-act","exists":true,"running":true,"detail":"","kubeconfig":"..."}`

---

## Exit codes (stable contract)

| Code | Name        | When                                                              |
| ---- | ----------- | ----------------------------------------------------------------- |
| 0    | success     | command completed and (for `run`) the pipeline succeeded          |
| 1    | generic     | uncategorised internal error                                      |
| 2    | usage       | bad flag, contradictory inputs, missing required arg              |
| 3    | environment | Docker not running, k3d/kubectl missing, cache dir not writable   |
| 4    | validate    | Tekton YAML rejected (parse, schema, DAG, when, results)          |
| 5    | pipeline    | run completed but a Task or finally task failed                   |
| 6    | timeout     | a Task or finally task ended due to its declared timeout          |
| 130  | cancelled   | SIGINT or SIGTERM during a run                                    |

These codes are part of `tkn-act`'s public contract and are safe to branch on.

```sh
tkn-act run -f pipeline.yaml
case $? in
  0) echo "ok" ;;
  4) echo "fix the YAML" ;;
  5) echo "tasks failed; check logs" ;;
  *) echo "unexpected: $?" ;;
esac
```

---

## Environment variables

- `XDG_CACHE_HOME` — base for the cache dir. Default
  `$HOME/.cache/tkn-act`. Holds workspace tmpdirs, kubeconfig, etc.
- `DOCKER_HOST` / `DOCKER_TLS_VERIFY` / `DOCKER_CERT_PATH` — standard Docker
  client env. Honored via `client.FromEnv`.
- `KUBECONFIG` — used only by `--cluster` mode for kubectl interactions; the
  cluster driver writes its own kubeconfig under the cache dir.
- `NO_COLOR` — any non-empty value disables color in pretty output (per
  https://no-color.org). Equivalent to `--color=never`.
- `FORCE_COLOR` / `CLICOLOR_FORCE` — any non-empty value forces color in
  pretty output even when stdout is not a TTY. `--color=always` does the same.
- `--configmap-dir <path>` / `--secret-dir <path>` — directory layout
  `<path>/<name>/<key>` per source for configMap and secret volumes.
  Defaults: `$XDG_CACHE_HOME/tkn-act/{configmaps,secrets}/`. Inline form:
  `--configmap <name>=<k1>=<v1>[,<k2>=<v2>...]` (repeatable; same shape
  for `--secret`). Three sources, layered (highest precedence first):
  1. inline `--configmap` / `--secret` flag,
  2. on-disk `--configmap-dir` / `--secret-dir`,
  3. `kind: ConfigMap` / `kind: Secret` (apiVersion `v1`) resources
     embedded in the same `-f` YAML stream as the Tasks/Pipelines.
  A higher layer overrides a lower layer per `(name, key)`. Both
  ConfigMap `data` and Secret `data` (base64) / `stringData` (plain)
  fields are honored; `stringData` wins over `data` for the same key
  per Kubernetes semantics. ConfigMap `binaryData` is rejected at load
  time. `Secret.type` is parsed-and-ignored — bytes are projected
  opaquely. CM/Secret YAML files must be passed explicitly with `-f`;
  there is no auto-discovery for them (only `pipeline.yaml` /
  `.tekton/` are auto-discovered, and only for Tasks/Pipelines).

`tkn-act` never reads or modifies your shell's `~/.kube/config`.

---

## Pretty output (humans only)

Pretty output is the default for `tkn-act run`. It streams step logs **in
arrival order**, prefixing each line with `<task>/<step>` so parallel tasks
remain readable. Verbosity:

- `-q` / `--quiet` — only task and run summaries; suppresses step logs and
  the pipeline header.
- (default) — pipeline header, live step logs, task and run summaries.
- `-v` / `--verbose` — adds step-start / step-end markers.

Color: `--color=auto` (default) | `always` | `never`. `--no-color` is kept as
an alias for `--color=never`. Resolution precedence is `--color=never` /
`--no-color` > `--color=always` > `NO_COLOR` env > `FORCE_COLOR` env > TTY
detection.

Pretty output is for humans and may change at any time. **Agents should
always pass `--output json`** — that contract is stable and these pretty
flags do not affect it.

---

## Common workflows for agents

### 1. Validate a pipeline before running

```sh
tkn-act validate -f pipeline.yaml -o json
```

Exit 0 → safe to run. Exit 4 → parse the `errors` array.

### 2. Run, with structured events on stdout

```sh
tkn-act run -f pipeline.yaml \
  -p revision=main \
  -w shared=./build \
  -o json
```

Pipe stdout through `jq -c .` and parse line-by-line. Exit 5 means a Task
failed; the corresponding `task_finished` event has `status: "failed"`.

### 3. Run inside a real Tekton controller

```sh
tkn-act doctor -o json | jq '.checks | map(select(.name=="k3d" or .name=="kubectl"))'
tkn-act cluster up
tkn-act run --cluster -f pipeline.yaml -o json
tkn-act cluster down -y
```

### 4. Discover what's in a repo

```sh
tkn-act list -o json
```

---

## Failure modes and what to do

| Symptom                                              | Likely exit | Remedy                                    |
| ---------------------------------------------------- | ----------- | ----------------------------------------- |
| `docker: Cannot connect to the Docker daemon`        | 3           | start Docker; rerun `tkn-act doctor`      |
| `--param expects key=value`                          | 2           | quote the value: `-p 'revision=main'`     |
| `multiple pipelines loaded`                          | 2           | pass `-p <pipeline-name>`                 |
| `validation error(s)`                                | 4           | read stderr or use `validate -o json`     |
| pipeline finishes with `status: "failed"`            | 5           | inspect last `task_finished` event        |
| `not found on PATH: k3d`                             | 3           | install k3d or stop using `--cluster`     |

---

## Project default: merge with squash + delete-branch

**When merging any PR in this repo, use squash merge and delete the source
branch.** Concretely:

```sh
gh pr merge <num> --squash --delete-branch
```

This is the project-wide default. Reasons:

- `main` history reads as one commit per landed PR — the PR title becomes
  the squash subject and the body becomes the squash body, which keeps the
  log skimmable and bisectable.
- Stale feature branches don't accumulate locally or on the remote.
- Force-push or merge-commit alternatives are not used; if a PR needs to
  preserve internal commit history (rare), call it out explicitly in the
  PR description and discuss before merging.

AI agents working on this repo should treat squash + delete-branch as the
default merge style without prompting; only deviate when the user
explicitly asks for a merge or rebase merge.

---

## Contribution rule: tests required

**Every PR that changes Go production code must include a test change.**
Concretely: if a PR's diff modifies any `*.go` file outside `_test.go` and
outside `vendor/`, it must also modify or add at least one `*_test.go` file.

This rule is enforced in CI by `.github/scripts/tests-required.sh` (run as
the `tests-required` job in `.github/workflows/ci.yml`). The script fails
the PR if the diff has Go code changes without an accompanying test change.

For genuinely test-immune changes (dependency bumps, doc typos in Go
comments, regenerated boilerplate, generated stubs), include the literal
token `[skip-test-check]` in any commit message in the PR. The script
greps `git log --format=%B base..head` for it.

The rationale is the usual one: every behavior we ship is one we must be
able to detect breaks in later. AI agents working on this repo should treat
this as a hard precondition for opening a PR.

### Coverage gate (sibling rule)

**Coverage must not drop below the target branch on a per-package basis.**
The `coverage` job in `.github/workflows/ci.yml` runs
`.github/scripts/coverage-check.sh` on every PR. It runs
`go test -cover -count=1 ./...` (default test set, no build tags) on both
the PR's base SHA and head SHA, then compares per-package: if any package
on HEAD has lower coverage than on BASE by more than 0.1 percentage points,
the gate fails and prints a per-package table showing the drop.

| Edge case | Behavior |
|---|---|
| Package new on HEAD (no BASE baseline) | not a drop; reported as `new` |
| Package removed on HEAD | not a drop; reported as `removed` |
| Package with `[no statements]` | treated as 100% on both sides |
| Package with `[no test files]` | skipped (no measurement) |
| Tests fail on either side | gate aborts with a clear message — does not silently pass as 0% |

For genuinely coverage-immune changes (a deletion that drops a covered
code path along with its tests, a refactor that intentionally drops dead
code), include the literal token `[skip-coverage-check]` in any commit
message in the PR. The script greps `git log --format=%B base..head` for
it, the same way `tests-required` looks for `[skip-test-check]`.

The gate runs only on `pull_request` (not on push to `main` — there's no
base to compare against). It only measures the default test set, not
`-tags integration` or `-tags cluster`; those run in their own workflows.

---

## Local development

The repo-root `Makefile` is the supported one-command bootstrap for new
contributors:

```sh
make quickstart   # doctor -> build -> cluster-up -> hello-cluster
```

`make help` lists every target. The Makefile is a convenience layer over
the same commands CI runs (`go test -race ./...`, `go vet` across all
build tags, `tkn-act cluster up`, `tkn-act run --cluster`); it does not
duplicate behavior CI already covers, and there is no CI gate that runs
`make` itself. See `docs/test-coverage.md` for what is and isn't gated.

K3d / kubectl version pins in the Makefile (`K3D_VERSION`,
`KUBECTL_VERSION`) mirror those in
`.github/workflows/cluster-integration.yml`; bump both together so local
runs continue to match CI.

---

## `stepTemplate` (DRY for Steps)

`Task.spec.stepTemplate` lets a Task declare base values that every
Step in `spec.steps` inherits. Inheritance rules tkn-act follows:

| Field | Behavior |
|---|---|
| `image`, `workingDir`, `imagePullPolicy` | Step value wins if non-empty; otherwise inherit. |
| `command`, `args` | Step value wins as a whole if non-empty (no element-wise merge). |
| `env` | Union by `name`; Step entry overrides template entry with the same name. |
| `resources` | Step value wins (replace); no deep merge of `limits` / `requests`. |
| `name`, `script`, `volumeMounts`, `results`, `onError` | Per-Step only; never inherited. |

This matches Tekton v1 semantics for the subset of Step fields
tkn-act reads. `volumes` / `volumeMounts` inheritance from
`stepTemplate` is **not** supported (gap, see `docs/feature-parity.md`).

---

## Sidecars (`Task.sidecars`)

`Task.spec.sidecars` declares long-lived helper containers that share
the Task's pod / network namespace for the duration of the Task.
Catalog Tasks use them for databases, mock services, registries.

Supported fields tkn-act honors on `--docker`:

| Field | Behavior |
|---|---|
| `name`, `image` | Required. Name must be unique within the Task and must not collide with a Step name. |
| `command`, `args`, `script`, `env`, `workingDir`, `imagePullPolicy`, `resources` | Same semantics as on Steps. |
| `volumeMounts` | Must reference a Task-declared `volumes` entry. |
| `workspaces` | Sidecar opts into Task workspaces by name; mounted at the workspace's path inside the sidecar. |
| `ports`, `readinessProbe`, `livenessProbe`, `startupProbe`, `securityContext`, `lifecycle` | Parsed for fidelity but ignored on `--docker` (`--cluster` forwards them to Tekton). Probe support is a deferred follow-up. |

Lifecycle on `--docker` (per Task with at least one sidecar):

1. Workspace prep (existing).
2. Sidecar images are pre-pulled by the engine; the pause image
   (`registry.k8s.io/pause:3.9`, ~700KB, cached forever
   after first pull) is pulled by the docker backend itself.
3. A tiny **pause container** is started with normal Docker
   networking. It owns the netns and only exits on signal (mirrors
   upstream Kubernetes / Tekton's "infra container" model).
4. Every sidecar in declaration order is started, each with
   `network_mode: container:<pause-id>`. Steps reach any sidecar at
   `localhost:<port>` exactly the way they would in a real Tekton
   pod.
5. After all sidecars are started, tkn-act waits a fixed grace
   (`--sidecar-start-grace`, default `2s`) before launching the
   first Step. Override the flag for slower-starting sidecars.
6. Each Step container is also started with
   `network_mode: container:<pause-id>`.
7. Sidecars are sent SIGTERM after the last Step ends, with a
   `--sidecar-stop-grace` window (default `30s`, matches upstream
   Tekton's `terminationGracePeriodSeconds`) before SIGKILL. The
   pause container is then stopped (hard 1s grace).
8. Workspace teardown (existing).

Tasks with zero sidecars pay no extra cost — no pause container, no
extra container starts.

Failure semantics:

| Scenario | Task status |
|---|---|
| Sidecar fails to start (image pull fails / container exits before grace) | `infrafailed`. One terminal `sidecar-end` event with `status: "infrafailed"`. |
| Sidecar crashes mid-Task (any sidecar — there is no "privileged" sidecar in the pause-container model) | Task status unchanged. Steps continue (the pause container, not the sidecar, owns the netns). The crash surfaces as `sidecar-end` with `status: "failed"` and the exit code. Matches upstream "sidecars are best-effort". |

JSON event stream additions (stable contract):

| Kind | Payload |
|---|---|
| `sidecar-start` | `task`, `step` (= sidecar name), `time`. `stream: "sidecar"`. |
| `sidecar-end` | `task`, `step`, `exitCode`, `status` (`succeeded` / `failed` / `infrafailed`), `time`. |
| `sidecar-log` | `task`, `step`, `stream` (= `"sidecar-stdout"` / `"sidecar-stderr"`), `line`, `time`. |

The `step` field is reused for the sidecar's name so existing JSON
consumers don't need new fields; consumers that care about
step-vs-sidecar disambiguation can branch on `kind` or on `stream`.

Cluster pass-through is automatic — tkn-act inlines the full
sidecars list into `pipelineSpec.tasks[].taskSpec.sidecars`, and
Tekton's controller takes it from there. The cluster watch loop
diffs `taskRun.status.sidecars[]` between events and emits matching
`sidecar-start` / `sidecar-end` events so the JSON event stream is
shape-equivalent across backends.

---

## StepActions (`tekton.dev/v1beta1`)

A `StepAction` is a referenceable Step shape: a top-level Tekton
resource (`apiVersion: tekton.dev/v1beta1`, `kind: StepAction`) that
the engine inlines into Steps that reference it via `ref:`. Same
multi-doc YAML stream as Tasks / Pipelines — pass with `-f`.

```yaml
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec:
  params: [{name: who, default: world}]
  results: [{name: greeting}]
  image: alpine:3
  script: |
    echo hello $(params.who) > $(step.results.greeting.path)
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: greeter}
spec:
  steps:
    - name: greet
      ref: {name: greet}
      params: [{name: who, value: tekton}]
```

Resolution rules tkn-act follows:

| Field | Behavior |
|---|---|
| `Step.ref.name` | Resolved against the loaded bundle's `StepActions` map. Unknown name → exit 4 (validate). |
| `Step.ref` + body fields | Mutually exclusive. A Step is either inline or a reference; setting both → exit 4. |
| `Step.params` | Bound into the StepAction's declared params. StepAction defaults apply for omitted entries; missing required params → exit 4. Caller param values are forwarded as LITERAL strings, so `value: $(params.repo)` survives the inner pass and resolves against the surrounding Task scope. |
| `name`, `onError` | Per-Step (kept from the calling Step). |
| `volumeMounts` | Union — StepAction body's mounts come first, caller's appended (matches Tekton). |
| `image`, `command`, `args`, `script`, `env`, `workingDir`, `imagePullPolicy`, `resources`, `results` | From the StepAction's body. |
| `$(params.X)` inside the StepAction body | Resolves against the StepAction-scoped param view (StepAction defaults + caller bindings); other `$(params.X)` survive into the outer Task pass. |
| `$(step.results.X.path)` | Writes to `/tekton/steps/<calling-step-name>/results/X` — same per-step results dir as inline `Step.results`. |
| `$(steps.<calling-step-name>.results.X)` from later steps | Reads the literal value, same as for inline Steps. |
| Inline Step (no `ref:`) without an `image:` | Rejected at validate time (exit 4). Image inheritance from `stepTemplate.image` counts. |
| Resolver-form `ref:` (`{resolver: hub, params: [...]}`) | Rejected at validate time with a clear "not supported in this release; see Track 1 #9" message. |
| Nested StepActions (a StepAction body containing `ref:`) | Schema-rejected: `StepActionSpec` does not model `ref:`. |
| StepAction `params[].default` of array/object type | Rejected at validate time (only string defaults are honored in v1). |

The cluster backend receives the same expanded Step shape — there is
no `kind: StepAction` apply onto the per-run namespace. Expansion is
fully client-side, so both backends are bit-identical at the
submission layer; no class of bug can have one Tekton controller
resolve a StepAction differently from another.

---

## Matrix fan-out (`PipelineTask.matrix`)

`PipelineTask.matrix` fans one PipelineTask out into N concrete
TaskRuns by Cartesian product over named string-list params, plus
optional `include` rows for named extras. v1 implements the high-value
subset (cross-product + include, per-row `when:`, result aggregation)
and defers Tekton's β knobs (`failFast: false`, `maxConcurrency`).

| Aspect | Behavior |
|---|---|
| Naming | The `task` field on every event always carries the per-expansion stable name. Cross-product expansions are `<parent>-<index>` (0-based, in row order, e.g. `build-1`); named `include` rows use their declared `name` (e.g. `arm-extra`); unnamed include rows continue the `<parent>-<i>` numbering past the cross-product. The new `matrix.parent` field on the event carries the original PipelineTask name. Consumers reading `Event.Task` keep working unchanged. |
| Row order | `matrix.params[0]` iterates slowest. For `[os=[linux,darwin], goversion=[1.21,1.22]]` the rows are `(linux,1.21)`, `(linux,1.22)`, `(darwin,1.21)`, `(darwin,1.22)`. Include rows are appended in declaration order. |
| Param merge | Per row: pipeline-task `params` ∪ matrix-row params; matrix wins on conflicts. |
| DAG | Downstream `runAfter: [<parent>]` is rewritten to wait on every expansion. Mixed-success matrix parents (some rows succeed, others skip-via-when) do NOT block downstream — only an all-non-success parent does. |
| Failure | Default: any expansion failing makes the parent fail; downstream skips. `failFast: false` is not implemented (β feature deferred). |
| Concurrency | Honors `--parallel` like ordinary parallel tasks. Per-matrix `maxConcurrency` is not implemented. |
| Cardinality cap | **256 rows total per matrix** (cross-product + include). Hardcoded; no env var, no flag. Exceeding is exit 4 (validate). If you need more, split the pipeline. |
| Result aggregation | A `string` result `Y` from a matrix-fanned task `X` is promoted to an array-of-strings, addressable as `$(tasks.X.results.Y[*])` and `$(tasks.X.results.Y[N])`. A scalar reference `$(tasks.X.results.Y)` (no `[*]`) returns the JSON-array literal *string* (e.g. `'["a","b","c"]'`) — matches Tekton's on-disk representation. `array` and `object` result types on a matrix-fanned task are validator-rejected. |
| `when:` | Evaluated **per row**. Each row's matrix-contributed params are visible in `$(params.<name>)` references inside the `when:` block. Rows that evaluate false emit one `task-skip` event under the *expansion* name (e.g. `build-1`) with `matrix:` populated. Other rows run normally. |
| JSON event shape | `task-start` / `task-end` / `task-skip` / `task-retry` events for an expansion carry `matrix: {parent, index, of, params}`. The field is `omitempty`, so non-matrix runs are byte-identical to today. |
| Cluster mode | Submitted to Tekton unchanged (round-trips through the existing JSON marshal path). The cluster backend reconstructs `MatrixInfo` per-TaskRun by canonical-hash matching the row's params against `taskRun.spec.params` (PRIMARY); falls back to `pr.status.childReferences` ordering with an `error` event warning if param-hashing produces no hit. |

`include` semantics: tkn-act always **appends** include rows as new
rows; it does NOT fold them into matching cross-product rows the way
upstream Tekton can. To prevent silent docker-vs-cluster divergence
(cluster delegates to the controller and would fold; docker would
not), `tkn-act validate` **rejects** any include row whose params
overlap a `matrix.params` name with an explicit error message and
exit 4. The `testdata/limitations/matrix-include-overlap/` fixture
is the documented example.

---

## Documentation rule: keep related docs in sync with every change

**Every change that touches user-visible behavior, supported features,
exit codes, JSON shapes, fixtures, or Tekton coverage must update the
related docs in the same PR.** "Related docs" includes, at minimum:

| If you change... | Also update |
|---|---|
| Tekton field/feature support (types, engine, validator, cluster) | `docs/feature-parity.md` row + `AGENTS.md` (and re-run `go generate ./cmd/tkn-act/` so `cmd/tkn-act/agentguide_data.md` mirrors it) |
| User-facing CLI behavior (flags, output, exit codes) | `AGENTS.md`, `cmd/tkn-act/agentguide_data.md`, and `README.md` |
| `testdata/e2e/<name>/` or limitations fixtures | `docs/test-coverage.md` and `docs/feature-parity.md` |
| Track 1/2/3 plan items in `docs/short-term-goals.md` | flip the row's Status when the work lands |
| New plan under `docs/superpowers/plans/` | reference it from the matching `feature-parity` / `short-term-goals` row |

CI's `parity-check` job (`.github/scripts/parity-check.sh`) is the
machine-checked enforcement of the parity ↔ fixtures invariant; the
broader rule above is enforced by reviewers and by AI agents working on
this repo. **AI agents must not open a PR that lands a feature, fixture,
or behavior change without updating every doc the change touches.** When
in doubt, grep for the symbol/feature in the docs tree and update every
hit.

---

## Pipeline results (`Pipeline.spec.results`)

A Pipeline can declare named results computed from task results once
the run terminates. Tekton's syntax:

```yaml
spec:
  results:
    - name: revision
      value: $(tasks.checkout.results.commit)
    - name: report
      value:
        - $(tasks.test.results.summary)
        - $(tasks.notify.results.id)        # finally tasks count too
    - name: meta
      value:
        owner: $(params.team)               # only $(tasks.X.results.Y) actually resolves; other refs drop the entry
        sha:   $(tasks.checkout.results.commit)
```

Resolution semantics tkn-act follows:

| Aspect | Behavior |
|---|---|
| When | After the entire run completes (tasks + finally), regardless of overall status. |
| Source | The same accumulated task-result map that powers `$(tasks.X.results.Y)` in PipelineTask params. Finally tasks contribute. |
| Failure handling | If a referenced task didn't succeed, or the result name wasn't produced, the pipeline result is **dropped** (omitted from the output). One `error` event per dropped result is emitted on **both** the docker and the cluster backend; the run's status and exit code are NOT changed. |
| Types | string / array / object (mirrors `ParamValue`). JSON-encoded as the matching shape. |
| Cluster mode | tkn-act reads `pr.status.results` from the Tekton controller's verdict — it does not re-resolve locally. Drops surface as `error` events on declared names absent from the verdict. |

Where they show up:

- **JSON (`-o json`)**: a `results` map on the `run-end` event, e.g.
  `{"kind":"run-end","status":"succeeded","results":{"revision":"abc123","report":["abc123","notify-42"]}}`.
- **Pretty output**: one line per resolved result after the run-summary
  line, in stable (alphabetical) key order, values truncated to 80 chars.
- **Library API (`engine.RunResult`)**: a new `Results map[string]any`
  field with values typed as string / `[]string` / `map[string]string`.

Pipeline-result-substitution back into other expressions (e.g.
referencing `$(results.X)` somewhere in the same run) is **not**
supported — Tekton itself doesn't do this; pipeline results are
output-only.

---

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
locally but is not surfaced on any event (carrying it on every
`step-log` line would balloon log output). The Step's `displayName` is
consumed in two places: the `step-log` JSON event (above) and pretty
output's log-line prefix.

In cluster mode, `Step.displayName` and `Step.description` are
**stripped** from the inlined PipelineRun before submission to Tekton
— the upstream Tekton v1 `Step` schema (as of v0.65) has no such
fields, and the admission webhook rejects unknown fields on strict
decode. The fields still surface on tkn-act's JSON event stream and
pretty output the same way they do under docker, since cluster mode
reads them from the input bundle (not the controller verdict) when
emitting events. Pipeline-, PipelineTask-, and TaskSpec-level
`displayName` / `description` round-trip intact (the upstream schema
supports them at those levels).

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

---

## Resolvers (Track 1 #9, shipped)

`taskRef.resolver` and `pipelineRef.resolver` (the Tekton catalog-consumption
pattern) are **fully supported** in v1.6. All six phases of the plan have
shipped: Phase 1 (scaffolding), Phase 2 (direct `git`), Phase 3 (`hub` +
`http`), Phase 4 (`bundles` + off-by-default `cluster`), Phase 5 (Mode B
remote resolver via `ResolutionRequest` CRD), and Phase 6 (`--offline`
end-to-end + on-disk cache + `tkn-act cache` subcommands):

- New types `TaskRef.Resolver` / `TaskRef.ResolverParams` and the
  `PipelineRef` counterparts. Existing inline-only YAML is unaffected
  (the new fields are `omitempty`).
- A new `internal/refresolver` package distinct from `internal/resolver`
  (which performs `$(...)` variable substitution). The two senses of
  "resolver" are intentionally separated.
- **Lazy resolution at task-dispatch time**: `resolver.params` may
  reference `$(tasks.X.results.Y)` and the engine schedules X before
  the resolver-backed task. Implicit DAG edges are now inferred from
  every `$(tasks.X.results.Y)` substring in `pt.Params` AND
  `pt.TaskRef.ResolverParams` (a baseline behavior change — see the
  PR description for one observable effect on previously-broken
  Pipelines).
- **Eager top-level resolution** for `pipelineRef.resolver` on a
  PipelineRun: resolved synchronously at load time before any DAG
  build, since a top-level ref cannot legally substitute upstream
  task results.
- **Cluster backend inlines** resolver-backed taskRefs into the
  submitted PipelineRun before sending it to the local k3d (which
  has no resolver credentials of its own).
- **Two new event kinds**: `resolver-start` and `resolver-end`,
  carrying optional `resolver`, `cached`, `sha256`, `source` fields,
  all `omitempty` so non-resolver events ignore them. The top-level
  pipelineRef resolution emits these two events with an empty `task`
  field — JSON consumers disambiguate via the absence of `task`.
- **`git` resolver (Phase 2, supported in v1.6)**: shallow clones a
  repo via `go-git/v5` and reads `pathInRepo` at the requested
  revision. Direct-mode params are:
  | Param | Required | Default | Notes |
  |---|---|---|---|
  | `url` | yes | — | `file://`, `https://`, `ssh://`, or `git@host:path`. Plain `http://` refused unless `--resolver-allow-insecure-http`. |
  | `revision` | no | `main` | Branch, tag, or full SHA. SHAs trigger a full clone + ResolveRevision fallback path. |
  | `pathInRepo` | yes | — | Relative path to the YAML inside the repo. `..` traversal refused. |
  Cache layout: `<--resolver-cache-dir>/git/<sha256(url+revision)>/repo/`.
  A second call with identical `(url, revision)` reuses the cached
  working tree without any network IO and emits `resolver-end` with
  `cached: true`. SSH delegation honors `ssh-agent`; tkn-act never
  reads SSH keys directly.
- **CLI flags**: `--resolver-cache-dir`,
  `--resolver-allow=git,hub,http,bundles` (default — `cluster` is
  intentionally absent and must be opted in explicitly),
  `--resolver-config`, `--offline`, `--remote-resolver-context`,
  `--resolver-allow-insecure-http` (now also opens HTTP for the
  bundles resolver against non-loopback registries),
  **`--cluster-resolver-context=<ctx>`** (Phase 4) opts the
  off-by-default `cluster` resolver in by naming the kubeconfig
  context to read from, and **`--cluster-resolver-kubeconfig=<path>`**
  overrides the kubeconfig path. Setting either flag implicitly flips
  the registry's `AllowCluster=true` so the cluster resolver registers
  in the default registry. **`--remote-resolver-context=<ctx>`**
  (Phase 5) flips the registry into Mode B; pair with
  `--remote-resolver-namespace=<ns>` (default `default`) and
  `--remote-resolver-timeout=<duration>` (default `60s`) to control
  where the `ResolutionRequest` lands and how long tkn-act waits for
  the controller to reconcile.
- **Validator** rejects unknown resolver names in direct mode (unless
  `--remote-resolver-context` is set, which short-circuits the
  allow-list); rejects `resolver.params` that reference a task not in
  `spec.tasks ∪ spec.finally`; rejects any cache-miss when `--offline`
  is set.

### Direct resolvers — what's wired

| Resolver | Status | `resolver.params` | Notes |
|---|---|---|---|
| `inline` | shipped (test harness) | `name` | Magic test resolver. The test harness preloads `(name → bytes)` pairs and the engine looks them up. |
| `git` | shipped (Phase 2) | `url` (req), `revision` (default `main`), `pathInRepo` (req) | Shallow clone via `go-git/v5`. Cache layout: `<--resolver-cache-dir>/git/<sha256(url+revision)>/repo/`. SSH delegation honors `ssh-agent`. |
| `hub` | shipped (Phase 3) | `name` (req), `version` (default `latest`), `kind` (default `task`), `catalog` (default `tekton`) | HTTPS GET to `<BaseURL>/v1/resource/<catalog>/<kind>/<name>/<version>/yaml`. Default BaseURL `https://api.hub.tekton.dev`. HTTPS-only (no opt-out for hub). 5xx retries once. Bearer token via `HubOptions.Token` library API; the standard `--resolver-config` file is read for `hub.token`. |
| `http` | shipped (Phase 3) | `url` (req) | Plain HTTPS GET. 5xx retries once. HTTPS-only by default; `--resolver-allow-insecure-http` opts plain http:// non-loopback URLs in (loopback always allowed for unit tests with `httptest.NewServer`). Bearer token via `HTTPOptions.Token` (library) or env `TKNACT_HTTP_RESOLVER_TOKEN` (CLI escape hatch). |
| `bundles` | shipped (Phase 4) | `bundle` (req — OCI ref like `gcr.io/foo/bar:v1`), `name` (req — resource `metadata.name` to extract), `kind` (default `task`) | Pulls a Tekton OCI bundle via `go-containerregistry`. Walks layers in declaration order, matches on the conventional `dev.tekton.image.{name,kind,apiVersion}` annotations, and returns the YAML embedded in the matching layer's tar. HTTPS-only by default; loopback registries always permit HTTP (so unit tests with `pkg/registry` work); `--resolver-allow-insecure-http` extends that to non-loopback registries. Auth honors `~/.docker/config.json` via `authn.DefaultKeychain`. |
| `cluster` | shipped (Phase 4) — **OFF BY DEFAULT** | `name` (req), `kind` (default `task`, also accepts `pipeline`), `namespace` (default `default`) | Reads from the user's KUBECONFIG via the kube dynamic client. Strips server-side bookkeeping (`status`, `metadata.uid`/`resourceVersion`/`generation`/`creationTimestamp`/`managedFields`) before serializing back to YAML. **Off by default in `NewDefaultRegistry`** — `KUBECONFIG` may point at production. Opt-in either by adding `cluster` to `--resolver-allow` or by setting `--cluster-resolver-context=<ctx>` (which also names the kubeconfig context). Both require explicit user consent before the resolver registers. |

Failure surfaces as `task-end` `status: "failed"` with `message: "resolver: hub: ..."` / `"resolver: http: ..."` / `"resolver: bundles: ..."` / `"resolver: cluster: ..."`, which routes through the standard pipeline-failure exit code (5).

### Mode B — remote resolver via `ResolutionRequest` (Phase 5, shipped)

Setting `--remote-resolver-context=<kubeconfig-context>` flips the
registry into **Mode B**: every `taskRef.resolver:` /
`pipelineRef.resolver:` block is dispatched by submitting a
`resolution.tekton.dev/v1beta1` `ResolutionRequest` CRD to the
remote Tekton cluster, watching `status.conditions[Succeeded]`, and
decoding `status.data` (base64) once the controller fills it in. The
direct-mode allow-list is short-circuited — any resolver name the
remote cluster's controller knows is dispatchable, including
arbitrary custom resolver names.

Wire-compat:
- v1beta1 first; falls back to **v1alpha1** on `NoKindMatchError`
  for older Tekton Resolution installs (long-term-support OpenShift
  Pipelines, etc.). Both API versions share the fields tkn-act
  reads (`spec.params`, `status.conditions`, `status.data`).

Cleanup discipline:
- The `ResolutionRequest` is **always Deleted** on the way out
  (success, controller-reported failure, timeout, **or
  `context.Cancel`** — the deferred Delete uses `context.Background()`
  so SIGINT mid-resolution still triggers cleanup).

Security stance:
- Mode B uses **the user's kubeconfig identity** — whatever
  service-account that context resolves to is the one the remote
  cluster sees. tkn-act never elevates privileges, never stores
  credentials of its own, and never modifies the kubeconfig file.
- The chosen kubeconfig context's RBAC must include
  `create / get / delete` on `resolutionrequests` in the namespace
  named by `--remote-resolver-namespace`.

Flags (all on `tkn-act run`):

| Flag | Default | Purpose |
|---|---|---|
| `--remote-resolver-context <ctx>` | unset | Kubeconfig context to dispatch ResolutionRequests through. Setting non-empty flips registry into Mode B. |
| `--remote-resolver-namespace <ns>` | `default` | Namespace where ResolutionRequests are submitted. |
| `--remote-resolver-timeout <duration>` | `60s` | Per-request wait budget for the controller's reconcile. Failure surfaces as `task-end` `status: "failed"` with a `remote: timeout after ...` message. |

### `--offline` mode + on-disk cache (Phase 6, shipped)

Every direct resolver writes resolved bytes + a small JSON metadata
sidecar to `--resolver-cache-dir` (default
`$XDG_CACHE_HOME/tkn-act/resolved/`) under
`<root>/<resolver>/<sha256(resolver+sortedKVs(SUBSTITUTED-params))>.{yaml,json}`.

Cross-run hits (a Pipeline that ran before, with the same
substituted resolver-params) skip the network entirely on the
second run, surface as `cached: true` on the `resolver-end` JSON
event, and as `(cached)` instead of a duration in pretty output.
The same DiskCache is used by both backends because resolution
happens above the backend layer.

`--offline` rejects every cache miss with a clear error:

- At validate time, the validator's `CacheCheck` callback queries
  the same DiskCache. A cache miss surfaces as exit code 4 before
  any task starts.
- At run time (e.g. for the eager top-level `pipelineRef.resolver`
  path that bypasses the validator), the registry's `Resolve`
  re-checks the cache and emits a `resolver-end` `status: failed`
  with a "cache miss while --offline is set" message; the parent
  task ends `failed` (exit code 5).

Use `--offline` in CI to guarantee no network egress unexpectedly
happens after the cache has been seeded.

### Cache management — `tkn-act cache <list|prune|clear>`

```sh
# List every cached entry (resolver, key, size, age).
tkn-act cache list
tkn-act cache list -o json    # stable JSON shape: {root, entries: [{resolver, key, path, size, mod_time}]}

# Delete entries older than a duration; default 30 days.
tkn-act cache prune                        # --older-than 720h
tkn-act cache prune --older-than 168h      # 7 days
tkn-act cache prune -o json                # JSON: {root, older_than, pruned}

# Wipe everything; -y required to confirm.
tkn-act cache clear -y
tkn-act cache clear -y -o json             # JSON: {root, cleared}
```

All three subcommands honor `--resolver-cache-dir` (the same flag
`tkn-act run` uses). The cache root may not exist yet — `cache
list` returns zero entries; `prune` and `clear` are no-ops.

See `docs/superpowers/plans/2026-05-04-resolvers.md` for the full plan
and `docs/superpowers/specs/2026-05-04-resolvers-design.md` for the spec.

---

## Conventions

- All JSON shapes documented above are part of the public contract; new
  fields may be added, but existing fields will not be renamed or have their
  type changed without a major version bump.
- Pretty output (the default) is for humans and may change at any time.
  Agents should always pass `--output json`.
- `tkn-act` is non-interactive when given `-y` on `cluster down`. There are
  no other prompts.

---

## Where to look next

- **Feature parity scoreboard:** `docs/feature-parity.md` — single source
  of truth for what's `shipped` / `in-progress` / `gap`, with the e2e
  fixture and limitations fixture for each row. CI's `parity-check` job
  enforces that this table doesn't drift from the tree.
- Spec: `docs/superpowers/specs/2026-05-01-tkn-act-design.md`
- Cluster spec: `docs/superpowers/specs/2026-05-01-tkn-act-cluster-backend-design.md`
- This file in the binary: `tkn-act agent-guide`
