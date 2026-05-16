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
	"time"

	"github.com/danielfbm/tkn-act/internal/debug"
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
//
// Self-cleaning on partial failure: the engine only defers Cleanup
// once Prepare has returned nil, so any error after VolumeCreate must
// unwind the resources we already created — otherwise a label-tagged
// volume and a half-started stager would survive the process.
func (b *Backend) startRemoteStaging(ctx context.Context, runID string, workspaces map[string]string) (err error) {
	b.runID = runID
	b.volName = runVolumeName(runID)
	b.userWorkspaces = workspaces
	b.workspaceByHostPath = buildHostPathIndex(workspaces)

	defer func() {
		if err != nil {
			_ = b.stopRemoteStaging()
		}
	}()

	if _, vErr := b.cli.VolumeCreate(ctx, volume.CreateOptions{
		Name: b.volName,
		Labels: map[string]string{
			"tkn-act.run":     runID,
			"tkn-act.purpose": "stage",
		},
	}); vErr != nil {
		return fmt.Errorf("create stage volume %q: %w", b.volName, vErr)
	}
	b.volumeCreated = true
	b.dbg.Emit(debug.Backend, func() (string, map[string]any) {
		return "volume created", map[string]any{"name": b.volName, "purpose": "stage"}
	})

	if iErr := b.ensureImage(ctx, b.pauseImg, "IfNotPresent"); iErr != nil {
		return fmt.Errorf("pull stager image %q: %w", b.pauseImg, iErr)
	}

	stagerName := stagerContainerName(runID)
	created, cErr := b.cli.ContainerCreate(ctx,
		&container.Config{Image: b.pauseImg},
		&container.HostConfig{
			AutoRemove: false,
			Mounts: []mount.Mount{{
				Type:   mount.TypeVolume,
				Source: b.volName,
				Target: stagerMountPoint,
			}},
		}, nil, nil, stagerName)
	if cErr != nil {
		return fmt.Errorf("create stager %q: %w", stagerName, cErr)
	}
	b.stagerID = created.ID

	if sErr := b.cli.ContainerStart(ctx, b.stagerID, container.StartOptions{}); sErr != nil {
		return fmt.Errorf("start stager %q: %w", stagerName, sErr)
	}
	b.dbg.Emit(debug.Backend, func() (string, map[string]any) {
		return "stager started", map[string]any{
			"id":         shortID(b.stagerID),
			"name":       stagerName,
			"workspaces": len(workspaces),
		}
	})

	// Seed each declared workspace (auto-allocated dirs are empty;
	// user-supplied -w name=/path dirs may have content). Empty dirs
	// are pushed too so the workspace subpath exists for subpath
	// mounts to attach to before any Task writes.
	for name, hostPath := range workspaces {
		if pErr := b.pushHostDir(ctx, hostPath, filepath.ToSlash(filepath.Join(stageWorkspaces, name))); pErr != nil {
			return fmt.Errorf("seed workspace %q from %q: %w", name, hostPath, pErr)
		}
	}
	return nil
}

// buildHostPathIndex inverts the pipeline-name → host-path map into
// a host-path → pipeline-name index, deterministically rejecting any
// HostPath shared by two pipeline workspaces. The reverse lookup
// (pipelineWorkspaceName) calls this once at Prepare time so per-Step
// mount construction doesn't iterate the map (which Go randomises)
// and so collisions surface as an obvious, reproducible startup error
// rather than nondeterministic mount targets.
func buildHostPathIndex(workspaces map[string]string) map[string]string {
	out := make(map[string]string, len(workspaces))
	collisions := map[string][]string{}
	for name, hostPath := range workspaces {
		if existing, dup := out[hostPath]; dup {
			collisions[hostPath] = append(collisions[hostPath], existing, name)
			continue
		}
		out[hostPath] = name
	}
	// On collision, emit a sentinel so pipelineWorkspaceName can
	// detect it deterministically. The sentinel is a slash-prefixed
	// string ("__ambiguous__:<paths>"), guaranteed not to be a real
	// pipeline workspace name. Aggregate-style fail-loud later.
	for hostPath, names := range collisions {
		out[hostPath] = ambiguousMarker + ":" + strings.Join(names, ",")
	}
	return out
}

