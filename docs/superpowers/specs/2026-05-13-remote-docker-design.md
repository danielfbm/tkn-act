# Remote docker daemon support for `tkn-act run --docker`

**Date:** 2026-05-13
**Status:** Draft — design-only PR; implementation tracked separately

## Problem

`tkn-act`'s docker backend assumes the docker daemon is **local and
shares the client's filesystem.** Two assumptions are baked into the
implementation today:

1. **Transport** — `internal/backend/docker/docker.go:New` calls
   `client.NewClientWithOpts(client.FromEnv)`, which respects
   `DOCKER_HOST` for `tcp://` and `unix://` but **does not** know how
   to dial `ssh://`. The docker CLI handles `ssh://` by shelling out
   to `ssh` and tunneling the daemon's unix socket; the moby Go SDK
   does not. Setting `DOCKER_HOST=ssh://user@host` today produces
   `dial tcp: lookup user@host: no such host`.
2. **Filesystem** — every container the backend launches uses
   `mount.Mount{Type: Bind, Source: <local /tmp path>, Target: ...}`.
   When the daemon is remote, the bind-source path doesn't exist on
   the daemon's host and the API rejects the container with
   `invalid mount config for type "bind": bind source path does not
   exist`. Even tunneling the daemon's unix socket over SSH
   (`ssh -L /tmp/sock:/var/run/docker.sock host`) hits this — the
   API call succeeds but the daemon can't see our `/tmp`.

The bind-mount call sites are concentrated in two files:
`internal/backend/docker/docker.go` (Step containers, results dir,
step-results dirs, volume sources) and `internal/backend/docker/sidecars.go`
(sidecar containers). Every fixture under `testdata/e2e/` exercises at
least one of these paths, so the failure is universal — no fixture
runs against a remote daemon today.

The user-facing impact is that `tkn-act run --docker` cannot run
against a daemon that is anywhere except the same host. Use cases the
gap blocks: developer laptops with no daemon talking to a beefier
build host, devcontainers / Codespaces / sandboxes where dockerd
can't run locally (newuidmap caps missing, cgroups v1, rootless
restrictions), and CI workers that bring a remote dockerd via
`DOCKER_HOST=tcp://...`.

## Goals

- `tkn-act run --docker` works against a remote daemon reached via
  `DOCKER_HOST=ssh://user@host`, `DOCKER_HOST=tcp://host:port`, or
  a `docker context` set via `--docker-host`. The user picks the
  transport; we follow.
- All 28 e2e fixtures under `testdata/e2e/` pass against a remote
  daemon, producing the **identical** JSON event sequence as a local
  daemon. The cross-backend invariant becomes "every fixture passes
  on local-docker, remote-docker, and cluster."
- A `docker:dind`-driven CI workflow exercises the remote path on
  every PR — no external server, no gated secret, runs same as the
  other integration workflows.
- Remote-daemon detection is automatic by default with an explicit
  override (`--remote-docker={auto,on,off}` + `TKN_ACT_REMOTE_DOCKER`).
  When auto-detection says "remote," the staging strategy switches
  to docker volumes instead of bind mounts; otherwise the existing
  fast bind-mount path is unchanged.
- Public-contract surface is extended additively: new flag, new env
  var, no rename or retype of existing fields. JSON event shape and
  exit codes are unchanged.

## Non-goals

- **Auto-wiring SSH key auth.** The user is responsible for having
  `ssh user@host` work without password prompts (`ssh-agent`,
  `~/.ssh/config`, agent-forwarding, etc.). We don't prompt for keys
  or manage `known_hosts`.
- **Multi-host scheduling.** One run targets one daemon. We don't
  fan tasks across multiple remote daemons.
- **Rootless-podman as a docker substitute.** Out of scope.
  Tracked separately if needed.
- **Removing the bind-mount path on local daemons.** Bind mounts
  are faster than `docker cp` round-trips. Local stays bind-mounted
  by default; the volume path is only taken when remote is detected
  or explicitly forced.
- **Threading `--remote-docker` through `tkn-act cluster ...`.** The
  cluster backend uses k3d-in-a-container; the docker daemon it
  uses is local to the k3d process. Out of scope for this spec.
- **Cross-platform image autoselection.** If the remote daemon is
  `linux/arm64` and the user pulls an `amd64`-only image, the
  daemon errors. We surface the error verbatim; we don't translate
  it. (See Risks.)

## Architecture

### Transport — `ssh://` scheme

Use a small in-process SSH dialer instead of shelling out to `ssh`.
Rationale: the moby SDK accepts `client.WithDialContext(func)`, so
adding `ssh://` support is a ~50-line dialer using
`golang.org/x/crypto/ssh` plus `ssh.Client.Dial("unix", "/var/run/docker.sock")`.
The docker CLI shells out to `ssh` because it predates that SDK
hook; we don't have that constraint. Costs:

