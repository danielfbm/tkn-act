# Agent-guide folder split

**Date:** 2026-05-13
**Status:** Draft — implementation in same PR
**Supersedes:** [`2026-05-08-agents-md-modularization-design.md`](2026-05-08-agents-md-modularization-design.md)

## Problem

`AGENTS.md` (~881 lines, `CLAUDE.md` is a symlink to it) interleaves two
audiences whose needs barely overlap:

- **A — agents and scripts *using* tkn-act.** They need JSON contracts,
  exit codes, env vars, common workflows, failure modes, and the
  feature reference (stepTemplate, sidecars, StepActions, matrix,
  pipeline results, displayName, timeouts, resolvers).
- **B — agents and humans *developing* tkn-act.** They need
  the squash-merge policy, tests-required rule, coverage gate, local
  development / Makefile, and the doc-sync rule.

Everything is embedded into the binary via `go:embed` and printed by
`tkn-act agent-guide`, so a consumer who runs the command for the JSON
contract is also told "use `gh pr merge --squash`" and "every PR must
include a test." That's audience-B noise inside an audience-A channel.

The 2026-05-08 modularization spec (draft, not yet implemented) tackled
a related but smaller problem — splitting feature-reference sections
into `docs/reference/<name>.md` while keeping AGENTS.md as a skeleton
with include directives. It does not address the audience split.

## Goals

- A single source-of-truth folder for audience-A content, easy to embed
  as a tree and easy to extract / mirror.
- `AGENTS.md` (and therefore `CLAUDE.md`) becomes the contributor /
  dev guide, matching the conventional reading of the filename.
- `tkn-act agent-guide` keeps its current default behavior — prints
  the complete user guide — and grows additive `--list` /
  `--section <name>` flags.
- One Go generator tool (`cmd/tkn-act/internal/agentguide-gen/`),
  ~80 LOC, pure stdlib, with unit tests.
- One PR for spec + implementation.

## Non-goals

- Changing the *content* of any section. This is a structural refactor;
  text moves verbatim.
- Adding new features, fixtures, or Tekton coverage.
- Rewriting the `tkn-act agent-guide` JSON shape — there isn't one;
  it prints markdown. No contract there to break.
- Renaming `tkn-act agent-guide` (would break anything scripting on it).

## Architecture

### Source layout

| Path | Audience | Role |
|---|---|---|
| `AGENTS.md` (repo root, `CLAUDE.md` symlinks here) | B (dev) | Contributor guide. Sections: squash-merge default, tests-required rule, coverage gate, local development, doc-sync rule. Plus a one-line cross-link to the user guide. |
| `docs/agent-guide/README.md` | A (user) | Top-of-guide: preamble, "What `tkn-act` is", machine-readable interfaces, exit codes, env vars, pretty output, common workflows, failure modes, conventions, where-to-look-next. |
| `docs/agent-guide/step-template.md` | A | `Task.spec.stepTemplate` semantics |
| `docs/agent-guide/sidecars.md` | A | `Task.sidecars` lifecycle, fields, JSON events |
| `docs/agent-guide/step-actions.md` | A | `StepAction` resolution rules |
| `docs/agent-guide/matrix.md` | A | `PipelineTask.matrix` fan-out semantics |
| `docs/agent-guide/pipeline-results.md` | A | `Pipeline.spec.results` resolution |
| `docs/agent-guide/display-name.md` | A | `displayName` / `description` surfacing + the JSON event field-naming note |
| `docs/agent-guide/timeouts.md` | A | Per-task vs pipeline-level timeout disambiguation |
| `docs/agent-guide/resolvers.md` | A | Track 1 #9 resolvers, Mode B, `--offline`, cache subcommands |

Each user-guide file starts with the same H2 heading currently used in
`AGENTS.md` (e.g. `## Resolvers (Track 1 #9, shipped)`). `README.md` is
multi-section (H2 per current AGENTS.md section in that range) plus an
H1 preamble.

### Hand-curated concatenation order

