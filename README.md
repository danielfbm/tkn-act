# tkn-act

Run [Tekton Pipelines](https://tekton.dev) locally on Docker — no Kubernetes
cluster required. Inspired by [`nektos/act`](https://github.com/nektos/act).

## Status

v1 alpha. Single Docker backend. k3d cluster backend planned for v1.x.

## Install

    go install github.com/dfbmorinigo/tkn-act/cmd/tkn-act@latest

Or via `tkn`'s plugin discovery — drop the binary on your PATH and run `tkn act ...`.

## Usage

    cd my-repo-with-pipeline.yaml
    tkn-act               # auto-discovers pipeline.yaml / .tekton/

    tkn-act run -f pipeline.yaml -p revision=main -w shared=./build
    tkn-act validate
    tkn-act list

## What's supported (v1)

- Tekton `tekton.dev/v1` `Task`, `Pipeline`, `PipelineRun`, `TaskRun`
- Steps with `image`, `command`, `args`, `script`, `env`, `workingDir`
- Params (string, array, object), defaults, `$(params.x)` and `$(params.x[*])`
- Results (file-based at `/tekton/results/<n>`) and `$(tasks.X.results.Y)`
- Workspaces shared across tasks (host bind mounts)
- DAG ordering via `runAfter` and result-data deps
- `when` expressions (operator: `in`, `notin`)
- `finally` tasks

## What's not supported in v1

Sidecars, StepActions, retries, Resolvers (git/hub/cluster), tekton-results,
custom tasks, signed pipelines, Windows.

See `docs/superpowers/specs/2026-05-01-tkn-act-design.md` for the full spec.

## Cluster backend (v1.1)

For Tekton-fidelity runs (real controller, real entrypoint shim):

    tkn-act cluster up               # one-time, ~30-60s
    tkn-act run --cluster -f pipeline.yaml
    tkn-act cluster status
    tkn-act cluster down

Requires `k3d` and `kubectl` on PATH.

## For AI agents

`tkn-act` has first-class support for AI agents and scripts:

- [`AGENTS.md`](AGENTS.md) — canonical guide (also embedded; print with
  `tkn-act agent-guide`).
- `tkn-act doctor -o json` — preflight: Docker, k3d, kubectl, cache dir.
- `tkn-act help-json` — full command/flag/example tree as JSON.
- Every command supports `--output json` with a stable shape.
- Stable, documented exit codes (0 ok, 2 usage, 3 env, 4 validate, 5 pipeline,
  130 cancelled).

## License

Apache 2.0
