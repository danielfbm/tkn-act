# Tekton LTS bump and cluster-CI version matrix

**Date:** 2026-05-13
**Status:** Draft — implementation in same PR

## Problem

`tkn-act` pins Tekton Pipelines to **v0.65.0**, which went end-of-life on
2025-10-28 (Tekton's LTS support window is 12 months from release). The
pin lives in two places in the Go source — `internal/cluster/tekton/install.go`
and `internal/backend/cluster/cluster.go` — and is not user-configurable.
Three problems follow:

1. **Security / support gap.** A consumer running `tkn-act cluster up` today
   installs an unsupported Tekton release. Any CVE filed against the v0.65
   line after EOL is unpatched. Users who care about staying on-LTS have no
   path that doesn't involve forking `tkn-act`.
2. **Feature-parity bit-rot.** `docs/feature-parity.md` carries two `gap`
   rows attributed to "Tekton v0.65 limitation" — matrix-fanned task
   results not aggregating as Pipeline-level results via
   `$(tasks.X.results.Y[*])` even with `enable-api-fields=alpha`, and
   `matrix.include` overlap semantics. The natural resolution path for
   either is "bump and re-test," and we never paid that cost.
3. **No cross-version safety net.** When we eventually bump, we have no
   evidence the cluster backend works on anything other than the version
   we're pinned to. The `parity-check` invariant ("`tkn-act` ships every
   Tekton feature on both backends") was only ever proven against one
   Tekton release. A future bump after this one could silently regress
   on the prior LTS.

The `docs/short-term-goals.md` Track 2 #2 row ("Map Tekton terminal
conditions to our task statuses") is also flagged open, but the work
already shipped in PRs #9 / #10 / #13 — `mapPipelineRunStatus` /
`mapTaskRunStatus` / the `anySkippedDueToTimeout` fallback are in
place. That row is doc-stale and can be flipped to Done in this same PR.

## Goals

- Default Tekton install version is the **newest supported LTS**
  (v1.12.0, EOL 2027-05-04). Single-binary upgrade for everyone.
- Cluster-integration CI runs every fixture against **two LTS versions**
  in a matrix: oldest supported (**v1.3.0**, EOL 2026-08-04) and newest
  (**v1.12.0**). The matrix protects against the next bump silently
  regressing on the prior LTS.
- A `--tekton-version <vX.Y.Z>` flag on `tkn-act cluster up` plus a
  `TKN_ACT_TEKTON_VERSION` env var so the CI matrix and on-prem users
  can override without rebuilding the binary.
- Installer downloads via `github.com/tektoncd/pipeline/releases/download/<v>/release.yaml`
  (works for v0.x and v1.x) instead of the GCS bucket
  (`storage.googleapis.com/tekton-releases/pipeline/previous/...`)
  which the v1.x releases don't populate.
- `docs/feature-parity.md` rows attributed to "v0.65 limitation" are
  re-validated: if the newer LTS resolves them, flip to `shipped` and
  delete the matching `testdata/limitations/` directory; if not, update
  the comment to name the newest LTS where the gap persists.
- `docs/short-term-goals.md` Track 2 #2 is flipped to Done.

## Non-goals

- **Dropping v0.x support.** The installer URL still resolves v0.65.0 (and
  earlier) via GitHub Releases, so anyone pinning their own version via
  the new flag keeps working. We just don't test those versions in CI.
- **Tekton bump cadence policy.** This spec doesn't pre-decide when we
  next bump or whether we bump on each new LTS. The matrix just makes
  "is this safe to bump?" mechanically checkable.
- **Adding the matrix to the `docker-integration` workflow.** The docker
  backend doesn't run a Tekton controller, so Tekton version is
  irrelevant there.
- **Adding `--tekton-version` to `tkn-act run --cluster`.** The Tekton
  install happens at `cluster up` time and persists for the cluster's
  lifetime; threading it through `run` would re-install on every
  invocation. Out of scope.
- **Removing the v1 `Step.displayName` / `Step.description` strip
  workaround in `cluster/run.go`.** v1.12.0 accepts `displayName` but not
  `description`; stripping both is defensive and harmless to retain.

## Architecture

### Default version bump

`internal/cluster/tekton/install.go:33` and
`internal/backend/cluster/cluster.go:58` both default `Version` /
`TektonVersion` to `"v0.65.0"`. Both bump to `"v1.12.0"`. The two
defaults must stay in sync; we add a single source-of-truth constant
`DefaultTektonVersion` in `internal/cluster/tekton` and both call sites
reference it.

### Installer URL

`internal/cluster/tekton/install.go:57` builds:

```go
url := fmt.Sprintf("https://storage.googleapis.com/tekton-releases/pipeline/previous/%s/release.yaml", i.opt.Version)
```

The GCS bucket carries v0.x releases but **not v1.x** (verified with
`curl` against `v1.3.0` and `v1.12.0` — both return 404). GitHub
Releases carries both. The URL pattern becomes:

```go
url := fmt.Sprintf("https://github.com/tektoncd/pipeline/releases/download/%s/release.yaml", i.opt.Version)
```

This is the same URL pattern Tekton's own quickstart docs use.

### CLI flag + env var

`tkn-act cluster up` grows a `--tekton-version` string flag:

```
tkn-act cluster up --tekton-version v1.3.0
```

Precedence (highest first):

1. `--tekton-version` flag.
2. `TKN_ACT_TEKTON_VERSION` env var.
3. `DefaultTektonVersion` constant.

The flag is part of the [public-contract stability surface](../../../AGENTS.md#public-contract-stability)
once added; renaming or retyping it later needs a major version bump.
The env var is a sibling, documented in the same place.

### Cluster-integration CI matrix

`.github/workflows/cluster-integration.yml` becomes a matrix job over
`tekton-version: [v1.3.0, v1.12.0]`, with each leg exporting the version
to the test environment:

```yaml
jobs:
  e2e:
    strategy:
      fail-fast: false
      matrix:
        tekton-version: [v1.3.0, v1.12.0]
    env:
      TKN_ACT_TEKTON_VERSION: ${{ matrix.tekton-version }}
```

`fail-fast: false` so an old-LTS regression doesn't mask a newer-LTS
regression on the same PR.

### e2e harness reads env var

`internal/clustere2e/cluster_e2e_test.go:71` builds the cluster Backend
with no `TektonVersion` set, so it defaults to the in-binary constant.
The harness reads `TKN_ACT_TEKTON_VERSION` (when set) and threads it
through `clusterbe.Options{TektonVersion: ...}` so a CI matrix leg
exercises the requested version end-to-end, not just the default
constant.

### Fixture-level version gating

If a fixture passes on v1.12.0 but fails on v1.3.0 (or vice versa),
that's information we want — we shouldn't silently skip. The first cut
runs every cluster fixture on every matrix leg. If a real divergence
surfaces during implementation, we add a `MinTektonVersion` /
`MaxTektonVersion` field to the fixture descriptor; until then, no new
descriptor field, no per-fixture conditionals.

### Doc convergence

| Doc | Update |
|---|---|
| `docs/feature-parity.md` | Re-validate `matrix-include-overlap` and the matrix-result `[*]` rows against v1.12.0. Update comments to name the current LTS. Flip rows if the newer LTS resolves the gap. |
| `docs/agent-guide/display-name.md` | The "v0.65 has no `displayName` on Step" remark is stale on v1.12 — re-word to note v1.12 has `Step.displayName` but not `Step.description`. Re-run `go generate ./cmd/tkn-act/`. |
| `docs/short-term-goals.md` | Flip Track 2 #2 to Done (already in code). |
| `AGENTS.md` | Add `--tekton-version` flag to the public-contract stability table under "CLI flag names". |
| `internal/e2e/fixtures/fixtures.go` | Re-word the v0.65 limitation comments on `matrix` and `matrix-include` fixtures. |
| `internal/cluster/tekton/install.go` / `internal/backend/cluster/run.go` | Update inline comments that name v0.65. |
| `Makefile` | No change (Makefile doesn't pin Tekton; the binary does). The K3D_VERSION / KUBECTL_VERSION pins are unrelated. |
| `.github/workflows/cluster-integration.yml` | The matrix change itself. |

## Migration steps

1. Add `DefaultTektonVersion = "v1.12.0"` constant in
   `internal/cluster/tekton`. Both default sites reference it.
2. Swap the installer URL to the GitHub Releases pattern.
3. Add `--tekton-version` flag + `TKN_ACT_TEKTON_VERSION` env-var
   reading in `cmd/tkn-act/cluster.go`.
4. Wire the env var into the cluster e2e harness.
5. Update `cluster-integration.yml` to matrix-run.
6. Run both matrix legs locally (one at a time with the env var) to
   smoke the fixture set on each LTS.
7. Sweep documentation per the table above.
8. Re-run `go generate ./cmd/tkn-act/`.
9. Update `internal/cluster/tekton/install_test.go` fixtures to use the
   new URL pattern and `DefaultTektonVersion`.

## Risks

- **GitHub release URL availability.** GitHub's release download
  endpoint has different rate-limit characteristics than the GCS
  bucket. For CI, `actions/checkout` runs in a token-authenticated
  context so rate limits aren't a concern. For on-prem / air-gapped
  users, the URL is a 302 redirect that resolves through GitHub's CDN —
  curl + kubectl both follow it transparently. The change widens the
  network egress requirement from `storage.googleapis.com` to
  `github.com` + the CDN; air-gapped consumers were already running
  their own mirrors, so this surfaces as a one-time mirror-source
  update.
- **v1.3 vs v1.12 fixture divergence.** Some Tekton behavior may
  legitimately differ between the two LTS versions (e.g. matrix
  pipeline-result aggregation graduated from alpha to GA somewhere in
  the v0.x → v1.x window). If a fixture passes on v1.12 but fails on
  v1.3, the matrix surfaces it as a hard failure rather than silent
  drift. Treat each as a per-fixture decision: gate via the new
  fixture-descriptor field, document the divergence, or pin the
  fixture to one side. No pre-decision in this spec.
- **`Step.displayName` strip workaround.** v0.65 strict-decoded Step and
  rejected `displayName` / `description`; v1.12 accepts `displayName`
  but not `description`. The strip is currently unconditional. Keeping
  it on v1.12 silently drops `displayName` from the inlined PipelineRun
  on the cluster path — but docker mode is the source of truth for that
  field anyway (see `cluster/run.go:210` comment), so the cluster-path
  drop is observationally invisible.
- **Doc-staleness regression.** The `docs/agent-guide/display-name.md`
  remark about v0.65 is currently true; rewording it for v1.12 needs
  to also flow through `cmd/tkn-act/agentguide_data/` via
  `go generate`. The `agentguide-freshness` CI gate catches drift, but
  forgetting to re-run the generator before commit is the most likely
  miss.

## What this spec deliberately does not decide

- **When to drop v1.3.0 from the matrix.** v1.3.0 EOLs 2026-08-04. A
  follow-up PR will need to pick the next-oldest-supported LTS and
  re-run. This spec doesn't pre-schedule that bump.
- **Whether to add `--tekton-version` to a hypothetical
  `tkn-act cluster status`.** The version is recorded at install time
  on the cluster CRDs; surfacing it in `cluster status -o json` is a
  follow-up if anyone asks.
- **Re-doing the v0.65-era cluster install tests.** The existing
  `install_test.go` cases exercise the URL-construction + idempotency
  paths; bumping the version string used in the test bodies is
  sufficient. We don't add new mock cases for the v1.3 vs v1.12 split.
- **Whether `tkn-act run --cluster` should refuse to run against a
  cluster installed with a Tekton version older than the binary's
  `DefaultTektonVersion`.** Some tkn-act features may rely on
  Tekton-side behavior introduced after a user's pinned version; the
  binary currently doesn't warn. That's a future hardening — for v1.x
  it stays best-effort.

## See also

- [`docs/superpowers/plans/2026-05-13-tekton-bump-and-matrix.md`](../plans/2026-05-13-tekton-bump-and-matrix.md)
- [`docs/feature-parity.md`](../../feature-parity.md)
- [`docs/short-term-goals.md`](../../short-term-goals.md)
- [Tekton release & support window table](https://github.com/tektoncd/pipeline/blob/main/releases.md)
