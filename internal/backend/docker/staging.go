package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
)

// Phase 3: remote daemon staging.
//
// The local-bind path is unaffected — the helpers in this file are
// only reached when Backend.remote is true. They give every Step and
// Sidecar container a single per-run docker volume to mount, with
// distinct subpaths slicing scripts / workspaces / results / etc. so
// the daemon never needs to see a host path the client wrote.
//
// Layout inside the per-run volume `tkn-act-<runID>`:
//
//	scripts/<taskRun>-<step>.sh        # generated step scripts
//	workspaces/<wsName>/...            # Pipeline-shared workspaces
//	results/<taskRun>/...              # /tekton/results
//	results/<taskRun>/steps/<step>/... # per-step results
//	volumes/<taskRun>/<volName>/...    # materialised cm/secret/emptyDir
//
// Subpath mounts require Docker Engine v25+ (mount.VolumeOptions.Subpath
// landed in moby v25, Jan 2024). Older daemons surface as a clear error
// at first container create — Backend.New does not pre-flight the
// version because most modern remote daemons (CI dind, devpods,
// Docker Desktop) already exceed it.

// Volume subdirectories. Single source of truth for the layout above —
// every helper below funnels through these so a future re-layout
// touches one place. Trailing slashes are not included; callers
// path.Join as needed.
const (
	stageScripts    = "scripts"
	stageWorkspaces = "workspaces"
	stageResults    = "results"
	stageVolumes    = "volumes"
)

// runVolumeName returns the per-run docker volume name. Stable per
// runID so tests can assert it directly, and so a torn-down run
// leaves behind a predictable name for cleanup-on-next-run if the
// process died before Cleanup ran.
func runVolumeName(runID string) string {
	return "tkn-act-" + runID
}

// stagerContainerName returns the per-run stager container name. The
// stager is the long-lived `pause(2)` process whose only job is to
// keep the volume mounted at /staged so docker cp can write into and
// read out of any sub-path.
func stagerContainerName(runID string) string {
	return "tkn-act-" + runID + "-stager"
}

// stagerMountPoint is where every sub-path lives inside the stager
// container. Mounted as the entire volume (no subpath) so a single
// CopyToContainer/CopyFromContainer call can target any layout key.
const stagerMountPoint = "/staged"

// startRemoteStaging is called by Prepare when Backend.remote is true.
// It creates the per-run volume, starts the stager, and pushes any
// pre-existing workspace host data into the volume so the first Task
// finds the same content the user passed via -w name=/host/path.
//
// Saves run.Workspaces on the Backend so Cleanup can pull post-run
// contents back to the same host paths, matching the local-bind
// semantics where every Task write is visible on the host afterwards.
func (b *Backend) startRemoteStaging(ctx context.Context, runID string, workspaces map[string]string) error {
	b.runID = runID
	b.volName = runVolumeName(runID)
	b.userWorkspaces = workspaces

	if _, err := b.cli.VolumeCreate(ctx, volume.CreateOptions{
		Name: b.volName,
		Labels: map[string]string{
			"tkn-act.run":      runID,
			"tkn-act.purpose":  "stage",
			"tkn-act.makeshift": "true",
		},
	}); err != nil {
		return fmt.Errorf("create stage volume %q: %w", b.volName, err)
	}

	if err := b.ensureImage(ctx, b.pauseImg, "IfNotPresent"); err != nil {
		return fmt.Errorf("pull stager image %q: %w", b.pauseImg, err)
	}

	stagerName := stagerContainerName(runID)
	created, err := b.cli.ContainerCreate(ctx,
		&container.Config{Image: b.pauseImg},
		&container.HostConfig{
			AutoRemove: false,
			Mounts: []mount.Mount{{
				Type:   mount.TypeVolume,
				Source: b.volName,
				Target: stagerMountPoint,
			}},
		}, nil, nil, stagerName)
	if err != nil {
		return fmt.Errorf("create stager %q: %w", stagerName, err)
	}
	b.stagerID = created.ID

	if err := b.cli.ContainerStart(ctx, b.stagerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start stager %q: %w", stagerName, err)
	}

	// Seed each declared workspace (auto-allocated dirs are empty;
	// user-supplied -w name=/path dirs may have content). Empty dirs
	// are pushed too so the workspace subpath exists for subpath
	// mounts to attach to before any Task writes.
	for name, hostPath := range workspaces {
		if err := b.pushHostDir(ctx, hostPath, filepath.ToSlash(filepath.Join(stageWorkspaces, name))); err != nil {
			return fmt.Errorf("seed workspace %q from %q: %w", name, hostPath, err)
		}
	}
	return nil
}

