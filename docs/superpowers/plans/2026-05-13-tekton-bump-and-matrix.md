# Tekton LTS bump and cluster-CI version matrix — Implementation Plan

> **Spec:** [`docs/superpowers/specs/2026-05-13-tekton-bump-and-matrix-design.md`](../specs/2026-05-13-tekton-bump-and-matrix-design.md)

**Goal:** Bump the default Tekton install from v0.65.0 (EOL 2025-10-28)
to v1.12.0 (newest LTS, EOL 2027-05-04), make it user-overridable via a
`--tekton-version` flag + `TKN_ACT_TEKTON_VERSION` env var, and add a
cluster-CI matrix that runs every fixture against both the oldest still-
supported LTS (v1.3.0) and the newest (v1.12.0) so a future bump can't
silently regress.

**Architecture:** Single source-of-truth constant in
`internal/cluster/tekton`. Two default sites collapse to it. Installer
URL pattern swaps from the GCS bucket (v0.x only) to GitHub Releases
(v0.x and v1.x). New CLI flag on `tkn-act cluster up`. Cluster-e2e
harness reads the env var. `.github/workflows/cluster-integration.yml`
becomes a matrix.

**Tech stack:** Go 1.25, no new dependencies. Reuses `cobra`, the
existing `cmdrunner` mock, the cross-backend `internal/e2e/fixtures`
table, and the four CI gates (`tests-required`, `coverage`,
`parity-check`, `agentguide-freshness`).

---

## Pre-flight (zero-cost sanity)

- [ ] `git status` is clean.
- [ ] `go vet ./... && go vet -tags integration ./... && go vet -tags cluster ./...` passes on `main` before any edits.
- [ ] `go test -race -count=1 ./...` passes on `main`.

---

## Phase 1 — installer URL pattern + version constant

**Files:** `internal/cluster/tekton/install.go`,
`internal/cluster/tekton/install_test.go`,
`internal/backend/cluster/cluster.go`.

- [ ] Add an exported constant `DefaultTektonVersion = "v1.12.0"` at the
      top of `internal/cluster/tekton/install.go` (with a comment naming
      the LTS support window and pointing at this plan).
- [ ] Replace the literal `"v0.65.0"` in `tekton.New` (line 33) with
      `DefaultTektonVersion`.
- [ ] Replace the literal `"v0.65.0"` in
      `internal/backend/cluster/cluster.go:58` with
      `tekton.DefaultTektonVersion`. Add the import if needed.
- [ ] Swap the URL format string in `Installer.Install`:
      ```go
      url := fmt.Sprintf("https://github.com/tektoncd/pipeline/releases/download/%s/release.yaml", i.opt.Version)
      ```
- [ ] Update every URL string in `install_test.go` to the GitHub
      Releases pattern. Update every test that uses `"v0.65.0"` to use
      the new default (or to `tekton.DefaultTektonVersion` for clarity).
- [ ] Add a new test `TestNewDefaultVersion` asserting `tekton.New(Options{})`
      installs `DefaultTektonVersion` when `Version` is left empty.
- [ ] Run `go test ./internal/cluster/tekton/... ./internal/backend/cluster/...`. Expect green.

**Risk in this phase:** A second test file or sibling caller may also
literal-`v0.65.0`. Grep is the safety net:

- [ ] `grep -rn 'v0\.65\.0' --include='*.go'` reports zero hits outside
      of intentional comments. (Comments that *historically* name v0.65
      can stay if they're about a pre-bump behavior; net new uses are
      forbidden.)

---

## Phase 2 — CLI flag + env var on `cluster up`

**File:** `cmd/tkn-act/cluster.go`.

- [ ] In `newClusterUpCmd`, add a string flag:
      ```go
      var tektonVersion string
      cmd.Flags().StringVar(&tektonVersion, "tekton-version", "",
          "Tekton Pipelines version to install (default: built-in DefaultTektonVersion; env: TKN_ACT_TEKTON_VERSION)")
      ```
- [ ] Resolution order inside `RunE` (highest first):
      1. `--tekton-version` flag if set.
      2. `os.Getenv("TKN_ACT_TEKTON_VERSION")` if set.
      3. Empty — `tekton.New` picks `DefaultTektonVersion`.
