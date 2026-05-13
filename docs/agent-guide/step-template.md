## `stepTemplate` (DRY for Steps)

`Task.spec.stepTemplate` lets a Task declare base values that every
Step in `spec.steps` inherits. Inheritance rules tkn-act follows:

| Field | Behavior |
|---|---|
| `image`, `workingDir`, `imagePullPolicy` | Step value wins if non-empty; otherwise inherit. |
| `command`, `args` | Step value wins as a whole if non-empty (no element-wise merge). |
| `env` | Union by `name`; Step entry overrides template entry with the same name. |
| `resources` | Step value wins (replace); no deep merge of `limits` / `requests`. |
| `name`, `script`, `volumeMounts`, `results`, `onError` | Per-Step only; never inherited. |

This matches Tekton v1 semantics for the subset of Step fields
tkn-act reads. `volumes` / `volumeMounts` inheritance from
`stepTemplate` is **not** supported (gap, see `docs/feature-parity.md`).
