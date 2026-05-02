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
| `step-state/`            | (none — illustrates step-isolation foot-gun)  | Each step is a fresh container; cwd / env / `/tmp` from prior steps gone.   |
| `sidecars/`              | `Task.sidecars`                               | Field dropped at parse time; no shared network namespace.                   |
| `step-volumes/`          | `Task.volumes` + `Step.volumeMounts`          | (To be addressed in phase 3 of the v1.2 fidelity work.)                     |

This list is short by design — features documented here either are
**fundamentally Kubernetes-shaped** (sidecars need a shared network
namespace) or **by-design behaviors of any container-per-step model**
(step-state isolation). Earlier entries that have been graduated to
`testdata/e2e/` are now supported in the docker backend; see
`docs/superpowers/specs/2026-05-02-tkn-act-docker-fidelity-design.md`.

Each `pipeline.yaml` has a header comment explaining the discrepancy, what
real Tekton would do, and what `tkn-act` actually does.

These fixtures are **not** part of the integration suite under
`internal/e2e/` — running them would fail by design. They are loaded by
`internal/loader/limitations_test.go` to confirm the YAML at least parses
cleanly even when the dropped fields are present, so we don't ship broken
examples.