`tkn-act agent-guide` (default, no flags) emits the full user guide as
a single markdown stream by concatenating these files in this exact
order, with a single blank line between adjacent files:

1. `README.md`
2. `step-template.md`
3. `sidecars.md`
4. `step-actions.md`
5. `matrix.md`
6. `pipeline-results.md`
7. `display-name.md`
8. `timeouts.md`
9. `resolvers.md`

Order is encoded in the generator and in `agentguide.go`'s constant so
the two cannot drift. Alphabetical was rejected: it would split
"step-actions" and "step-template" out of the "Step" semantics group,
and it would put "resolvers" before "timeouts" against the current
AGENTS.md reading order.

### Embed model

`embed` can read a directory tree but cannot escape the source file's
directory. The generator mirrors `docs/agent-guide/` (repo-root
relative) into `cmd/tkn-act/agentguide_data/` (committed). Then:

```go
//go:embed all:agentguide_data
var agentGuideFS embed.FS
```

`agentguide.go` opens entries from `agentGuideFS` in the curated
order to produce the default output, and exposes them individually
for the `--section` flag.

The existing `cmd/tkn-act/agentguide_data.md` file is removed.

### Generator tool

`cmd/tkn-act/internal/agentguide-gen/main.go` (~80 LOC, stdlib only):

- Reads `-src` (default `../../docs/agent-guide`) and `-dst` (default
  `./agentguide_data`).
- Walks `-src` for `*.md` files.
- Validates that every name in the curated order is present and that
  no extra `*.md` files exist (catches "forgot to add to the order"
  and "rename without updating the order").
- Writes each file to `-dst` only if its bytes differ (avoids mtime
  churn).
- Removes any `*.md` file in `-dst` not in the curated order (so a
  rename of a source file is reflected in `-dst`).
- Errors print `agentguide-gen: <message>` and exit 1.

Why a Go tool rather than a shell pipeline:

- Deterministic across mac/linux (no `awk`/`sed` dialect issues).
- Easy unit tests in `cmd/tkn-act/internal/agentguide-gen/main_test.go`.
- Pure stdlib — no new deps.

### CLI surface

`tkn-act agent-guide` — unchanged default: prints the full guide.

New, additive:

| Flag | Behavior |
|---|---|
| `--list` (no value) | Prints one section name per line, in curated order. JSON when `-o json` is set: `{"sections": ["overview", "step-template", ...]}`. |
| `--section <name>` | Prints the single file matching `<name>` (without the `.md`). `overview` is the alias for `README.md`. Unknown name → exit 2 with a clear list of valid names. |

