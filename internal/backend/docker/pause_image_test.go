package docker

import "testing"

func TestResolvePauseImage(t *testing.T) {
	if got := resolvePauseImage(""); got != defaultPauseImage {
		t.Errorf("resolvePauseImage(\"\") = %q, want default %q", got, defaultPauseImage)
	}
	override := "registry.internal.example/pause:3.9"
	if got := resolvePauseImage(override); got != override {
		t.Errorf("resolvePauseImage(%q) = %q, want passthrough", override, got)
	}
}
