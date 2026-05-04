package docker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/pkg/stdcopy"
)

// pauseImage is the per-Task netns owner. Tiny (~700KB), cached
// forever after first pull, blocks on pause(2) until killed. See
// the design spec §3.1 for provenance and rationale (chosen over
// "first-sidecar-as-netns-owner" so any sidecar can crash without
// taking the netns down).
const pauseImage = "gcr.io/google-containers/pause:3.9"

// sidecarStdout / sidecarStderr are the fine-grained Stream values
// emitted on EvtSidecarLog so consumers can filter sidecar logs
// from step logs. Stable contract — see AGENTS.md.
const (
	sidecarStdout = "sidecar-stdout"
	sidecarStderr = "sidecar-stderr"
)

// pauseStopGrace is the SIGTERM-then-SIGKILL window for the pause
// container. pause(2) responds to SIGTERM immediately so a long
// grace serves no purpose; the longer --sidecar-stop-grace is for
// user sidecars only.
const pauseStopGrace = 1 * time.Second

// sidecarContainerName returns the Docker container name for a
// sidecar of the given task in the given run. Mirrors the step-name
// format with "sidecar-" interposed.
func sidecarContainerName(runID, taskRun, sidecarName string) string {
	return fmt.Sprintf("tkn-act-%s-%s-sidecar-%s", runID, taskRun, sidecarName)
}

// pauseContainerName returns the Docker container name for the
// per-Task pause container that owns the netns. Every sidecar and
// every step in the Task joins it via network_mode: container:<id>.
func pauseContainerName(runID, taskRun string) string {
	return fmt.Sprintf("tkn-act-%s-%s-pause", runID, taskRun)
}

// runningSidecar holds the per-sidecar state the Task lifecycle
// needs across start / liveness-check / teardown. Each entry is the
// post-start view; pre-start failures don't allocate one (they
// surface as start-fail before this slice is populated).
type runningSidecar struct {
	name        string
	containerID string
	// terminated is set once the sidecar's terminal sidecar-end
	// event has been emitted (either at mid-run liveness check or
	// at teardown). Prevents duplicate emission.
	terminated bool
}

// startPause creates and starts the per-Task pause container that
// owns the netns. Returns the container ID. Pulls pauseImage with
// IfNotPresent — the image is tiny and cached forever after first
// pull, so subsequent Tasks pay zero pull cost.
func (b *Backend) startPause(ctx context.Context, inv backend.TaskInvocation) (string, error) {
	if err := b.ensureImage(ctx, pauseImage, "IfNotPresent"); err != nil {
		return "", fmt.Errorf("pull pause image: %w", err)
	}
	cfg := &container.Config{Image: pauseImage}
	hostConf := &container.HostConfig{AutoRemove: false}
	name := pauseContainerName(inv.RunID, inv.TaskRunName)
	created, err := b.cli.ContainerCreate(ctx, cfg, hostConf, nil, nil, name)
	if err != nil {
		return "", fmt.Errorf("create pause %s: %w", name, err)
	}
	if err := b.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		// Best-effort cleanup on start failure.
		_ = b.cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("start pause %s: %w", name, err)
	}
	return created.ID, nil
}

// stopPause stops and removes the pause container with a hard
// pauseStopGrace window. Always uses a fresh background context so
// teardown still runs on a cancelled run context.
func (b *Backend) stopPause(pauseID string) {
	if pauseID == "" {
		return
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), pauseStopGrace+5*time.Second)
	defer cancel()
	grace := int(pauseStopGrace.Seconds())
	_ = b.cli.ContainerStop(stopCtx, pauseID, container.StopOptions{Timeout: &grace})
	_ = b.cli.ContainerRemove(context.Background(), pauseID, container.RemoveOptions{Force: true})
}