const ambiguousMarker = "__ambiguous__"

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
// name by host path against the precomputed index built at Prepare
// time (b.workspaceByHostPath). The engine's TaskInvocation.Workspaces
// keys by the Task-local workspace name (e.g. "data"), but the per-run
// volume layout buckets by Pipeline name (e.g. "shared") because
// that's what makes a write from one Task visible to the next.
//
// Returns (name, true) on a unique hit; ("", false) on miss; an
// error-return path is intentionally absent — collisions are flagged
// at lookup time by the ambiguousMarker sentinel and surface as a
// clear mount-construction failure at the call site.
func (b *Backend) pipelineWorkspaceName(hostPath string) (string, bool) {
	name, ok := b.workspaceByHostPath[hostPath]
	if !ok {
		return "", false
	}
	if strings.HasPrefix(name, ambiguousMarker) {
		// Collision: same HostPath registered for multiple workspace
		// names. Returning "" forces the caller into the not-found
		// branch which raises an actionable error mentioning the
		// HostPath.
		return "", false
	}
	return name, true
}

// stopRemoteStaging is called by Cleanup, and also by
// startRemoteStaging on partial failure. Each resource is gated on
// its own field so a half-built run (volume created but stager start
// failed) still gets the volume removed.
//
// Uses a fresh background context capped at stagerStopBudget so a
// hung remote daemon can't strand the process forever — the cancel-
// immune property survives a cancelled run context, but the absolute
// ceiling means worst-case latency stays bounded.
func (b *Backend) stopRemoteStaging() error {
	bg, cancel := context.WithTimeout(context.Background(), stagerStopBudget)
	defer cancel()

	var first error
	captureErr := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}

	if b.stagerID != "" {
		// Pull workspaces back BEFORE the stager dies so the daemon
		// can still service CopyFromContainer. Failures here capture
		// the first error but never short-circuit teardown.
		for name, hostPath := range b.userWorkspaces {
			captureErr(b.pullHostDir(bg, filepath.ToSlash(filepath.Join(stageWorkspaces, name)), hostPath))
		}

		timeoutSecs := 1
		stoppedID := b.stagerID
		if err := b.cli.ContainerStop(bg, b.stagerID, container.StopOptions{Timeout: &timeoutSecs}); err != nil {
			captureErr(fmt.Errorf("stop stager: %w", err))
		}
		if err := b.cli.ContainerRemove(bg, b.stagerID, container.RemoveOptions{Force: true}); err != nil {
			captureErr(fmt.Errorf("remove stager: %w", err))
		}
		b.stagerID = ""
		b.dbg.Emit(debug.Backend, func() (string, map[string]any) {
			return "stager stopped", map[string]any{"id": shortID(stoppedID)}
		})
	}
	if b.volumeCreated && b.volName != "" {
		if err := b.cli.VolumeRemove(bg, b.volName, true); err != nil {
			captureErr(fmt.Errorf("remove stage volume %q: %w", b.volName, err))
		}
		b.volumeCreated = false
		b.volName = ""
	}
	b.userWorkspaces = nil
	b.workspaceByHostPath = nil
	return first
}

// stagerStopBudget caps total Cleanup time on the remote-staging path.
// Workspace pulls dominate when -w name=/big/path was used; 2 minutes
// covers GB-scale workspaces over a typical SSH tunnel while still
// guaranteeing the process exits in bounded time on a half-broken
// daemon.
const stagerStopBudget = 2 * time.Minute

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

