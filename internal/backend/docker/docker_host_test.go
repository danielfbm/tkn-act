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
// assert the error wraps the dialer's specific prefix. A bug that
// re-routed to client.FromEnv would fail with "docker daemon not
// reachable" instead — matching the literal "ssh transport:" prefix
// (added in newDockerClient when wrapping the dialer error) is what
// makes this test fail closed on a re-routing regression.
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
	if !strings.Contains(err.Error(), "ssh transport:") {
		t.Errorf("error %q is missing the 'ssh transport:' wrap; routing may have fallen through to FromEnv", err)
	}
}

// TestNew_HostOptionRoutesTCP exercises the new non-ssh non-empty
// branch of newDockerClient. Without it, a regression that flipped
// the WithHost append to a no-op (or appended FromEnv twice) would
// not be caught by tests that only cover ssh:// routing or the
// FromEnv fallback. Uses a deliberately-unreachable tcp:// host so
// the daemon Ping fails fast with a connect error — proves the
// override actually steered the client at the supplied address
// rather than the env or default unix socket.
func TestNew_HostOptionRoutesTCP(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")

	_, err := New(Options{Host: "tcp://127.0.0.1:1"})
	if err == nil {
		t.Fatal("expected ping failure against tcp://127.0.0.1:1, got nil")
	}
	// "docker daemon not reachable" is the New() wrap around a Ping
	// failure; the underlying dial error mentions 127.0.0.1:1 only
	// when the override actually took effect.
	msg := err.Error()
	if !strings.Contains(msg, "docker daemon not reachable") {
		t.Fatalf("expected ping wrap, got: %v", err)
	}
	if !strings.Contains(msg, "127.0.0.1:1") {
		t.Errorf("ping error %q does not mention 127.0.0.1:1; Options.Host may not have steered the client", err)
	}
}
