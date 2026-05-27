# Lint cleanup + golangci-lint v2 migration + blocking gate — design

Date: 2026-05-27
Status: approved (brainstorming)
Topic: lint cleanup, golangci-lint v1→v2 migration, blocking CI lint gate

## Problem

`tkn-act` carries a backlog of lint findings and runs an end-of-life linter
configuration:

- **Lint is not gated.** `golangci-lint` only runs inside the `cli-self-build`
  pipeline's best-effort `golangci-lint` task, which records `lint-exit` as a
  pipeline result but always `exit 0`. Findings never fail a PR, so the
  backlog grows unchecked.
- **The linter is on an EOL major.** CI pins `golangci-lint@v1.64.8` and
  `.golangci.yml` is in the v1 config format, while v2 is the current
  release (latest `v2.12.2`). Running v2 locally against the v1 config fails
  outright (`unsupported version of the configuration`), so contributors with
  a current install can't lint at all.
- **58 open findings** (measured with v2.12.2 against the migrated config):

  | Linter | Count | Nature |
  |---|---|---|
  | `revive` | 26 | unused params (rename to `_`), vars shadowing builtins (`cap`/`max`/`real`/`copy`), 1 empty block |
  | `errcheck` | 21 | unchecked error returns — ~18 in `_test.go` (runs/gc/index/store tests), a few in prod (`internal/refresolver/git.go`) |
  | `staticcheck` | 8 | ST1005 capitalized error strings (3), SA1019 deprecated API (2), SA4006 dead assignment (2), QF1003 (1) |
  | `gofmt` | 3 | formatting |

This is the first of a planned three-PR refactor sequence
(lint → characterization tests → structural simplification). It is sequenced
first because it is small, low-risk, and establishes a green, enforced
baseline that the riskier later PRs must keep green.

## Goals

- Migrate the linter and its config to golangci-lint v2 (`v2.12.2`).
- Resolve all 58 findings with **zero behavior change**.
- Make lint a **blocking** CI gate on every PR.

## Non-goals

- **No file restructuring.** Splitting `engine.go` (1128 lines),
  `cluster/run.go` (1072), `docker.go` (896), etc. is the third PR in the
  sequence, not this one.
- **No new tests for coverage.** Raising coverage on the docker backend
  (24%), `workspace` (47%), and `cmd/tkn-act` (64%) is the second PR.
- **No logic changes.** Every fix preserves observable behavior; the existing
  test suite is the contract and must stay green untouched (except where a
  test file's *own* lint finding is the thing being fixed).

## Architecture / design

### A. golangci-lint v2 migration

- Replace `.golangci.yml` with the v2-format config produced by
  `golangci-lint migrate`:
  - `version: "2"`.
  - `linters.enable`: `revive`, `misspell` (the rest — `errcheck`, `govet`,
    `ineffassign`, `staticcheck`, `unused` — are default-on in v2;
    `gosimple` is folded into `staticcheck`, so the effective linter set is
    unchanged from the v1 config).
  - `formatters.enable`: `gofmt`, `goimports` (v2 moves formatters out of
    `linters`).
  - `testdata` excluded under both `linters.exclusions.paths` and
    `formatters.exclusions.paths`.
- `pipelines/tasks.yaml` (`golangci-lint` task):
  - `golangci-lint-version` default `v1.64.8` → `v2.12.2`.
  - install path `github.com/golangci/golangci-lint/cmd/golangci-lint@VER`
    → `github.com/golangci/golangci-lint/v2/cmd/golangci-lint@VER` (the `/v2/`
    module-path segment is required for v2).

### B. Fix the 58 findings

Landed as category-scoped commits so the diff is reviewable per concern:

1. `revive` — rename unused parameters to `_` (interface-method params can't
   be removed); rename locals/params shadowing builtins; delete the empty
   block.
2. `errcheck` — handle the error where it carries meaning; assign to `_ =`
   for genuinely-ignorable returns (predominantly test teardown such as
   `defer r.Finalize()`). **No `//nolint` directives** — fixes are real.
3. `staticcheck` — lowercase error strings (ST1005), swap deprecated APIs for
   their replacements (SA1019), drop dead assignments (SA4006), apply the
   QF1003 quick-fix.
4. `gofmt` — run the formatter.

### C. Blocking lint gate

Add a `lint` job to `.github/workflows/ci.yml`:

- `actions/checkout@v5` + `actions/setup-go@v6` (Node 24 majors, matching the
  rest of CI).
- Direct `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2`
  then `golangci-lint run ./...` — **no third-party action**, consistent with
  the repo's current no-third-party-actions posture (only `actions/*` are
  used today).
- Runs on `pull_request` and `push`; any finding fails the job.

The best-effort `golangci-lint` task inside `cli-self-build` is left as-is —
it exercises tkn-act's own pipeline capability and is not the gate.

## Guardrails / interplay with existing gates

- **`tests-required`**: satisfied naturally — several findings live in
  `_test.go` files, so the PR's diff includes test changes alongside the
  production fixes. No `[skip-test-check]` token needed.
- **`coverage` (no-drop)**: the changes are behavior-preserving, so no
  per-package coverage drop is expected; the 0.5pp tolerance absorbs any
  measurement noise.
- **Verification before push**: `go test -race -count=1 ./...` green;
  `go vet ./... && go vet -tags integration ./... && go vet -tags cluster ./...`;
  and `golangci-lint run ./...` reports **0** issues.

## Doc-rule updates

- `docs/test-coverage.md`: document the new blocking `lint` job and the v2
  migration; note it runs on the default test set with no docker dependency.
- `AGENTS.md` (Local development): point the lint reference at v2 and the
  `golangci-lint run ./...` gate; bump the version note where v1.64.8 is
  mentioned.
- This spec + the implementation plan under `docs/superpowers/plans/`.

## Risks

- **v2 surfaces different findings than v1.** Mitigated: the baseline above
  was measured with the exact pinned v2.12.2 + migrated config, so the 58
  count is the real target, not an estimate.
- **An "ignorable" errcheck masks a real bug.** Mitigated: each `_ =` is a
  per-site judgement (mostly test teardown); production sites get real
  handling, reviewed in the `errcheck` commit.
- **Behavior drift from a "mechanical" fix** (e.g. a builtin-shadow rename
  touching the wrong scope). Mitigated: the full `-race` suite is the
  regression contract and must stay green with no test edits beyond the
  findings themselves.

## What this spec deliberately does not decide

- The **tuning of revive rules** (whether to disable noisy rules vs. fix every
  site). Default: fix every current site; revisit rule selection only if v2
  enables a rule that produces low-value noise at scale.
- Whether to **pin golangci-lint by SHA** rather than tag — out of scope; the
  repo pins `actions/*` by floating major tag and this follows that
  convention with an explicit `v2.12.2`.
- The **second and third refactor PRs** (characterization tests, structural
  simplification) — separately brainstormed when this lands.
