## Docker backend (`tkn-act run --docker`)

The docker backend is the default for `tkn-act run`. Each Step is
launched as its own container, workspaces are exposed to those
containers, and a tiny pause container owns the netns when the Task
has sidecars. By default this all happens against the local Docker
daemon (`unix:///var/run/docker.sock`), with workspaces and
result/script dirs **bind-mounted** straight off the host filesystem.

For most users that is the whole story. The rest of this section
covers the remote-daemon path that ships in v1.7: targeting a
daemon over `ssh://` or `tcp://`, the per-run volume staging that
makes bind-mounts unnecessary, and the air-gap / restricted-network
escape hatches.

### Targeting a remote daemon

The daemon address resolves with this precedence:

1. `--docker-host=<url>` (the per-invocation flag)
2. `$DOCKER_HOST` (the standard Docker client env)
3. empty → moby SDK default (`unix:///var/run/docker.sock`)

Accepted schemes are the same set the Docker client uses:

| Scheme | Use |
|---|---|
| `unix://<path>` | local daemon at a non-default socket path |
| `tcp://<host>:<port>` | remote daemon exposing the API over TCP (with or without TLS — `DOCKER_TLS_VERIFY` / `DOCKER_CERT_PATH` are honored when set) |
| `ssh://[user@]host[:port]` | remote daemon, reached via an in-tree SSH dialer (no shell-out to `ssh`) |

Use `--docker-host` for a one-off pivot (e.g. `--docker-host=ssh://root@build-vm tkn-act run …`) without mutating process-wide env. Use `$DOCKER_HOST` when every command in a shell should target the same daemon.

For `tcp://` and other non-SSH overrides, `$DOCKER_TLS_VERIFY`, `$DOCKER_CERT_PATH`, `$DOCKER_API_VERSION` are still honored — internally the moby client loads them via `client.FromEnv` first and only the host is then re-pointed at the override. The `ssh://` path does not consult those env vars (authentication is publickey-based and TLS does not apply); the SSH dialer is built directly without `client.FromEnv`.

### SSH transport

`ssh://` URLs are handled by an in-tree dialer (the moby SDK alone does not understand the scheme). Authentication is **publickey only** — password and keyboard-interactive auth are intentionally not attempted, since a TTY-less daemon would hang on them.

Key lookup order:

1. `SSH_AUTH_SOCK` — the ssh-agent, if set and reachable. Every key the agent offers is tried.
2. `~/.ssh/id_ed25519`
3. `~/.ssh/id_rsa`

A `MaxAuthTries` footgun lives here: a forwarded agent with many keys may exhaust the server's per-connection auth budget *before* the fallback file key gets its turn. If `ssh root@host` works from the same shell but `tkn-act` fails with `attempted methods [none publickey], no supported methods remain`, the file key in `~/.ssh/` is authorized but the agent's keys are not. Either unset `SSH_AUTH_SOCK` for the `tkn-act` invocation, or add the file key to the agent with `ssh-add ~/.ssh/id_ed25519`.

Host-key verification reads `~/.ssh/known_hosts` by default. CI / dind cases where the daemon's host key is ephemeral can bypass with:

```sh
TKN_ACT_SSH_INSECURE=1 tkn-act run --docker-host=ssh://… …
```

The remote daemon socket defaults to `/var/run/docker.sock`. Override per-host via `$TKN_ACT_DOCKER_SOCKET` when the daemon listens elsewhere.

### Remote-mode volume staging

When the daemon is remote, bind-mounting a host path doesn't work — the daemon's filesystem can't see it. `tkn-act` detects remote daemons and switches workspaces, script storage, and per-Task result dirs from bind mounts to a **single docker volume per run**, named `tkn-act-<runID>`, with each Task and Step seeing only the subpath it needs.

Lifecycle:

1. `Backend.New` decides `remote` via `--remote-docker` / `$TKN_ACT_REMOTE_DOCKER` / auto-detect (see next section).
2. If remote, `Prepare` creates `tkn-act-<runID>` and a long-lived **stager** container with the whole volume mounted at `/staged` inside that container. The stager copies workspace seeds onto the volume via `CopyToContainer`.
3. Each Task / Step container subpath-mounts the appropriate slice of `<volume>` (`workspaces/<name>/`, `results/<taskRun>/...`, `scripts/...`).
4. After each step, per-step result files are pulled back to the host via `CopyFromContainer` so the engine's existing `$(steps.X.results.Y)` substitution code keeps working.
5. `Cleanup` stops the stager, pulls any workspace dirs back to the host for inspection, then removes the volume.

Subpath mounts require Docker Engine ≥25 on the remote daemon. Older engines ENOENT at the first container create.

### Auto-detect vs explicit pin

`--remote-docker={auto|on|off}` (env: `$TKN_ACT_REMOTE_DOCKER`, with `auto` the default).

`auto` resolves like this:

| daemon address | auto verdict |
|---|---|
| empty / `unix://...` | local |
| any other scheme | probe the daemon's `Info.Name`; if it equals the client's `os.Hostname()`, local — otherwise remote. An **empty `Info.Name`** or an unreachable `os.Hostname()` is treated as **remote**, because misclassifying a remote daemon as local is the destructive outcome (it would silently bind-mount paths the daemon can't see). A **daemon `Info` call error** (the network probe itself failed) is propagated as a startup error rather than silently classified — pin `--remote-docker=on|off` to skip the probe in that case. |

Pin `=on` whenever the auto verdict is unstable or you want to short-circuit the probe — CI workflows running against `docker:dind` are the canonical case, where the daemon hostname is a random container id that auto-detect would classify as remote anyway, but pinning makes the test stay green even if `Info` flakes.

Pin `=off` when you want the bind-mount path *despite* a non-unix `$DOCKER_HOST` — e.g. a local `tcp://` daemon on the same machine where bind-mounting is faster than staging.

The flag is per-invocation; the env is stable across invocations. Precedence: flag > env > `auto`.

The flag and the strict-validation pattern apply: `--remote-docker=onn` is rejected at parse time; `TKN_ACT_REMOTE_DOCKER=yes` (anything outside the three accepted values) is silently treated as `auto` — explicit user intent is strict, inherited env is tolerant.

### Air-gap and restricted-network notes

The docker backend pulls one image of its own — the **pause / stager image**, default `registry.k8s.io/pause:3.9`. Air-gapped daemons that can't reach `registry.k8s.io` point the backend at a mirror:

```sh
TKN_ACT_PAUSE_IMAGE=registry.internal/pause:3.9 tkn-act run …
# or per-invocation:
tkn-act run --pause-image registry.internal/pause:3.9 …
```

The override applies to both the pause container (sidecar netns owner) and the stager container (remote-mode volume staging).

Daemon-side registry mirrors (`/etc/docker/daemon.json` `registry-mirrors`) work normally for the Steps' own images — `tkn-act` doesn't pull Step images itself, the daemon does, so whatever the daemon resolves is what runs.

### Common gotchas

- **Forwarded ssh-agent keys aren't authorized on the target host.** Verbose `ssh -v` shows the agent's keys all offered and rejected before the file key is tried; with `MaxAuthTries` set to a low value the file key never gets a turn. Workaround: `SSH_AUTH_SOCK= tkn-act …` or `ssh-add` the right key.
- **Subpath mount returns ENOENT on the first container create.** Daemon is on an Engine version older than v25. There's no preflight — the error fires at the first `ContainerCreate` call. Upgrade the daemon, or use a non-subpath layout (not currently supported — file an issue with your dockerd version).
- **`docker info` from inside dind hangs the health-check.** GitHub Actions `services.<id>.env: VAR: ''` does NOT pass an empty string — it inherits from the host, which is usually unset. Use `options: -e VAR=` instead for explicit empties. (Bit us in `remote-docker-integration.yml`.)
- **A step result whose name starts with the step name extracts under the wrong filename.** Was a real bug fixed in v1.7's untar logic; if you see this on an older binary, upgrade.