// ensureStagerDir creates dirPath (and every ancestor below
// stagerMountPoint) inside the stager. moby's CopyToContainer
// requires the destination directory to already exist on the daemon
// side — without this, the first push to e.g. /staged/workspaces/foo
// would fail because /staged/workspaces doesn't exist yet. We emit
// one tar with a TypeDir entry per ancestor segment, rooted at
// stagerMountPoint, so a single CopyToContainer call materialises
// the whole chain.
func (b *Backend) ensureStagerDir(ctx context.Context, dirPath string) error {
	rel := strings.TrimPrefix(dirPath, stagerMountPoint)
	rel = strings.TrimPrefix(rel, "/")
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return nil // /staged itself always exists (it's the volume mount point).
	}
	segments := strings.Split(rel, "/")

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := range segments {
		entry := strings.Join(segments[:i+1], "/") + "/"
		if err := tw.WriteHeader(&tar.Header{
			Name:     entry,
			Mode:     0o755,
			Typeflag: tar.TypeDir,
		}); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return b.cli.CopyToContainer(ctx, b.stagerID, stagerMountPoint, &buf, container.CopyToContainerOptions{})
}

// mkdirInVolume materialises the volume-relative subpath inside the
// stager (i.e. /staged/<subpath>) so subsequent containers can
// VolumeOptions.Subpath into it. Engine v25+ resolves subpath via
// openat2, which ENOENTs on a missing entry — call this for any
// subpath the run will mount before the first ContainerCreate that
// targets it. Cheap (single CopyToContainer of an empty tar header
// chain) and idempotent per ensureStagerDir's tar-merge semantics.
func (b *Backend) mkdirInVolume(ctx context.Context, subpath string) error {
	full := stagerMountPoint + "/" + strings.TrimPrefix(filepath.ToSlash(subpath), "/")
	return b.ensureStagerDir(ctx, full)
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
//
// Path-safety: every resolved target must stay under filepath.Clean(dstDir).
// Substring `..` matching would silently drop legitimate names like
// `package..json.bak` or `lib...so`; the prefix check below catches
// real escape attempts (`../foo`, `a/../../b`) without dropping valid
// content. Symlink Linkname gets the same check so a malicious entry
// can't redirect a subsequent file write outside the destination.
func untarToDir(r io.Reader, dstDir, stripPrefix string) error {
	cleanRoot := filepath.Clean(dstDir)
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		// CopyFromContainer wraps every entry under the basename of the
		// source path. Two shapes are possible:
		//   "greet/"           — the dir-itself entry (TypeDir)
		//   "greet"            — the same dir-itself entry without
		//                        trailing slash (some tar producers
		//                        do this)
		//   "greet/greeting"   — files inside the dir
		// Strip the wrapper only when it's the literal prefix. Using
		// `TrimPrefix(name, stripPrefix)` unguarded would also chop the
		// prefix off any file whose own name happens to start with it
		// (e.g. a result called `greeting` inside step `greet` ended
		// up extracted as `ing` — Phase 5 hit this against dind).
		name := hdr.Name
		switch {
		case name == stripPrefix, name == stripPrefix+"/":
			name = ""
		case strings.HasPrefix(name, stripPrefix+"/"):
			name = name[len(stripPrefix)+1:]
		}
		name = strings.TrimPrefix(name, "/")
		if name == "" {
			continue
		}
		target := filepath.Join(dstDir, filepath.FromSlash(name))
		if !pathContainedIn(target, cleanRoot) {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeReg:
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
			// Resolve the symlink target as the kernel would on follow
			// and ensure that resolved path stays inside the dstDir.
			// Absolute Linknames and relative ones both go through
			// filepath.Join so an Linkname like "../../etc/passwd"
			// gets the same prefix check as the entry name.
			linkResolved := hdr.Linkname
			if !filepath.IsAbs(linkResolved) {
				linkResolved = filepath.Join(filepath.Dir(target), linkResolved)
			}
			if !pathContainedIn(linkResolved, cleanRoot) {
				continue
			}
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

// pathContainedIn reports whether candidate sits inside (or equal to)
// the cleaned root. Used to reject tar entries and symlink targets
// that would land outside dstDir, which is the canonical CVE class
// for tar extractors. Both sides are cleaned to normalise `./` and
// `..` segments before the prefix compare.
func pathContainedIn(candidate, cleanRoot string) bool {
	cleaned := filepath.Clean(candidate)
	if cleaned == cleanRoot {
		return true
	}
	prefix := cleanRoot + string(os.PathSeparator)
	return strings.HasPrefix(cleaned, prefix)
}