// pushTaskVolumeHosts seeds the per-Task materialised volumes
// (configMap / secret / emptyDir) from inv.VolumeHosts host paths
// into the volume at volumes/<taskRun>/<volName>/. No-op when the
// Task has no Volume declarations or the backend isn't remote.
func (b *Backend) pushTaskVolumeHosts(ctx context.Context, taskRun string, volumeHosts map[string]string) error {
	for name, hostPath := range volumeHosts {
		dest := filepath.ToSlash(filepath.Join(stageVolumes, taskRun, name))
		if err := b.pushHostDir(ctx, hostPath, dest); err != nil {
			return fmt.Errorf("seed task volume %q from %q: %w", name, hostPath, err)
		}
	}
	return nil
}

// pushScript writes a single executable script under scripts/<base>.sh
// in the volume. Caller-supplied basename keeps the helper indifferent
// to whether the caller is staging a Step script (`<taskRun>-<step>`)
// or a Sidecar script (`<taskRun>-sidecar-<name>`). Always 0o755 to
// match the local-bind path's WriteFile mode.
func (b *Backend) pushScript(ctx context.Context, base string, body []byte) error {
	return b.pushFile(ctx, stageScripts, base+".sh", body, 0o755)
}

// pullStepResults copies results/<taskRun>/steps/<step>/ from the
// stager into <inv.ResultsHost>/steps/<step>/ so the existing
// per-step substitution code can read them off the host.
func (b *Backend) pullStepResults(ctx context.Context, taskRun, stepName, hostStepDir string) error {
	src := filepath.ToSlash(filepath.Join(stageResults, taskRun, "steps", stepName))
	return b.pullHostDir(ctx, src, hostStepDir)
}

// pullTaskResults copies results/<taskRun>/ from the stager into
// inv.ResultsHost so the existing Task-level result-file reads (and
// any previously-pulled per-step subdirs) are present on the host.
func (b *Backend) pullTaskResults(ctx context.Context, taskRun, hostResultsDir string) error {
	src := filepath.ToSlash(filepath.Join(stageResults, taskRun))
	return b.pullHostDir(ctx, src, hostResultsDir)
}

// stepScriptBase is the per-Step script basename (no .sh extension).
// Mirrors the local-bind path's `<taskRun>-<step>` pattern so a
// side-by-side diff stays small.
func stepScriptBase(taskRun, stepName string) string {
	return taskRun + "-" + stepName
}

// sidecarScriptBase is the per-Sidecar script basename (no .sh
// extension). The "sidecar-" interposition keeps Step / Sidecar
// names from colliding inside the shared scripts/ subdir.
func sidecarScriptBase(taskRun, sidecarName string) string {
	return taskRun + "-sidecar-" + sidecarName
}

// remoteVolumeMount is a small constructor for the recurring
// "subpath of the per-run volume" mount. Centralises the
// VolumeOptions{Subpath: ...} dance so call sites read like the
// local-bind ones.
func (b *Backend) remoteVolumeMount(target, subpath string, readOnly bool) mount.Mount {
	return mount.Mount{
		Type:     mount.TypeVolume,
		Source:   b.volName,
		Target:   target,
		ReadOnly: readOnly,
		VolumeOptions: &mount.VolumeOptions{
			Subpath: filepath.ToSlash(subpath),
		},
	}
}

// pipelineWorkspaceName reverse-looks-up the Pipeline-level workspace
// name by host path. The engine's TaskInvocation.Workspaces keys by
// the Task-local workspace name (e.g. "data"), but the per-run volume
// layout buckets by Pipeline name (e.g. "shared") because that's what
// makes a write from one Task visible to the next. Both sides hold
// the same HostPath string the manager produced, so a HostPath equality
// match is the canonical join.
func (b *Backend) pipelineWorkspaceName(hostPath string) (string, bool) {
	for name, p := range b.userWorkspaces {
		if p == hostPath {
			return name, true
		}
	}
	return "", false
}

// stopRemoteStaging is called by Cleanup. Pulls workspace contents back
// to the host paths the user supplied (so post-run state is visible
// just like the local-bind path), then tears the stager and volume
// down. Errors are logged via the returned aggregate error but never
// short-circuit cleanup — the volume removal must be attempted even
// if a workspace pull failed.
func (b *Backend) stopRemoteStaging() error {
	if b.stagerID == "" {
		return nil
	}
	bg := context.Background()

	var first error
	captureErr := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}

	for name, hostPath := range b.userWorkspaces {
		captureErr(b.pullHostDir(bg, filepath.ToSlash(filepath.Join(stageWorkspaces, name)), hostPath))
	}

	timeoutSecs := 1
	if err := b.cli.ContainerStop(bg, b.stagerID, container.StopOptions{Timeout: &timeoutSecs}); err != nil {
		captureErr(fmt.Errorf("stop stager: %w", err))
	}
	if err := b.cli.ContainerRemove(bg, b.stagerID, container.RemoveOptions{Force: true}); err != nil {
		captureErr(fmt.Errorf("remove stager: %w", err))
	}
	if b.volName != "" {
		if err := b.cli.VolumeRemove(bg, b.volName, true); err != nil {
			captureErr(fmt.Errorf("remove stage volume %q: %w", b.volName, err))
		}
	}
	b.stagerID = ""
	b.volName = ""
	b.userWorkspaces = nil
	return first
}

