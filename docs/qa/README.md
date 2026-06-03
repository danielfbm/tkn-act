# tkn-act QA — exploratory & regression test program

This directory is the home of the **black-box / behavioural** QA effort for
`tkn-act`: testing the shipped binary the way a user or AI agent drives it,
from the outside in. It complements — does not replace — the in-tree Go test
suites (`go test ./...`, `-tags integration`, `-tags cluster`) inventoried in
[`../test-coverage.md`](../test-coverage.md).

| File | Purpose |
|---|---|
| [`README.md`](README.md) | This file — scope, environment, conventions, how to run, how findings become issues. |
| [`exploratory-test-plan.md`](exploratory-test-plan.md) | The thinking artifact: every command area, normal paths **and** edge cases, with charters and questions to probe. Use it when you want breadth and to find *new* bugs. |
| [`regression-suite.md`](regression-suite.md) | The durable artifact: numbered, reproducible specs (`AREA-NNN`) with exact command, preconditions, and expected exit code / observable output. Use it to confirm nothing *regressed*. |

These are the **plan and the specs** — the durable, reviewable artifacts. The
output of an actual *execution pass* is not committed here: confirmed defects
become **GitHub issues** (label `qa`, see the rubric below), and any new
reproducible behaviour worth guarding becomes a new spec in `regression-suite.md`.

## Why this exists

The Go suites prove that *units behave as their authors intended*. This
program asks a different question: **does the binary, as a whole, behave as a
user reasonably expects — including on inputs the author didn't think of?**
That gap is exactly where exploratory testing earns its keep: malformed YAML,
contradictory flags, env-var precedence, signal handling, huge/odd inputs,
Unicode, partial filesystems, and the seams *between* features.

## Scope

In scope (black-box, against `bin/tkn-act`):

- Every subcommand: `run`, `validate`, `list`, `doctor`, `help-json`,
  `agent-guide`, `cluster`, `cache`, `logs`, `runs`, `version`, `completion`.
- The full flag surface, including global flags and their precedence rules.
- The stable contracts: exit codes (`0/1/2/3/4/5/6/130`), JSON event kinds and
  field names, `-o json` shapes for every command.
- Both backends: `--docker` (default) and `--cluster` (k3d). Remote-docker
  paths where a daemon is reachable.
- Cross-cutting concerns: env-var precedence, signal handling, concurrent
  runs, retention/pruning, the persistence/replay round-trip.

Out of scope (covered elsewhere or non-goals):

- Internal Go unit behaviour (owned by `go test`).
- Windows, arm64-only paths, signed pipelines, tekton-results, custom tasks
  (v1 non-goals per [`../feature-parity.md`](../feature-parity.md)).
- Performance/benchmark regressions (no perf gate exists; noted as a known gap).

## Environment baseline

Note the actual environment (and quote it in any issue you file) for every
pass. The plan assumes:

- `bin/tkn-act` built from the branch under test (`go build -o bin/tkn-act ./cmd/tkn-act`).
- `tkn-act doctor -o json` reports `ok: true` for the `default` checks (Docker
  reachable). `k3d` + `kubectl` present enables the `--cluster` charters.
- A scratch `TKN_ACT_STATE_DIR` and `XDG_CACHE_HOME` per pass so retention /
  cache / runs tests start from a known-empty state and never clobber the
  user's real store. Example:

  ```sh
  export TKN_ACT_STATE_DIR="$(mktemp -d)/state"
  export XDG_CACHE_HOME="$(mktemp -d)/cache"
  ```

- Network egress is **not** assumed. Resolver charters that need a real remote
  (`git`/`hub`/`http`/`bundles` against the public internet) are marked
  `NET`; everything else must pass offline.

## How a pass is run

1. Build the binary from the branch under test.
2. Note the environment (versions, OS/arch, `doctor -o json`) so it can be
   quoted in any issue you file.
3. Walk `regression-suite.md` top to bottom; for each spec confirm PASS, or
   for a FAIL open a GitHub issue with the repro.
4. Walk the `exploratory-test-plan.md` charters; for each, timebox the
   session and promote any *reproducible surprise* into (a) a new regression
   spec and (b) a GitHub issue.

Fixtures live under the repo's existing `testdata/e2e/` and `pipelines/`
trees wherever possible; ad-hoc fixtures a charter needs are created inline in
a scratch dir and the YAML is pasted into the issue / spec so it stays
reproducible.

## Severity & priority rubric (for filed issues)

Every confirmed defect becomes a GitHub issue carrying both axes. **Severity**
= how bad the behaviour is; **Priority** = how soon it should be fixed.

| Severity | Meaning |
|---|---|
| `S1-critical` | Data loss, wrong exit code on a success/failure boundary an agent branches on, crash/panic, or a documented stable contract violated. |
| `S2-major` | A documented feature produces wrong output or wrong status, but with a workaround; misleading error that sends the user down the wrong path. |
| `S3-minor` | Cosmetic-but-wrong: bad help text, imprecise error message, JSON field present-but-wrong in a non-load-bearing way. |
| `S4-trivial` | Typos, polish, nice-to-haves. |

| Priority | Meaning |
|---|---|
| `P0` | Fix before next release; blocks agents/CI that rely on the contract. |
| `P1` | Fix soon; user-visible and likely to be hit. |
| `P2` | Schedule; real but narrow or low-frequency. |
| `P3` | Backlog. |

Contract violations (exit-code meaning, JSON field rename/retype, event-kind
rename) are **always** at least `S1`/`P0` because `AGENTS.md` declares them
public and stable.

## Issue template

Each filed issue uses this body shape so triage is mechanical:

```
## Summary
<one sentence: what's wrong>

## Severity / Priority
S?-… / P?

## Environment
tkn-act <version>, <os>/<arch>, backend <docker|cluster>, doctor ok=<bool>

## Repro
<exact commands + minimal fixture YAML inline>

## Expected
<what the contract / docs / common sense say should happen>

## Actual
<what happened, incl. exact exit code and relevant output>

## Regression spec
<AREA-NNN id if promoted into regression-suite.md>

## Notes
<root-cause guess, related code path, links>
```

Labels applied: `qa`, `bug`, the `S?-…` severity, the `P?` priority, and an
area label (`area/run`, `area/resolver`, `area/cluster`, `area/cli`, …).
