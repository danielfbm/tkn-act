# Test coverage — what CI runs and what it doesn't

Last updated: 2026-05-03 (cross-backend fidelity work; covers v1.2).

This document inventories what the GitHub Actions pipelines and the
test suite cover today, and — equally important — what they do not. It
is the place to look before claiming "the tests pass" really means "the
behavior I changed is exercised."

---

## 1. Workflows

Three workflows live under `.github/workflows/`:

| File | When it runs | What it runs |
|---|---|---|
| `ci.yml` | every push, every PR | `go vet`, `go build`, `go test -race -count=1 ./...` (untagged), `tkn-act help-json` smoke; matrix: ubuntu-latest + macos-latest. PR-only `tests-required` job. |
| `docker-integration.yml` | `push` to `main`/`release/**` always; PR only when paths in its filter changed | `go test -tags integration -count=1 -timeout 15m ./internal/e2e/...` on ubuntu-latest. Pre-pulls `alpine:3`. |
| `cluster-integration.yml` | same trigger pattern | installs `kubectl` + `k3d`, then `go test -tags cluster -count=1 -timeout 25m ./internal/clustere2e/...` on ubuntu-latest. Dumps cluster state on failure. |

`tests-required` (in `ci.yml`) fails any PR whose diff modifies any
`*.go` file outside `_test.go`/`vendor/` without modifying any
`_test.go` file. Override per-PR by including the literal token
`[skip-test-check]` in any commit message in the PR.

### Path scopes (PR-only gates)

| Workflow | Triggers when these change |
|---|---|
| `docker-integration` | `cmd/tkn-act/**`, `internal/backend/{backend.go,docker/**}`, `internal/{engine,exitcode,loader,reporter,resolver,tektontypes,validator,volumes,workspace}/**`, `internal/e2e/**`, `testdata/e2e/**`, `go.mod`, `go.sum`, the workflow file itself |
| `cluster-integration` | `cmd/tkn-act/{cluster*.go,doctor*.go}`, `internal/backend/{backend.go,cluster/**}`, `internal/cluster/**`, `internal/clustere2e/**`, `internal/cmdrunner/**`, `internal/{engine,loader,tektontypes,validator}/**`, `testdata/e2e/**`, `go.mod`, `go.sum`, the workflow file itself |

Pushes to `main` or `release/**` ignore the path filter and always run
both, so a release branch can never ship without the full matrix going
green.

---

## 2. Tests by build tag

### Default (no tag) — runs in `ci.yml`

Pure-Go unit tests with no Docker, no cluster, no network. Fast (<3s
total locally). Covers:

| Package | What's tested |
|---|---|
| `cmd/tkn-act/` | help-json shape & exit-code table; doctor JSON shape; `agent-guide` content; list/validate/version JSON; reporter-flag routing & mutual exclusion; `--color` parsing |
| `internal/backend/` | `LogSink` plumbing; `TaskInvocation`/`TaskResult` shapes |
| `internal/backend/cluster/` | unit-level `RunPipeline` shape (no real k8s) |
| `internal/cluster/k3d/` | driver state machine using a fake `cmdrunner` |
| `internal/cluster/tekton/` | YAML-apply readiness using a fake kube client |
| `internal/cmdrunner/` | exec wrapper |
| `internal/discovery/` | `pipeline.yaml` / `.tekton/` discovery |
| `internal/engine/` | DAG ordering, failure propagation, when-skip; **policy loop** (retries to success, retries-all-fail, task timeout, timeout-not-retried) |
| `internal/engine/dag/` | cycle detection, level computation |
| `internal/exitcode/` | stable code numbers (incl. exit 6 = timeout); `Wrap`/`From` |
| `internal/loader/` | YAML parsing; **limitations fixtures parse cleanly** so dropped fields don't silently break the docs |
| `internal/reporter/` | JSON one-event-per-line; pretty live-ordering across parallel tasks; quiet/verbose; color on/off; `ParseColorMode`; `ResolveColor` (`NO_COLOR`/`FORCE_COLOR`/`CLICOLOR_FORCE` precedence) |
| `internal/resolver/` | `$(params.x)`, `$(tasks.X.results.Y)`, `$(context.*)`, `$(results.X.path)`, `$(workspaces.X.path)`, array `[*]` expansion, `$$` escape; **per-step**: `$(step.results.X.path)`, `$(steps.X.results.Y)`; `SubstituteAllowStepRefs` deferral semantics |
| `internal/tektontypes/` | `ParamValue` JSON round-trip across string/array/object |
| `internal/validator/` | task ref / DAG / when-operator checks; **policy: negative retries, malformed timeout, unknown onError**; volume-kind plumbing (rejects unknown kinds, multiple sources, undeclared volumeMounts) |
| `internal/volumes/` | `Store` inline-vs-dir precedence; emptyDir/hostPath/configMap/secret materialization; items-projection; rejects `..` traversal |
| `internal/workspace/` | tmpdir + results-dir provisioning; cleanup boundaries |

