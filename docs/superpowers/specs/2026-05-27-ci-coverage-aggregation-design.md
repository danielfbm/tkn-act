# CI coverage aggregation

**Date:** 2026-05-27
**Status:** Implemented — implementation in same PR

## Problem

Four distinct gaps in CI coverage visibility motivated this change.

### 1. The coverage gate was blind to integration and cluster test runs

The PR-only `coverage` job in `ci.yml` runs `go test -cover -count=1 ./...`
(default test set, no build tags). It enforces a per-package no-drop invariant,
but it never sees the lines exercised by `-tags integration` (docker e2e) or
`-tags cluster` (k3d e2e). Packages like `internal/backend/docker` are dominated
by code that only runs under Docker; the default-tag measurement of that package
shows a misleadingly low absolute number (~24%) even when all real behavior is
well-exercised by the integration suite.

The gate was never wrong about regressions — if a unit-tested path stopped being
covered, the gate would catch it. But there was no way to see how much of the
codebase the full test suite (unit + integration) actually covers as a whole.

### 2. No aggregate total coverage number

There was no single number answering "what percentage of tkn-act's statements
does the full CI test suite cover?" The coverage gate's per-package table is
useful for regression detection but useless for answering that question.
External contributors, code reviewers, and the project's own coverage tracking
had no baseline to reference.

### 3. GitHub Actions runtime deprecation

All six workflows pinned `actions/checkout@v4`, `actions/setup-go@v5`, and
`actions/upload-artifact@v4`. GitHub announced the deprecation of the Node 20
runtime (which backs v4/v5 of those actions) in favor of Node 24. `upload-artifact@v5`
is still Node 20; v6 is the first Node 24 major. Running deprecated action
runtimes produces warning noise in every CI run and will eventually become a
hard failure.

### 4. Per-package measurement noise causing false-positive gate failures

`go test -cover` reports per-package coverage by counting which statements the
Go test runner exercised. On packages like `cmd/tkn-act` the coverage number
swings by up to 0.3pp between CI runs with no code change, driven by goroutine
scheduling and map iteration order flipping which blocks execute during test
setup. A 0.1pp tolerance absorbed only rounding error, not realistic measurement
noise, causing occasional spurious `coverage no-drop` gate failures on no-Go-change
PRs.

## Goals

- **An aggregated total coverage number** for the full unit + integration
  test suite, computed by merging Go 1.20+ binary coverage profiles from
  both suites.
- **A job summary** showing the total and a per-package breakdown on every
  PR and every push to main, surfaced directly in the GitHub Actions UI
  without navigating to artifact downloads.
- **A downloadable artifact** (`coverage-report`) containing the merged
  profile, rendered HTML, and per-package text — for local diffing.
- **Separate cluster coverage reporting** that tracks how much the cluster
  e2e suite adds, without conflating it with the docker-based total (the
  cluster suite runs on a Tekton-version matrix; merging it would require
  de-duplicating across matrix legs).
- **A wider per-package tolerance** (0.5pp) on the no-drop gate that
  absorbs realistic measurement noise without weakening the invariant
  that matters: catching actual regressions.
- **Action version bumps** to Node 24-compatible versions across all
  six workflows to eliminate deprecation warnings.

## Non-goals

- **Adding new tests or raising coverage numbers.** The baseline unit-only
  total is 72.5%. The lowest packages are `internal/backend/docker` (24.1%,
  almost all code under `-tags integration`), `internal/workspace` (47.1%),
  and `cmd/tkn-act` (64.5%). Raising these is a separate future PR; this
  spec and its implementation deliberately do not touch test files.
- **Publishing coverage to an external service** (Codecov, Coveralls, custom
  dashboards). The job summary covers the immediate need; an external service
  can be wired later.
- **Merging cluster coverage into the aggregate total.** The cluster suite
  runs on a Tekton-version matrix. Merging across matrix legs would require
  either running the full matrix sequentially (2× wall time) or de-duplicating
  profiles post-hoc (fragile). Reported separately for now.
- **Coverage gating on the integration total.** The merged profile includes
  network I/O, Docker daemon behavior, and test-harness warming paths that
  are inherently non-deterministic. Using it as a gate would produce more
  noise than signal. The per-package no-drop gate on the unit suite remains
  the actual enforcement mechanism.
- **Reducing wall time** of the existing workflows. The new `coverage.yml`
  job adds wall time; the design accepts this since it runs the integration
  suite on ubuntu-latest where Docker is available and the integration suite
  already runs in `docker-integration.yml`.

## Architecture

### Binary coverage (Go 1.20+) and `covdata` merge

`go test -cover -count=1 ./...` emits per-package line-count profiles in the
legacy text format. These cannot be merged across build tags: the default-tag
and `-tags integration` compilations produce binaries with different
instrumentation shapes, so summing their text profiles double-counts any
statement exercised by both.

Go 1.20 introduced binary coverage mode: compile with `-coverpkg=./... ...
-args -test.gocoverdir=DIR` and the runtime writes raw binary coverage data
into `DIR`. Multiple runs (different tags, different packages) write into the
same or separate directories. `go tool covdata merge -i dir1,dir2 -o merged`
correctly unions them: a statement covered by either run appears as covered.
`go tool covdata textfmt -i merged -o coverage.txt` converts the merged binary
data to the standard text format; `go tool cover -func coverage.txt` produces
the per-function and total-line summary.

This is the mechanism `coverage-report.sh` uses. Two phases:

1. **Unit pass**: `go test -coverpkg=./... -count=1 ./... -args -test.gocoverdir=unit-cov/`
2. **Integration pass** (skipped when `COVERAGE_INTEGRATION=0`):
   `go test -tags integration -coverpkg=./... -count=1 ./internal/e2e/...
   ./internal/backend/docker/... -args -test.gocoverdir=integration-cov/`
