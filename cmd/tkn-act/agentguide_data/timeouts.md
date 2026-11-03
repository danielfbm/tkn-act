## Timeout disambiguation

Two distinct timeout primitives can both end a task with `status: "timeout"`
and exit code 6:

| Field | Scope | Where |
|---|---|---|
| `Task.spec.timeout` (per-task) | Wall clock for one Task attempt; retries reset it. | v1.2 |
| `Pipeline.spec.timeouts.pipeline` | Whole-run wall clock (tasks + finally). | v1.3 |
| `Pipeline.spec.timeouts.tasks` | Wall clock for the tasks DAG only. | v1.3 |
| `Pipeline.spec.timeouts.finally` | Wall clock for finally tasks only. | v1.3 |

When a `pipeline`, `tasks` or `finally` budget fires, tasks that were
running end with `timeout`. Tasks that never started end with `not-run`.
A task killed by a budget gets a `task-end` event with
`status: "timeout"` and a `message` that names the budget, such as
`"pipeline timeout 2s exceeded"` or `"tasks timeout 200ms exceeded"`.
Every task that never started, in every level after the one that was
running, gets one `task-skip` event with that message and a `not-run`
entry in the run result. The run-end status is `timeout`.

When you cancel the run (Ctrl-C), every task that never started, and
every finally task, gets a `task-skip` event with the message
`"pipeline run cancelled"` and a `not-run` entry. Finally tasks do not
run. The run-end status is `cancelled`.

`Task.spec.timeout` also applies to tasks fetched by a resolver
(`taskRef.resolver`). The timeout is read from the fetched Task.

`tasks` and `finally` are independent budgets — exhausting `tasks` does not
shorten `finally`, and vice versa.

**Cluster mode on Tekton < v1.13:** before upstream's
`PipelineRunTimeoutRunningFinally` handling landed in v1.13, a
`timeouts.tasks` expiry terminates the whole PipelineRun before the
finally tasks complete. Under `--cluster` with such a release (e.g. the
`v1.9.9` leg of the CI matrix, or `--tekton-version v1.9.9`) finally
tasks may therefore be cut short or never start, where the docker
backend and Tekton ≥ v1.13 (including the `v1.15.5` default) run them
to completion.

We do not default `timeouts.pipeline` to `1h` the way upstream Tekton does;
omission means "no budget at this level." This may change in a future
release.
