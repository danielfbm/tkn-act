# `pipelines/` — local-dev CI for tkn-act, run on tkn-act

Two real-use Tekton pipelines that run tkn-act's own vet/test/build/lint
flow, executed by tkn-act on the docker backend. They are not wired into
GitHub Actions; they exist for local-dev convenience and as a worked example
of using tkn-act on a non-toy pipeline (real params, real results,
parallel DAG).

| File | Pipeline | Source input |
|---|---|---|
| `tasks.yaml` | (Tasks only) | shared `go-vet`, `go-test`, `go-build`, `golangci-lint` |
| `ci-workspace.yaml` | `ci-workspace` | bind-mount the working tree via `-w source=$(pwd)` |
| `ci-git.yaml` | `ci-git` | clone from a git URL (params: `repo-url`, `revision`) |

## Quick start (workspace mode)

```sh
make build                       # rebuild bin/tkn-act on the host
./bin/tkn-act run \
  -f pipelines/tasks.yaml -f pipelines/ci-workspace.yaml \
  -w "source=$(pwd)" \
  --param packages=./internal/exitcode/...
```

The four tasks (`go-vet`, `go-test`, `go-build`, `golangci-lint`) run in
parallel on `golang:1.25-alpine`. The pipeline emits four results on the
final `run-end` event:

| Result | Source | Example |
|---|---|---|
| `version` | `git describe --tags --always --dirty` inside the build container | `"2fc56a7-dirty"` |
| `binary-sha256` | `sha256sum bin/tkn-act-pipeline` | `"7c0cc677e82b8d5d…"` |
| `test-count` | `grep -c '^=== RUN' /tmp/test.log` | `"8"` |
| `lint-exit` | golangci-lint's exit code (0 = clean) | `"0"` |

Pass `-o json` to capture the full event stream, including
`task-start` / `step-log` / `task-end` / `run-end` per AGENTS.md.

## Quick start (git-clone mode)

```sh
WS=$(mktemp -d)
./bin/tkn-act run \
  -f pipelines/tasks.yaml -f pipelines/ci-git.yaml \
  -w "source=$WS" \
  --param "repo-url=file://$(pwd)" \
  --param revision=HEAD \
  --param packages=./internal/exitcode/...
```

Adds a `clone` Task (alpine + git) that runs first; the four checks
`runAfter: [clone]`. An additional pipeline result, `revision-resolved`,
exposes the SHA the clone task checked out.

## Pipeline params

`ci-workspace` and `ci-git` share these:

| Param | Default | Notes |
|---|---|---|
| `packages` | `./...` | Forwarded to `go vet`, `go test`, `golangci-lint`. Use a tight subset (e.g. `./internal/exitcode/...`) for fast iteration. |
| `goproxy` | `https://proxy.golang.org,direct` | Override for restricted networks: e.g. `--param goproxy=https://goproxy.cn,https://goproxy.io` (no `direct` fallback to skip github.com lookups). |

`ci-git` adds:

| Param | Default | Notes |
|---|---|---|
| `repo-url` | (required) | `file://`, `https://`, `ssh://`, or `git@host:path`. |
| `revision` | `main` | Branch, tag, or SHA. |

`go-build` Task accepts:

| Param | Default | Notes |
|---|---|---|
| `package` | `./cmd/tkn-act` | Go package path to build. |
| `output-path` | `bin/tkn-act-pipeline` | Output path inside workspace. Default deliberately differs from `bin/tkn-act` so a workspace-bind-mount run does not clobber the host-built binary. |

`golangci-lint` Task accepts:

| Param | Default | Notes |
|---|---|---|
| `golangci-lint-version` | `v1.64.8` | `go install`-compatible version selector. |

## Notes / gotchas

- **Workspace path must be absolute.** Docker rejects relative bind-mount
  paths, so use `-w source="$(pwd)"`, not `-w source=.`.
- **GOPROXY chains and `direct`.** When the network blocks github.com
  (which Go's `direct` mode falls back to for some VCS lookups), use
  a no-`direct` chain like `https://goproxy.cn,https://goproxy.io`.
- **golangci-lint runs with CGO disabled** (`CGO_ENABLED=0`) inside
  alpine — pure Go, no `gcc` / `ld` requirement, much faster install.
- **Soft-fail lint.** The lint task captures the lint exit code as a
  result, scripts always exit 0, and a failed `go install` is
  surfaced as `lint-exit: "install-failed"` rather than failing the
  pipeline. Pipeline status is independent of lint findings.
- **Pre-pull and parameterized images.** Step images that still contain
  `$(...)` (e.g. `image: golang:$(params.go-version)-alpine` driven by
  a matrix axis) are skipped at run-start pre-pull and pulled
  on-demand by the per-step `ensureImage(IfNotPresent)` path at
  task-dispatch time. The first task using a parameterized image
  therefore pays the pull cost mid-run rather than at startup; literal
  image strings continue to be pre-pulled in parallel up front.