- [ ] Thread the resolved version into the driver-build path (the
      cluster `Backend` constructor in
      `internal/backend/cluster/cluster.go` accepts `TektonVersion` via
      `Options`). `cluster up` currently only builds the k3d driver and
      calls `Ensure`; the Tekton install happens later when a
      `--cluster` run triggers `Backend.Prepare`. Implementation choice:
      run `tekton.Install` directly here so `cluster up` actually
      installs Tekton at the requested version, mirroring the comment
      `Short: "Create the local cluster and install Tekton (idempotent)"`.

  **Detail:** currently `newClusterUpCmd` calls only `drv.Ensure(...)`,
  which boots k3d but does NOT install Tekton — that happens lazily on
  the first `--cluster` run. The short-description lies today. The
  install moves here so the version flag has somewhere to act and the
  cluster is ready-to-use after `cluster up` rather than at first run.

- [ ] Add a unit test exercising the flag (`--tekton-version v1.3.0`)
      that asserts the resolved version reaches the installer's
      `Options.Version`. Use a fake `cmdrunner.Runner` and a fake
      `Apiext` returning NotFound for the CRD, so `Install` actually
      calls the runner.

---

## Phase 3 — cluster-e2e harness reads env var

**File:** `internal/clustere2e/cluster_e2e_test.go`.

- [ ] At the top of `TestClusterE2E`, read
      `os.Getenv("TKN_ACT_TEKTON_VERSION")`; pass it as
      `TektonVersion` in the `clusterbe.Options` literal currently at
      line 71 (currently absent; default kicks in).
- [ ] When the env var is empty, no behavior change — the default still
      wins.
- [ ] No new test cases. The harness already drives the full fixture
      table; the env-var read just changes which Tekton version they
      hit.

---

## Phase 4 — CI workflow matrix

**File:** `.github/workflows/cluster-integration.yml`.

- [ ] Add `strategy.matrix.tekton-version: [v1.3.0, v1.12.0]` to the
      `e2e` job.
- [ ] Add `fail-fast: false` so each matrix leg reports independently.
- [ ] Inject `TKN_ACT_TEKTON_VERSION: ${{ matrix.tekton-version }}` into
      the `env:` block.
- [ ] Update the job name to include the matrix value:
      `name: k3d e2e (-tags cluster) — Tekton ${{ matrix.tekton-version }}`.
- [ ] No change to k3d / kubectl versions — those pins (`v5.7.4`,
      `v1.31.0`) are unrelated to the Tekton bump.
- [ ] Dry-run the workflow file with `actionlint` (if installed) or
      manual inspection.

---

## Phase 5 — fixture sweep + limitation re-validation

**Files:** `internal/e2e/fixtures/fixtures.go`,
`internal/backend/cluster/run.go` (comments),
`internal/backend/cluster/runpipeline_test.go` (comments).

- [ ] Re-read every `v0.65` mention in `fixtures.go` (lines 260, 274,
      328, 329). For each, decide:
      - If the gap persists in v1.12.0 → reword the comment to name
        v1.12 instead.
      - If v1.12 resolves it → drop the `DockerOnly` flag and the
        comment; delete the matching `testdata/limitations/<name>/`
        directory; flip the `docs/feature-parity.md` row to `shipped`
        on both backends. Trigger: matrix CI on v1.12 leg passes
        without `DockerOnly`.
- [ ] Run the cluster-e2e suite locally against v1.12.0 with
      `TKN_ACT_TEKTON_VERSION=v1.12.0 go test -tags cluster ./internal/clustere2e/...`
      and observe which fixtures the matrix-result `[*]` gap and
      `matrix-include` overlap rows actually exercise. If any of those
      fixtures pass on v1.12.0 but were previously marked DockerOnly,
      flip them per the bullet above.
- [ ] Update the v0.65 references in
      `internal/backend/cluster/run.go:210` and
      `internal/backend/cluster/runpipeline_test.go:889` to v1.12.0
      where the comment still describes current behavior.

**Decision rule on borderline cases:** if a v0.65 comment describes a
*historical* reason (e.g. "we initially chose X because v0.65 …"),
keep it as historical context with a `(historical: ` prefix. If the
comment describes *current* code behavior, update to name v1.12.0.

---

## Phase 6 — agent-guide content updates

**Files:** `docs/agent-guide/display-name.md`, then re-generate.

- [ ] Update `docs/agent-guide/display-name.md:25` to reflect v1.12.0's
      schema: `Step.displayName` is supported, `Step.description` is
      not. Adjust the "tkn-act surfaces displayName from authored YAML"
      framing accordingly — the framing stays the same, only the
      version reference changes.
