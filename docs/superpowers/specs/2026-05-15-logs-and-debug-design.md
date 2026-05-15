# Pipeline log replay (`tkn-act logs`) + a real `--debug` flag

**Date:** 2026-05-15
**Status:** Draft — design + implementation will land together (per project
convention: spec + PR together).

## Problem

`tkn-act run` already streams step logs live in pretty mode and emits
`step-log` / `sidecar-log` JSON events. The current UX gap is narrower
than "no logs at all" — three concrete deficiencies:

1. **`--debug` is dead.** `cmd/tkn-act/root.go:94` registers
   `--debug` as a persistent flag described as *"verbose internal logs"*,
   but `gf.debug` is read from zero call sites. Anyone passing it today
   sees no behavior change.
2. **No post-hoc replay.** Once `tkn-act run` exits, the run's logs and
   event stream are gone unless the user redirected stdout themselves.
   There is no `tkn-act logs <run-id>` equivalent to
   `tkn pipelinerun logs <name>` — re-investigating a failing run means
   re-running the whole pipeline.
3. **Live display is unfiltered and missing two channels.** No way to
   isolate output from one task / step on a multi-task pipeline.
   Sidecar logs surface only in JSON (`sidecar-log` events) — pretty
   mode drops them on the floor. Timestamps are absent from the pretty
   stream (they exist in JSON events).

The user-facing impact is that triage workflows that work upstream with
`tkn` — "run a pipeline, then `tkn pr logs <pr> --task <t>` to read just
that task" — have no analogue here, and that `--debug` doesn't actually
let an operator see *why* a particular task chose its image, which
resolver served a `bundle://` ref, or which container ID failed.

## Goals

- **`--debug` actually does something.** When set, the run emits a new
  class of trace lines from three components: the backend (docker /
  cluster internals — container IDs, image pulls, exec'd commands,
  kubectl events), the resolver dispatcher (which resolver matched,
  cache hit/miss, fetched bytes), and the engine state machine (task
  readiness, skip reasons, retry decisions). Surfaced inline in pretty
  mode (`  [debug] component=resolver cache=hit ref=hub://git-clone:0.9`)
  and as a new additive `kind:"debug"` JSON event.
- **`tkn-act logs <id>` replays a previous run.** Same reporter
  pipeline as `tkn-act run` (pretty / JSON), same `--task` / `--step`
  filters, same `--quiet` / `--verbose` knobs.
  `tkn-act logs` with no arg defaults to the latest run.
- **Persistence is implicit.** Every `tkn-act run` writes the same
  JSON event stream it emits on stdout to a per-run state directory.
  The user does nothing extra to enable replay. Default location
  follows XDG; teams that want repo-local state set
  `TKN_ACT_STATE_DIR=./.tkn-act` (in a Makefile target, a shell
  alias, or an `.envrc`) or pass `--state-dir`.
- **Live polish:** add sidecar log lines to pretty mode (today JSON-only),
  optional timestamps via `--timestamps`, and `--task` / `--step`
  filters that work on both `tkn-act run` and `tkn-act logs`.
- **Public-contract additions are additive.** New subcommands, new
  flags, new env vars, one new JSON event kind. Nothing renamed.

## Non-goals

- **`tkn-act logs --follow` to tail an in-progress run.** `tkn-act run`
  is already the live stream; we don't ship a multi-process tail
  mode. Opening a second terminal on a long run is rare enough to
  defer.
- **Log shipping to remote storage** (S3, OCI, etc.). The state-dir is
  local. Users who need durable artifacts can `tar` it themselves.
- **A new backend.** Persistence is a reporter sink, not a new
  execution mode.
- **`--prefix` / `--no-prefix` toggle.** Daniel explicitly dropped it
  from this round. Pretty mode keeps the existing
  `  task-name │ line` format; users wanting raw lines can pipe
  `-o json` to `jq -r '.line'`.
- **Rename or retype any existing event field.** The persistence sink
  writes the JSON event stream exactly as it lands on stdout today.
  Replay re-emits it 1:1.
