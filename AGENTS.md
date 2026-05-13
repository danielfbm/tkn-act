# AGENTS.md — contributor guide for `tkn-act`

This file is the canonical guide for **AI agents and humans contributing to
`tkn-act`** — the project's merge policy, test/coverage gates, doc-sync rule,
and local development bootstrap.

> Looking for the **user guide** (JSON contracts, exit codes, env vars,
> common workflows, feature semantics)?
> See [`docs/agent-guide/`](docs/agent-guide/README.md) — that content is
> also embedded in the binary and printed by `tkn-act agent-guide`.

`CLAUDE.md` symlinks to this file so Claude Code picks it up as project
instructions when working *on* the repo.

---

## Project default: merge with squash + delete-branch

**When merging any PR in this repo, use squash merge and delete the source
branch.** Concretely:

```sh
gh pr merge <num> --squash --delete-branch
```

This is the project-wide default. Reasons:

- `main` history reads as one commit per landed PR — the PR title becomes
  the squash subject and the body becomes the squash body, which keeps the
  log skimmable and bisectable.
- Stale feature branches don't accumulate locally or on the remote.
- Force-push or merge-commit alternatives are not used; if a PR needs to
  preserve internal commit history (rare), call it out explicitly in the
  PR description and discuss before merging.

AI agents working on this repo should treat squash + delete-branch as the
default merge style without prompting; only deviate when the user
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
`.github/workflows/cluster-integration.yml`; bump both together so local
runs continue to match CI.

---

## Documentation rule: keep related docs in sync with every change

**Every change that touches user-visible behavior, supported features,
exit codes, JSON shapes, fixtures, or Tekton coverage must update the
related docs in the same PR.** "Related docs" includes, at minimum:

| If you change... | Also update |
|---|---|
| A user-facing guide section in `docs/agent-guide/<name>.md` | Re-run `go generate ./cmd/tkn-act/` (or `make agentguide`) so `cmd/tkn-act/agentguide_data/` mirrors it. |
| The list of user-guide sections (new section, rename, removal) | Update the curated order in `cmd/tkn-act/internal/agentguide-gen/order.go` AND `cmd/tkn-act/agentguide.go` AND re-run the generator. |
| Tekton field/feature support (types, engine, validator, cluster) | `docs/feature-parity.md` row + the matching `docs/agent-guide/<name>.md` file (then re-run `go generate`). |
| User-facing CLI behavior (flags, output, exit codes) | `docs/agent-guide/README.md` (run `go generate` after) and `README.md`. |
| `testdata/e2e/<name>/` or limitations fixtures | `docs/test-coverage.md` and `docs/feature-parity.md`. |
| Track 1/2/3 plan items in `docs/short-term-goals.md` | flip the row's Status when the work lands. |
| New plan under `docs/superpowers/plans/` | reference it from the matching `feature-parity` / `short-term-goals` row. |

CI's `parity-check` job (`.github/scripts/parity-check.sh`) is the
machine-checked enforcement of the parity ↔ fixtures invariant; an
`agentguide-freshness` test makes the generator-drift case CI-visible
too. The broader rule above is enforced by reviewers and by AI agents
working on this repo. **AI agents must not open a PR that lands a
feature, fixture, or behavior change without updating every doc the
change touches.** When in doubt, grep for the symbol/feature in the docs
tree and update every hit.
