# Full-project audit fixes + Tekton v1.15 LTS bump — Implementation Plan

> **Spec:** [`docs/superpowers/specs/2026-11-03-audit-and-tekton-v1.15-design.md`](../specs/2026-11-03-audit-and-tekton-v1.15-design.md)

Each workstream lands as its own commit(s) with tests. Every fix gets a
regression test that fails before the fix. File:line references are as
of `64c55be`.

## Pre-flight

- [ ] `go vet` (default/integration/cluster tags), `go test -race ./...`,
      `golangci-lint run ./...` (v2.12.2), `make check-agentguide`,
      `parity-check.sh` all green on base.

---

## WS-A — Tekton bump, installer, CI

- [ ] A1 `DefaultTektonVersion` → `v1.15.5` (`internal/cluster/tekton/install.go:25`); update tests.
- [ ] A2 Installer reads `tekton-pipelines/pipelines-info` `data.version`; re-applies `release.yaml` on mismatch with the requested version, and when the CRD exists but controller/webhook deployments are missing (`install.go:54-65`). `cluster up` prints the *actual* installed version (`cmd/tkn-act/cluster.go:88`).
- [ ] A3 `run --cluster` passes the resolved Tekton version (flag/env/default) into `clusterbe.Options.TektonVersion` (`cmd/tkn-act/run.go:~211`).
- [ ] A4 Stale comments: `enable-step-actions` is a no-op since v1.9 (`install.go:91-103`); alpha gating "through v1.15"; `internal/backend/cluster/run.go:72,252-258`; `docs/agent-guide/display-name.md:26`.
- [ ] A5 CI matrix `v1.9.9` + `v1.15.5` (`.github/workflows/cluster-integration.yml:53-54`); pin k3s image explicitly (`--image rancher/k3s:<v1.3x>` in `internal/cluster/k3d`), bump k3d/kubectl pins in Makefile + workflow together; install k3d by tagged release binary instead of `main/install.sh` (`cluster-integration.yml:75`, `scripts/check-cluster-deps.sh:59`).
- [ ] A6 `tests-required.sh` + `coverage-check.sh` diff against `git merge-base base head`.
- [ ] A7 `parity-check.sh` fails when a `both` row's fixture is `DockerOnly`/`ClusterOnly`; fix `matrix` row (`docs/feature-parity.md:136`) accordingly.
- [ ] A8 Examples `v1.3.0` → `v1.9.9` in README.md:91, `docs/agent-guide/README.md:271`, `cmd/tkn-act/cluster.go:75`, `docs/short-term-goals.md`, `docs/qa/*`, `docs/test-coverage.md:21`, `internal/e2e/fixtures/fixtures.go:334`; note pre-v1.13 `timeouts.tasks`+finally divergence in `docs/agent-guide/timeouts.md`.

## WS-B — Security + dependencies (refresolver, go.mod)

- [ ] B1 git resolver: refuse paths whose resolved target escapes the clone (symlinks) — `internal/refresolver/git.go` Resolve.
- [ ] B2 Bump go-git ≥ v5.19.3, go-billy ≥ v5.9.0, x/net, x/crypto, x/text; set `go`/`toolchain` so CI builds with go ≥ 1.25.13; `govulncheck ./...` reports no reachable fixable vulns.
- [ ] B3 http + hub clients: `CheckRedirect` rejects scheme downgrade (unless insecure allowed) and strips `Authorization` on host change.
- [ ] B4 Redact credentials/query strings in http/hub `Source` + errors; redact params in cache meta; cache files `0600`.
- [ ] B5 Allow-list check before cache lookup (`refresolver.go` Resolve).
- [ ] B6 `isLoopbackHost`/`isLoopbackRegistryHost` use `net.ParseIP(..).IsLoopback()` or `localhost`.
- [ ] B7 `io.LimitReader` on http/hub bodies and bundle layer decompression; bundles require the `kind` annotation.
- [ ] B8 Cache: `List`/`Prune`/`Clear` cover `git/<key>/` clones; clone into temp + atomic rename; git `@branch` refs re-fetch unless offline (document).
- [ ] B9 hub resolver: honour `type` param — `tekton` works; `artifact` fails with a clear error; doc gap row.
- [ ] B10 `TKN_ACT_HTTP_RESOLVER_TOKEN` accepted as alias of `TKNACT_HTTP_RESOLVER_TOKEN` (old name kept).

## WS-C1 — Substitution (resolver + engine substitution paths)

- [x] C1.1 Unknown-prefix `$(...)` (e.g. `$(date)`, `$(pwd)`) left literal; `substituteSpec` (`engine.go:756-799`) stops discarding errors; `$(workspaces.X.bound)` supported.
- [x] C1.2 Whole-array pass-through `$(params.arr[*])` as string value or sole array item (`engine.go:560-590`, `resolver.go:266`).
- [x] C1.3 `$(tasks.status)` and `$(tasks.<t>.status)` in finally tasks (params + when).
- [x] C1.4 `[N]` indexing and object `.key` for params and task results; `[*]` inside `when` values.
- [x] C1.5 Missing `$(context.*)`: `task.name`, `task.retry-count`, `pipelineTask.retries`, `*.namespace`, `*.uid`.