- **Tekton controller logs surfaced through `--debug` for the cluster
  backend.** The k3d controller's own stdout is reachable via
  `kubectl logs -n tekton-pipelines deploy/tekton-pipelines-controller`;
  we don't proxy it. Cluster `--debug` surfaces what *tkn-act* does
  with the controller (TaskRun YAML applied, pod scheduling waits,
  kubectl events relevant to the run), not what the controller logs
  internally.
- **Wire-level docker / k8s API traces.** Daniel ruled this out as too
  noisy. If someone needs `client-go --v=8` semantics, they can run
  `DOCKER_API_TRACE=1` or `KUBECTL_TRACE=1` externally.

## Architecture

### Data flow

`tkn-act run` already has a `reporter` abstraction
(`internal/reporter/`) that fans events out to one or more sinks. Today
the active sink is either `pretty` or `json` depending on `--output`.
We add a third sink, `persist`, that runs *in addition* to the active
output sink whenever the state-dir is writable:

```
                       ┌─────────────────┐
engine + backend ─►    │ reporter        │
+ resolver + sidecars  │ event router    │
                       └─┬───────────────┘
                         │
              ┌──────────┴────────────┐
              ▼                       ▼
        pretty / json sink      persist sink
        (stdout)                (events.jsonl on disk)
```

The persist sink is one goroutine appending newline-delimited JSON to
`<state-dir>/<ulid>/events.jsonl`. Same exact bytes as `-o json`
produces; no transformation. On `run-end` the sink flushes, closes the
file, and writes `meta.json` (run metadata for the index).

`tkn-act logs` reverses the flow: open `events.jsonl`, deserialize, run
the events through the same reporter pipeline that `tkn-act run` uses,
with the user's chosen `--output` / `--task` / `--step` / `--quiet` /
`--verbose` / `--timestamps` knobs applied. Replay is bounded by file
size, not by clock — events emit as fast as they decode, with no
synthetic delays.

### Debug emission

A new `internal/debug` package exposes a typed emitter:

```go
type Emitter interface {
    Emit(component Component, fields map[string]any, msg string)
}

type Component string
const (
    Backend  Component = "backend"
    Resolver Component = "resolver"
    Engine   Component = "engine"
)
```

When `--debug` is off the emitter is a no-op (no allocation, no map
build — callers pass a closure that only runs when the level is
enabled, via a `debug.Enabled()` guard). When `--debug` is on the
emitter routes through the same reporter so:

- Pretty mode renders each event as `  [debug] component=<c> <fields…> — <msg>`,
  indented to the same column as step logs so the visual flow holds.
- JSON mode emits a new event:
  ```json
  {"kind":"debug","time":"…","component":"resolver",
   "msg":"cache hit","fields":{"ref":"hub://git-clone:0.9","bytes":4096}}
  ```
- Persist sink stores it in `events.jsonl` like any other event.

Component coverage in v1:

| Component | Call sites |
|---|---|
| `backend` (docker) | container create / start / exec / inspect / logs stream open + close, image pull progress milestones, volume create / mount, stager container lifecycle. |
| `backend` (cluster) | TaskRun YAML applied (just the name + UID, not the body), pod scheduling state changes, image pull state changes, kubectl events the engine receives via the informer. |
| `resolver` | which resolver matched a `tekton://` URI, cache hit / miss + bytes, fallback chain when a resolver returns `ErrNotFound`, fetched-size summary on resolver-end. |
| `engine` | task readiness transitions (`pending → running`), `when:` skip reasons with the expression evaluated, retry decisions with retry count + remaining, param-resolution end with the final value (truncated to 64 chars per value). |

The list is closed for this PR — new debug emitters in future PRs are
additive but require touching the matching agent-guide section.

### Storage layout

```
<state-dir>/
  runs/
    01HQYZ…-<n>/
      meta.json
      events.jsonl
    01HQZ…/
      meta.json
      events.jsonl
  index.json        # ordered list: ulid + sequential numeric index + pipelineRef + summary
```

