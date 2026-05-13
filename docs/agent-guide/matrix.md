## Matrix fan-out (`PipelineTask.matrix`)

`PipelineTask.matrix` fans one PipelineTask out into N concrete
TaskRuns by Cartesian product over named string-list params, plus
optional `include` rows for named extras. v1 implements the high-value
subset (cross-product + include, per-row `when:`, result aggregation)
and defers Tekton's β knobs (`failFast: false`, `maxConcurrency`).

| Aspect | Behavior |
|---|---|
| Naming | The `task` field on every event always carries the per-expansion stable name. Cross-product expansions are `<parent>-<index>` (0-based, in row order, e.g. `build-1`); named `include` rows use their declared `name` (e.g. `arm-extra`); unnamed include rows continue the `<parent>-<i>` numbering past the cross-product. The new `matrix.parent` field on the event carries the original PipelineTask name. Consumers reading `Event.Task` keep working unchanged. |
| Row order | `matrix.params[0]` iterates slowest. For `[os=[linux,darwin], goversion=[1.21,1.22]]` the rows are `(linux,1.21)`, `(linux,1.22)`, `(darwin,1.21)`, `(darwin,1.22)`. Include rows are appended in declaration order. |
| Param merge | Per row: pipeline-task `params` ∪ matrix-row params; matrix wins on conflicts. |
| DAG | Downstream `runAfter: [<parent>]` is rewritten to wait on every expansion. Mixed-success matrix parents (some rows succeed, others skip-via-when) do NOT block downstream — only an all-non-success parent does. |
| Failure | Default: any expansion failing makes the parent fail; downstream skips. `failFast: false` is not implemented (β feature deferred). |
| Concurrency | Honors `--parallel` like ordinary parallel tasks. Per-matrix `maxConcurrency` is not implemented. |
| Cardinality cap | **256 rows total per matrix** (cross-product + include). Hardcoded; no env var, no flag. Exceeding is exit 4 (validate). If you need more, split the pipeline. |
| Result aggregation | A `string` result `Y` from a matrix-fanned task `X` is promoted to an array-of-strings, addressable as `$(tasks.X.results.Y[*])` and `$(tasks.X.results.Y[N])`. A scalar reference `$(tasks.X.results.Y)` (no `[*]`) returns the JSON-array literal *string* (e.g. `'["a","b","c"]'`) — matches Tekton's on-disk representation. `array` and `object` result types on a matrix-fanned task are validator-rejected. |
| `when:` | Evaluated **per row**. Each row's matrix-contributed params are visible in `$(params.<name>)` references inside the `when:` block. Rows that evaluate false emit one `task-skip` event under the *expansion* name (e.g. `build-1`) with `matrix:` populated. Other rows run normally. |
| JSON event shape | `task-start` / `task-end` / `task-skip` / `task-retry` events for an expansion carry `matrix: {parent, index, of, params}`. The field is `omitempty`, so non-matrix runs are byte-identical to today. |
| Cluster mode | Submitted to Tekton unchanged (round-trips through the existing JSON marshal path). The cluster backend reconstructs `MatrixInfo` per-TaskRun by canonical-hash matching the row's params against `taskRun.spec.params` (PRIMARY); falls back to `pr.status.childReferences` ordering with an `error` event warning if param-hashing produces no hit. |

`include` semantics: tkn-act always **appends** include rows as new
rows; it does NOT fold them into matching cross-product rows the way
upstream Tekton can. To prevent silent docker-vs-cluster divergence
(cluster delegates to the controller and would fold; docker would
not), `tkn-act validate` **rejects** any include row whose params
overlap a `matrix.params` name with an explicit error message and
exit 4. The `testdata/limitations/matrix-include-overlap/` fixture
is the documented example.
