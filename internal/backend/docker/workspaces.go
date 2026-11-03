package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/docker/docker/api/types/mount"
)

// workspaceMounts builds the container mounts for every workspace bound
// to the Task, honouring (like Tekton):
//
//   - the Task's WorkspaceDecl.mountPath (default /workspace/<name>),
//   - WorkspaceDecl.readOnly (OR'd with the binding's ReadOnly),
//   - the PipelineTask binding's subPath, which is created inside the
//     workspace when missing (kubelet does the same for subPath mounts).
//
// Remote mode subpaths into workspaces/<pipelineWorkspace>[/<subPath>]
// on the per-run volume, keyed by the Pipeline workspace name so writes
// from Task A are visible to Task B (the PVC-equivalent semantics). The
// host-path reverse lookup is what joins inv.Workspaces[tName].HostPath
// (Task-local key) to the Pipeline name buckets in the volume. owner is
// used in error messages (e.g. `step "build"`).
func (b *Backend) workspaceMounts(ctx context.Context, inv backend.TaskInvocation, owner string) ([]mount.Mount, error) {
	decls := make(map[string]tektontypes.WorkspaceDecl, len(inv.Task.Workspaces))
	for _, d := range inv.Task.Workspaces {
		decls[d.Name] = d
	}
	names := make([]string, 0, len(inv.Workspaces))
	for n := range inv.Workspaces {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]mount.Mount, 0, len(names))
	for _, tName := range names {
		wm := inv.Workspaces[tName]
		decl, ok := decls[tName]
		if !ok {
			decl = tektontypes.WorkspaceDecl{Name: tName}
		}
		target := decl.EffectiveMountPath()
		readOnly := wm.ReadOnly || decl.ReadOnly
		sub := ""
		if wm.SubPath != "" {
			sub = filepath.Clean(filepath.FromSlash(wm.SubPath))
			if !filepath.IsLocal(sub) {
				return nil, fmt.Errorf("%s workspace %q: subPath %q must be a relative path inside the workspace", owner, tName, wm.SubPath)
			}
		}
		if b.remote {
			pipelineName, ok := b.pipelineWorkspaceName(wm.HostPath)
			if !ok {
				return nil, fmt.Errorf("%s workspace %q: cannot map host path %q to a Pipeline workspace name", owner, tName, wm.HostPath)
			}
			volSub := filepath.Join(stageWorkspaces, pipelineName, sub)
			if sub != "" {
				if err := b.mkdirInVolume(ctx, filepath.ToSlash(volSub)); err != nil {
					return nil, fmt.Errorf("%s workspace %q: create subPath %q: %w", owner, tName, wm.SubPath, err)
				}
			}
			out = append(out, b.remoteVolumeMount(target, volSub, readOnly))
			continue
		}
		src := wm.HostPath
		if sub != "" {
			src = filepath.Join(wm.HostPath, sub)
			// Create only when missing: never re-mode an existing
			// directory in a user-supplied workspace.
			if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
				if err := mkdirWorldWritable(src); err != nil {
					return nil, fmt.Errorf("%s workspace %q: create subPath %q: %w", owner, tName, wm.SubPath, err)
				}
			}
		}
		out = append(out, mount.Mount{
			Type:     mount.TypeBind,
			Source:   src,
			Target:   target,
			ReadOnly: readOnly,
		})
	}
	return out, nil
}