- Directory name is a ULID (`01HQYZAB…`) for lexical-chronological
  sort. `github.com/oklog/ulid/v2` is a small zero-dep Go module
  (would be a new direct dep). If P1 rejects the new dep, fall
  back to an in-house `time.Now().UTC().UnixMilli()` +
  `crypto/rand` 10-byte tail formatted as Crockford base32 — about
  30 lines.
- `meta.json` holds: `{run_id, ulid, seq, writer_version,
  pipeline_ref, started_at, ended_at, exit_code, status, args}`.
  `seq` is the user-facing monotonic integer (1, 2, 3 …) assigned at
  run-start. `writer_version` is the `tkn-act version` that produced
  the run (used for replay-drift detection — see Risks).
- `index.json` is the authoritative ordered list. Held under a file
  lock during increment / prune so concurrent runs don't collide.
  Touched O(runs) per new run; runs are small.
- `events.jsonl` is append-only newline-delimited JSON, one event per
  line, identical bytes to what `-o json` writes to stdout.

State-dir resolution precedence (highest first):

1. `--state-dir <path>` flag on `run` / `logs` / `runs`.
2. `TKN_ACT_STATE_DIR` env var.
3. `$XDG_DATA_HOME/tkn-act/` (default).
4. `$HOME/.local/share/tkn-act/` (XDG fallback when `XDG_DATA_HOME`
   is unset).

The default lives outside the user's project tree — a casual
`tkn-act run` in a contributor's checkout doesn't drop a `.tkn-act/`
beside their source. Teams that *want* per-repo run history set
`TKN_ACT_STATE_DIR=./.tkn-act` in a Makefile target, a shell alias,
or an `.envrc`. We intentionally don't introduce a `.tkn-act.yaml`
config-file mechanism in v1 — the project has no config-file
infrastructure today, and the env-var / flag knobs cover the
opt-in case without one. If a richer per-project config story
emerges later, state-dir is a candidate key.

### Retention

Auto-GC runs at the start of every `tkn-act run`, before the new
run's directory is created:

- **Count:** keep the most recent 50 runs (default).
- **Age:** delete runs whose `meta.ended_at` is more than 30 days old
  (default).
- Both apply: a run is kept only if it's in the last 50 *and* not yet
  30 days old.

Tunable via:

| Var | Default | Effect |
|---|---|---|
| `TKN_ACT_KEEP_RUNS` | `50` | Max runs to keep. `0` disables count-based pruning. |
| `TKN_ACT_KEEP_DAYS` | `30` | Max age in days. `0` disables age-based pruning. |

`tkn-act runs prune` runs the same logic on demand. `tkn-act runs prune --all`
deletes every run.

### `tkn-act logs` — usage

```
tkn-act logs                       # replay latest run
tkn-act logs latest                # alias for the above
tkn-act logs 7                     # replay run #7 (seq from meta.json)
tkn-act logs 01HQYZAB              # replay by ULID prefix (unique match required)
tkn-act logs --task build          # filter while replaying latest
tkn-act logs 7 --task build --step compile -o json
```

Shared flags with `tkn-act run` (same meaning, same precedence):
`-o / --output`, `-q / --quiet`, `-v / --verbose`, `--task`, `--step`,
`--timestamps`, `--color`, `--state-dir`. Replay-specific flags:
none; we keep the surface tight.

Exit codes (reuses the existing public table — no new codes added):

| Code | Meaning |
|---|---|
| `0` (OK) | Replay completed. Underlying run's `exit_code` is printed in the final summary. |
| `2` (Usage) | Bad ID, ambiguous ULID prefix, no matching run, unknown flag. |
| `3` (Env) | State-dir unreadable, `events.jsonl` truncated or corrupt (replay aborts on first malformed line; prior events have already streamed). |

