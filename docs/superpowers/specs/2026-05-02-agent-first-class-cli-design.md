# tkn-act: First-class AI-agent CLI support

Status: design
Date: 2026-05-02
Author: assisted by Claude (autonomous task)

## Goal

Make `tkn-act` something an AI agent can pick up, with no prior knowledge of
this repo, and use successfully on the first try. The agent should be able to:

1. Discover what the CLI can do (commands, flags, examples) without scraping
   human help text.
2. Verify the local environment is healthy before running a pipeline.
3. Parse every command's output mechanically (JSON).
4. Distinguish error categories from the exit code without reading stderr.
5. Find a single canonical document explaining the conventions above.

## Non-goals

- Building an MCP server. (Possible follow-up; out of scope here.)
- Adding new pipeline-execution capabilities. This is purely about the CLI's
  agent ergonomics.
- Changing the existing human-facing pretty output format.

## Design

Eight changes, all additive:

### 1. `AGENTS.md` at the repo root

A single canonical document for AI agents. Sections:

- One-paragraph overview of what `tkn-act` is.
- Quick start: discover, validate, run.
- Machine-readable interfaces: `--output json`, `tkn-act help-json`, `tkn-act
  doctor --output json`.
- Stable exit-code table.
- Environment variables (`XDG_CACHE_HOME`, `DOCKER_HOST`, `KUBECONFIG`).
- Common workflows with copy-pasteable invocations.
- Failure modes and what to do about each.

The same content is embedded into the binary via `go:embed` and printed by
`tkn-act agent-guide`, so agents that don't have repo access can still read it.

### 2. `tkn-act doctor` command

Preflight diagnostic. Checks, in order:

- OS / architecture
- `tkn-act` version
- Cache dir path and writability
- Docker daemon: reachable, server version
- `k3d` on PATH (optional, only required for `--cluster`)
- `kubectl` on PATH (optional, only required for `--cluster`)
- Cluster status (if applicable)

Honors the global `--output json`. The JSON shape is:

```json
{
  "ok": true,
  "version": "dev",
  "os": "linux",
  "arch": "amd64",
  "cache_dir": "/home/u/.cache/tkn-act",
  "checks": [
    {"name": "docker", "ok": true, "detail": "Server v25.0.3"},
    {"name": "k3d",    "ok": false, "detail": "not found on PATH",
     "required_for": "cluster"}
  ]
}
```

Exit code 0 if all *required* checks pass (Docker is required by default);
exit code 3 (environment) otherwise.

### 3. `tkn-act help-json` command

Walk the cobra command tree and emit a structured document of every command,
its flags (long, short, default, type, description), examples, and short/long
help text. Lets an agent build a correct invocation without parsing
`--help` output.

Shape (abbreviated):

```json
{
  "name": "tkn-act",
  "version": "dev",
  "commands": [
    {
      "path": "tkn-act run",
      "short": "Run a pipeline on the local backend",
      "examples": ["tkn-act run -f pipeline.yaml -p revision=main"],
      "flags": [
        {"name": "file", "short": "f", "type": "stringSlice",
         "default": "", "usage": "Tekton YAML file(s)", "persistent": false}
      ]
    }
  ],
  "global_flags": [...],
  "exit_codes": [{"code": 0, "name": "success", ...}]
}
```

### 4. `Example` block on every command

Cobra's `Example` field is rendered in `--help`. Add concrete invocations to
`run`, `list`, `validate`, `version`, `cluster {up,down,status}`, and the new
`doctor`/`agent-guide`/`help-json` commands. Examples are also surfaced via
`help-json` (see §3).

### 5. JSON output for every command

`--output json` already affects `run`. Extend it to:

- `list`: `{"pipelines": [...], "tasks": [...]}`
- `validate`: `{"ok": true|false, "pipeline": "...", "errors": [...]}`
- `version`: `{"name": "tkn-act", "version": "dev"}`
- `doctor`: see §2
- `cluster status`: `{"name":"...","exists":true,"running":true,"detail":""}`

### 6. Stable, documented exit codes

```
0  success
1  generic / unexpected error
2  usage error (bad flags, missing file, contradictory inputs)
3  environment error (no Docker, no k3d, kubeconfig missing, etc.)
4  validation error (Tekton YAML rejected before run)
5  pipeline failure (a Task or finally task failed)
130 cancelled (SIGINT)
```

Implementation: a typed `cliError{code int, err error}` flowing through `RunE`.
`main` extracts the code; defaults to 1 if a plain error escapes. Nothing else
changes about how errors are reported to humans.

### 7. `tkn-act agent-guide` command

Prints the embedded `AGENTS.md` to stdout. So an agent that can run the binary
can always read the guide, even with no repo on disk.

### 8. README "For AI agents" section

Three lines pointing at AGENTS.md, `tkn-act doctor`, and `tkn-act help-json`.

## Interfaces

### Exit-code helper

```go
// internal package, used by every cmd
package exitcode

const (
    OK        = 0
    Generic   = 1
    Usage     = 2
    Env       = 3
    Validate  = 4
    Pipeline  = 5
    Cancelled = 130
)

type Error struct {
    Code int
    Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }
func Wrap(code int, err error) error { ... }
func From(err error) int { ... } // 0 on nil, code if *Error, else Generic
```

`main()` switches from `os.Exit(1)` to `os.Exit(exitcode.From(err))`. The
existing `os.Exit(1)` after a failed `RunPipeline` becomes
`exitcode.Wrap(exitcode.Pipeline, ...)`.

### help-json walker

A small function in `cmd/tkn-act/helpjson.go` that walks `cobra.Command`
recursively, collecting flag metadata via `pflag.Flag`. Emitted via
`encoding/json`. No external deps.

### doctor

`cmd/tkn-act/doctor.go`. Each check is a small function returning `(ok bool,
detail string, err error)`. Pretty output is a colored table; JSON is the
struct.

## Testing

- Unit tests for `exitcode` (round-trip wrap/unwrap/from).
- Unit test for `help-json` shape: parse the JSON output and assert presence
  of `tkn-act run`, `tkn-act doctor`, the `--output` global flag, and at least
  one `Example`.
- Unit test for `doctor` JSON output by injecting a check stub.
- Unit test for `agent-guide` output: contains "Exit codes" and "JSON".
- Unit tests for `list`/`validate`/`version` JSON output (use a temp dir
  with a fixture).
- Existing tests must keep passing.

## Self-review

- Placeholders: none — every section has concrete content.
- Internal consistency: exit-code list (§6) matches what's emitted by
  `help-json` (§3) and `AGENTS.md` (§1). doctor (§2) returns code 3 from §6.
- Scope: one PR. No backend changes, no schema changes.
- Ambiguity: `cluster status` JSON is a new shape; documented above. `validate`
  errors are returned as `[]string` for now (matches the existing `[]error`
  printed format).