- [ ] `go generate ./cmd/tkn-act/` (regenerates
      `cmd/tkn-act/agentguide_data/`).
- [ ] `git diff cmd/tkn-act/agentguide_data/` shows the mirror caught
      the change.
- [ ] Run the `agentguide-freshness` test:
      `go test ./cmd/tkn-act/ -run TestAgentguideFreshness`. Expect green.

---

## Phase 7 — public-contract & parity table

**Files:** `AGENTS.md`, `docs/feature-parity.md`,
`docs/short-term-goals.md`, `docs/agent-guide/README.md`.

- [ ] In `AGENTS.md` under "Public-contract stability" → "CLI flag
      names (long form)" row, add `--tekton-version` to the list.
- [ ] In `docs/feature-parity.md`, update the agent-guide / cluster
      install row(s) referencing v0.65 to read v1.12.0 (or whichever
      LTS resolves the row's gap, per Phase 5).
- [ ] In `docs/short-term-goals.md`, flip the Track 2 #2 row's Status
      to `Done in PR #<this-pr>`. (Track 2 #2 is the
      terminal-condition mapping; the cluster code maps
      `PipelineRunTimeout` / `TaskRunTimeout` to `timeout` since PR
      #9, with the `anySkippedDueToTimeout` fallback added in PR #13.)
- [ ] If `docs/agent-guide/README.md` mentions cluster install
      mechanics (it currently does under "Cluster mode"), confirm the
      install URL example doesn't quote the GCS bucket. If it does,
      update the example to either omit the URL or quote the new GH
      pattern.
- [ ] Re-run `go generate ./cmd/tkn-act/` if `docs/agent-guide/README.md`
      changed.

---

## Phase 8 — local gates + PR

- [ ] `go vet ./... && go vet -tags integration ./... && go vet -tags cluster ./...` passes.
- [ ] `go test -race -count=1 ./...` passes.
- [ ] `make check-agentguide` passes (re-generated tree matches sources).
- [ ] `.github/scripts/parity-check.sh` passes (no orphans between
      `docs/feature-parity.md` rows and `testdata/{e2e,limitations}/`).
- [ ] `.github/scripts/tests-required.sh main HEAD` passes (every Go
      change has an accompanying test change).
- [ ] Commit messages all use Conventional-Commits prefixes (likely
      `feat:` for the matrix + flag, `chore:` for the version bump, or
      one `feat:` squash subject).
- [ ] `gh pr create` with a Summary + Test plan body per the AGENTS.md
      template.
- [ ] Wait for cluster-integration CI to come back green on both
      matrix legs. The cluster job runs ~10 minutes per leg, so plan
      for ~20 minutes wall-clock.
- [ ] On green: dispatch `code-review-subagent` (per the team's review
      policy in `[[feedback-code-review-then-merge]]`).
- [ ] On clean review: `gh pr merge <num> --squash --delete-branch`.

---

## Doc convergence

The implementation lands these doc updates in the same PR:

- [ ] `docs/superpowers/specs/2026-05-13-tekton-bump-and-matrix-design.md` (this spec)
- [ ] `docs/superpowers/plans/2026-05-13-tekton-bump-and-matrix.md` (this plan)
- [ ] `docs/agent-guide/display-name.md`
- [ ] `cmd/tkn-act/agentguide_data/display-name.md` (generated)
- [ ] `docs/feature-parity.md`
- [ ] `docs/short-term-goals.md`
- [ ] `AGENTS.md`
- [ ] `.github/workflows/cluster-integration.yml`
- [ ] `internal/cluster/tekton/install.go` (comment + constant)
- [ ] `internal/backend/cluster/cluster.go` (use constant)
- [ ] `internal/backend/cluster/run.go` (comment)
- [ ] `internal/backend/cluster/runpipeline_test.go` (comment)
- [ ] `internal/e2e/fixtures/fixtures.go` (comments)
- [ ] `internal/clustere2e/cluster_e2e_test.go` (env-var read)
- [ ] `cmd/tkn-act/cluster.go` (flag + install hook)

## See also

- [Spec for this plan](../specs/2026-05-13-tekton-bump-and-matrix-design.md)
- [`docs/feature-parity.md`](../../feature-parity.md)
- [`docs/short-term-goals.md`](../../short-term-goals.md)
- [Tekton release & support window table](https://github.com/tektoncd/pipeline/blob/main/releases.md)
- [Public-contract stability rule](../../../AGENTS.md#public-contract-stability)
