// Package docker implements backend.Backend using a local Docker daemon.
// Each Step runs as one container; per-Task results dir is bind-mounted as
// /tekton/results into every Step container of the Task.
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
	"github.com/danielfbm/tkn-act/internal/resolver"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

type Options struct {
	// PullPolicy overrides per-step ImagePullPolicy when non-empty.
	PullPolicyOverride string
	// SidecarStartGrace is how long to wait after starting all
	// sidecars before launching the first step. Substitutes for
	// upstream Tekton's readinessProbe-driven start gate. Default
	// 2s; New() applies the default when zero.
	SidecarStartGrace time.Duration
	// SidecarStopGrace is the SIGTERM-then-SIGKILL window when
	// stopping sidecars at end of Task. Default 30s; matches
	// upstream Tekton's terminationGracePeriodSeconds.
	SidecarStopGrace time.Duration
	// Remote selects the daemon-location detection strategy:
	//
	//   ""     same as "auto" (default; compares daemon Info.Name to client hostname)
	//   "auto" auto-detect
	//   "on"   force remote (use volume-staging path, Phase 3)
	//   "off"  force local (use bind-mount path)
	//
	// Phase 2 wires the detection only — Backend.remote is set but
	// not yet consumed by the Step/Sidecar code paths.
	Remote string
}

type Backend struct {
	cli       *client.Client
	opts      Options
	scriptDir string
	// remote reports whether the daemon's filesystem is independent
	// of the client's (so bind mounts of local paths won't work).
	// Set during New(). Phase 3 will switch staging accordingly.
	remote bool
}

func New(opts Options) (*Backend, error) {
	dockerHost := os.Getenv("DOCKER_HOST")
	cli, err := newDockerClient(dockerHost)
	if err != nil {
		return nil, err
	}
	if _, err := cli.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("docker daemon not reachable: %w", err)
	}
	if opts.SidecarStartGrace == 0 {
		opts.SidecarStartGrace = 2 * time.Second
	}
	if opts.SidecarStopGrace == 0 {
		opts.SidecarStopGrace = 30 * time.Second
	}
	remote, err := decideRemote(opts.Remote, dockerHost, cli)
	if err != nil {
		return nil, err
	}
	return &Backend{cli: cli, opts: opts, remote: remote}, nil
}

// decideRemote resolves the Options.Remote setting plus DOCKER_HOST
// and (for "auto") a one-shot cli.Info() probe into a single bool.
// Extracted so the policy is testable without a real daemon.
func decideRemote(mode, dockerHost string, cli *client.Client) (bool, error) {
	switch mode {
	case "on":
		return true, nil
	case "off":
		return false, nil
	case "", "auto":
		// fall through
	default:
		return false, fmt.Errorf("docker backend: invalid Remote=%q (want auto|on|off)", mode)
	}
	// auto: unix:// (or empty DOCKER_HOST defaulting to unix://) is
	// local by definition. Otherwise compare the daemon's hostname
	// to the client's; if Info fails for any reason, fall back to
	// "remote" so the safer staging path is taken in Phase 3.
	if dockerHost == "" || strings.HasPrefix(dockerHost, "unix://") {
		return false, nil
	}
	info, err := cli.Info(context.Background())
	if err != nil {
		return true, nil //nolint:nilerr // best-effort detection; Phase 3 staging is the safety net
	}
	local, err := os.Hostname()
	if err != nil || local == "" {
		return true, nil
	}
	return info.Name != "" && info.Name != local, nil
}

// IsRemote reports the result of remote-daemon detection. Exposed for
// Phase 3 (volume staging) and for tests.
func (b *Backend) IsRemote() bool { return b.remote }

