# AGENTS.md Modularization

**Date:** 2026-05-08
**Status:** Draft — awaiting user review

## Problem

`AGENTS.md` (which `CLAUDE.md` symlinks to) has grown to ~881 lines. It is
the canonical agent guide *and* is embedded into the binary via `go:embed`
and printed by `tkn-act agent-guide`. Several deep feature sections
(resolvers, matrix, sidecars, StepActions, stepTemplate, pipeline results,
displayName/description, timeouts) make up roughly 500 of those lines and
dominate the file, even though they are reference material an agent only
needs when working on or with that specific feature.

Goals:

- Reduce the editable surface so a contributor adding a single feature
  edits one focused reference file, not a section buried in an 881-line
  document.
- Keep `tkn-act agent-guide` shipping the **same complete content** it
  ships today — no agent who relies on the binary loses information.
- Preserve every existing public contract (JSON shapes, exit codes, env
  vars, naming conventions).

## Non-goals

- Changing the *content* of any section. This is a structural refactor.
  Section text moves verbatim into reference files; rewording is
  out of scope.
- Adding new features, fixtures, or tests beyond what the new build step
  needs to verify.
- Touching `README.md` or other top-level docs beyond linking to the new
  reference files where appropriate.
- Slimming the embedded `agent-guide` output. The binary still emits the
  full guide.

## Architecture

### Source layout

| File | Role |
|---|---|
| `AGENTS.md` (symlinked from `CLAUDE.md`) | Skeleton: preamble, JSON interfaces, exit codes, env vars, pretty output, common workflows, failure modes, project rules (merge / tests / coverage / docs), local development, conventions, "where to look next." Contains `<!-- include: docs/reference/<name>.md -->` directives where deep sections used to live. |
| `docs/reference/step-template.md` | `Task.spec.stepTemplate` semantics |
| `docs/reference/sidecars.md` | `Task.sidecars` lifecycle, fields, JSON events |
| `docs/reference/step-actions.md` | `tekton.dev/v1beta1` `StepAction` resolution rules |
| `docs/reference/matrix.md` | `PipelineTask.matrix` fan-out semantics |
| `docs/reference/pipeline-results.md` | `Pipeline.spec.results` resolution |
| `docs/reference/display-name.md` | `displayName` / `description` surfacing + JSON event field naming note |
| `docs/reference/timeouts.md` | Per-task vs pipeline-level timeout disambiguation |
| `docs/reference/resolvers.md` | Track 1 #9 resolvers, Mode B, `--offline`, cache subcommands |

Each reference file starts with the same H2 heading currently used in
`AGENTS.md` (e.g., `## Resolvers (Track 1 #9, shipped)`). The expanded
output preserves section ordering and content; minor whitespace
normalization at section boundaries is acceptable. The freshness test
(below) pins whatever a clean generation produces, so the checked-in
`agentguide_data.md` becomes the canonical reference after the first
regeneration.

The "Local development" section stays inline in `AGENTS.md` per user
preference — short, useful up-front for new contributors.

### Include directive

A line of the exact form:

```
<!-- include: docs/reference/<name>.md -->
```

is the only directive recognized. Restrictions:

- Path is repo-root-relative.
- Must match `^docs/reference/[a-z0-9-]+\.md$` (no `..`, no absolute
  paths, no other directories — guards against accidental inclusion of
  arbitrary files).
- Unknown / missing target → expander exits non-zero with a clear
  message naming the directive line.
- Comments that aren't include directives (`<!-- ... -->`) are passed
  through unchanged.

### Build pipeline

`cmd/tkn-act/generate.go` today is one line:

```go
//go:generate sh -c "cp ../../AGENTS.md ./agentguide_data.md"
```

That becomes:

```go
//go:generate go run ./internal/agentguide-gen -in ../../AGENTS.md -out ./agentguide_data.md
```

`cmd/tkn-act/internal/agentguide-gen/main.go` is a small Go program
(~80 lines) that:

1. Reads `-in`.
2. Locates the repo root by walking up from the input file until it
   finds `go.mod`.
3. For each line matching the include directive, reads the target file
   relative to repo root and substitutes its contents (stripping a
   trailing newline if present, then appending exactly one newline so
   adjacent sections stay separated).
4. Validates path shape (regex above). Errors print
   `agentguide-gen: <path>:<line>: <message>` and exit 1.
