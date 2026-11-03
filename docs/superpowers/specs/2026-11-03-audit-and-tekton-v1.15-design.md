# Full-project audit fixes + Tekton v1.15 LTS bump

**Date:** 2026-11-03
**Status:** Implemented in the same PR
**Plan:** [`docs/superpowers/plans/2026-11-03-audit-and-tekton-v1.15.md`](../plans/2026-11-03-audit-and-tekton-v1.15.md)

## Problem

1. **Tekton pin is stale and the CI matrix is past EOL.** The default
   install is `v1.12.0` and the cluster-integration matrix is
   `v1.3.0` + `v1.12.0`. Per upstream
   [`releases.md`](https://github.com/tektoncd/pipeline/blob/main/releases.md)
   (checked 2026-11-03) v1.3 went EOL 2026-08-04 and v1.6 went EOL
   2026-10-31. Supported LTS lines are v1.9 (EOL 2027-01-30),
   v1.12 (EOL 2027-05-04) and v1.15 (EOL 2027-07-31, newest declared
   LTS). Each line has shipped patch releases (v1.9.9, v1.12.3,
   v1.15.5 on 2026-10-23) that we don't pick up because we pin `.0`.
   v1.18.0 was tagged 2026-10-26 (the October LTS slot) but is not yet
   declared LTS in `releases.md`; it is out of scope here.
2. **A full audit (five parallel reviewers: Tekton-compat, engine,
   backends, CLI/validator/runstore, refresolver/CI/docs) found ~70
   verified defects.** Highlights: host-file read via symlinks in git
   resolver clones, path traversal on remote-docker workspace pull-back
   and on `runs prune`, reachable CVEs in go-git and the Go 1.25.0
   stdlib, shell `$(cmd)` silently blanking step scripts, when-skipped
   tasks blocking `runAfter` dependents, missing `$(tasks.status)`,
   log-stream hangs on >1MiB lines, the installer never upgrading an
   existing cluster, positional file args silently ignored by
   `run`/`validate`, the documented `--cluster-resolver-context` opt-in
   failing validation, and CI's `tests-required`/`coverage` gates
   diffing against the base tip instead of the merge-base.

## Goals

- Default Tekton → **v1.15.5**; CI matrix → **v1.9.9** (oldest
  supported LTS) + **v1.15.5** (newest LTS). Pin latest patch, not `.0`.
- Policy (amends the 2026-05-13 spec's non-decision): on each new
  *declared* LTS, bump default to its latest patch and set the matrix to
  {oldest still-supported LTS, newest LTS}; refresh patch versions
  opportunistically.
- `cluster up` / `run --cluster` detect the installed Tekton version
  (`tekton-pipelines/pipelines-info` ConfigMap `data.version`) and
  re-apply `release.yaml` when it differs from the requested version,
  or when the CRD exists but deployments don't (interrupted install).
  `run --cluster` honours `TKN_ACT_TEKTON_VERSION`.
- Fix every high and medium audit finding that is a defect in shipped
  behaviour, each with a regression test; fix lows where cheap.
- Bring all docs (README, agent-guide, feature-parity, test-coverage,
  short-term-goals, QA plans) in line with the code.

## Non-goals (follow-ups, listed in the PR description)

- Artifact Hub support for the `hub` resolver (`type: artifact`, the
  upstream default). This PR makes `type` explicit: `tekton` works,
  `artifact` (or unset → documented as tekton-hub-only) is a `gap`
  row in feature-parity.
- Emitting `step-start` / `step-end` JSON events (listed as stable
  kinds but never emitted). Documented as a gap; not removed (contract).
- Pipelines-in-Pipelines (`PipelineTask.pipelineRef/pipelineSpec`).
- Moving from `github.com/docker/docker` to `github.com/moby/moby/client`.
- SHA-pinning GitHub Actions.
- Cluster namespace garbage collection beyond documenting it.

## Architecture / approach

Work is partitioned by package into six independent workstreams, each
implemented on its own worktree and merged into the PR branch:

| WS | Scope | Packages |
|---|---|---|
| A | Tekton bump, installer version reconciliation, CI matrix + k3d/kubectl pins, CI script merge-base + parity DockerOnly check, Tekton-related doc refresh | `internal/cluster/*`, `cmd/tkn-act/cluster.go`, `.github/`, `Makefile`, `scripts/` |
| B | Security + deps: refresolver symlink/redirect/redaction/allow-list order/loopback/size limits/cache refresh, go-git + x/* + toolchain bumps | `internal/refresolver`, `go.mod` |
| C1 | Substitution: unknown `$(...)` left literal, `[*]`/`[N]`/object-key refs, `$(tasks.status)`, `$(context.*)` | `internal/resolver`, `internal/engine` (substitution paths) |
| C2 | Scheduling: when-skip non-blocking, implicit edges from `when` + indexed refs + matrix parents, timeout/cancel marking, parallel finally + missing-result skip, retry state reset, optional CM/Secret, resolved-task timeout | `internal/engine` (scheduling), `internal/volumes`, `internal/workspace` |
| D | Backends: untar symlink traversal, long log lines, log join, cluster log readiness, ssh deadlines + host-key algos, watch re-establish + cancel patch, `computeResources`, dir modes, workspace subPath/mountPath/readOnly, stop-grace rounding, ConfigMap BinaryData, registry auth | `internal/backend/*`, `internal/tektontypes` |
| E | CLI/validator/runstore: positional args, cluster-resolver opt-in, required params, `validate` ≡ `run` options, runstore ULID validation, abandoned runs, KEEP_RUNS off-by-one, cancelled status, `-o json` coverage, validator gaps + deterministic order + v1.14 `$(...)` prefix rule, `--resolver-config` | `cmd/tkn-act` (non-cluster), `internal/validator`, `internal/runstore`, `internal/reporter` |

### Behavioural decisions

- **Unknown `$(...)` references** (no recognised prefix) are left
  literal, matching Tekton. Recognised-prefix references that fail to
  resolve fail the task (no silent blanking).
- **Positional args** on `run`/`validate`: treated as `-f` (additive,
  matches the docs' own examples) — not a contract break.
- **`$(context.taskRun.retry-count)` etc.** are populated on docker
  with Tekton's values; namespace is `default`-equivalent per spec of
  the docker backend (documented).
- **Finally tasks** run concurrently (bounded by `--parallel`), matching
  Tekton.
- **Docker result/workspace dirs** are created `0o777` to match Tekton
  emptyDir semantics for non-root images.
- **CI gates** diff against `git merge-base base head`.

## Doc-rule updates

No change to AGENTS.md rules. AGENTS.md public-contract env-var list
gains `TKNACT_HTTP_RESOLVER_TOKEN` (existing, undocumented there).

## Risks

- v1.9.9 leg: `timeouts.tasks` + finally terminates early on pre-v1.13
  Tekton (`PipelineRunTimeoutRunningFinally` landed in v1.13). The
  `tasks-timeout` fixture asserts only run status, so it stays green;
  documented in `timeouts.md`.
- Engine semantic changes (when-skip, finally parallelism) can change
  outcomes of existing user pipelines — in the direction of Tekton
  fidelity. Called out in the PR.
- Cluster e2e can't run in the authoring sandbox; the CI matrix is the
  verification for v1.9.9 / v1.15.5.
