# tkn-act

Run [Tekton Pipelines](https://tekton.dev) locally — on Docker, or on an
ephemeral [k3d](https://k3d.io) cluster with a real Tekton controller.
No production Kubernetes required. Inspired by
[`nektos/act`](https://github.com/nektos/act).

## Status

v1.4 — `tekton.dev/v1` Pipelines and Tasks run locally with two
backends, structured JSON output, and stable exit codes for CI/agents.
See [`docs/feature-parity.md`](docs/feature-parity.md) for the full
shipped/in-progress/gap scoreboard.

## Quickstart

If you have Docker, [`k3d`](https://k3d.io), and `kubectl` on your
`PATH`:

    make quickstart

That builds `bin/tkn-act`, boots a local k3d cluster, installs Tekton,
and runs the `testdata/e2e/hello/pipeline.yaml` fixture against the real
controller. If anything is missing, run `make doctor` first — it prints
install hints and pinned versions matching the
[`cluster-integration`](.github/workflows/cluster-integration.yml) CI
job. `make help` lists every target; [`AGENTS.md`](AGENTS.md) is the
canonical agent / JSON contract.

## Install

    go install github.com/dfbmorinigo/tkn-act/cmd/tkn-act@latest

Or via `tkn`'s plugin discovery — drop the binary on your `PATH` and
run `tkn act ...`.

## Usage

    cd my-repo-with-pipeline.yaml
    tkn-act                                # auto-discovers pipeline.yaml / .tekton/
    tkn-act run -f pipeline.yaml -p revision=main -w shared=./build
    tkn-act validate
    tkn-act list

For machine-readable output (CI, agents, scripts):

    tkn-act doctor      -o json            # preflight environment check
    tkn-act run         -o json -f pipeline.yaml   # one event per line on stdout
    tkn-act validate    -o json
    tkn-act list        -o json
    tkn-act help-json                      # full command/flag tree

## Two backends

| Mode | Trigger | Fidelity | Speed | Needs |
|---|---|---|---|---|
| Docker (default) | (no flag) | Each Step is a container | Fast (<1s startup) | Docker daemon |
| Cluster | `--cluster` | Real Tekton controller, real entrypoint shim | ~30–60s first run | `k3d`, `kubectl` |

```sh
tkn-act cluster up                         # one-time, ~30-60s
tkn-act run --cluster -f pipeline.yaml
tkn-act cluster status
tkn-act cluster down -y
```

Cross-backend fixtures in [`internal/e2e/fixtures`](internal/e2e/fixtures/fixtures.go)
are exercised by both backends in CI — divergences are explicit
`DockerOnly` / `ClusterOnly` flags, never silent omissions.

## Tekton features supported

- `tekton.dev/v1` `Task`, `Pipeline`, `PipelineRun`, `TaskRun`
- Steps with `image`, `command`, `args`, `script`, `env`, `workingDir`,
  `imagePullPolicy`, `resources`, `onError`, per-step `results`
- Params (string, array, object), defaults, `$(params.x)` and
  `$(params.x[*])`
- Results (file-based at `/tekton/results/<n>`) and
  `$(tasks.X.results.Y)`
- `Pipeline.spec.results` — named outputs surfaced on the `run-end`
  event (string / array / object); resolved from task results after
  the entire run completes
- Workspaces shared across tasks (host bind mounts)
- DAG ordering via `runAfter` and result-data deps
- `when` expressions (`in` / `notin`)
- `finally` tasks
- `Task.spec.timeout` (per-task) and `Pipeline.spec.timeouts.{pipeline,
  tasks, finally}` (whole-run / DAG / finally budgets)
- `PipelineTask.retries`
- Volumes: `emptyDir`, `hostPath`, `configMap`, `secret`
  (inline via `--configmap`/`--secret`, directory layout via
  `--configmap-dir`/`--secret-dir`, or `kind: ConfigMap` /
  `kind: Secret` resources embedded directly in the `-f` YAML stream)
- `Task.spec.stepTemplate` — Steps inherit `image`, `command`, `args`,
  `env`, `workingDir`, `imagePullPolicy`, `resources` from a per-Task
  base template (`env` merged by name; Step values always win)
- `displayName` / `description` on Task / Pipeline / PipelineTask / Step
  — surfaced on the JSON event stream as `display_name` / `description`,
  preferred over `name` in pretty output
- `taskRef.resolver` / `pipelineRef.resolver` — **scaffolding only in
  v1.6.x**: types, lazy dispatch at task-dispatch time, eager top-level
  pipelineRef resolution at load time, cluster-backend inline-before-submit,
  validator pre-flight, two new event kinds (`resolver-start` /
  `resolver-end`), and CLI flags. Concrete resolvers (`git`, `hub`,
  `http`, `bundles`, `cluster`) and the remote `ResolutionRequest`
  driver land in subsequent phases — see
  [`docs/superpowers/plans/2026-05-04-resolvers.md`](docs/superpowers/plans/2026-05-04-resolvers.md)

The single source of truth, with one row per Tekton field and links to
fixtures, plans, and PRs, is
[`docs/feature-parity.md`](docs/feature-parity.md). CI's `parity-check`
job enforces that the table doesn't drift from the tree.

## Not yet supported

Sidecars (cluster-only), StepActions, Resolvers (git/hub/cluster/
bundles), `PipelineTask.matrix`, custom tasks, signed pipelines,
tekton-results, Windows.

See [`docs/short-term-goals.md`](docs/short-term-goals.md) for the
prioritized track of what's next.

## For AI agents and CI

`tkn-act` has first-class support for AI agents and scripts:

- Stable JSON shapes on every command (`--output json`).
- Stable exit codes: `0` ok, `2` usage, `3` env, `4` validate,
  `5` pipeline failure, `6` timeout, `130` cancelled.
- `tkn-act doctor -o json` — preflight: Docker, k3d, kubectl, cache dir.
- `tkn-act help-json` — full command / flag / example tree.
- `tkn-act agent-guide` prints the embedded agent guide
  ([`AGENTS.md`](AGENTS.md)) — also the canonical place to read about
  conventions, exit codes, JSON contracts, and the project rule that
  every change must update related docs in the same PR.

## Documentation

| Document | Purpose |
|---|---|
| [`AGENTS.md`](AGENTS.md) | Canonical guide for AI agents and scripts: machine-readable interfaces, exit codes, JSON shapes, environment variables, and project rules (squash-merge, tests-required, docs-sync). Also embedded in the binary (`tkn-act agent-guide`). |
| [`docs/feature-parity.md`](docs/feature-parity.md) | Single source of truth for which Tekton features are `shipped` / `in-progress` / `gap`, with the e2e fixture and limitations fixture per row. CI gate: `parity-check`. |
| [`docs/short-term-goals.md`](docs/short-term-goals.md) | Prioritized tracks for upcoming work (Track 1 = Tekton features, Track 2 = backend parity, Track 3 = ergonomics). Status updated as items land. |
| [`docs/test-coverage.md`](docs/test-coverage.md) | What runs in each CI workflow, which paths trigger which workflow, and which fixtures are in `-tags integration` vs `-tags cluster`. |

## License

Apache 2.0