5. Writes the assembled bytes to `-out` only if they differ from
   what's already there (avoids spurious file-mtime churn).

Why a Go program instead of a shell pipeline:

- Deterministic on macOS and Linux (no awk/sed dialect issues).
- Errors point at the directive line, not a cryptic shell exit.
- Easy to unit-test in `cmd/tkn-act/internal/agentguide-gen/main_test.go`.

The `Makefile`'s `agentguide` target keeps its current name; it still
runs `go generate ./cmd/tkn-act/`, which now invokes the new tool.

### Verification

Two layers of safety:

1. **Generated-file freshness gate.** A new
   `cmd/tkn-act/agentguide_freshness_test.go` re-runs the expander
   into a temp file and compares against the checked-in
   `agentguide_data.md`. A mismatch fails the test with a `run: go
   generate ./cmd/tkn-act/` hint. This makes "forgot to regenerate"
   a CI failure on the existing default test set, so the coverage
   gate sees it too.
2. **Content presence test.** Adds assertions to the existing
   `cmd/tkn-act/agentguide_test.go` that `agentguide_data.md`
   contains characteristic strings from each reference file
   (e.g., `"Matrix fan-out"`, `"Resolvers (Track 1 #9"`,
   `"Sidecars (\`Task.sidecars\`)"`). Catches an empty / wrong
   include path that happens to produce a file the same length as
   the previous one.

CI's existing `tests-required` and `coverage` gates already cover this
work, since the new tool ships with tests.

## Doc-rule update

The "keep related docs in sync" table in `AGENTS.md` gets a new row:

> | If you change... | Also update |
> |---|---|
> | A section now living in `docs/reference/<name>.md` (resolvers, matrix, sidecars, StepActions, stepTemplate, pipeline results, displayName, timeouts) | Edit the reference file directly AND re-run `go generate ./cmd/tkn-act/` (or `make agentguide`) so `cmd/tkn-act/agentguide_data.md` reflects the change |

Existing rows stay. The freshness test means CI catches missed
regeneration even if the contributor forgets.

## Migration steps (high level — full plan in writing-plans output)

1. Add `cmd/tkn-act/internal/agentguide-gen/` with main + tests.
2. Update `cmd/tkn-act/generate.go` to invoke the new tool.
3. Create `docs/reference/` and copy each target section verbatim from
   `AGENTS.md` into its file (each starts with the existing H2).
4. Replace each section in `AGENTS.md` with the matching include
   directive.
5. Run `go generate ./cmd/tkn-act/` to regenerate `agentguide_data.md`.
6. Add `agentguide_freshness_test.go` + content-presence assertions.
7. Update the doc-rule table.
8. Run the full test set, including `go test -race ./...`, to confirm
   no regressions.

The migration must be one PR (or a stack where the final PR lands the
include directives) — there is no in-between state where `AGENTS.md`
lacks a section *and* the reference file isn't created.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| Contributor edits `AGENTS.md` to add a section that should live in `docs/reference/` and the file gets bloated again | Doc-rule update + reviewer judgment. Not machine-enforced; the freshness test only catches stale generation. |
| `agentguide_data.md` gets out of sync with sources | New freshness test fails CI on any drift. |
| Include directive points at a file outside `docs/reference/` (typo, or someone tries `../../etc/passwd`) | Regex validation in expander rejects non-conforming paths at generate time. |
| Inline byte difference between old and new `agentguide_data.md` (trailing newlines, etc.) breaks downstream tools | Expander normalizes section boundaries to exactly one trailing newline; the expected baseline is whatever `go generate` produces from a clean checkout. The freshness test pins this. |
| `cmd/tkn-act/internal/agentguide-gen/` tool itself breaks, blocking all builds | Tool is small (<100 LOC), pure-Go stdlib, with its own unit tests. |

## What this spec deliberately does not decide

- Whether *additional* sections (e.g., env vars, exit codes) should also
  move to reference files in a future pass. Out of scope; revisit if the
  skeleton grows back over ~400 lines.
- Whether `README.md` should grow links to the new reference files.
  Probably yes, but not part of this refactor — separate doc-polish PR
  if desired.
- Whether to swap the `CLAUDE.md → AGENTS.md` symlink direction. Keeping
  current direction (`CLAUDE.md` is the symlink, `AGENTS.md` is the
  real file) — matches what the binary embeds and what `tkn-act
  agent-guide` advertises.
