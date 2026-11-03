package engine

import "github.com/danielfbm/tkn-act/internal/tektontypes"

// workspaceMountPaths maps each Task workspace declaring a mountPath to
// that path, so $(workspaces.<name>.path) resolves to where the backend
// actually mounts it. Workspaces without a mountPath are omitted and
// fall back to the resolver's /workspace/<name> default.
func workspaceMountPaths(decls []tektontypes.WorkspaceDecl) map[string]string {
	var out map[string]string
	for _, d := range decls {
		if d.MountPath == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[d.Name] = d.MountPath
	}
	return out
}
