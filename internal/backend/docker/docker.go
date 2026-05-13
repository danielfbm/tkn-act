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
	"github.com/docker/docker/api/types/system"
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
	//   "" or "auto" auto-detect via cli.Info().Name vs os.Hostname()
	//   "on"          force remote (use volume-staging path, Phase 3)
	//   "off"         force local (use bind-mount path)
	//
	// Phase 2 wires the detection only — Backend.remote is set but
	// not yet consumed by the Step/Sidecar code paths.
	Remote string

	// PauseImage overrides the per-Task pause container image used for
	// netns ownership (and, in Phase 3 remote mode, for the volume
	// stager). Empty falls back to defaultPauseImage. Air-gap users
	// point this at an internal-mirror tag like
	// "registry.internal/pause:3.9".
	PauseImage string
}

type Backend struct {
	cli       *client.Client
	opts      Options
	scriptDir string
	// remote reports whether the daemon's filesystem is independent
	// of the client's (so bind mounts of local paths won't work).
	// Set during New(). Phase 3 will switch staging accordingly.
	remote bool
	// pauseImg is the resolved pause/stager image (Options.PauseImage
	// or defaultPauseImage). Captured at New() so air-gap overrides
	// don't have to round-trip through Options at every call site.
	pauseImg string

	// Remote-mode (Phase 3) staging state. Populated by startRemoteStaging
	// in Prepare and drained by stopRemoteStaging in Cleanup; unused on
	// the local-bind path. Keeping them on the Backend lets RunTask
	// stay oblivious to the per-run lifecycle.
	runID          string
	volName        string
	stagerID       string
	userWorkspaces map[string]string
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
	return &Backend{cli: cli, opts: opts, remote: remote, pauseImg: resolvePauseImage(opts.PauseImage)}, nil
}

// resolvePauseImage returns the pause/stager image the Backend should
// use, applying the built-in default when Options.PauseImage is empty.
// Extracted so the override-vs-default decision is unit-testable
// without a real daemon.
func resolvePauseImage(opt string) string {
	if opt == "" {
		return defaultPauseImage
	}
	return opt
}

// daemonInfoer is the slice of the moby client.Client API that
// decideRemote needs. Defined here so tests can substitute a stub
// without standing up a daemon.
type daemonInfoer interface {
	Info(ctx context.Context) (system.Info, error)
}

