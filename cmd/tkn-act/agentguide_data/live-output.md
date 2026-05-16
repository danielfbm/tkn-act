## Live-output controls: `--timestamps`, `--task`, `--step`

Three global persistent flags shape the pretty and JSON live streams
without changing what is recorded on disk. The persistence sink
(under `$XDG_DATA_HOME/tkn-act`) is always full-fidelity, so any
filter applied at run time can be re-applied or relaxed later via
`tkn-act logs`.

### `--timestamps`

Off by default. When set, prepends a UTC time prefix to step,
sidecar, and debug lines in pretty mode:

```
[14:08:31.412]   ▸ build:compile │ + go build ./...
[14:08:32.105]   ◊ build:redis   │ ready to accept connections
[14:08:32.310]   [debug] component=backend container=build/compile id=8f3b…
```

The prefix shape is `[HH:MM:SS.mmm]` (UTC, millisecond precision).
Step boundaries (`▸`, `◇`), task-start / task-end lines, the run
banner, and the run-end summary are unchanged so the visual scan
order doesn't shift. In JSON output the `time` field is already
present on every event regardless of this flag — `--timestamps`
exists only to make pretty mode parsable by `tail -F` consumers.

Events with a zero `time` (unset by upstream code) suppress the
prefix so test fixtures and replay against pre-feature events
don't render `[00:00:00.000]`.

### `--task <name>` and `--step <name>`

Repeatable filters that narrow the *live* output (both pretty and
JSON) to one or more tasks and/or steps. AND semantics: both must
match. Persistence is unaffected.

```sh
# Watch only the "build" task live; persist everything
tkn-act run pipeline.yaml --task build

# Two tasks, one step; JSON consumers see exactly these lines
tkn-act run pipeline.yaml --task build --task lint --step compile -o json
```

Pass-through rules (events that always reach the user even with
narrow filters):

| Event class | Why it always passes |
|---|---|
| `run-start`, `run-end`, `error` | The user must see the run envelope and any out-of-band failure. |
| Any event with empty `Task` | Top-level resolver-start / resolver-end for pipeline-ref resolution doesn't belong to any task; suppressing it would hide pre-task resolution failures. |
| Task-level events for matching tasks (task-start, task-end, task-skip, task-retry, sidecar-*, resolver-* for that task) | They carry the configured task. |

Step filter only refuses events that *carry* a `Step` field that's
not on the list — task-level events without a `Step` pass through
under the task filter. Otherwise filtering by step would suppress
every non-step-log event for the matching task.

### Replay (`tkn-act logs`)

The same `--task` / `--step` flags work on `tkn-act logs`. The
recorded `events.jsonl` is full-fidelity, so the flags select what
the current invocation renders rather than re-encoding the stream.
Run with no filter to see the complete recording, then re-run with
narrower filters as needed.

```sh
tkn-act logs latest --task build --step compile
tkn-act logs 7 -o json --task deploy
```

### Interplay with `--quiet` and `--verbose`

Order of effect:

1. The filter wrapper decides whether an event is delivered at all.
2. If delivered, the pretty sink consults `--quiet` / `--verbose`
   to decide which fields to render.

So `--quiet --task=build` shows the run banner plus only the
filtered task's terminal status, and `--verbose --task=build`
shows step boundaries for that task. Useful for narrow CI logs.
