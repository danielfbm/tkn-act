## Scheduling: `runAfter`, `when`, and `finally`

The docker backend schedules a Pipeline the way Tekton's controller does.
These rules decide whether a task runs, is skipped, or never starts.

### Edges

A task waits for:

- every task named in its `runAfter`, and
- every task whose result it references (an *implicit* edge). References
  are found in `params` (string, array items, object values), `when`
  `input` and `values`, and `taskRef` resolver `params`. The forms
  `$(tasks.a.results.x)`, `$(tasks.a.results.x[*])` and
  `$(tasks.a.results.x[2])` all count. A reference to a `matrix` task
  waits for every one of its rows.

### Skips

| Upstream state | `runAfter`-only dependent | Dependent that uses its results |
|---|---|---|
| succeeded | runs | runs |
| skipped by its own `when` | **runs** | skipped: `Results were missing: …` |
| failed / timed out | skipped: `upstream failure` | skipped: `upstream failure` |
| not run (any reason above) | skipped: `parent task skipped` | skipped |

A `when` expression skips only the task that carries it. Tasks that only
need to run *after* it still run. Tasks that use its results are skipped,
because those results never existed, and their own dependents are skipped
too. For a `matrix` task, rows are evaluated one by one. If at least one
row succeeded, dependents run. If every row was `when`-skipped, the task
counts as `when`-skipped.

### Events

| Outcome | Events | `RunResult.Tasks[...].status` |
|---|---|---|
| ran | `task-start`, (`task-retry`…), `task-end` | `succeeded` / `failed` / `infrafailed` / `timeout` |
| `when` evaluated false | `task-start`, `task-skip` (no `task-end`) | `skipped` |
| blocked upstream, results missing, budget fired, cancelled | `task-skip` only | `not-run` |

The `task-skip` `message` gives the reason (the evaluated `when` clause,
`upstream failure`, `parent task skipped`, `Results were missing: …`,
`tasks timeout 5m0s exceeded`, `pipeline run cancelled`, …). Plain tasks
and `matrix` rows produce the same events.

### `finally`

Finally tasks run after every DAG task is done, whatever the outcome.
They run **in parallel**, up to `--parallel` at a time, the way Tekton
runs them. If a finally task uses a result from a task that failed or was
skipped, it is skipped with `Results were missing: …` and never runs.
Other finally tasks still run.

If the run is cancelled (Ctrl-C), DAG tasks that had not started and all
finally tasks get a `task-skip` with the message `pipeline run cancelled`.