- One new dependency: `golang.org/x/crypto/ssh`. Already a transitive
  dep via go-git (resolver-git), so net new direct dep but no new
  module added to the dependency tree.
- SSH key discovery: read `SSH_AUTH_SOCK` first (agent), then
  `~/.ssh/id_ed25519` / `id_rsa` (same precedence as `ssh`).
- `known_hosts`: load `~/.ssh/known_hosts`, fall back to insecure
  host-key callback only when `TKN_ACT_SSH_INSECURE=1`. Match the
  docker CLI's `--ssh-opt strict_host_key_checking=no` posture
  via env var rather than a flag.

The remote daemon socket path is parameterizable via the URL host
suffix: `ssh://user@host?path=/var/run/docker.sock`. Default path
is `/var/run/docker.sock`.

### Transport — `tcp://`

Works today via `client.FromEnv`. No code change beyond the
remote-detection hook that switches staging to volumes.

### Remote detection

The `Backend` constructor probes once after dialing:

```go
info, err := cli.Info(ctx)
local := info.Name == os.Hostname() ||
         strings.HasPrefix(daemonHost, "unix://")
```

`info.Name` is the daemon's reported hostname.  If `DOCKER_HOST`
is `unix://...`, the daemon is local by definition (same kernel).
Otherwise compare hostnames and fall to "remote" on mismatch. The
result is recorded on the `Backend` struct as `b.remote bool`.

Override precedence (highest first):
1. `--remote-docker=on` or `--remote-docker=off`
2. `TKN_ACT_REMOTE_DOCKER=on|off`
3. Auto-detection

`--remote-docker=auto` (default) selects the auto-detected value.

### Staging — per-Task docker volumes

When `b.remote == true`, every place that today builds a
`mount.Mount{Type: Bind, Source: <hostPath>, Target: <target>}`
instead:

1. Creates a per-Task named volume on the daemon if it doesn't
   exist: `tkn-act-<runID>-<taskName>`.
2. Stages bytes into the volume by running a transient "stager"
   container (`alpine:3` mounted with the volume), then
   `docker cp <localPath> <stagerID>:/staged/<key>`. Wait for the
   stager to exit (it just `sleep infinity` until we kill it), then
   stop it.
3. Replaces the original mount with
   `mount.Mount{Type: Volume, Source: <volName>, Target: <target>}`,
   pointing the container at the same path inside the volume.

The stager container is cached per Task: one stager is created
when the first input is staged, reused for subsequent inputs and
for output capture, and torn down at Task end.

For per-Step inputs (the `script.sh`), staging happens before
`docker run`. For results capture (the `/tekton/results` dir and
`/tekton/steps/<step>/results`), staging is reversed at Task end:
`docker cp <stagerID>:/staged/results/. <localResultsHostPath>/`
pulls the bytes back so the engine's existing result-reading code
sees them at the same paths it does today.

Workspaces are similar but live for the whole Task, so the same
volume is reused across Steps + Sidecars within one Task. Across
Tasks, workspaces are independent volumes — workspace fan-out
across a Pipeline is already serialized through the engine's
WorkspaceManager, so per-Task volume isolation is fine.

### Sidecars

Sidecar containers already use `network_mode: container:<pause>`
and join the pause container's netns. That model doesn't depend on
where the daemon is. The only change is the staging swap: bind
mounts become volume mounts pointing at the same per-Task volume.

### Pause container + per-Task netns

Unchanged — `network_mode: container:<id>` is daemon-side state, no
filesystem touching. Pause containers continue to be created
locally w.r.t. the daemon.

### Image availability

Images must be pullable from the daemon's network — not the
client's. The daemon does its own registry pulls. If the user's
client has access to a registry the daemon can't reach (corporate
proxy, etc.), the daemon's pull fails and we surface the error.
We do not implement client-side `docker save`+`docker load`
shipping; that's a separate feature (image-sideloading) and is
out of scope.

## CLI / env surface

Added to public-contract surface in `AGENTS.md`:

| Surface | Addition |
|---|---|
| CLI flag | `--remote-docker={auto,on,off}` on `tkn-act run` (and on any future docker-using subcommand). Default: `auto`. |
| CLI flag | `--docker-host <URL>` on `tkn-act run`. Overrides `DOCKER_HOST`. |
| Env var | `TKN_ACT_REMOTE_DOCKER=on\|off` — overrides auto-detection. |
| Env var | `TKN_ACT_SSH_INSECURE=1` — skips `known_hosts` verification when using `ssh://`. |

No new event kinds. No new exit codes. No renames of existing
fields.

