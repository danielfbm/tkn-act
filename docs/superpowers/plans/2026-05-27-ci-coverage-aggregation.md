# CI coverage aggregation — Implementation Plan

**Goal:** Compute an aggregated TOTAL coverage number across the unit and
docker-integration test suites using Go 1.20+ binary coverage (`covdata`
merge), surface it as a GitHub Actions job summary and downloadable artifact
on every PR and push to main, report cluster coverage separately per
Tekton-version matrix leg, widen the per-package no-drop tolerance from
0.1pp to 0.5pp to absorb measurement noise, and bump all six workflows to
Node 24-compatible action versions.

**Architecture:** New `coverage-report.sh` script (Go binary-coverage
merge approach) + new `coverage.yml` report workflow + tolerance bump in
`coverage-check.sh` + coverage instrumentation in `cluster-integration.yml`
+ action version bumps across all six workflow files. No new Go production
code. No new tests. Implements
`docs/superpowers/specs/2026-05-27-ci-coverage-aggregation-design.md`.

**Scope:** CI scripts, workflow YAML, and documentation only. Raising
absolute coverage numbers is explicitly out of scope and is a separate
future PR.

---

## Task 1 — Bump GitHub Actions to Node 24-compatible versions

**Files:**
- `.github/workflows/ci.yml`
- `.github/workflows/docker-integration.yml`
- `.github/workflows/remote-docker-integration.yml`
- `.github/workflows/cluster-integration.yml`
- `.github/workflows/cli-e2e.yml`
- `.github/workflows/cli-self-build.yml`

- [x] Replace `actions/checkout@v4` with `actions/checkout@v5` in all six
      workflow files.
- [x] Replace `actions/setup-go@v5` with `actions/setup-go@v6` in all six
      workflow files.
- [x] Replace `actions/upload-artifact@v4` with `actions/upload-artifact@v6`
      in all workflow files that use it (note: v5 is still Node 20; v6 is the
      first Node 24 major).
- [x] Verify no other Node 20-pinned actions remain across the six files.

---

## Task 2 — Widen coverage-check.sh tolerance from 0.1pp to 0.5pp

**Files:**
- `.github/scripts/coverage-check.sh`

- [x] Change the tolerance constant from `0.1` to `0.5` in
      `coverage-check.sh`. The change is a one-line edit to the comparison
      threshold.