// pushHostDir tar-streams a host directory's contents into the stager
// at /staged/<destSubpath>/. Empty directories produce a tar with one
// directory entry — sufficient to materialise the subpath so subsequent
// container mounts can attach to it.
func (b *Backend) pushHostDir(ctx context.Context, hostDir, destSubpath string) error {
	rc, err := tarHostDir(hostDir)
	if err != nil {
		return err
	}
	dest := stagerMountPoint + "/" + strings.TrimPrefix(destSubpath, "/")
	// CopyToContainer untars `content` into `dstPath`. We pre-create
	// the destination directory by tarring an empty entry so missing
	// parents in the volume don't fail the copy on Engine versions
	// that don't auto-mkdir.
	if err := b.ensureStagerDir(ctx, dest); err != nil {
		return err
	}
	return b.cli.CopyToContainer(ctx, b.stagerID, dest, rc, container.CopyToContainerOptions{})
}

// pushFile writes a single regular file at /staged/<destSubpath>/<base>
// with the given mode bits. Used for generated step scripts where
// streaming a whole host directory would be wasteful.
func (b *Backend) pushFile(ctx context.Context, destSubpath, baseName string, data []byte, mode os.FileMode) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: baseName,
		Mode: int64(mode.Perm()),
		Size: int64(len(data)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	dest := stagerMountPoint + "/" + strings.TrimPrefix(destSubpath, "/")
	if err := b.ensureStagerDir(ctx, dest); err != nil {
		return err
	}
	return b.cli.CopyToContainer(ctx, b.stagerID, dest, &buf, container.CopyToContainerOptions{})
}

// pullHostDir extracts /staged/<srcSubpath>/ from the stager into
// hostDir. CopyFromContainer wraps its output in a tar entry whose
// root segment is the basename of the source path; we strip it so the
// extracted layout mirrors what the host would have seen via a
// bind-mount.
func (b *Backend) pullHostDir(ctx context.Context, srcSubpath, hostDir string) error {
	src := stagerMountPoint + "/" + strings.TrimPrefix(srcSubpath, "/")
	rc, _, err := b.cli.CopyFromContainer(ctx, b.stagerID, src)
	if err != nil {
		return fmt.Errorf("copy from stager %q: %w", src, err)
	}
	defer func() { _ = rc.Close() }()
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		return err
	}
	return untarToDir(rc, hostDir, filepath.Base(src))
}

// ensureStagerDir creates the destination directory inside the stager
// before the first CopyToContainer targeted at it. Using a tar header
// with TypeDir is cheap and works on every Engine version.
func (b *Backend) ensureStagerDir(ctx context.Context, dirPath string) error {
	parent, base := filepath.Split(strings.TrimSuffix(dirPath, "/"))
	parent = strings.TrimSuffix(parent, "/")
	if parent == "" || parent == stagerMountPoint {
		parent = stagerMountPoint
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name:     base + "/",
		Mode:     0o755,
		Typeflag: tar.TypeDir,
	}); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return b.cli.CopyToContainer(ctx, b.stagerID, parent, &buf, container.CopyToContainerOptions{})
}

// tarHostDir packs hostDir into a tar stream with paths relative to
// hostDir (no leading directory entry). Symlinks are stored as
// symlinks, not dereferenced — Tekton workspace semantics include
// preserving them across Tasks.
func tarHostDir(hostDir string) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	st, err := os.Stat(hostDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Materialise an empty directory entry so the subpath
			// exists for subsequent mounts.
			if werr := tw.WriteHeader(&tar.Header{Name: "./", Mode: 0o755, Typeflag: tar.TypeDir}); werr != nil {
				return nil, werr
			}
			if cerr := tw.Close(); cerr != nil {
				return nil, cerr
			}
			return &buf, nil
		}
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("tarHostDir: %q is not a directory", hostDir)
	}

	walkErr := filepath.Walk(hostDir, func(path string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(hostDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		var link string
		if fi.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if fi.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = io.Copy(tw, f)
		return err
	})
	if walkErr != nil {
		return nil, walkErr
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// untarToDir extracts a tar stream into dstDir. CopyFromContainer
// wraps every entry under the basename of the source path; stripPrefix
// (set to that basename) is removed before reconstructing the host
// layout. Unknown header types are skipped.
func untarToDir(r io.Reader, dstDir, stripPrefix string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(hdr.Name, stripPrefix+"/")
		name = strings.TrimPrefix(name, stripPrefix)
		name = strings.TrimPrefix(name, "/")
		if name == "" {
			continue
		}
		// Defend against tar entries that climb out of the destination
		// directory. CopyFromContainer is internal traffic but the
		// stager mounts a writable volume the daemon could in principle
		// be tricked into populating with crafted entries.
		if strings.Contains(name, "..") {
			continue
		}
		target := filepath.Join(dstDir, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			_ = os.Remove(target)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
}
