# AGENTS.md — contributor guide for `tkn-act`

This file is the canonical guide for **AI agents and humans contributing to
`tkn-act`** — the pre-PR checklist, commit / merge conventions, test and
coverage gates, doc-sync rule, the cross-backend invariant, the
public-contract stability rule, and the spec-and-plan workflow.

> Looking for the **user guide** (JSON contracts, exit codes, env vars,
> common workflows, feature semantics)?
> See [`docs/agent-guide/`](docs/agent-guide/README.md) — that content is
> also embedded in the binary and printed by `tkn-act agent-guide`.

`CLAUDE.md` symlinks to this file so Claude Code picks it up as project
instructions when working *on* the repo.

---

## Pre-PR checklist (AI agents read this first)

Before opening a PR — and before reporting a task as "done" — every AI
agent working on this repo must walk this checklist. Each item is
elaborated in the rule sections below; the checklist is the operating
summary.

- [ ] **Tests added or updated** for every Go production-code change.
      CI's `tests-required` gate refuses PRs that fail this. Override
      token `[skip-test-check]` exists only for genuinely test-immune
      changes.
- [ ] **Per-package coverage held or improved** vs. the base branch
      (≤0.1pp drop tolerated). CI's `coverage` gate enforces this.
- [ ] **`docs/agent-guide/<name>.md` updated** for any user-visible
      behavior change (flags, output, JSON shapes, exit codes, supported
      features). Then `go generate ./cmd/tkn-act/` (or
      `make agentguide`) — the freshness test fails CI if the embedded
      tree is stale.
- [ ] **`docs/feature-parity.md` row flipped** if you implemented,
      changed, or removed a Tekton feature. CI's `parity-check` enforces
      that the parity table and the `testdata/{e2e,limitations}/` trees
      agree.
- [ ] **Cross-backend e2e fixture added** under `testdata/e2e/<name>/`
      with a descriptor entry in `internal/e2e/fixtures.All()` if the
      feature is `shipped`, so both docker and cluster backends exercise
      it. Delete any matching `testdata/limitations/<name>/` in the same
      PR.
- [ ] **Commit subjects use Conventional-Commits prefix**
      (`feat:` / `fix:` / `chore:` / `docs:` / `refactor:` / `test:` /
      `revert:`).