// newDockerClient builds a moby client honoring DOCKER_HOST including
// the ssh:// scheme — which client.FromEnv alone does not understand.
// For unix:// and tcp:// the moby SDK's normal env handling is used.
func newDockerClient(dockerHost string) (*client.Client, error) {
	if strings.HasPrefix(dockerHost, "ssh://") {
		dialer, err := newSSHDialer(dockerHost)
		if err != nil {
			return nil, fmt.Errorf("ssh transport: %w", err)
		}
		cli, err := client.NewClientWithOpts(
			client.WithAPIVersionNegotiation(),
			client.WithHost("unix://"+remoteSocketPath()),
			client.WithDialContext(dialer),
		)
		if err != nil {
			return nil, fmt.Errorf("docker client (ssh): %w", err)
		}
		return cli, nil
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return cli, nil
}

func (b *Backend) Prepare(ctx context.Context, run backend.RunSpec) error {
	d, err := os.MkdirTemp("", "tkn-act-scripts-"+run.RunID+"-")
	if err != nil {
		return err
	}
	b.scriptDir = d
	for _, img := range run.Images {
		if err := b.ensureImage(ctx, img, "IfNotPresent"); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) Cleanup(ctx context.Context) error {
	if b.scriptDir != "" {
		_ = os.RemoveAll(b.scriptDir)
	}
	return nil
}

func (b *Backend) RunTask(ctx context.Context, inv backend.TaskInvocation) (backend.TaskResult, error) {
	res := backend.TaskResult{Started: time.Now(), Status: backend.TaskSucceeded, Results: map[string]string{}}

	// Per-Task sidecar lifecycle. When a Task declares one or more
	// sidecars, tkn-act starts a tiny pause container that owns the
	// netns, then starts every sidecar joining that netns, then
	// runs every Step joining the same netns. Steps reach sidecars
	// at localhost:<port> exactly as in a Tekton pod. See spec §3.1.
	var pauseID string
	var sidecars []runningSidecar
	if len(inv.Task.Sidecars) > 0 {
		var err error
		pauseID, err = b.startPause(ctx, inv)
		if err != nil {
			res.Status = backend.TaskInfraFailed
			res.Err = fmt.Errorf("pause container: %w", err)
			res.Ended = time.Now()
			return res, nil
		}
		// Defer teardown in reverse order: sidecars first, then
		// pause. Both run on background contexts so a cancelled
		// run still drains them.
		defer b.stopPause(pauseID)
		// Capture sidecars by reference so the deferred stop sees
		// the populated slice.
		defer func() { b.stopSidecars(context.Background(), inv, sidecars) }()
		sidecars, err = b.startSidecars(ctx, inv, pauseID)
		if err != nil {
			// Surface a terminal sidecar-end with infrafailed for
			// any sidecar we never got past start (the slice
			// returned by startSidecars only includes ones that
			// did start; the failed name is in the error). The
			// last segment of the error contains the sidecar name.
			res.Status = backend.TaskInfraFailed
			res.Err = err
			res.Ended = time.Now()
			// Best-effort: emit start-fail event for the failed sidecar
			// (the one not in the returned slice, which is identified
			// implicitly by the error). We can't easily extract the
			// name here without more plumbing; the per-sidecar event
			// stream will surface the running ones via teardown, and
			// an EvtError message captures the failed one.
			return res, nil
		}
	}

	// stepResults accumulates as each step finishes. Earlier steps are
	// substituted into later steps' refs ($(steps.<step>.results.<name>)).
	stepResults := map[string]map[string]string{}

	for _, rawStep := range inv.Task.Steps {
		// Per-step substitution pass: resolves the placeholders the engine's
		// task-level pass intentionally left intact. The Context is minimal
		// — only step refs need resolving here.
		step, err := substituteStepRefs(rawStep, stepResults)
		if err != nil {
			res.Status = backend.TaskInfraFailed
			res.Err = fmt.Errorf("step %q: %w", rawStep.Name, err)
			res.Ended = time.Now()
			return res, nil
		}

		stepRes := backend.StepResult{Name: step.Name, Started: time.Now()}

		policy := step.ImagePullPolicy
		if b.opts.PullPolicyOverride != "" {
			policy = b.opts.PullPolicyOverride
		}
		if err := b.ensureImage(ctx, step.Image, policy); err != nil {
			res.Status = backend.TaskInfraFailed
			res.Err = fmt.Errorf("step %q: %w", step.Name, err)
			res.Ended = time.Now()
			res.Steps = append(res.Steps, stepRes)
			return res, nil
		}

		// Ensure this step's per-step results dir exists on the host before
		// the container starts; later steps see it read-only.
		stepResultsHost := filepath.Join(inv.ResultsHost, "steps", step.Name)
		if err := os.MkdirAll(stepResultsHost, 0o755); err != nil {
			res.Status = backend.TaskInfraFailed
			res.Err = fmt.Errorf("step %q: mkdir step results: %w", step.Name, err)
			res.Ended = time.Now()
			return res, nil
		}

		exitCode, err := b.runStep(ctx, inv, step, pauseID)
		stepRes.ExitCode = exitCode
		stepRes.Ended = time.Now()
		if err != nil {
			res.Status = backend.TaskInfraFailed
			res.Err = err
			stepRes.Status = backend.StepFailed
			res.Steps = append(res.Steps, stepRes)
			res.Ended = time.Now()
			return res, nil
		}

		// Read this step's declared per-step results so later steps in the
		// same Task can substitute them.
		if len(step.Results) > 0 {
			rs := map[string]string{}
			for _, decl := range step.Results {
				p := filepath.Join(stepResultsHost, decl.Name)
				if data, err := os.ReadFile(p); err == nil {
					rs[decl.Name] = strings.TrimRight(string(data), "\n")
				}
			}
			stepResults[step.Name] = rs
		}

		if exitCode != 0 {
			stepRes.Status = backend.StepFailed
			res.Steps = append(res.Steps, stepRes)
			if step.OnError == "continue" {
				continue
			}
			res.Status = backend.TaskFailed
			res.Ended = time.Now()
			return res, nil
		}
		stepRes.Status = backend.StepSucceeded
		res.Steps = append(res.Steps, stepRes)

		// Inter-step sidecar liveness check. A sidecar that has
		// crashed since the last step gets a terminal sidecar-end
		// event but does NOT fail the Task — matches upstream
		// "sidecars are best-effort". Only a pause-container exit
		// (defense-in-depth, should never fire) infrafails.
		if len(sidecars) > 0 || pauseID != "" {
			pauseAlive, _ := b.checkSidecarLiveness(ctx, inv, sidecars, pauseID)
			if !pauseAlive {
				res.Status = backend.TaskInfraFailed
				res.Err = fmt.Errorf("netns owner (pause container) exited unexpectedly")
				res.Ended = time.Now()
				return res, nil
			}
		}
	}

	// Read Task-level result files (existing behavior).
	for _, decl := range inv.Task.Results {
		p := filepath.Join(inv.ResultsHost, decl.Name)
		if data, err := os.ReadFile(p); err == nil {
			res.Results[decl.Name] = strings.TrimRight(string(data), "\n")
		}
	}

	res.Ended = time.Now()
	return res, nil
}

// substituteStepRefs runs a per-step substitution pass on a Step, resolving
// any remaining $(step.results.X.path) and $(steps.<step>.results.<name>)
// placeholders. All other refs were already resolved by the engine.
func substituteStepRefs(st tektontypes.Step, stepResults map[string]map[string]string) (tektontypes.Step, error) {
	rctx := resolver.Context{StepResults: stepResults, CurrentStep: st.Name}
	out := st
	var err error
	if out.Image, err = resolver.Substitute(st.Image, rctx); err != nil {
		return st, err
	}
	if out.Script, err = resolver.Substitute(st.Script, rctx); err != nil {
		return st, err
	}
	if out.WorkingDir, err = resolver.Substitute(st.WorkingDir, rctx); err != nil {
		return st, err
	}
	if len(st.Command) > 0 {
		if out.Command, err = resolver.SubstituteArgs(st.Command, rctx); err != nil {
			return st, err
		}
	}
	if len(st.Args) > 0 {
		if out.Args, err = resolver.SubstituteArgs(st.Args, rctx); err != nil {
			return st, err
		}
	}
	if len(st.Env) > 0 {
		out.Env = make([]tektontypes.EnvVar, len(st.Env))
		for i, e := range st.Env {
			v, eErr := resolver.Substitute(e.Value, rctx)
			if eErr != nil {
				return st, eErr
			}
			out.Env[i] = tektontypes.EnvVar{Name: e.Name, Value: v}
		}
	}
	return out, nil
}

func (b *Backend) ensureImage(ctx context.Context, img, policy string) error {
	if policy == "" {
		policy = "IfNotPresent"
	}
	if policy == "Never" {
		return nil
	}
	if policy == "IfNotPresent" {
		_, _, err := b.cli.ImageInspectWithRaw(ctx, img)
		if err == nil {
			return nil
		}
	}
	rc, err := b.cli.ImagePull(ctx, img, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", img, err)
	}
	defer func() { _ = rc.Close() }()
	_, _ = io.Copy(io.Discard, rc) // drain to ensure pull completes
	return nil
}

func (b *Backend) runStep(ctx context.Context, inv backend.TaskInvocation, step tektontypes.Step, pauseID string) (int, error) {
	cmd := step.Command
	args := step.Args

	// Script support: write to scriptDir, mount, exec.
	var extraMounts []mount.Mount
	if step.Script != "" {
		body := step.Script
		if !strings.HasPrefix(body, "#!") {
			body = "#!/bin/sh\nset -e\n" + body
		}
		host := filepath.Join(b.scriptDir, fmt.Sprintf("%s-%s.sh", inv.TaskRunName, step.Name))
		if err := os.WriteFile(host, []byte(body), 0o755); err != nil {
			return 0, err
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

	// Workspace mounts.
	for tName, wm := range inv.Workspaces {
		extraMounts = append(extraMounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   wm.HostPath,
			Target:   "/workspace/" + tName,
			ReadOnly: wm.ReadOnly,
		})
	}

	// Task-level results mount.
	extraMounts = append(extraMounts, mount.Mount{
		Type:   mount.TypeBind,
		Source: inv.ResultsHost,
		Target: "/tekton/results",
	})

	// Per-step results: this step's own dir RW, every earlier step's RO.
	stepsRoot := filepath.Join(inv.ResultsHost, "steps")
	if entries, err := os.ReadDir(stepsRoot); err == nil {
		for _, ent := range entries {
			if !ent.IsDir() {
				continue
			}
			ro := ent.Name() != step.Name
			extraMounts = append(extraMounts, mount.Mount{
				Type:     mount.TypeBind,
				Source:   filepath.Join(stepsRoot, ent.Name()),
				Target:   "/tekton/steps/" + ent.Name() + "/results",
				ReadOnly: ro,
			})
		}
	}

	// Step volumeMounts. Each one references a Task-level volume that the
	// engine has already materialised onto a host path.
	for _, vm := range step.VolumeMounts {
		hostPath, ok := inv.VolumeHosts[vm.Name]
		if !ok {
			return 0, fmt.Errorf("step %q volumeMount %q: no host path for volume", step.Name, vm.Name)
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

	env := make([]string, 0, len(step.Env))
	for _, e := range step.Env {
		env = append(env, e.Name+"="+e.Value)
	}

	hostConf := &container.HostConfig{Mounts: extraMounts, AutoRemove: false}
	// When a Task has sidecars, every Step joins the per-Task pause
	// container's netns so localhost:<port> reaches a sidecar.
	if pauseID != "" {
		hostConf.NetworkMode = container.NetworkMode("container:" + pauseID)
	}
	if step.Resources != nil {
		if step.Resources.Limits.Memory != "" {
			if v, err := parseMemory(step.Resources.Limits.Memory); err == nil {
				hostConf.Memory = v
			}
		}
		if step.Resources.Limits.CPU != "" {
			if v, err := parseCPU(step.Resources.Limits.CPU); err == nil {
				hostConf.NanoCPUs = v
			}
		}
	}

	cfg := &container.Config{
		Image:      step.Image,
		Cmd:        append(append([]string{}, cmd...), args...),
		Env:        env,
		WorkingDir: step.WorkingDir,
	}
	containerName := fmt.Sprintf("tkn-act-%s-%s-%s", inv.RunID, inv.TaskRunName, step.Name)

	created, err := b.cli.ContainerCreate(ctx, cfg, hostConf, nil, nil, containerName)
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", containerName, err)
	}
	defer func() {
		_ = b.cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	}()

	if err := b.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return 0, fmt.Errorf("start %s: %w", containerName, err)
	}

	// Stream logs.
	logRC, err := b.cli.ContainerLogs(ctx, created.ID, container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: true, Timestamps: false,
	})
	if err != nil {
		return 0, fmt.Errorf("logs %s: %w", containerName, err)
	}
	go streamLogs(logRC, inv.LogSink, inv.TaskName, step.Name, step.DisplayName)

	statusCh, errCh := b.cli.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return 0, err
		}
	case st := <-statusCh:
		return int(st.StatusCode), nil
	case <-ctx.Done():
		_ = b.cli.ContainerStop(context.Background(), created.ID, container.StopOptions{})
		return 0, ctx.Err()
	}
	return 0, nil
}