## WS-C2 — Scheduling (engine, volumes, workspace)

- [ ] C2.1 when-skipped (and all-rows-skipped matrix) tasks don't block `runAfter` dependents (`engine.go:669-720`).
- [ ] C2.2 Implicit DAG edges from `when` inputs/values; `taskResultRefPat` accepts `[*]`/`[N]`; matrix parents map to all expansions (`implicit_edges.go`).
- [ ] C2.3 Timeout/cancel: remaining levels get `task-skip` + appear in `RunResult.Tasks`; in-flight tasks killed by `timeouts.tasks/pipeline` reported `timeout` (`engine.go:316-320`).
- [ ] C2.4 Finally tasks run concurrently (bounded by `--parallel`); finally task with missing upstream results is skipped, not failed.
- [ ] C2.5 Retries get fresh results dir + emptyDir; `Optional` ConfigMap/Secret mount empty; `taskTimeoutFor` honours resolved Task timeout; non-matrix when-skip doesn't emit `task-end`.

## WS-D — Backends

- [ ] D1 `untarToDir` refuses writes through symlinked parents (`internal/backend/docker/staging.go:595-614`).
- [ ] D2 Log scanner handles >1MiB lines (truncate/split, drain on error) — `docker.go:841-857`, `sidecars.go:464-480`.
- [ ] D3 Join log goroutines (bounded wait) before container removal / task end — docker + cluster.
- [ ] D4 Cluster: wait for container running/terminated before `GetLogs(Follow)`, retry with backoff (`cluster/run.go:931-959`).
- [ ] D5 SSH: deadline on raw conn during handshake; `Ping` with timeout; `HostKeyAlgorithms` from known_hosts (`sshdial.go`).
- [ ] D6 Cluster watch re-establishes on close; context cancel patches PipelineRun `spec.status: Cancelled`; document namespace retention.
- [ ] D7 Step `computeResources` (v1) accepted, `resources` kept as alias; `securityContext`, `envFrom`, `env[].valueFrom`, step `timeout` either supported or rejected by the validator (no silent drop).
- [ ] D8 Results / step-results / workspace dirs `0o777` (docker local + remote staging).
- [ ] D9 Docker workspace `subPath`, `mountPath`, `readOnly` honoured (mount + `$(workspaces.X.path)`).
- [ ] D10 Sub-second stop grace rounds up to 1s; ConfigMap non-UTF-8 → `BinaryData`; `ImagePull` sends credentials from docker config.

## WS-E — CLI, validator, runstore

- [ ] E1 Positional args on `run`/`validate` treated as `-f` (error on mixing ambiguous inputs).
- [ ] E2 `--cluster-resolver-context`/`--cluster-resolver-kubeconfig` add `cluster` to allow-list (validator + registry).
- [ ] E3 Required pipeline params checked before execution (exit 4), `providedParams` wired.
- [ ] E4 `validate` uses the same `validator.Options` as `run`.
- [ ] E5 runstore: every index entry's ULID validated before path joins (prune/show/logs).
- [ ] E6 Abandoned runs (no end, old) prunable and reported `status: unknown`; zero times omitted.
- [ ] E7 `TKN_ACT_KEEP_RUNS` off-by-one; cancelled/timeout runs recorded with their real status.
- [ ] E8 `-o json` honoured by `runs prune` (and others listed in the audit) or docs narrowed.
- [ ] E9 Validator: finally-result consumption by main tasks, when-input refs to unknown tasks, empty `when.values`, duplicate step names, undeclared `$(params.x)`, negative task timeout, `PipelineTask.timeout`, v1.14 `$(...)` prefix rule; deterministic error order.
- [ ] E10 `--resolver-config` wired for `hub.token` (or claim removed); `list -C` missing dir errors; case-insensitive ULID prefix; ambiguous-prefix candidate list; read-only `logs`/`runs show`.
- [ ] E11 Docs: `runs list -o json` keys, `task_finished`, `-p` misuse in README, feature-parity stable-kinds list, `step-start/step-end` gap note.

---

## Doc convergence

- [ ] `docs/feature-parity.md` — matrix row backends, hub `type: artifact` gap, `$(context.*)`, `$(tasks.status)`, finally parallelism, step fields.
- [ ] `docs/agent-guide/*.md` touched by each WS, then `go generate ./cmd/tkn-act/`.
- [ ] `README.md` — Tekton version examples, positional args, resolver flags.
- [ ] `docs/test-coverage.md` — matrix versions, any new fixtures.
- [ ] `docs/short-term-goals.md` — Tekton version references.
- [ ] `AGENTS.md` — env-var list gains `TKN_ACT_HTTP_RESOLVER_TOKEN` / `TKNACT_HTTP_RESOLVER_TOKEN`.
- [ ] Follow-up issues filed for spec non-goals.
