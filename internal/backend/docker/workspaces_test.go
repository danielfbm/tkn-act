package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
	"github.com/danielfbm/tkn-act/internal/tektontypes"
	"github.com/docker/docker/api/types/mount"
)

// TestWorkspaceMounts_Local covers Task mountPath, Task readOnly and
// binding subPath, all previously ignored by the docker backend.
func TestWorkspaceMounts_Local(t *testing.T) {
	host := t.TempDir()
	if err := os.Mkdir(filepath.Join(host, "existing"), 0o700); err != nil {
		t.Fatal(err)
	}
	inv := backend.TaskInvocation{
		Task: tektontypes.TaskSpec{Workspaces: []tektontypes.WorkspaceDecl{
			{Name: "src", MountPath: "/custom/src", ReadOnly: true},
			{Name: "out"},
		}},
		Workspaces: map[string]backend.WorkspaceMount{
			"src":   {HostPath: host, SubPath: "a/b"},
			"out":   {HostPath: host},
			"extra": {HostPath: host, SubPath: "existing", ReadOnly: true},
		},
	}
	b := &Backend{}
	got, err := b.workspaceMounts(context.Background(), inv, `step "s"`)
	if err != nil {
		t.Fatal(err)
	}
	want := []mount.Mount{
		{Type: mount.TypeBind, Source: filepath.Join(host, "existing"), Target: "/workspace/extra", ReadOnly: true},
		{Type: mount.TypeBind, Source: host, Target: "/workspace/out"},
		{Type: mount.TypeBind, Source: filepath.Join(host, "a", "b"), Target: "/custom/src", ReadOnly: true},
	}
	if len(got) != len(want) {
		t.Fatalf("mounts = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mount[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	st, err := os.Stat(filepath.Join(host, "a", "b"))
	if err != nil || !st.IsDir() {
		t.Fatalf("subPath not created: %v", err)
	}
	if st.Mode().Perm() != 0o777 {
		t.Errorf("created subPath mode = %o, want 777", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Join(host, "existing")); st.Mode().Perm() != 0o700 {
		t.Errorf("existing subPath was re-moded to %o", st.Mode().Perm())
	}
}

func TestWorkspaceMounts_RejectsEscapingSubPath(t *testing.T) {
	for _, sub := range []string{"../x", "/etc", "a/../../x"} {
		inv := backend.TaskInvocation{Workspaces: map[string]backend.WorkspaceMount{
			"w": {HostPath: t.TempDir(), SubPath: sub},
		}}
		_, err := (&Backend{}).workspaceMounts(context.Background(), inv, `step "s"`)
		if err == nil || !strings.Contains(err.Error(), "subPath") {
			t.Errorf("subPath %q: err = %v, want rejection", sub, err)
		}
	}
}

func TestWorkspaceMounts_RemoteNoSubPath(t *testing.T) {
	b := &Backend{remote: true, volName: "vol", workspaceByHostPath: map[string]string{"/h": "shared"}}
	inv := backend.TaskInvocation{
		Task:       tektontypes.TaskSpec{Workspaces: []tektontypes.WorkspaceDecl{{Name: "w", MountPath: "/m", ReadOnly: true}}},
		Workspaces: map[string]backend.WorkspaceMount{"w": {HostPath: "/h"}},
	}
	got, err := b.workspaceMounts(context.Background(), inv, `step "s"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Target != "/m" || !got[0].ReadOnly || got[0].VolumeOptions.Subpath != "workspaces/shared" {
		t.Errorf("mount = %+v (opts %+v)", got, got[0].VolumeOptions)
	}
	inv.Workspaces["w"] = backend.WorkspaceMount{HostPath: "/unknown"}
	if _, err := b.workspaceMounts(context.Background(), inv, `step "s"`); err == nil {
		t.Error("want error for unmapped host path")
	}
}