// startSidecars creates and starts every sidecar in declaration
// order, each joining the pause container's netns via
// network_mode: container:<pauseID>. After all are running, sleeps
// b.opts.SidecarStartGrace before returning, then verifies each
// sidecar is still running. If any sidecar exited before grace,
// returns an error and the caller treats the Task as infrafailed.
//
// Logs are streamed in the background via streamSidecarLogs; a
// sidecar-start event is emitted as soon as the container reports
// running.
func (b *Backend) startSidecars(ctx context.Context, inv backend.TaskInvocation, pauseID string) ([]runningSidecar, error) {
	out := make([]runningSidecar, 0, len(inv.Task.Sidecars))
	for _, sc := range inv.Task.Sidecars {
		id, err := b.startOneSidecar(ctx, inv, sc, pauseID)
		if err != nil {
			// Surface a terminal sidecar-end with infrafailed for
			// the one that failed to start (no preceding
			// sidecar-start). Already-started sidecars are torn
			// down by the caller via the RunTask defer chain.
			emitSidecarEndWith(inv, sc.Name, 0, backend.TaskInfraFailed, fmt.Sprintf("failed to start: %v", err))
			return out, fmt.Errorf("sidecar %q: %w", sc.Name, err)
		}
		out = append(out, runningSidecar{name: sc.Name, containerID: id})
		emitSidecarStart(inv, sc.Name)
	}

	// Start-grace window. Wait the configured duration, but bail on
	// context cancel so a cancelled run doesn't pin us here.
	grace := b.opts.SidecarStartGrace
	if grace > 0 {
		select {
		case <-time.After(grace):
		case <-ctx.Done():
			return out, ctx.Err()
		}
	}

	// Liveness verification: every sidecar must still be running
	// after the grace period. A sidecar that exited (immediate
	// crash) makes the Task infrafailed.
	for i := range out {
		rs := &out[i]
		insp, err := b.cli.ContainerInspect(ctx, rs.containerID)
		if err != nil {
			emitSidecarEndWith(inv, rs.name, 0, backend.TaskInfraFailed, "failed to inspect")
			rs.terminated = true
			return out, fmt.Errorf("inspect sidecar %q: %w", rs.name, err)
		}
		if insp.State == nil || !insp.State.Running {
			exit := 0
			if insp.State != nil {
				exit = insp.State.ExitCode
			}
			emitSidecarEndWith(inv, rs.name, exit, backend.TaskInfraFailed, fmt.Sprintf("exited before start grace (exit %d)", exit))
			rs.terminated = true
			return out, fmt.Errorf("sidecar %q failed to start (exit %d)", rs.name, exit)
		}
	}
	return out, nil
}

// startOneSidecar prepares and starts a single sidecar container.
// The sidecar joins the pause container's netns and (where
// applicable) the same Task workspaces / volume hosts as Steps.
// Logs are streamed in the background.
func (b *Backend) startOneSidecar(ctx context.Context, inv backend.TaskInvocation, sc tektontypes.Sidecar, pauseID string) (string, error) {
	// Image pre-pull. The engine already adds sidecar images to
	// RunSpec.Images, so this is the IfNotPresent fast-path in the
	// common case; we still call ensureImage so that an inline
	// taskSpec image not in RunSpec.Images still resolves.
	policy := sc.ImagePullPolicy
	if b.opts.PullPolicyOverride != "" {
		policy = b.opts.PullPolicyOverride
	}
	if err := b.ensureImage(ctx, sc.Image, policy); err != nil {
		return "", fmt.Errorf("pull %s: %w", sc.Image, err)
	}

	cmd := sc.Command
	args := sc.Args
	var extraMounts []mount.Mount

	// Script-mode mirrors the Step path: write the body to scriptDir
	// and bind-mount it. Sidecars live for the whole Task so the
	// script file is OK to leave in place; Cleanup removes scriptDir
	// at end of run.
	if sc.Script != "" {
		body := sc.Script
		if !strings.HasPrefix(body, "#!") {
			body = "#!/bin/sh\nset -e\n" + body
		}
		host := filepath.Join(b.scriptDir, fmt.Sprintf("%s-sidecar-%s.sh", inv.TaskRunName, sc.Name))
		if err := os.WriteFile(host, []byte(body), 0o755); err != nil {
			return "", err
		}
		cmd = []string{"/tekton/scripts/script.sh"}
		args = nil
		extraMounts = append(extraMounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   host,
			Target:   "/tekton/scripts/script.sh",
			ReadOnly: true,
		})
	}

	// Workspace mounts — same path layout as Steps so a sidecar
	// can co-operate with the Task on the workspace.
	for tName, wm := range inv.Workspaces {
		extraMounts = append(extraMounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   wm.HostPath,
			Target:   "/workspace/" + tName,
			ReadOnly: wm.ReadOnly,
		})
	}

	// Sidecar volumeMounts from the Task's volumes set.
	for _, vm := range sc.VolumeMounts {
		hostPath, ok := inv.VolumeHosts[vm.Name]
		if !ok {
			return "", fmt.Errorf("volumeMount %q: no host path for volume", vm.Name)
		}
		src := hostPath
		if vm.SubPath != "" {
			src = filepath.Join(hostPath, vm.SubPath)
		}
		extraMounts = append(extraMounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   src,
			Target:   vm.MountPath,
			ReadOnly: vm.ReadOnly,
		})
	}

	env := make([]string, 0, len(sc.Env))
	for _, e := range sc.Env {
		env = append(env, e.Name+"="+e.Value)
	}

	hostConf := &container.HostConfig{
		Mounts:      extraMounts,
		AutoRemove:  false,
		NetworkMode: container.NetworkMode("container:" + pauseID),
	}
	if sc.Resources != nil {
		if sc.Resources.Limits.Memory != "" {
			if v, err := parseMemory(sc.Resources.Limits.Memory); err == nil {
				hostConf.Memory = v
			}
		}
		if sc.Resources.Limits.CPU != "" {
			if v, err := parseCPU(sc.Resources.Limits.CPU); err == nil {
				hostConf.NanoCPUs = v
			}
		}
	}

	cfg := &container.Config{
		Image:      sc.Image,
		Cmd:        append(append([]string{}, cmd...), args...),
		Env:        env,
		WorkingDir: sc.WorkingDir,
	}
	name := sidecarContainerName(inv.RunID, inv.TaskRunName, sc.Name)
	created, err := b.cli.ContainerCreate(ctx, cfg, hostConf, nil, nil, name)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", name, err)
	}
	if err := b.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		_ = b.cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("start %s: %w", name, err)
	}

	// Stream logs in the background. The goroutine ends when Docker
	// closes the log reader (i.e. when the container exits).
	logRC, err := b.cli.ContainerLogs(ctx, created.ID, container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: true, Timestamps: false,
	})
	if err == nil {
		go streamSidecarLogs(logRC, inv.LogSink, inv.TaskName, sc.Name)
	}
	return created.ID, nil
}