- [x] Update the script's inline comment to note the rationale: `go test
      -cover` measurements on goroutine-synchronized packages (e.g.
      `cmd/tkn-act`) swing up to 0.3pp between identical-code CI runs;
      0.5pp absorbs this without masking a real 1pp regression.
- [x] Verify the `[skip-coverage-check]` override token, the per-package
      table output, and the PR-only trigger are unchanged.

---

## Task 3 — Write coverage-report.sh (covdata binary merge)

**Files:**
- `.github/scripts/coverage-report.sh`

- [x] Create `.github/scripts/coverage-report.sh` (executable).
- [x] **Unit pass:** `go test -coverpkg=./... -count=1 ./... -args
      -test.gocoverdir=<tmpdir>/unit-cov`. Exit 1 on failure (unit suite is
      not optional).
- [x] **Integration pass** (guarded by `COVERAGE_INTEGRATION != 0`):
      `go test -tags integration -coverpkg=./... -count=1
      ./internal/e2e/... ./internal/backend/docker/...
      -args -test.gocoverdir=<tmpdir>/integration-cov`. Log failure and
      continue (best-effort; does not exit 1).
- [x] **Merge:** `go tool covdata merge -i <unit-cov>[,<integration-cov>]
      -o <tmpdir>/merged-cov`. Exit 1 on failure.
- [x] **Text profile:** `go tool covdata textfmt -i merged-cov
      -o coverage/coverage.txt`.
- [x] **HTML report:** `go tool cover -html coverage/coverage.txt
      -o coverage/coverage.html`.
- [x] **Per-package:** `go tool covdata percent -i merged-cov
      > coverage/per-package.txt`.
- [x] **Job summary:** write a Markdown summary to `$GITHUB_STEP_SUMMARY`
      (when set) with: the total percentage from `go tool cover -func`
      (`total:` line), and a `<details>` collapsible block containing the
      per-package table.
- [x] Ensure `coverage/` directory is created if absent.

---

## Task 4 — Add coverage.yml workflow

**Files:**
- `.github/workflows/coverage.yml`

- [x] Create `.github/workflows/coverage.yml` with:
  - Triggers: `pull_request` + `push` to `main` and `release/**`.
  - Single job `coverage` on `ubuntu-latest`, named "total coverage
    (unit + integration)".
  - Steps: `actions/checkout@v5`, `actions/setup-go@v6`, docker sanity
    check (`docker info`), warm images (`docker pull alpine:3`,
    `docker pull registry.k8s.io/pause:3.9`), run
    `.github/scripts/coverage-report.sh`, `actions/upload-artifact@v6`
    with name `coverage-report` and path `coverage/`.
- [x] This is a **report** job, not a gate. It does not gate merge; its
      failure only means the report is missing, not that the PR is broken.

---

## Task 5 — Add cluster coverage instrumentation to cluster-integration.yml

**Files:**
- `.github/workflows/cluster-integration.yml`

- [x] Add `-coverpkg=./... -coverprofile=coverage-cluster.txt` to the
      `go test -tags cluster` invocation.
- [x] After the test step, emit a per-Tekton-version coverage total to the
      job summary: run `go tool cover -func coverage-cluster.txt` and append
      a `## Cluster coverage (<version>)` section to `$GITHUB_STEP_SUMMARY`.
- [x] Upload `coverage-cluster.txt` as artifact `coverage-cluster-<version>`
      using `actions/upload-artifact@v6`.
- [x] Keep the `-tags cluster` run independent from the `coverage.yml`
      unit+integration total: cluster coverage is reported per matrix leg,
      not merged into the aggregate.

---

## Task 6 — Doc convergence

All documentation updated in the same PR as the implementation.

- [x] **`docs/superpowers/specs/2026-05-27-ci-coverage-aggregation-design.md`**
      — this spec. Describes the problem, goals/non-goals, architecture
      (covdata merge approach, report-vs-gate split, why cluster is separate,
      0.5pp tolerance rationale, action version matrix), doc-rule updates,
      risks, and what the spec does not decide.
- [x] **`docs/superpowers/plans/2026-05-27-ci-coverage-aggregation.md`**
      — this plan. Six tasks as checkboxes, all marked done. Doc convergence
      section listing every touched document.
- [x] **`AGENTS.md` "Coverage gate (sibling rule)" section** — update the
      tolerance reference from 0.1pp to 0.5pp with the noise rationale.
- [x] **`docs/test-coverage.md`** — three updates:
  1. Change the 0.1pp → 0.5pp tolerance reference in the "Coverage gate"
     subsection and in the "How to read CI failures" table.
  2. Add a new "Aggregated coverage report" subsection under the Workflows
     section describing `coverage.yml`, `coverage-report.sh`, the merged
     profile, job summary, artifact, and `COVERAGE_INTEGRATION` env var.
  3. Add a row to the "Workflows" table for `coverage.yml`.
  4. Note the separate cluster coverage in the `cluster-integration.yml`
     row and in the "Not run by CI" section (removing the stale "no coverage
     dashboards" claim).

---

## Future PR (out of scope here)

- Raise absolute coverage in `internal/backend/docker` (currently 24.1%
  unit-only; most paths only exercised under `-tags integration`).
- Raise `internal/workspace` (47.1%) and `cmd/tkn-act` (64.5%).
- These require new test code and ship as a separate PR with their own
  tests-required and coverage-gate checkpoints.