### `-tags integration` — runs in `docker-integration.yml`

Real Docker required. Drives the full engine + docker backend through
fixtures under `testdata/e2e/`:

| Fixture | What it exercises |
|---|---|
| `hello/` | minimal task |
| `multilog/` | parallel tasks producing interleaved step logs |
| `params-and-results/` | params + Task-level results across the DAG |
| `workspaces/` | shared workspace bind-mounted across tasks |
| `when-and-finally/` (×2) | when-expressions + finally tasks (dev skip, prod run) |
| `failure-propagation/` | downstream skip on upstream failure |
| `onerror/` | step `onError: continue` |
| `retries/` | task succeeds on the third attempt (`retries: 3`) |
| `timeout/` | task ends `timeout` after 1s sleep-30 |
| `step-results/` | per-step results substitution |
| `volumes/` | inline configMap + emptyDir mounted into a step |
| `pipeline-timeout/` | `Pipeline.spec.timeouts.pipeline: 2s` triggers run-level timeout |
| `tasks-timeout/`    | `tasks` budget fires; `finally` block still runs to completion |
| `finally-timeout/`  | `finally` budget fires; `tasks` block already succeeded |
| `step-template/`    | `Task.spec.stepTemplate` inheritance: image + env, with one Step overriding env |

Plus `internal/backend/docker/docker_integration_test.go`.

### `-tags cluster` — runs in `cluster-integration.yml`

Needs `kubectl` + `k3d` on the host. Boots the project's ephemeral
cluster, installs Tekton, and runs the **same `internal/e2e/fixtures`
table the docker-integration job uses**, against the real controller.
Both backends consume `fixtures.All()`, so any new fixture under
`testdata/e2e/` automatically runs on both backends — divergences are
explicit `DockerOnly` / `ClusterOnly` flags on the descriptor rather
than silent omissions.

