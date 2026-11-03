## Variable substitution (`$(...)`)

tkn-act resolves Tekton variables in PipelineTask `params` / `when`
and in Step / Sidecar `image`, `command`, `args`, `script`,
`workingDir` and `env` values. Only `$(...)` tokens whose first segment
is a Tekton namespace are interpreted:

| Namespace | Supported references |
|---|---|
| `params` | `$(params.x)`, `$(params.arr[N])`, `$(params.obj.key)`; `$(params.arr[*])` as a whole array element (see below). |
| `tasks` | `$(tasks.t.results.r)`, `$(tasks.t.results.r[N])` (array result), `$(tasks.t.results.r.key)` (object result), `$(tasks.t.results.r[*])`; `$(tasks.status)` and `$(tasks.t.status)` in **finally** tasks only. |
| `context` | `pipelineRun.name`, `pipelineRun.namespace`, `pipelineRun.uid`, `pipeline.name`, `taskRun.name`, `taskRun.namespace`, `taskRun.uid`, `task.name`, `task.retry-count`, `pipelineTask.retries`. |
| `workspaces` | `$(workspaces.w.path)`, `$(workspaces.w.bound)` (`true` / `false`), `$(workspaces.w.claim)` (always empty: tkn-act binds host directories, not PVCs), `$(workspaces.w.volume)`. |
| `results` | `$(results.r.path)`. |
| `steps` / `step` | `$(steps.s.results.r)`, `$(step.results.r.path)`. |

**Anything else is left untouched**, exactly like Tekton. Shell command
substitution such as `$(date)`, `$(pwd)` or `$(hostname)` in a `script`,
`args` item or `env` value reaches the container verbatim and is
evaluated by the shell.

A reference in one of the namespaces above that can't be resolved (an
undeclared param, a typo'd context var, an out-of-range `[N]`, a
non-JSON array/object result) **fails the task** with a message naming
the step and field, e.g. `step "build": script: unknown param "vesion"`.
The task never runs with a blanked field.

### Whole-array references (`[*]`)

`$(params.arr[*])` and `$(tasks.t.results.r[*])` expand to every element
and are valid only as a whole array element:

- an item of a Step's `command` / `args`;
- a PipelineTask param value — either the whole value
  (`value: $(params.arr[*])`, bound as an array) or an item spliced into
  a longer array (`value: ["$(params.arr[*])", extra]`);
- an item of `when[].values`.

Embedding `[*]` inside a larger string (`"pre $(params.arr[*])"`, or in a
`script`) is rejected with a clear error, as Tekton does.

### `$(tasks.status)` and `$(tasks.<t>.status)`

Available only to `finally` tasks (in `params` and `when`); a main task
that references them fails. Values follow Tekton:

| Reference | Values |
|---|---|
| `$(tasks.<t>.status)` | `Succeeded`, `Failed` (failed / timed out), `None` (skipped or never ran). Matrix-fanned tasks aggregate their rows: any failed row → `Failed`, all succeeded → `Succeeded`. |
| `$(tasks.status)` | `Failed` (any main task failed), `Completed` (none failed, at least one skipped), `Succeeded` (all succeeded), `None` (some main task never reached a terminal state). |

### Backend notes for `$(context.*)`

On the docker backend `*.namespace` is `default` and `*.uid` is a
deterministic UUID-shaped value derived from the run ID (stable across
retries of the same TaskRun). `context.task.name` is the referenced
Task's name, or the TaskRun name for an embedded `taskSpec`. On
`--cluster` the real Tekton controller supplies these values: the
namespace is the per-run namespace, the UIDs are Kubernetes UIDs, and —
because tkn-act inlines referenced Tasks into the submitted
PipelineRun — `context.task.name` is the TaskRun name there too.