3. **Merge**: `go tool covdata merge -i unit-cov,integration-cov -o merged-cov/`
4. **Output**: `go tool covdata textfmt` → `coverage.txt`;
   `go tool cover -func coverage.txt` for per-function / total;
   `go tool cover -html coverage.txt -o coverage.html` for browser view;
   `go tool covdata percent -i merged-cov/` for per-package percentages.

### Report vs. gate split

The `coverage.yml` workflow is a **report**, not a gate. It writes a Markdown
summary to `$GITHUB_STEP_SUMMARY` (total + collapsible per-package table) and
uploads the `coverage-report` artifact. It does **not** fail the PR on a coverage
drop. The existing `coverage` job in `ci.yml` remains the gate.

The rationale: the integration suite is best-effort in the presence of Docker
daemon flakiness (pre-pull failures, port contention). `coverage-report.sh` exits 1
only if the unit pass or the covdata merge tooling fails; a flaky integration
run is logged but does not fail the report job. Making the report job a gate
would make every flaky Docker pre-pull a PR blocker — exactly the wrong tradeoff.

### Why cluster coverage is separate

`cluster-integration.yml` runs a Tekton-version matrix (currently v1.3.0 and
v1.12.0). Each leg boots its own k3d cluster and runs the full fixture table.
The coverage profiles from the two legs measure the same Go source with the
same compilation unit, so naively merging them adds no information — the union
is the same as either leg individually. But the merge step would require the
legs to finish before the aggregate could be computed, forcing the matrix to be
sequential or requiring a post-matrix fan-in job.

The chosen design: `cluster-integration.yml` emits a `go tool cover -func`
total as a per-Tekton-version job summary section and uploads a
`coverage-cluster-<version>` artifact per leg. This gives reviewers the
cluster-coverage number for each Tekton version independently and without
adding a fan-in step.

### Tolerance: 0.1pp → 0.5pp

The prior 0.1pp tolerance was chosen as a rounding buffer: `go test -cover`
reports one decimal place, so adjacent rounds differ by at most 0.05pp. In
practice the measurement oscillates by up to 0.3pp on packages with
goroutine-synchronized code paths (observed on `cmd/tkn-act` across five
identical-code CI runs). 0.5pp absorbs that noise while remaining tight
enough to catch a 1pp regression (which corresponds to ~5 statements on
a 500-statement package like `cmd/tkn-act`).

The gate logic, script structure, override token, and PR-only trigger are
unchanged.

### Action version matrix

| Action | Old | New | Why |
|---|---|---|---|
| `actions/checkout` | v4 | v5 | First Node 24 major |
| `actions/setup-go` | v5 | v6 | First Node 24 major |
| `actions/upload-artifact` | v4 | v6 | v5 is still Node 20; v6 is the first Node 24 major |

All six workflow files updated: `ci.yml`, `docker-integration.yml`,
`remote-docker-integration.yml`, `cluster-integration.yml`,
`cli-e2e.yml`, `cli-self-build.yml`.

## Doc-rule updates

No new `docs/agent-guide/` sections are needed (this is a CI-only change with
no user-facing CLI behavior). `docs/test-coverage.md` is updated to reflect:

- The 0.5pp tolerance on the coverage gate.
- The new `coverage.yml` workflow, `coverage-report.sh`, the merged-profile
  approach, the job summary, and the artifact.
- The separate cluster coverage section in `cluster-integration.yml`.
- A note in "Not run by CI" that a job summary and artifact now exist
  (removing the stale claim that "absolute coverage numbers over time are not
  tracked anywhere").

`AGENTS.md`'s "Coverage gate (sibling rule)" section is updated to reference
0.5pp tolerance.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| Integration run in `coverage-report.sh` is flaky (Docker pre-pull fails, port contention) | Script exits 0 on integration failure; only unit + merge tooling failure exits 1. Flakiness shows as a lower total in the job summary, not a PR block. |
| `coverage-report.sh` adds significant wall time to PRs | Job runs on ubuntu-latest in parallel with existing workflows; it doesn't gate anything. Accepted. |
| 0.5pp tolerance allows a real 0.4pp regression to slip through | A 0.4pp drop on a 500-statement package is ~2 statements. The tests-required gate ensures every code change has a test; the combination keeps the practical risk low. |
| `go tool covdata` binary format changes in a future Go release | Binary coverage has been stable since Go 1.20; `go tool covdata merge` is the documented merge path. Risk is low for the next several minor versions. |
| Action version bumps break a workflow | All six workflows are exercised on every PR; a broken step fails the job immediately and visibly. |
| Cluster leg wall time increases due to `-coverpkg=./...` instrumentation overhead | Instrumented binaries are ~10% slower. The cluster-integration job already has a 25m timeout; the overhead is within the existing budget. |

## What this spec deliberately does not decide

- Whether to publish the merged coverage total to an external service
  (Codecov, Coveralls, a custom badge). The job summary covers the immediate
  need; external publishing is a follow-up.
- Whether the `coverage.yml` job should eventually become a gate (e.g., fail
  if the integration total drops below 70%). Deferred until the integration
  suite is reliably non-flaky.
- Whether to raise the per-package tolerance further (e.g., to 1pp) for
  inherently noisy packages. The 0.5pp choice is conservative; revisit if
  false positives persist.
- Whether to add an arm64 or macOS leg to `coverage.yml`. Out of scope per
  the v1 platform matrix.
- How to track coverage trends over time (historical dashboards, PR comments
  with coverage diffs). Deferred; the job summary gives a per-run number and
  the artifact gives a per-run profile for manual comparison.
