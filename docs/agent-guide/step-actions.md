## StepActions (`tekton.dev/v1beta1`)

A `StepAction` is a referenceable Step shape: a top-level Tekton
resource (`apiVersion: tekton.dev/v1beta1`, `kind: StepAction`) that
the engine inlines into Steps that reference it via `ref:`. Same
multi-doc YAML stream as Tasks / Pipelines — pass with `-f`.

```yaml
apiVersion: tekton.dev/v1beta1
kind: StepAction
metadata: {name: greet}
spec:
  params: [{name: who, default: world}]
  results: [{name: greeting}]
  image: alpine:3
  script: |
    echo hello $(params.who) > $(step.results.greeting.path)
---
apiVersion: tekton.dev/v1
kind: Task
metadata: {name: greeter}
spec:
  steps:
    - name: greet
      ref: {name: greet}
      params: [{name: who, value: tekton}]
```

Resolution rules tkn-act follows:

| Field | Behavior |
|---|---|
| `Step.ref.name` | Resolved against the loaded bundle's `StepActions` map. Unknown name → exit 4 (validate). |
| `Step.ref` + body fields | Mutually exclusive. A Step is either inline or a reference; setting both → exit 4. |
| `Step.params` | Bound into the StepAction's declared params. StepAction defaults apply for omitted entries; missing required params → exit 4. Caller param values are forwarded as LITERAL strings, so `value: $(params.repo)` survives the inner pass and resolves against the surrounding Task scope. |
| `name`, `onError` | Per-Step (kept from the calling Step). |
| `volumeMounts` | Union — StepAction body's mounts come first, caller's appended (matches Tekton). |
| `image`, `command`, `args`, `script`, `env`, `workingDir`, `imagePullPolicy`, `resources`, `results` | From the StepAction's body. |
| `$(params.X)` inside the StepAction body | Resolves against the StepAction-scoped param view (StepAction defaults + caller bindings); other `$(params.X)` survive into the outer Task pass. |
| `$(step.results.X.path)` | Writes to `/tekton/steps/<calling-step-name>/results/X` — same per-step results dir as inline `Step.results`. |
| `$(steps.<calling-step-name>.results.X)` from later steps | Reads the literal value, same as for inline Steps. |
| Inline Step (no `ref:`) without an `image:` | Rejected at validate time (exit 4). Image inheritance from `stepTemplate.image` counts. |
| Resolver-form `ref:` (`{resolver: hub, params: [...]}`) | Rejected at validate time with a clear "not supported in this release; see Track 1 #9" message. |
| Nested StepActions (a StepAction body containing `ref:`) | Schema-rejected: `StepActionSpec` does not model `ref:`. |
| StepAction `params[].default` of array/object type | Rejected at validate time (only string defaults are honored in v1). |

The cluster backend receives the same expanded Step shape — there is
no `kind: StepAction` apply onto the per-run namespace. Expansion is
fully client-side, so both backends are bit-identical at the
submission layer; no class of bug can have one Tekton controller
resolve a StepAction differently from another.
