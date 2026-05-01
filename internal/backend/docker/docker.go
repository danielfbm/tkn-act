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

	"github.com/dfbmorinigo/tkn-act/internal/backend"
	"github.com/dfbmorinigo/tkn-act/internal/tektontypes"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

type Options struct {
	// PullPolicy overrides per-step ImagePullPolicy when non-empty.
	PullPolicyOverride string
}

type Backend struct {
	cli       *client.Client
	opts      Options
	scriptDir string
}

func New(opts Options) (*Backend, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	if _, err := cli.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("docker daemon not reachable: %w", err)
	}
	return &Backend{cli: cli, opts: opts}, nil
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

	for _, step := range inv.Task.Steps {
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

		exitCode, err := b.runStep(ctx, inv, step)
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
		if exitCode != 0 {
			stepRes.Status = backend.StepFailed
			res.Steps = append(res.Steps, stepRes)
			res.Status = backend.TaskFailed
			res.Ended = time.Now()
			return res, nil
		}
		stepRes.Status = backend.StepSucceeded
		res.Steps = append(res.Steps, stepRes)
	}

	// Read result files.
	for _, decl := range inv.Task.Results {
		p := filepath.Join(inv.ResultsHost, decl.Name)
		if data, err := os.ReadFile(p); err == nil {
			res.Results[decl.Name] = strings.TrimRight(string(data), "\n")
		}
	}

	res.Ended = time.Now()
	return res, nil
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
	defer rc.Close()
	_, _ = io.Copy(io.Discard, rc) // drain to ensure pull completes
	return nil
}

func (b *Backend) runStep(ctx context.Context, inv backend.TaskInvocation, step tektontypes.Step) (int, error) {
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

	// Results mount.
	extraMounts = append(extraMounts, mount.Mount{
		Type:   mount.TypeBind,
		Source: inv.ResultsHost,
		Target: "/tekton/results",
	})

	env := make([]string, 0, len(step.Env))
	for _, e := range step.Env {
		env = append(env, e.Name+"="+e.Value)
	}

	hostConf := &container.HostConfig{Mounts: extraMounts, AutoRemove: false}
	if step.Resources != nil {
		if step.Resources.Limits.Memory != "" {
			if v, err := parseMemory(step.Resources.Limits.Memory); err == nil {
				hostConf.Resources.Memory = v
			}
		}
		if step.Resources.Limits.CPU != "" {
			if v, err := parseCPU(step.Resources.Limits.CPU); err == nil {
				hostConf.Resources.NanoCPUs = v
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
	go streamLogs(logRC, inv.LogSink, inv.Task, step.Name)

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

func streamLogs(rc io.ReadCloser, sink backend.LogSink, _ tektontypes.TaskSpec, stepName string) {
	defer rc.Close()
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
	go scan(stdoutR, sink, stepName, "stdout")
	scan(stderrR, sink, stepName, "stderr")
}

func scan(r io.Reader, sink backend.LogSink, stepName, stream string) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	for s.Scan() {
		sink.StepLog("", stepName, stream, s.Text())
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
