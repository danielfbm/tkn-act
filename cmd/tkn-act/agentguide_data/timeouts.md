## Timeout disambiguation

Two distinct timeout primitives can both end a task with `status: "timeout"`
and exit code 6:

| Field | Scope | Where |
|---|---|---|
| `Task.spec.timeout` (per-task) | Wall clock for one Task attempt; retries reset it. | v1.2 |
| `Pipeline.spec.timeouts.pipeline` | Whole-run wall clock (tasks + finally). | v1.3 |
| `Pipeline.spec.timeouts.tasks` | Wall clock for the tasks DAG only. | v1.3 |
| `Pipeline.spec.timeouts.finally` | Wall clock for finally tasks only. | v1.3 |

When a `pipeline` budget fires, in-flight tasks end `timeout` and unstarted
tasks end `not-run`. The terminal `task-end` event for a budget-killed task
reports `status: "timeout"` and a `message` such as `"pipeline timeout 2s
exceeded"`. The run-end status is `timeout`.

`tasks` and `finally` are independent budgets — exhausting `tasks` does not
shorten `finally`, and vice versa.

We do not default `timeouts.pipeline` to `1h` the way upstream Tekton does;
omission means "no budget at this level." This may change in a future
release.
