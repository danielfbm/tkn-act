## `tkn-act logs` and the run-store

Every `tkn-act run` invocation persists its JSON event stream to
disk. After the run finishes you can re-stream the same events with
`tkn-act logs`, inspect or prune the history with `tkn-act runs`,
and apply the same `--task` / `--step` filters to the replay that
work on the live stream. The on-disk format is full-fidelity, so
agents that drop the live stream (process killed, network blip in a
remote tail) can recover the exact byte sequence by replaying.

### Where state lives

The run-store sits under `$XDG_DATA_HOME/tkn-act/` by default
(`$HOME/.local/share/tkn-act/` on Linux when `XDG_DATA_HOME` is
unset; the OS user-data dir on macOS / Windows). Two overrides, in
precedence order:

1. `--state-dir <path>` — per-invocation flag. Highest precedence.
2. `TKN_ACT_STATE_DIR=<path>` — process env var. Wins over the XDG
   default; the flag wins over the env var.

`tkn-act runs list` and `tkn-act logs` never create the state-dir
as a side effect — a non-existent path returns an empty list / a
usage error respectively, so introspection on a fresh machine is
side-effect-free.

### Per-run directory layout

```
$STATE_DIR/
├── index.json           # ordered list of recorded runs (newest seq first)
└── runs/
    └── 01HQYZAB.../     # ULID per run (sortable by start time)
        ├── meta.json    # pipeline ref, args, start/end time, status, writer_version
        └── events.jsonl # one JSON event per line, exactly as -o json emitted
```

`events.jsonl` is the canonical recording: every event written to
stdout under `-o json` is also persisted here, byte-for-byte. The
persistence sink ignores `--task` / `--step` / `--quiet` / `-o
pretty` so the on-disk file always carries the full stream.

### Identifying a run

Anywhere `<id>` appears below you can pass:

| Form | Meaning |
|---|---|
| (omitted) or `latest` | The newest run by start time. |
| `<N>` (decimal integer) | Run with sequence number `N`. `runs list` shows the column. |
| `<ulid-prefix>` | Any unique prefix of a ULID. Ambiguous prefixes return exit code 2 (usage) with the candidate list on stderr. |

### `tkn-act logs [id]`

Re-streams the recorded events. Accepts the same `--output`,
`--color`, `--quiet`, `--verbose`, `--task`, `--step`, and
`--timestamps` flags as `tkn-act run`. The filter wraps the
output sink only — the recorded `events.jsonl` is untouched — so
re-running with different filters narrows or widens the view.

```sh
# Pretty replay of the most recent run
tkn-act logs

# JSON replay of run #7, filtered to one task
tkn-act logs 7 --task build -o json

# Tail with timestamps, narrow to one step
tkn-act logs latest --timestamps --task build --step compile
```

Exit codes:

| Code | When |
|---|---|
| 0 | Replay completed cleanly. |
| 2 | No runs recorded yet, `<id>` didn't parse, ambiguous ULID prefix, no run matched, or empty positional. |
| 3 | State-dir present but unreadable, or `events.jsonl` is corrupt. |

Note that `logs` does not re-derive the original run's exit code —
exit `0` means the replay itself succeeded, not that the recorded
pipeline succeeded. Inspect the final `run-end` event's `status`
field to know whether the original run passed or failed.

### `tkn-act runs list`

Prints a table (or `-o json` array) of recorded runs, newest first.
Columns: sequence number, ULID, start time, status, pipeline name.

```sh
tkn-act runs list                # recent runs (default: most recent 20)
tkn-act runs list --all          # every recorded run
tkn-act runs list -o json        # machine-readable; array of objects
```

`--all` and `-o json` compose. The JSON shape is one object per
run with stable keys (`seq`, `ulid`, `started`, `ended`, `status`,
`pipeline`).

### `tkn-act runs show <id>`

Prints the `meta.json` for a single run. `-o json` emits the file
verbatim; the default pretty mode formats it as a labeled table.
Useful for confirming the pipeline ref and args of a past run
before replaying.

### `tkn-act runs prune`

Applies retention immediately (the same policy that runs
automatically after every `tkn-act run`). With `--all --yes`,
deletes every recorded run.

```sh
tkn-act runs prune               # apply default retention now
tkn-act runs prune --all --yes   # nuke every recorded run
```

`--all` without `--yes/-y` errors with exit code 2 — the
destructive case is opt-in. `--yes` without `--all` also errors so
shell-variable typos can't silently bypass the confirmation.

### Retention policy

Every successful `tkn-act run` invocation runs the retention gate
in the background:

- Keep the most recent `TKN_ACT_KEEP_RUNS` runs (default `50`).
- AND drop runs older than `TKN_ACT_KEEP_DAYS` days (default `30`).

A run only survives if it satisfies BOTH conditions. Set either
env var to `0` to disable that gate; set both to `0` to keep
everything until you manually `runs prune --all`.

```sh
TKN_ACT_KEEP_RUNS=200 tkn-act run pipeline.yaml   # this CI keeps more history
TKN_ACT_KEEP_DAYS=0   tkn-act run pipeline.yaml   # no age-based eviction
```

### Worked example

```sh
# 1. Run a pipeline
tkn-act run -f pipeline.yaml --param revision=main

# 2. See it in the history
tkn-act runs list

# Output (truncated):
#   SEQ  ULID                            STARTED              STATUS    PIPELINE
#   42   01HQYZAB7DPQM4VT6Y3Z5K8N1F      2026-05-16 14:08:31  succeeded build-and-deploy
#   41   01HQYY9ABG4D6X7E2KZ8RC0VPB      2026-05-16 09:14:02  failed    build-and-deploy

# 3. Re-stream run #42 as JSON, narrowed to one task
tkn-act logs 42 --task build -o json | jq -c '{kind, task, step, status}'

# 4. Compare two runs side-by-side
diff \
  <(tkn-act logs 41 --task build -o json) \
  <(tkn-act logs 42 --task build -o json)
```

### Conventions

- The `events.jsonl` file is append-only and never edited in place.
  A corrupt or truncated file (process crash mid-write) is detected
  on replay and returns exit code 3.
- `meta.json` carries a `writer_version` field so future readers
  can refuse incompatible recordings rather than mis-parsing them.
- `tkn-act logs` and `tkn-act runs *` operate read-only against the
  state-dir; only `tkn-act run` (during the run) and
  `tkn-act runs prune` (on demand) ever write or delete.
