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
`step-start`, `step-end`, `step-log`, `error`. `task-retry` fires between
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