Note: `tkn-act logs` *replay exit code* is whether replay succeeded.
The replayed *pipeline's* exit code is in the final summary line and
in `meta.exit_code`, but isn't re-asserted by the `logs` subcommand —
that would conflate "did the replay work" with "did the pipeline
succeed", and triage flows want them separate.

### `tkn-act runs` — usage

```
tkn-act runs list                  # table of recent runs (default: 20)
tkn-act runs list -o json          # JSON: array of {seq, ulid, pipeline_ref, started_at, exit_code, status}
tkn-act runs list --all            # all runs in the state-dir
tkn-act runs show 7                # human view of meta.json for run #7
tkn-act runs show 7 -o json        # raw meta.json
tkn-act runs prune                 # apply retention policy now
tkn-act runs prune --all           # delete every run (asks for confirmation unless -y)
```

`list` columns: `#  ULID         pipeline                   started               duration   exit  status`.

### Live-display polish details

- **Sidecar pretty format.** Sidecar log lines render as
  `  task-name ◊ sidecar-name │ line` — the `◊` glyph distinguishes
  from steps (which use the existing format). Stderr from sidecars
  uses the same `! ` marker as step stderr.
- **`--timestamps`.** When set, prepends `[15:04:05.123] ` to every
  step / sidecar / debug line in pretty mode. Format is fixed
  (`HH:MM:SS.mmm`); we don't expose a `--time-format` flag. JSON
  events already carry RFC3339 `time` — no change.
- **`--task <name>` / `--step <name>`.** Repeated flag, OR semantics
  within each axis, AND across axes. `--task build --task deploy`
  shows lines from either task; `--task build --step compile` shows
  lines from `build`'s `compile` step only. Filter applies to
  `step-log`, `sidecar-log`, `step-start`, `step-end`,
  `sidecar-start`, `sidecar-end`, `task-start`, `task-end`,
  `task-skip`, `task-retry`, and (when emitting from a known task /
  step) `debug` events. `run-start` / `run-end` / `error` always
  emit regardless of filter — the user needs to see the run result.

## CLI / env surface

Additions to `AGENTS.md` public-contract table:

| Surface | Addition |
|---|---|
| Subcommand | `tkn-act logs [id\|latest\|seq\|ulid-prefix]` — replay a stored run. |
| Subcommand | `tkn-act runs list` / `tkn-act runs show <id>` / `tkn-act runs prune` — manage the state-dir. |
| Flag | `--debug` (already registered; now functional). |
| Flag | `--state-dir <path>` on `run`, `logs`, `runs` — overrides resolution. |
| Flag | `--task <name>` (repeatable) on `run`, `logs` — filter. |
| Flag | `--step <name>` (repeatable) on `run`, `logs` — filter. |
| Flag | `--timestamps` on `run`, `logs` — pretty-mode timestamps. |
| Event kind | `debug` — `{kind, time, component, msg, fields}`. Additive. |
| Env var | `TKN_ACT_STATE_DIR` — overrides default state-dir. |
| Env var | `TKN_ACT_KEEP_RUNS` (default `50`, `0` = unbounded). |
| Env var | `TKN_ACT_KEEP_DAYS` (default `30`, `0` = no age prune). |
| Exit codes | None added. `tkn-act logs` reuses existing codes: `2` for usage / not-found, `3` for state-dir corruption. |

No renames. No type changes. Existing `--debug` keeps its name and
its description tightens from *"verbose internal logs"* to
*"emit debug events from backend, resolver, and engine"*.

## Doc convergence

The implementation PR must update:

- `docs/agent-guide/logs.md` — new file documenting `tkn-act logs`,
  the state-dir, run identifiers, filters, replay semantics, and
  retention.
- `docs/agent-guide/debug.md` — new file documenting `--debug`,
  the component breakdown, the inline pretty format, and the new
  `debug` JSON event schema.
- `docs/agent-guide/README.md` — add the two new sections to the
  curated ordering in `cmd/tkn-act/internal/agentguide/order.go`,
  add a one-line operational-flag mention for `--debug`,
  `--task`, `--step`, `--timestamps`, `--state-dir`.