// decideRemote resolves the Options.Remote setting plus DOCKER_HOST
// and (for "auto") a one-shot cli.Info() probe into a single bool.
// Extracted so the policy is testable without a real daemon.
//
// Auto-detection rules: unix:// (or unset DOCKER_HOST) is local. Any
// other scheme triggers the Info probe — matching hostnames mean
// local, mismatching mean remote, and any ambiguity (Info error,
// empty daemon Name, missing client hostname) is reported as remote.
// "Unknown" defaults to remote because misclassifying a remote
// daemon as local in Phase 3 would silently bind-mount paths that
// don't exist on the daemon's host. The user can pin the answer
// with --remote-docker=on|off.
func decideRemote(mode, dockerHost string, info daemonInfoer) (bool, error) {
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
	if dockerHost == "" || strings.HasPrefix(dockerHost, "unix://") {
		return false, nil
	}
	if info == nil {
		return true, nil
	}
	di, err := info.Info(context.Background())
	if err != nil {
		return false, fmt.Errorf("docker backend: auto-detect remote daemon: %w (set --remote-docker=on|off to skip detection)", err)
	}
	local, err := os.Hostname()
	if err != nil || local == "" {
		return true, nil
	}
	if di.Name == "" {
		return true, nil
	}
	return di.Name != local, nil
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
	if b.remote {
		if err := b.startRemoteStaging(ctx, run.RunID, run.Workspaces); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) Cleanup(ctx context.Context) error {
	if b.remote {
		// Best-effort: surface the first failure but always continue
		// to scriptDir cleanup so a stager teardown error doesn't
		// strand a local tempdir. Returning the error preserves the
		// engine's existing "ignore Cleanup err" semantics — the
		// caller logs nothing — while still leaving a hook for tests
		// that want to assert cleanup succeeded.
		_ = b.stopRemoteStaging()
	}
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

	// Remote mode: seed the per-Task materialised volumes
	// (configMap/secret/emptyDir) into the per-run stage volume so the
	// upcoming Step containers can subpath-mount them. Local mode bind
	// mounts the host paths directly and skips this.
	if b.remote {
		if err := b.pushTaskVolumeHosts(ctx, inv.TaskRunName, inv.VolumeHosts); err != nil {
			res.Status = backend.TaskInfraFailed
			res.Err = err
			res.Ended = time.Now()
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

		// Remote mode: pull this step's per-step results dir back to
		// the host so the existing per-step substitution (which reads
		// off disk) keeps working unchanged. Local mode wrote there
		// directly via the bind mount.
		if b.remote {
			if perr := b.pullStepResults(ctx, inv.TaskRunName, step.Name, stepResultsHost); perr != nil {
				res.Status = backend.TaskInfraFailed
				res.Err = fmt.Errorf("step %q: pull results: %w", step.Name, perr)
				stepRes.Status = backend.StepFailed
				res.Steps = append(res.Steps, stepRes)
				res.Ended = time.Now()
				return res, nil
			}
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

	// Remote mode: pull the whole Task results subtree back to the
	// host before reading. This is a superset of the per-step pulls
	// already done — overwriting them with identical content is fine
	// and lets us use a single shared read path below.
	if b.remote {
		if perr := b.pullTaskResults(ctx, inv.TaskRunName, inv.ResultsHost); perr != nil {
			res.Status = backend.TaskInfraFailed
			res.Err = fmt.Errorf("pull task results: %w", perr)
			res.Ended = time.Now()
			return res, nil
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

	// Script support: stage the body either to scriptDir (local bind)
	// or to scripts/<taskRun>-<step>.sh in the per-run volume (remote).
	// Both paths set Cmd to /tekton/scripts/script.sh — the call sites
	// downstream don't have to care which staging path produced it.
	var extraMounts []mount.Mount
	if step.Script != "" {
		body := step.Script
		if !strings.HasPrefix(body, "#!") {
			body = "#!/bin/sh\nset -e\n" + body
		}
		cmd = []string{"/tekton/scripts/script.sh"}
		args = nil
		if b.remote {
			base := stepScriptBase(inv.TaskRunName, step.Name)
			if err := b.pushScript(ctx, base, []byte(body)); err != nil {
				return 0, err
			}
			extraMounts = append(extraMounts, b.remoteVolumeMount(
				"/tekton/scripts/script.sh",
				filepath.Join(stageScripts, base+".sh"),
				true,
			))
		} else {
			host := filepath.Join(b.scriptDir, fmt.Sprintf("%s-%s.sh", inv.TaskRunName, step.Name))
			if err := os.WriteFile(host, []byte(body), 0o755); err != nil {
				return 0, err
			}
			extraMounts = append(extraMounts, mount.Mount{
				Type:     mount.TypeBind,
				Source:   host,
				Target:   "/tekton/scripts/script.sh",
				ReadOnly: true,
			})
		}
	}

	// Workspace mounts. Remote mode subpaths into workspaces/<wsName>
	// keyed by the Pipeline workspace name so writes from Task A are
	// visible to Task B (the PVC-equivalent semantics). The host-path
	// reverse lookup is what joins inv.Workspaces[tName].HostPath
	// (Task-local key) to the Pipeline name buckets in the volume.
	for tName, wm := range inv.Workspaces {
		if b.remote {
			pipelineName, ok := b.pipelineWorkspaceName(wm.HostPath)
			if !ok {
				return 0, fmt.Errorf("step %q workspace %q: cannot map host path %q to a Pipeline workspace name", step.Name, tName, wm.HostPath)
			}
			extraMounts = append(extraMounts, b.remoteVolumeMount(
				"/workspace/"+tName,
				filepath.Join(stageWorkspaces, pipelineName),
				wm.ReadOnly,
			))
		} else {
			extraMounts = append(extraMounts, mount.Mount{
				Type:     mount.TypeBind,
				Source:   wm.HostPath,
				Target:   "/workspace/" + tName,
				ReadOnly: wm.ReadOnly,
			})
		}
	}

	// Task-level results mount.
	if b.remote {
		extraMounts = append(extraMounts, b.remoteVolumeMount(
			"/tekton/results",
			filepath.Join(stageResults, inv.TaskRunName),
			false,
		))
	} else {
		extraMounts = append(extraMounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: inv.ResultsHost,
			Target: "/tekton/results",
		})
	}

	// Per-step results: this step's own dir RW, every earlier step's RO.
	// In local mode we walk the host's results-dir for already-existing
	// step subdirs. In remote mode the same dirs were created on the
	// host by RunTask (mkdir for current step, plus pulled subdirs for
	// earlier steps), so the same walk works — only the mount type
	// flips.
	stepsRoot := filepath.Join(inv.ResultsHost, "steps")
	if entries, err := os.ReadDir(stepsRoot); err == nil {
		for _, ent := range entries {
			if !ent.IsDir() {
				continue
			}
			ro := ent.Name() != step.Name
			if b.remote {
				extraMounts = append(extraMounts, b.remoteVolumeMount(
					"/tekton/steps/"+ent.Name()+"/results",
					filepath.Join(stageResults, inv.TaskRunName, "steps", ent.Name()),
					ro,
				))
			} else {
				extraMounts = append(extraMounts, mount.Mount{
					Type:     mount.TypeBind,
					Source:   filepath.Join(stepsRoot, ent.Name()),
					Target:   "/tekton/steps/" + ent.Name() + "/results",
					ReadOnly: ro,
				})
			}
		}
	}

	// Step volumeMounts. Each one references a Task-level Volume the
	// engine has already materialised onto a host path; in remote mode
	// the host path was previously seeded into volumes/<taskRun>/<vol>/
	// via pushTaskVolumeHosts.
	for _, vm := range step.VolumeMounts {
		hostPath, ok := inv.VolumeHosts[vm.Name]
		if !ok {
			return 0, fmt.Errorf("step %q volumeMount %q: no host path for volume", step.Name, vm.Name)
		}
		if b.remote {
			sub := filepath.Join(stageVolumes, inv.TaskRunName, vm.Name)
			if vm.SubPath != "" {
				sub = filepath.Join(sub, vm.SubPath)
			}
			extraMounts = append(extraMounts, b.remoteVolumeMount(vm.MountPath, sub, vm.ReadOnly))
		} else {
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