- [ ] **PR body has a Summary + Test plan** (see "Commit and PR
      conventions" below).
- [ ] **For non-trivial work, the spec + plan landed first** (or in this
      same PR) under `docs/superpowers/{specs,plans}/`. See "Design
      before code."
- [ ] **No public-contract break** — JSON event fields are additive,
      existing fields keep their name and type, exit codes are stable.
      See "Public-contract stability."

When in doubt, grep the symbol/feature/flag across `docs/`,
`testdata/`, and `cmd/` and update every hit.

---

## Commit and PR conventions

### Commit subjects — Conventional Commits

Every commit subject uses a Conventional-Commits prefix:

| Prefix | Use for |
|---|---|
| `feat:` | A new user-visible feature or capability. |
| `fix:` | A bug fix (no new behavior). |
| `refactor:` | Restructuring without behavior change. |
| `docs:` | Documentation-only changes (including `docs/agent-guide/`). |
| `test:` | Test-only changes (new tests for existing behavior, harness changes). |
| `chore:` | Build, tooling, dependency bumps, generated boilerplate. |
| `revert:` | Reverts of a previously-landed commit. |

Imperative mood. Under ~70 characters in the subject line. Body wraps
at ~72 columns. Reference the issue / plan / spec in the body when
relevant.

### PR body

Every PR body has at least:

```
## Summary
<1-3 bullets of what changed and why>

## Test plan
- [ ] go test -race -count=1 ./...
- [ ] <feature-specific manual / fixture verification>
- [ ] CI: <which workflows are expected to run>
```

The Summary is for the squash subject context; the Test plan is what
reviewers verify against the diff.

### Merge: squash + delete-branch

**When merging any PR in this repo, use squash merge and delete the
source branch.** Concretely:

```sh
gh pr merge <num> --squash --delete-branch
```

Reasons:

- `main` history reads as one commit per landed PR — the PR title
  becomes the squash subject and the body becomes the squash body,
  which keeps the log skimmable and bisectable.
- Stale feature branches don't accumulate locally or on the remote.
- Force-push or merge-commit alternatives are not used; if a PR
  needs to preserve internal commit history (rare), call it out
  explicitly in the PR description and discuss before merging.

AI agents working on this repo should treat squash + delete-branch as
the default merge style without prompting; only deviate when the user
explicitly asks for a merge or rebase merge.

---

## Contribution rule: tests required

**Every PR that changes Go production code must include a test change.**
Concretely: if a PR's diff modifies any `*.go` file outside `_test.go` and
outside `vendor/`, it must also modify or add at least one `*_test.go` file.

This rule is enforced in CI by `.github/scripts/tests-required.sh` (run as
the `tests-required` job in `.github/workflows/ci.yml`). The script fails
the PR if the diff has Go code changes without an accompanying test change.

For genuinely test-immune changes (dependency bumps, doc typos in Go
comments, regenerated boilerplate, generated stubs), include the literal
token `[skip-test-check]` in any commit message in the PR. The script
greps `git log --format=%B base..head` for it.

The rationale is the usual one: every behavior we ship is one we must be
able to detect breaks in later. AI agents working on this repo should treat
this as a hard precondition for opening a PR.

### Coverage gate (sibling rule)

**Coverage must not drop below the target branch on a per-package basis.**
The `coverage` job in `.github/workflows/ci.yml` runs
`.github/scripts/coverage-check.sh` on every PR. It runs
`go test -cover -count=1 ./...` (default test set, no build tags) on both
the PR's base SHA and head SHA, then compares per-package: if any package
on HEAD has lower coverage than on BASE by more than 0.1 percentage points,
the gate fails and prints a per-package table showing the drop.

| Edge case | Behavior |
|---|---|
| Package new on HEAD (no BASE baseline) | not a drop; reported as `new` |
| Package removed on HEAD | not a drop; reported as `removed` |
| Package with `[no statements]` | treated as 100% on both sides |
| Package with `[no test files]` | skipped (no measurement) |
| Tests fail on either side | gate aborts with a clear message — does not silently pass as 0% |

For genuinely coverage-immune changes (a deletion that drops a covered
code path along with its tests, a refactor that intentionally drops dead
code), include the literal token `[skip-coverage-check]` in any commit
message in the PR. The script greps `git log --format=%B base..head` for
it, the same way `tests-required` looks for `[skip-test-check]`.

The gate runs only on `pull_request` (not on push to `main` — there's no
base to compare against). It only measures the default test set, not
`-tags integration` or `-tags cluster`; those run in their own workflows.

---

## Cross-backend invariant

**`tkn-act` ships every Tekton feature on both backends, or it ships
the feature limited and documents the gap.** "Both backends" means
`--docker` (default; per-Step containers) and `--cluster` (ephemeral
k3d cluster with a real Tekton controller). The fixture set under
`internal/e2e/fixtures.All()` runs every shipped feature through both
harnesses so this invariant is mechanically verifiable.

Concretely, for any feature whose status is `shipped` in
[`docs/feature-parity.md`](docs/feature-parity.md):

- An `testdata/e2e/<name>/pipeline.yaml` fixture exists and is
  registered in `internal/e2e/fixtures.All()`.
- The docker-integration and cluster-integration CI workflows both
  exercise it.
- The corresponding `docs/feature-parity.md` row's `Backends` column
  reads `both` (or, rarely, names the one backend with a documented
  reason).
- No `testdata/limitations/<name>/` directory remains. CI's
  `parity-check` enforces that limitations and parity rows agree.

When a feature genuinely cannot be supported identically on both
backends (e.g. Tekton webhook semantics that the docker pause-container
model can't reproduce), it is `gap` or `out-of-scope` on the relevant
backend, with a `testdata/limitations/<name>/` fixture documenting the
exact divergence.

The full step-by-step playbook for picking up a `gap` feature lives in
[`docs/feature-parity.md`](docs/feature-parity.md#per-feature-workflow-every-contributor-reads-this)
under "Per-feature workflow." Read it before starting a Tekton-feature
PR.

---

## Public-contract stability

The following surfaces are part of `tkn-act`'s public contract.
**Additions are allowed; renames and type changes need a major version
bump.**

| Surface | Stable means |
|---|---|
| Exit codes (`0`, `1`, `2`, `3`, `4`, `5`, `6`, `130`) | Codes keep their meaning. New codes may be added; existing meanings are not redefined. |
| `tkn-act run -o json` event kinds | `run-start`, `run-end`, `task-start`, `task-end`, `task-skip`, `task-retry`, `step-start`, `step-end`, `step-log`, `error`, `resolver-start`, `resolver-end`, `sidecar-start`, `sidecar-end`, `sidecar-log`. New kinds may be added; existing kinds are not renamed. |
| Event field names | Existing camelCase fields stay camelCase. New multi-word fields use `snake_case` (see [`docs/agent-guide/display-name.md`](docs/agent-guide/display-name.md) for the rule). Fields are never renamed or retyped. |
| `tkn-act help-json` shape | Top-level keys, command-tree shape, flag-info shape. |
| `tkn-act doctor -o json` shape | `ok`, `checks[]` with `name`/`ok`/`detail`/`required_for`. |
| CLI flag names (long form) | `--output`, `--cluster`, `--param`, `-w` / `--workspace`, `--tekton-version` (on `cluster up`), `--remote-docker`, `--docker-host`, `--pause-image`, `--sidecar-start-grace`, `--sidecar-stop-grace`, `--debug`, `--timestamps`, `--task`, `--step`, etc. Short forms (`-o`, `-p`, `-q`, `-v`, `-f`) are stable. |
| Environment variables | `TKN_ACT_TEKTON_VERSION` (overrides the built-in Tekton install version for `cluster up`), `TKN_ACT_REMOTE_DOCKER` (`auto` / `on` / `off` for the docker backend's remote-daemon detection), `TKN_ACT_SSH_INSECURE` (`1` bypasses `~/.ssh/known_hosts` for the `ssh://` docker transport), `TKN_ACT_DOCKER_SOCKET` (remote daemon socket path; default `/var/run/docker.sock`), `TKN_ACT_PAUSE_IMAGE` (per-Task pause / remote-mode stager image; default `registry.k8s.io/pause:3.9`). Existing env vars keep their names. |
| Subcommand names | `run`, `validate`, `list`, `doctor`, `help-json`, `agent-guide`, `cluster`, `cache`, `version`. |

If a change must break one of these, call it out in the PR description
under a "Breaking change" heading and bump the binary's major version
in the same PR. The reflexive answer to "do I need to rename this
field?" is "no — add a new one and deprecate the old one in the
release notes."

---

## Design before code

For non-trivial work — anything beyond a one-file bugfix or a doc
typo — write the design before the implementation:

1. **Spec** at `docs/superpowers/specs/<YYYY-MM-DD>-<topic>-design.md`.
   States the problem, goals/non-goals, architecture, doc-rule updates,
   migration steps, risks, and what the spec deliberately doesn't
   decide.
2. **Plan** at `docs/superpowers/plans/<YYYY-MM-DD>-<topic>.md`. Lists
   the implementation phases as checkbox tasks (`- [ ]`) that can be
   ticked off as work lands. Every plan ends with a "Doc convergence"
   step that lists every doc the implementation must update — that
   step is what materializes the doc-sync rule for the specific
   feature.
3. The implementation PR references both. Specs and plans can land in
   the same PR as the first implementation phase (the existing
   `2026-05-13-agent-guide-folder-design.md` did this), or as a
   separate review-only PR before the work begins (the
   `2026-05-04-resolvers.md` plan did this).

Examples to read for shape:

- [`docs/superpowers/specs/2026-05-04-resolvers-design.md`](docs/superpowers/specs/2026-05-04-resolvers-design.md) +
  [`docs/superpowers/plans/2026-05-04-resolvers.md`](docs/superpowers/plans/2026-05-04-resolvers.md)
  — six-phase feature roll-out, biggest example in the tree.
- [`docs/superpowers/plans/2026-05-04-step-actions.md`](docs/superpowers/plans/2026-05-04-step-actions.md)
  — single-PR feature with full design embedded in the plan.
- [`docs/superpowers/specs/2026-05-13-agent-guide-folder-design.md`](docs/superpowers/specs/2026-05-13-agent-guide-folder-design.md)
  — refactor spec that supersedes a previous draft.

AI agents must not skip this step on a non-trivial change. The pattern
exists because every prior feature that didn't have one shipped with
load-bearing decisions buried in commit messages, and reviewers had to
reconstruct them by reading the diff.

---

## Local development

The repo-root `Makefile` is the supported one-command bootstrap for new
contributors:

```sh
make quickstart   # doctor -> build -> cluster-up -> hello-cluster
```

`make help` lists every target. The Makefile is a convenience layer over
the same commands CI runs (`go test -race ./...`, `go vet` across all
build tags, `tkn-act cluster up`, `tkn-act run --cluster`); it does not
duplicate behavior CI already covers, and there is no CI gate that runs
`make` itself. See `docs/test-coverage.md` for what is and isn't gated.

K3d / kubectl version pins in the Makefile (`K3D_VERSION`,
`KUBECTL_VERSION`) mirror those in
`.github/workflows/cluster-integration.yml`; bump both places together so
local runs continue to match CI.

Before opening a PR, run the same gates CI will run:

```sh
go vet ./... && go vet -tags integration ./... && go vet -tags cluster ./...
go test -race -count=1 ./...
make check-agentguide
.github/scripts/parity-check.sh
.github/scripts/tests-required.sh main HEAD
```

---

## Documentation rule: keep related docs in sync with every change

**Every change that touches user-visible behavior, supported features,
exit codes, JSON shapes, fixtures, or Tekton coverage must update the
related docs in the same PR.** "Related docs" includes, at minimum:

| If you change... | Also update |
|---|---|
| A user-facing guide section in `docs/agent-guide/<name>.md` | Re-run `go generate ./cmd/tkn-act/` (or `make agentguide`) so `cmd/tkn-act/agentguide_data/` mirrors it. |
| The list of user-guide sections (new section, rename, removal) | Update the curated order in `cmd/tkn-act/internal/agentguide/order.go` AND re-run the generator. |
| Tekton field/feature support (types, engine, validator, cluster) | `docs/feature-parity.md` row + the matching `docs/agent-guide/<name>.md` file (then re-run `go generate`). |
| User-facing CLI behavior (flags, output, exit codes) | `docs/agent-guide/README.md` (run `go generate` after) and `README.md`. |
| `testdata/e2e/<name>/` or limitations fixtures | `docs/test-coverage.md` and `docs/feature-parity.md`. |
| Track 1/2/3 plan items in `docs/short-term-goals.md` | flip the row's Status when the work lands. |
| New plan under `docs/superpowers/plans/` | reference it from the matching `feature-parity` / `short-term-goals` row. |
| Public-contract surface (event kinds, exit codes, flag names) | Add a one-line note under "Public-contract stability" above naming the new addition. |

CI's `parity-check` job (`.github/scripts/parity-check.sh`) is the
machine-checked enforcement of the parity ↔ fixtures invariant; an
`agentguide-freshness` test makes the generator-drift case CI-visible
too. The broader rule above is enforced by reviewers and by AI agents
working on this repo. **AI agents must not open a PR that lands a
feature, fixture, or behavior change without updating every doc the
change touches.** When in doubt, grep for the symbol/feature in the docs
tree and update every hit.
