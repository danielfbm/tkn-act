package docker

import (
	"strings"
	"testing"
)

func TestResolveDockerHost(t *testing.T) {
	cases := []struct {
		name string
		opt  string
		env  string
		want string
	}{
		{"both empty", "", "", ""},
		{"opt wins over env", "ssh://flag@host", "tcp://env-host:2375", "ssh://flag@host"},
		{"empty opt falls through to env", "", "tcp://env-host:2375", "tcp://env-host:2375"},
		{"opt wins even when same value", "unix:///x.sock", "unix:///x.sock", "unix:///x.sock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DOCKER_HOST", tc.env)
			got := resolveDockerHost(tc.opt)
			if got != tc.want {
				t.Errorf("resolveDockerHost(%q) with $DOCKER_HOST=%q = %q, want %q", tc.opt, tc.env, got, tc.want)
			}
		})
	}
}

// TestNew_HostOptionRoutesSSH is the routing assertion the Phase 4
// plan calls for: when Options.Host is an ssh:// URL, New must reach
// newSSHDialer rather than the FromEnv path. We don't stand up a real
// daemon (covered by TestSSHDialer_HandshakeAndForward); we just
// pass a deliberately-broken ssh:// host with no auth available and
// assert the error message says something SSH-flavoured. A bug that
// re-routed to client.FromEnv would fail with "docker daemon not
// reachable" instead.
func TestNew_HostOptionRoutesSSH(t *testing.T) {
	// Make sure no agent or key file is found so newSSHDialer errors
	// in a deterministic, recognisable way.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("DOCKER_HOST", "")
	t.Setenv(envSSHInsecure, "1")

	_, err := New(Options{Host: "ssh://nobody@invalid.example:22"})
	if err == nil {
		t.Fatal("expected error from broken ssh host, got nil")
	}
	if !strings.Contains(err.Error(), "ssh") && !strings.Contains(err.Error(), "SSH") {
		t.Errorf("error %q does not mention ssh; routing may have fallen through to FromEnv", err)
	}
}
