package tektontypes

import "testing"

func TestWorkspaceDeclEffectiveMountPath(t *testing.T) {
	if got := (WorkspaceDecl{Name: "src"}).EffectiveMountPath(); got != "/workspace/src" {
		t.Errorf("default = %q", got)
	}
	if got := (WorkspaceDecl{Name: "src", MountPath: "/x"}).EffectiveMountPath(); got != "/x" {
		t.Errorf("declared = %q", got)
	}
}