Both flags compose with `-o json`: `--list -o json` for the JSON list,
`--section <name> -o json` is a no-op (markdown still prints to stdout
— there's no JSON shape for a single guide section, and we explicitly
don't invent one).

Help text gets two examples added.

### Generate pipeline

`cmd/tkn-act/generate.go` switches from:

```go
//go:generate sh -c "cp ../../AGENTS.md ./agentguide_data.md"
```

to:

```go
//go:generate go run ./internal/agentguide-gen -src ../../docs/agent-guide -dst ./agentguide_data
```

The `Makefile`'s `agentguide` and `check-agentguide` targets keep their
names; they continue to run `go generate ./cmd/tkn-act/` and a
`git diff --exit-code` against the generated tree.

### Verification

Two layers:

1. **Freshness gate.** New `cmd/tkn-act/agentguide_freshness_test.go`
   re-runs the generator into a tempdir and compares its output
   (a recursive directory diff) against the checked-in
   `cmd/tkn-act/agentguide_data/` tree. A mismatch fails with a
   `run: go generate ./cmd/tkn-act/` hint. Lives in the default test
   set so coverage and tests-required gates both see it.

2. **Content presence.** `cmd/tkn-act/agentguide_test.go` is updated
   to assert that the concatenated default output contains the
   canonical strings it already asserts (`tkn-act doctor`, `Exit
   codes`, `help-json`, `--output json`), plus one characteristic
   string per per-feature file (e.g. `Matrix fan-out`,
   `Resolvers (Track 1 #9`). Catches an empty or wrong-section
   `agentguide_data/` tree that happens to be the right total
   byte count.

3. **Generator unit tests.** `cmd/tkn-act/internal/agentguide-gen/
   main_test.go` covers: happy-path copy, idempotent rerun
   (no-op when source unchanged), missing-from-order error, extra-
   in-order error, stale file removal on rename.

## Doc-rule update

The "keep related docs in sync" table now lives in the dev-focused
`AGENTS.md`. It gets two rows added and one row changed:

> | If you change... | Also update |
> |---|---|
> | A user-facing guide section in `docs/agent-guide/<name>.md` | Re-run `go generate ./cmd/tkn-act/` (or `make agentguide`) so `cmd/tkn-act/agentguide_data/` mirrors it |
> | The list of user-guide sections (new section, rename, removal) | Update the curated order in `cmd/tkn-act/internal/agentguide-gen/order.go` AND `cmd/tkn-act/agentguide.go` AND re-run the generator |
> | Tekton field/feature support — *changed row* | `docs/feature-parity.md` row + the matching `docs/agent-guide/<name>.md` file (then re-run `go generate`) |

The old wording that pointed at `AGENTS.md` and `cmd/tkn-act/
agentguide_data.md` for user-facing changes is removed — those targets
no longer host user-facing content.

## Cross-references to update

- `README.md` Documentation table — add a row for `docs/agent-guide/`;
  re-aim the existing `AGENTS.md` row to "Contributor guide".
- `README.md` body — line 27 ("AGENTS.md is the canonical agent / JSON
  contract") and lines 189–192 (around `tkn-act agent-guide`) point
  at `docs/agent-guide/` instead.
- `cmd/tkn-act/root.go:56, 59` — long help mentions; keep the
  `tkn-act agent-guide` reference, drop the "(AGENTS.md)" parenthetical.
- `cmd/tkn-act/helpjson.go:52` — `AGENTS.md` → `docs/agent-guide/`.
- `.github/workflows/cli-e2e.yml:7, 86` — comments cite `AGENTS.md`
  for the exit-code contract; re-aim at `docs/agent-guide/README.md`.
- `docs/remote-resolvers-guide.md:14, 359` — `AGENTS.md` anchor links
  → `docs/agent-guide/resolvers.md` anchors.
- `docs/short-term-goals.md:73` — `AGENTS.md` mention re-aimed where
  it refers to user-facing content.
- `docs/superpowers/plans/2026-05-04-resolvers.md` (multiple) and
  `docs/superpowers/plans/2026-05-04-step-actions.md` — replace
  "mirror to `agentguide_data.md` via `go generate`" with "edit the
  matching `docs/agent-guide/<name>.md`, then `go generate`".
- `.github/scripts/tests-required.sh:61` and
  `.github/scripts/coverage-check.sh:232` — *no change required*.
  Those reference `AGENTS.md "Contribution rule"` / `"Coverage gate"`;
  those sections stay in `AGENTS.md` (dev-focused), so the pointers
  are already correct.

## Migration steps

One PR. The migration must be atomic — no in-between state where
`AGENTS.md` has a section removed but `docs/agent-guide/` doesn't
exist yet.

1. Create `docs/agent-guide/` with `README.md` + the 8 per-feature
   files, copying audience-A sections verbatim from `AGENTS.md`.
2. Add `cmd/tkn-act/internal/agentguide-gen/main.go` and tests.
3. Update `cmd/tkn-act/generate.go` to invoke the new tool.
4. Run `go generate ./cmd/tkn-act/` to populate
   `cmd/tkn-act/agentguide_data/`.
5. Delete `cmd/tkn-act/agentguide_data.md`.
6. Rewrite `cmd/tkn-act/agentguide.go` for `embed.FS`, the curated
   concatenation, and the `--list` / `--section` flags.
7. Update `cmd/tkn-act/agentguide_test.go` (presence assertions) and
   add `cmd/tkn-act/agentguide_freshness_test.go`.
8. Trim `AGENTS.md` to audience-B only; add cross-link to the user
   guide; update the doc-rule table.
9. Update `Makefile` targets (`agentguide` and `check-agentguide` work
   against the tree, not the single file).
10. Update cross-references (see table above).
11. Annotate the 2026-05-08 spec as superseded.
12. Run the full local test set: `go generate ./cmd/tkn-act/`,
    `go vet ./...`, `go test -race ./...`, `make check-agentguide`.
13. Commit, push, open PR.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| Contributor edits `AGENTS.md` to add a user-facing section and the dev guide gets bloated again | Doc-rule update + reviewer judgment. Not machine-enforced. Mitigated structurally: anyone looking for the user guide finds `docs/agent-guide/` first because `tkn-act agent-guide`, `README.md`, and the `AGENTS.md` preamble all point there. |
| Generated tree drifts from sources | New freshness test fails CI on any drift, same way tests-required catches missing tests. |
| Section rename without updating the curated order | Generator rejects extra files in `-src` not in the order AND missing files in the order. Both classes of mistake fail at `go generate` time, before tests even compile. |
| `--list` / `--section` flags grow ad-hoc and become inconsistent | Spec'd up-front in this doc; no other ergonomics added in the same PR. Future additions go through a follow-up spec. |
| Anyone scripting on `tkn-act agent-guide` output sees a byte diff | Default output is a concatenation of the same text content currently in AGENTS.md (audience-A subset), so consumers extracting JSON contracts / exit codes / feature semantics see no semantic change. The contributor-only sections that disappear from the default output weren't relevant to consumers. |
| Inline byte differences (trailing newlines, separator style) | Generator normalizes: exactly one trailing newline per file in `-dst`; concatenation inserts exactly one blank line between files. Freshness test pins the result of a clean run. |
| `cmd/tkn-act/internal/agentguide-gen/` itself breaks | <100 LOC, pure stdlib, with unit tests, runs at `go generate` time so a break surfaces immediately on the contributor's machine. |

## What this spec deliberately does not decide

- Whether `docs/agent-guide/` should grow further subsections in
  future passes (e.g. splitting `resolvers.md` into per-resolver
  files). Out of scope; revisit if `resolvers.md` grows back over
  ~400 lines.
- Whether `tkn-act agent-guide --section <name> -o json` should
  invent a JSON envelope for a single section. Decided: no — the
  content is markdown and embedding it as a JSON string adds nothing.
- Whether the symlink direction (`CLAUDE.md → AGENTS.md`) should
  flip. No: `AGENTS.md` is the conventional contributor-guide name,
  Claude's project-instructions discovery already finds it via
  `CLAUDE.md`, and that's what the binary's `agent-guide` doc points
  at as "for dev work, see AGENTS.md."

## Relationship to the superseded 2026-05-08 spec

| Topic | 2026-05-08 spec | This spec |
|---|---|---|
| Audience | All content kept user-facing | Split: AGENTS.md → dev, `docs/agent-guide/` → user |
| Folder for split files | `docs/reference/` | `docs/agent-guide/` (binds the folder name to the command name) |
| AGENTS.md role | Skeleton with `<!-- include: -->` directives | Self-contained dev guide, no includes |
| Generator job | Parse include directives, expand inline | Copy a folder tree, in curated order |
| Generator complexity | Include grammar, path-shape regex, comment passthrough | Walk tree, validate against order, write deltas |
| `tkn-act agent-guide` flags | Unchanged | Adds `--list` / `--section` |
| Freshness test | Single-file diff | Recursive tree diff |
| Doc-rule update | One new row | Two new rows + one changed row |

What carries over verbatim: the case for a pure-Go stdlib tool over a
shell pipeline, the freshness-test pattern, the content-presence test
pattern, the one-PR migration discipline, the rule that section text
moves without rewording.

The 2026-05-08 spec stays in the repo with a `Status: Superseded`
banner so its rationale isn't lost.