- `docs/agent-guide/output-format.md` (or equivalent JSON-event doc)
  — add the `debug` event row with field shape.
- `README.md` — short "Replaying past runs" subsection with one
  example: `tkn-act run -f pipeline.yaml && tkn-act logs latest --task build`.
- `docs/feature-parity.md` — does *not* need a new row (these are
  CLI-UX features, not Tekton feature support). The `Last updated:`
  stamp does get bumped.
- `docs/test-coverage.md` — note that `tkn-act logs` is exercised by
  a new unit-test package and that an e2e fixture (`logs-replay/`)
  validates replay produces the same events as the original.
- `AGENTS.md` — new rows in the public-contract table (subcommands,
  flags, env vars, event kind), plus a brief note that the project
  now persists runs by default.
- `CLAUDE.md` symlink target (== `AGENTS.md`) — same.
- Regenerate: `go generate ./cmd/tkn-act/` (agent-guide freshness
  test fails CI otherwise).

## Migration

Pure addition. No flag flips, no existing-behavior changes.

- Users who never run `--debug` see nothing different.
- Users on `-o json` still get the same event stream on stdout; the
  new `debug` event only appears if `--debug` is set.
- The persist sink runs by default — disk impact is bounded by
  retention. CI environments where disk-write is undesirable point
  the state-dir at tmpfs: `TKN_ACT_STATE_DIR=/tmp/tkn-act-ci`. A
  `--no-persist` flag is not shipped in v1; add only if a workload
  demonstrates the need.
- The dead `--debug` flag becomes functional. Anyone scripting
  against `tkn-act help-json` saw it advertised already; behavior
  goes from "no-op" to "emit debug events." Closest thing to a
  breaking change in the PR — call it out in the release notes
  even though no existing script can depend on the no-op behavior.

## Test plan

- **Unit:**
  - `internal/runstore/` — write events.jsonl, read it back, retention
    GC (count-based, age-based, both), index-lock contention.
  - `internal/debug/` — Enabled() guard, emission routing through a
    fake reporter, no-op when disabled.
  - `internal/reporter/filters/` — `--task` / `--step` filter logic
    over the full event-kind set, including the "always pass"
    set (`run-start`, `run-end`, `error`).
- **Integration (`-tags integration`):** `tkn-act logs` against a
  canned events.jsonl fixture — pretty vs JSON output, filter
  combos, exit codes for missing / corrupt files.
- **E2E:**
  - New fixture `testdata/e2e/logs-replay/` — runs `tkn-act run`,
    captures the live JSON stream and the persisted `events.jsonl`,
    asserts byte-equality.
  - Extend at least two existing fixtures with `--debug` set on the
    `run` invocation in a separate CI matrix axis; assert at least
    one `debug` event per component is emitted.
- **Backwards-compat:** existing fixtures run with no flag change
  and must produce the same stdout bytes they do today (no debug
  events, no timestamps, no persistence-side-effects on stdout).
- **CI gates:** `tests-required`, `coverage` (per-package no-drop),
  `parity-check`, `agentguide-freshness` all pass.

## Risks

