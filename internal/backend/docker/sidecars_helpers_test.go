package docker

import (
	"testing"
)

func TestSidecarContainerName(t *testing.T) {
	got := sidecarContainerName("abc12345", "build", "redis")
	want := "tkn-act-abc12345-build-sidecar-redis"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPauseContainerName(t *testing.T) {
	got := pauseContainerName("abc12345", "build")
	want := "tkn-act-abc12345-build-pause"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDefaultPauseImage(t *testing.T) {
	// Pinned to upstream Kubernetes' pause image; ~700KB; cached
	// forever after first pull. See spec §3.1 and Open Question #3.
	// Air-gap users override via Options.PauseImage / --pause-image;
	// the default below is what an unconfigured tkn-act reaches for.
	if defaultPauseImage != "registry.k8s.io/pause:3.9" {
		t.Errorf("defaultPauseImage = %q; pin must match the spec exactly", defaultPauseImage)
	}
}

func TestSidecarStreamValues(t *testing.T) {
	// Stream values must use the fine-grained "sidecar-stdout" /
	// "sidecar-stderr" pair so consumers can filter sidecar vs.
	// step output. Locked-in by the JSON event-shape contract in
	// AGENTS.md.
	if sidecarStdout != "sidecar-stdout" {
		t.Errorf("sidecarStdout = %q; must match documented contract", sidecarStdout)
	}
	if sidecarStderr != "sidecar-stderr" {
		t.Errorf("sidecarStderr = %q; must match documented contract", sidecarStderr)
	}
}
