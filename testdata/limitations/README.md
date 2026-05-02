# Docker-backend limitations — illustrative pipelines

Each directory here is a Tekton Pipeline that **runs differently under
`tkn-act --docker` than it does on a real Tekton controller**, because the
docker backend does not implement the corresponding Tekton feature. The
intent is documentation by example: when triaging "why is my pipeline
behaving oddly," check whether you're hitting one of these.

`--cluster` mode (k3d + the real Tekton controller) supports all of the
features below.

| Fixture                  | Tekton feature it relies on                   | What `tkn-act --docker` does                                               |
|--------------------------|-----------------------------------------------|----------------------------------------------------------------------------|
| `onerror-continue/`      | `Step.onError: continue`                      | Field dropped at parse time; first non-zero exit fails the Task.            |
| `step-state/`            | (none — illustrates step-isolation foot-gun)  | Each step is a fresh container; cwd / env / `/tmp` from prior steps gone.   |
| `step-results/`          | per-Step `results:` and `$(steps.X.results.Y)`| Only Task-level results exist; substitution left as literal text.           |
| `sidecars/`              | `Task.sidecars`                               | Field dropped at parse time; no shared network namespace.                   |
| `retries/`               | `PipelineTask.retries`                        | Field dropped at parse time; the Task fails on its first attempt.           |
| `timeout/`               | `Task.timeout` / `PipelineRun.timeouts`       | Field dropped at parse time; hung steps run to completion.                  |
| `step-volumes/`          | `Task.volumes` + `Step.volumeMounts`          | Fields dropped at parse time; only declared workspaces are mounted.         |

Each `pipeline.yaml` has a header comment explaining the discrepancy, what
real Tekton would do, and what `tkn-act` actually does.

These fixtures are **not** part of the integration suite under
`internal/e2e/` — running them would fail by design. They are loaded by
`internal/loader` parser tests (`testdata/limitations/limitations_test.go`)
to confirm the YAML at least parses cleanly even when the dropped fields
are present, so we don't ship broken examples.