func streamLogs(rc io.ReadCloser, sink backend.LogSink, taskName, stepName, stepDisplayName string) {
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
	go scan(stdoutR, sink, taskName, stepName, stepDisplayName, "stdout")
	scan(stderrR, sink, taskName, stepName, stepDisplayName, "stderr")
}

func scan(r io.Reader, sink backend.LogSink, taskName, stepName, stepDisplayName, stream string) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	for s.Scan() {
		sink.StepLog(taskName, stepName, stepDisplayName, stream, s.Text())
	}
}

func parseMemory(s string) (int64, error) {
	// Accept Mi, Gi, M, G suffixes.
	suffixes := []struct {
		suf string
		mul int64
	}{
		{"Gi", 1 << 30}, {"Mi", 1 << 20}, {"Ki", 1 << 10},
		{"G", 1_000_000_000}, {"M", 1_000_000}, {"K", 1_000},
	}
	for _, sf := range suffixes {
		if strings.HasSuffix(s, sf.suf) {
			var n int64
			_, err := fmt.Sscanf(strings.TrimSuffix(s, sf.suf), "%d", &n)
			if err != nil {
				return 0, err
			}
			return n * sf.mul, nil
		}
	}
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

func parseCPU(s string) (int64, error) {
	// "500m" → 0.5 CPU = 500_000_000 nano. "2" → 2 CPU.
	if strings.HasSuffix(s, "m") {
		var n int64
		_, err := fmt.Sscanf(strings.TrimSuffix(s, "m"), "%d", &n)
		if err != nil {
			return 0, err
		}
		return n * 1_000_000, nil
	}
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return int64(f * 1_000_000_000), err
}
