# tkn-act agent guide

This document is the canonical guide for AI agents (and humans writing
scripts) using `tkn-act`. It is also embedded in the binary and printed
by:

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
  --param revision=main \
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
- Working *on* `tkn-act`? See [`AGENTS.md`](../../AGENTS.md) at the repo root for the contributor guide (merge policy, test/coverage gates, doc-sync rule, local development).
