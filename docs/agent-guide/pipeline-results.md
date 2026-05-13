## Pipeline results (`Pipeline.spec.results`)

A Pipeline can declare named results computed from task results once
the run terminates. Tekton's syntax:

```yaml
spec:
  results:
    - name: revision
      value: $(tasks.checkout.results.commit)
    - name: report
      value:
        - $(tasks.test.results.summary)
        - $(tasks.notify.results.id)        # finally tasks count too
    - name: meta
      value:
        owner: $(params.team)               # only $(tasks.X.results.Y) actually resolves; other refs drop the entry
        sha:   $(tasks.checkout.results.commit)
```

Resolution semantics tkn-act follows:

| Aspect | Behavior |
|---|---|
| When | After the entire run completes (tasks + finally), regardless of overall status. |
| Source | The same accumulated task-result map that powers `$(tasks.X.results.Y)` in PipelineTask params. Finally tasks contribute. |
| Failure handling | If a referenced task didn't succeed, or the result name wasn't produced, the pipeline result is **dropped** (omitted from the output). One `error` event per dropped result is emitted on **both** the docker and the cluster backend; the run's status and exit code are NOT changed. |
| Types | string / array / object (mirrors `ParamValue`). JSON-encoded as the matching shape. |
| Cluster mode | tkn-act reads `pr.status.results` from the Tekton controller's verdict — it does not re-resolve locally. Drops surface as `error` events on declared names absent from the verdict. |

Where they show up:

- **JSON (`-o json`)**: a `results` map on the `run-end` event, e.g.
  `{"kind":"run-end","status":"succeeded","results":{"revision":"abc123","report":["abc123","notify-42"]}}`.
- **Pretty output**: one line per resolved result after the run-summary
  line, in stable (alphabetical) key order, values truncated to 80 chars.
- **Library API (`engine.RunResult`)**: a new `Results map[string]any`
  field with values typed as string / `[]string` / `map[string]string`.

Pipeline-result-substitution back into other expressions (e.g.
referencing `$(results.X)` somewhere in the same run) is **not**
supported — Tekton itself doesn't do this; pipeline results are
output-only.