// checkSidecarLiveness inspects every still-living sidecar. Any
// sidecar that has exited since the last check gets a terminal
// sidecar-end event and its `terminated` flag flipped. Returns
// whether the pause container is still alive — if it's gone, the
// Task is unsalvageable and RunTask should infrafail.
//
// Sidecar exits NEVER fail the Task on their own (mirrors upstream
// "sidecars are best-effort"). The pause container exit path is
// defense-in-depth; pause(2) only exits on signal, so it should
// never fire in practice.
func (b *Backend) checkSidecarLiveness(ctx context.Context, inv backend.TaskInvocation, sidecars []runningSidecar, pauseID string) (pauseAlive bool, _ []runningSidecar) {
	pauseAlive = true
	if pauseID != "" {
		insp, err := b.cli.ContainerInspect(ctx, pauseID)
		if err != nil || insp.State == nil || !insp.State.Running {
			pauseAlive = false
		}
	}
	for i := range sidecars {
		rs := &sidecars[i]
		if rs.terminated {
			continue
		}
		insp, err := b.cli.ContainerInspect(ctx, rs.containerID)
		if err != nil {
			// Container vanished underneath us — mark terminal,
			// emit with unknown exit. Shouldn't happen unless an
			// outside actor removes it.
			emitSidecarEnd(inv, rs.name, -1, backend.TaskFailed, "container disappeared")
			rs.terminated = true
			continue
		}
		if insp.State == nil || insp.State.Running {
			continue
		}
		exit := insp.State.ExitCode
		status := backend.TaskStatus(backend.TaskFailed)
		msg := "crashed mid-task"
		if exit == 0 {
			status = backend.TaskSucceeded
			msg = ""
		}
		emitSidecarEnd(inv, rs.name, exit, status, msg)
		rs.terminated = true
	}
	return pauseAlive, sidecars
}