| Risk | Mitigation |
|---|---|
| Disk usage in CI environments (each PR runs many `tkn-act run` invocations under integration / cluster workflows, each leaving up to 50 runs × N MB on the runner). | Retention policy + small default footprint (events.jsonl is typically < 1 MB per run). CI workflows can set `TKN_ACT_STATE_DIR=/tmp/tkn-act-ci` to keep state on tmpfs and have it discarded with the runner. |
| State-dir collision when two concurrent `tkn-act run` invocations target the same dir (e.g., parallel make targets). | `index.json` is updated under a `flock(2)` (or `os.O_EXCL` lockfile on platforms without it). Per-run directories are ULID-named so they don't collide; only the sequence-number increment is serialized. |
| Replay drift if the JSON event schema changes between writer and reader (e.g., a `tkn-act` upgrade encounters older `events.jsonl`). | `meta.json` records the writer version. `tkn-act logs` reports a soft warning if the writer version is more than one minor below current. Same-major versions stay replay-compatible by construction of the additive-only event-kind rule. |
| `--debug` output volume on big pipelines (a 30-task pipeline with chatty image pulls could emit hundreds of debug lines). | Debug is opt-in; pretty rendering is indented and prefixed so it's grep-friendly. JSON consumers can `jq 'select(.kind != "debug")'`. Component-level filtering (`--debug-component backend` etc.) is **not** in v1 — defer until we see a workload that needs it. |
| Adding `--task` / `--step` filters introduces a footgun where users miss the run failure summary. | `run-start` / `run-end` / `error` events bypass the filter. The final exit line always prints. |
| `--timestamps` collides with users who already pipe pretty output through `ts` (moreutils). | They get double timestamps. Documented as a "don't do this" note in `docs/agent-guide/output-format.md`. |
| Existing `--debug` flag is advertised today (in `help-json`) with the description *"verbose internal logs"* — agents may have wired it up and discovered the no-op. | Going from no-op to real output is the only behavior change; help text is tightened in the same PR. |
| ULID library churn (`oklog/ulid` is stable but introduces a new direct dependency). | Vendor at a known release; `time.Now().UnixNano()` + 6 random bytes is a 30-line in-house fallback if the dep is rejected. Decide in P1. |

## What this spec doesn't decide

- **`tkn-act logs --follow`** — out of scope for v1. If a future user
  wants it, the persist sink already writes events.jsonl
  incrementally; adding a fsnotify-based tail is a small follow-up.
- **Per-component `--debug-component=resolver`** — same reasoning.
  Defer until a workload demonstrates the need.
- **`tkn-act runs export <id>` to bundle a run as a single
  archive** — useful for sharing reproductions, but not v1.
- **Whether `tkn-act validate` and `tkn-act doctor` should also
  persist a run record.** Likely no — they're not pipeline executions
  — but the persist sink is harmless if they do, and the call has
  no `--debug` impact. P4 implementation chooses.
- **Component vocabulary expansion** — adding `cache`, `sidecars`, or
  `cluster-controller` as components later is allowed (additive). v1
  ships exactly three.
- **Whether `--debug` flips `-v` semantics.** It does not: `--debug`
  adds the new event class on top of the user's `-v` / `-q` choice.
  `--debug -q` is meaningful — quiet step output, but show debug
  events.
- **Stager-image / pause-image awareness in debug output** — the
  remote-docker backend's stager activity is "backend internals" and
  surfaces accordingly; we don't add a new component for it.

## Implementation phases

The companion plan at
`docs/superpowers/plans/2026-05-15-logs-and-debug.md` covers six
phases, each a small reviewable PR (per the project's "spec + PR
together" rule, this spec lands with Phase 1):

1. **Persistence sink + storage layout.** New `internal/runstore`
   package; persist sink writes events.jsonl + meta.json; index.json
   maintained under file lock; state-dir resolution honors flag /
   env / config / XDG.
2. **`tkn-act logs` replay.** Decode events.jsonl, route through the
   existing reporter pipeline, support `<seq>` / `<ulid-prefix>` /
   `latest`.
3. **`tkn-act runs` family + retention GC.** `list`, `show`, `prune`;
   retention applied at run-start.
4. **`--debug` wiring + new `debug` JSON event kind.** Component
   emitters in backend (docker + cluster), resolver, engine. Helper
   `internal/debug` package.
5. **Live polish.** Sidecar lines in pretty mode, `--timestamps`,
   `--task` / `--step` repeatable filters (engine + replay).
6. **Doc convergence.** Two new agent-guide pages, README updates,
   `AGENTS.md` / `CLAUDE.md` rows, `agentguide` regen.

Phases 1-3 ship the post-hoc story; phase 4 ships `--debug`; phase 5
is independent polish; phase 6 is the doc sweep. Phases 1, 4, and 5
can be reordered if a different ordering shortens the critical path.