## Doc convergence

The implementation PR must update:

- `docs/agent-guide/docker-backend.md` — new section "Remote docker
  daemons" describing the auto-detect behavior, the override flag,
  and the volume-staging trade-off (slower than bind mounts).
- `docs/agent-guide/README.md` — bullet under "Operational flags"
  for `--remote-docker` and `--docker-host`.
- `README.md` — one-line example showing
  `DOCKER_HOST=ssh://user@host tkn-act run -f pipeline.yaml`.
- `docs/feature-parity.md` — header note that the docker backend
  supports remote daemons; bump `Last updated:`.
- `docs/test-coverage.md` — workflow table gains a
  `remote-docker-integration` row.
- `AGENTS.md` — new rows in the public-contract table (`--remote-docker`,
  `--docker-host`, `TKN_ACT_REMOTE_DOCKER`, `TKN_ACT_SSH_INSECURE`).
- Regenerate via `go generate ./cmd/tkn-act/` (agent-guide freshness).

## Migration

This is purely additive. Existing users see no behavior change:

- Local daemon (`unix://...` or auto-detected local TCP): bind-mount
  path unchanged. No perf regression.
- Users who today fail with `DOCKER_HOST=tcp://...` (today they
  fail at first bind-mount) start succeeding via the volume path.
- Users who today fail with `DOCKER_HOST=ssh://...` (today they
  fail at dial) start succeeding via the SSH dialer + volume path.

No flag-flip, no opt-in needed for the common case.

## Risks

| Risk | Mitigation |
|---|---|
| `docker cp` round-trips slow Task setup vs. bind mounts. | Stager container is per-Task, reused for all Steps. Most fixtures stage ≤2 KB of scripts, so the overhead is one network round-trip per Task, not per Step. Bench on `hello` + `params-and-results` in P5a before claiming parity. |
| Per-Task volume leak if `tkn-act` is killed mid-run (`SIGKILL`). | Run-scoped cleanup hook (`Backend.Cleanup`) does `docker volume ls --filter label=tkn-act.run=<runID>` and removes leftovers. Volumes carry a `tkn-act.run` label at create time. |
| SSH key UX — silent hang on password prompt. | The in-process dialer disables password auth; only `publickey` (agent + key files) is tried. If no key, fail fast with a clear error referencing `ssh-add`. |
| `known_hosts` lockout breaking CI. | `TKN_ACT_SSH_INSECURE=1` exists for CI; the `remote-docker-integration` workflow sets it (the dind container's host key isn't stable across runs). |
| Remote daemon arch mismatch (client amd64, daemon arm64, image amd64-only). | The daemon errors at pull time. We surface the daemon's error message verbatim. Out of scope to translate. |
| Volume-mount semantics differ from bind: container `/workspace/foo` on a Volume is owned by image's UID rather than the host's mounting user. | Existing bind-mount path also has this issue when running rootful — the docker backend already chowns staged files inside the container if needed. Re-verify in the `volumes`, `workspaces`, and `configmap-from-yaml` fixtures during P5a. |
| `ssh://` URL format: the moby CLI accepts `ssh://user@host:port`, we'd also need `?path=` for non-default socket. | Mirror the docker CLI's syntax: `ssh://[user@]host[:port]`. Custom socket path via `TKN_ACT_DOCKER_SOCKET=/var/run/docker.sock` env var (rare-use). Documented in `docker-backend.md`. |

## What this spec doesn't decide

- **Image-sideloading for air-gapped remote daemons.** If the
  daemon can't reach the registries the user has, the workaround
  today is `docker save | ssh host docker load`. Building that into
  `tkn-act` is a separate feature.
- **`docker context` integration.** We respect `DOCKER_HOST` and
  the new `--docker-host` flag. We don't parse `~/.docker/contexts/`
  or honor `docker context use`. The docker CLI translates contexts
  to env vars on every invocation; users can do the same via
  `eval $(docker context inspect <ctx> --format '...')`.
- **Concurrent runs against the same daemon sharing a volume.**
  Volume names include `<runID>`, so two parallel `tkn-act` runs
  get distinct volumes. We don't deduplicate even if the inputs
  are identical.
- **Stager image choice.** P3 implementation can decide whether to
  use `alpine:3`, `busybox`, or a tiny purpose-built image. The
  tradeoff is pull cost vs. attack surface; alpine is the default
  unless P3 measurements show pull-time dominates.

## Approval gate

This is a design-only PR. The implementation lands in a series of
follow-up PRs following the plan at
[`docs/superpowers/plans/2026-05-13-remote-docker.md`](../plans/2026-05-13-remote-docker.md).
Phase 1 (SSH dialer) is the smallest reviewable unit and ships
first; phases 2-6 build on it.
