## `--debug` flag and `kind:"debug"` events

`--debug` is a global persistent flag that turns on verbose internal
trace output. With `--debug -o pretty` the trace lines render inline
under each task. With `--debug -o json` the trace lines surface as a
new event kind on the existing one-event-per-line stream.

### Event shape

A debug event is an ordinary `kind:"debug"` line on the JSON stream.
Two new top-level fields appear only on debug events; every other
existing field stays `omitempty` and is absent.

```json
{
  "kind": "debug",
  "time": "2026-05-16T04:18:09Z",
  "component": "resolver",
  "message": "cache hit (per-run)",
  "fields": {
    "resolver": "git",
    "key": "9c5b3a01",
    "bytes": 4096
  }
}
```

| Field | Meaning |
|---|---|
| `component` | One of `backend`, `resolver`, `engine`. Identifies which subsystem emitted the line. |
| `message` | Short human-readable label (e.g. `container created`, `task ready`, `cache hit (per-run)`). |
| `fields` | Per-event payload (string keys, JSON-serializable values). |

`component` and `fields` are JSON-additive: they never appear on
non-debug events. Existing event kinds (`run-start`, `task-end`, etc.)
are unchanged.

### Pretty rendering

In `-o pretty` mode (the default), debug events render as one indented
line per event:

```
  [debug] component=resolver key=9c5b3a01 resolver=git bytes=4096 — cache hit (per-run)
```

Fields render in sorted key order so output is deterministic across
runs. Debug events are suppressed in `--quiet`.

### What gets emitted

This list will grow over time; the labels and `fields` keys are
stable once shipped.

**`component:"resolver"`** (every `Registry.Resolve` call):

- `dispatch` — entry point.
- `cache hit (per-run)`, `cache hit (disk)` — short-circuited fetches.
- `direct dispatch`, `remote dispatch` — about to call upstream.
- `allow-list rejected`, `not registered` — dispatch refused.
- `offline gate refused` — `--offline` rejected a miss.
- `resolve ok` — successful fetch, with `bytes` and `sha256`.

**`component:"backend"`** (docker):

- `image cache hit`, `image pull start`, `image pull done` (with
  `duration_ms`), `image pull failed`.
- `container created` (with `id`, `name`, `image`, `taskrun_name`,
  `step_name`), `container started`.
- `stager started`, `stager stopped`, `volume created` (remote-daemon
  staging path only).

**`component:"backend"`** (cluster):

- `pipelinerun applied` — Tekton accepted the PipelineRun.
- `taskrun seen` — first-observation of each TaskRun via watch.
- `pod attached` — log-stream goroutine launched on a TaskRun's pod.

**`component:"engine"`**:

- `task ready` — eg.Go closure entered; task about to start.
- `params resolved` — count of resolved params plus a `truncated_values`
  preview. Param values whose **name** matches
  `secret|token|password|passwd|key|credential|auth` (case-insensitive
  substring) are replaced with `<redacted>` so debug-archived
  events.jsonl doesn't capture credentials.
- `task skipped` — `when:` evaluator rejected the task. Carries
  `reason`, `expression` (raw `pt.When`), `evaluated` (post-
  substitution form), and on matrix expansions also `matrix_row` /
  `matrix_of` / `matrix_parent`.
- `task retry` — retry budget triggered another attempt; carries
  `attempt`, `total`, `reason`, `message`.

### Cost when disabled

Off by default. Every emit site is wrapped in a closure that the
emitter invokes only when `Enabled()` is true, so a default-mode
run pays nothing beyond an interface-method-call boolean check per
site.

### Replay

Debug events are recorded into the per-run `events.jsonl` alongside
every other event, so `tkn-act logs <id>` replays them too. Pair with
`--output json` if you want to feed the trace into another tool.