The cluster backend also surfaces the Tekton condition reason +
message verbatim on `engine.RunResult` (and on the run-end JSON
event's message field). When a fixture's `WantStatus` doesn't match,
the e2e harness prints both alongside the per-task outcome map,
turning previously opaque CI failures (`status = X, want Y ()`) into
attributable ones (`status = X, want Y reason="…" message="…"
tasks=[…]`).

---

## 3. What is NOT covered

In rough order of "you should be aware":

### By design (won't be tested locally)

- **Sidecars.** Need shared network namespaces; out of scope for the
  Docker backend. `testdata/limitations/sidecars/` documents the gap.
  Cluster mode covers them but our cluster e2e doesn't yet exercise a
  sidecar fixture.
- **Step-state isolation.** Each step is a separate container; cwd /
  env / `/tmp` from a prior step is gone. Documented as a foot-gun in
  `testdata/limitations/step-state/`.
- **Loading `kind: ConfigMap` / `kind: Secret` manifests** via
  `tkn-act -f`. Use `--configmap` / `--configmap-dir` (and the
  `--secret` equivalents). Manifest support is deferred to v1.3.

### Plumbed but not covered by an automated test

- **`emptyDir.medium: Memory`.** Parsed but treated as disk-backed; no
  `tmpfs` mount and no test asserts the difference.
- **SIGINT / SIGTERM during a run** — the documented exit code 130
  pathway has no integration test.
- **`--cluster-driver=kind`** — only k3d exists; there is no driver
  abstraction test.
- **StepActions, custom tasks, resolvers (git/hub/cluster/bundles),
  signed pipelines / tekton-chains, tekton-results.** All v1
  non-goals; no tests.

### Not run by CI

- **Linters beyond `go vet`.** No `golangci-lint`, `staticcheck`,
  `gosec`, or `govulncheck`. Style and security regressions would slip
  through.
- **Coverage reporting.** No `-coverprofile`, no Codecov / Coveralls
  upload. We don't track line coverage over time.
- **macOS docker / cluster integration.** macOS runners run only the
  build/vet/unit job (`ci.yml`); the integration workflows are
  ubuntu-only because docker isn't preinstalled on macos runners and
  k3d is Linux-friendliest.
- **Windows.** Out of scope per the v1 spec. No runner.
- **arm64.** Not in the runner matrix (ubuntu-latest is amd64).
- **Performance / benchmarks.** No `go test -bench`, no regression
  detection on pipeline wall time.
- **Pretty-UX visual regression.** Tests assert presence/absence of
  ANSI codes and the live-ordering invariant, but there are no
  end-to-end snapshot tests of the rendered output.
- **Doc / link checking.** No markdown linter, no broken-link check.
  This file's hyperlinks are not verified.
- **`tkn-act run` against a private registry.** Image pull tests use
  `alpine:3` (public). Auth paths are untested.

### Smoke vs. real e2e gaps

- The cluster-integration job runs the full shared fixture table from
  `internal/e2e/fixtures` (same descriptors as docker-integration), so
  parity is now a checkable invariant for every v1.2 feature.
  Backend-specific divergences (none today) would surface as
  `DockerOnly` / `ClusterOnly` flags on the descriptor, not silent
  omissions.
- `tkn-act doctor -o json` is asserted in unit tests but the full
  agent flow ("doctor → list → validate → run -o json → exit code")
  isn't asserted as a single integration sequence.

---

## 4. How to read CI failures

| Job fails | Look at |
|---|---|
| `build & test` | `go vet`, `go build`, or any unit test. Fast and local-reproducible: `go test -race ./...`. |
| `tests-required` | The PR has a Go code change without a `_test.go` change. Either add a test or include `[skip-test-check]` in a commit message. |
| `docker e2e` | An `internal/e2e/` test failed under real Docker. Reproduce locally with `go test -tags integration ./internal/e2e/...`. |
| `k3d e2e` | The k3d cluster failed to come up, Tekton failed to install, or the cluster fixture failed. The job dumps cluster pod state on failure; check that block in the GitHub Actions log. |

---

## 5. Adding new behavior

Per the contribution rule in `AGENTS.md`: every Go code change ships
with a test. When the new behavior touches:

- **Engine semantics** (status, retries, ordering): unit test in
  `internal/engine/` *and* a fixture in `testdata/e2e/` if it's
  user-visible.
- **Resolver / substitution**: `internal/resolver/*_test.go`.
- **CLI flags or output shape**: `cmd/tkn-act/helpjson_test.go` (so
  the agent contract reflects it) and a routing test in
  `cmd/tkn-act/`.
- **A docker-backend feature**: ideally an `_integration_test.go`
  case **and** a graduated fixture under `testdata/e2e/` so it runs
  in `docker-integration.yml`.
- **A cluster-backend feature**: a fixture exercised by
  `internal/clustere2e/`.

If a feature is intentionally Docker-only or Cluster-only, document
the discrepancy in `testdata/limitations/` so the gap is visible
rather than silent.
