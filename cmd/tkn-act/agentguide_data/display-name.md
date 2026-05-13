## `displayName` / `description`

`Pipeline.spec`, `PipelineTask` (entries under `spec.tasks` and
`spec.finally`), `Task.spec`, and `Step` all accept optional
`displayName` and `description` fields per Tekton v1. tkn-act surfaces
them on the JSON event stream as snake_case keys:

| Event kind | `display_name` source | `description` source |
|---|---|---|
| `run-start` | `Pipeline.spec.displayName` | `Pipeline.spec.description` |
| `run-end` | `Pipeline.spec.displayName` | — |
| `task-start` | `PipelineTask.displayName` | resolved `TaskSpec.description` |
| `task-end` / `task-skip` / `task-retry` | `PipelineTask.displayName` | — |
| `step-log` | `Step.displayName` | — |

`step-start` and `step-end` event kinds are defined in the API but no
production code emits them today (v1.5). Step `description` is parsed
locally but is not surfaced on any event (carrying it on every
`step-log` line would balloon log output). The Step's `displayName` is
consumed in two places: the `step-log` JSON event (above) and pretty
output's log-line prefix.

In cluster mode, `Step.displayName` and `Step.description` are
**stripped** from the inlined PipelineRun before submission to Tekton
— the upstream Tekton v1 `Step` schema (as of v0.65) has no such
fields, and the admission webhook rejects unknown fields on strict
decode. The fields still surface on tkn-act's JSON event stream and
pretty output the same way they do under docker, since cluster mode
reads them from the input bundle (not the controller verdict) when
emitting events. Pipeline-, PipelineTask-, and TaskSpec-level
`displayName` / `description` round-trip intact (the upstream schema
supports them at those levels).

Empty fields are omitted from the JSON object. Agents should fall
back to the corresponding `pipeline` / `task` / `step` (raw name) field
when `display_name` is empty. Pretty output prefers `displayName` over
`name` everywhere a label appears.

Substitution (`$(params.X)`) inside `displayName` / `description` is
NOT honored — strings are passed through literally.

### JSON event field naming

Existing event fields are camelCase (`runId`, `exitCode`, `durationMs`)
and remain so for backward compatibility — renaming them would break
the public-contract rule. **New event fields added from v1.5 onward
use snake_case** (`display_name`, `description`). This rule is
forward-going and applies to any future multi-word event field added
to the `Event` struct.