// stopSidecars sends SIGTERM with a SidecarStopGrace window to each
// still-living sidecar, drains its logs, emits the terminal
// sidecar-end event, and removes the container. Always runs on a
// background context so a cancelled run still drains.
func (b *Backend) stopSidecars(ctx context.Context, inv backend.TaskInvocation, sidecars []runningSidecar) {
	stopGrace := b.opts.SidecarStopGrace
	if stopGrace <= 0 {
		stopGrace = 30 * time.Second
	}
	for i := range sidecars {
		rs := &sidecars[i]
		if rs.containerID == "" {
			continue
		}
		// Inspect before stopping so we know if the container
		// already exited (clean shutdown mid-task). If it's still
		// running, send SIGTERM and wait stopGrace before SIGKILL.
		insp, err := b.cli.ContainerInspect(context.Background(), rs.containerID)
		exit := 0
		alreadyExited := false
		if err == nil && insp.State != nil {
			if !insp.State.Running {
				alreadyExited = true
				exit = insp.State.ExitCode
			}
		}
		if !alreadyExited {
			stopCtx, cancel := context.WithTimeout(context.Background(), stopGrace+5*time.Second)
			grace := int(stopGrace.Seconds())
			_ = b.cli.ContainerStop(stopCtx, rs.containerID, container.StopOptions{Timeout: &grace})
			cancel()
			// Re-inspect for the final exit code.
			if insp2, err2 := b.cli.ContainerInspect(context.Background(), rs.containerID); err2 == nil && insp2.State != nil {
				exit = insp2.State.ExitCode
			}
		}
		_ = b.cli.ContainerRemove(context.Background(), rs.containerID, container.RemoveOptions{Force: true})
		if !rs.terminated {
			status := backend.TaskStatus(backend.TaskSucceeded)
			msg := ""
			if exit != 0 {
				status = backend.TaskFailed
				msg = "exited non-zero at teardown"
			}
			emitSidecarEnd(inv, rs.name, exit, status, msg)
			rs.terminated = true
		}
	}
}

// emitSidecarStart fires EvtSidecarStart through the LogSink's
// underlying reporter — in practice every production LogSink is
// the *reporter.LogSink, which exposes an Emit-equivalent via its
// SidecarLog hook. We reuse SidecarLog with a sentinel zero-line
// payload to keep the LogSink interface from growing; cluster mode
// goes the long way through reporter.NewLogSink and emits its own
// event. Docker's path is direct: the runReporter shim type below.
func emitSidecarStart(inv backend.TaskInvocation, name string) {
	if r, ok := inv.LogSink.(sidecarEventEmitter); ok {
		r.EmitSidecarStart(inv.TaskName, name)
	}
}

func emitSidecarEnd(inv backend.TaskInvocation, name string, exitCode int, status backend.TaskStatus, msg string) {
	emitSidecarEndWith(inv, name, exitCode, status, msg)
}

func emitSidecarEndWith(inv backend.TaskInvocation, name string, exitCode int, status backend.TaskStatus, msg string) {
	if r, ok := inv.LogSink.(sidecarEventEmitter); ok {
		r.EmitSidecarEnd(inv.TaskName, name, exitCode, string(status), msg)
	}
}

// sidecarEventEmitter is the optional interface a LogSink may
// satisfy to receive sidecar-start / sidecar-end events. Avoids
// growing the LogSink contract for both backends — only the
// production reporter.LogSink implements it; test fakes get
// nothing and the lifecycle still works.
type sidecarEventEmitter interface {
	EmitSidecarStart(taskName, sidecarName string)
	EmitSidecarEnd(taskName, sidecarName string, exitCode int, status, message string)
}

// streamSidecarLogs forwards a sidecar container's log reader
// through the LogSink as EvtSidecarLog events. Mirrors streamLogs
// for steps but uses sidecar-stdout / sidecar-stderr stream values.
func streamSidecarLogs(rc io.ReadCloser, sink backend.LogSink, taskName, sidecarName string) {
	defer func() { _ = rc.Close() }()
	if sink == nil {
		_, _ = io.Copy(io.Discard, rc)
		return
	}
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	go func() {
		_, _ = stdcopy.StdCopy(stdoutW, stderrW, rc)
		_ = stdoutW.Close()
		_ = stderrW.Close()
	}()
	go scanSidecar(stdoutR, sink, taskName, sidecarName, sidecarStdout)
	scanSidecar(stderrR, sink, taskName, sidecarName, sidecarStderr)
}

func scanSidecar(r io.Reader, sink backend.LogSink, taskName, sidecarName, stream string) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	for s.Scan() {
		sink.SidecarLog(taskName, sidecarName, stream, s.Text())
	}
}
