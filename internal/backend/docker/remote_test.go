package docker

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/system"
)

// fakeInfoer is a daemonInfoer stub that lets tests drive the
// "auto + non-unix DOCKER_HOST" branch of decideRemote without
// standing up a daemon.
type fakeInfoer struct {
	name string
	err  error
}

func (f fakeInfoer) Info(_ context.Context) (system.Info, error) {
	return system.Info{Name: f.name}, f.err
}

// TestDecideRemote_Static covers the branches that don't call Info.
func TestDecideRemote_Static(t *testing.T) {
	cases := []struct {
		name       string
		mode       string
		dockerHost string
		wantRemote bool
		wantErr    bool
	}{
		{name: "force on", mode: "on", wantRemote: true},
		{name: "force off", mode: "off", wantRemote: false},
		{name: "auto + unix scheme is local", mode: "auto", dockerHost: "unix:///var/run/docker.sock", wantRemote: false},
		{name: "empty mode = auto + unix is local", mode: "", dockerHost: "unix:///var/run/docker.sock", wantRemote: false},
		{name: "auto + empty DOCKER_HOST is local", mode: "auto", dockerHost: "", wantRemote: false},
		{name: "invalid mode errors", mode: "yes", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remote, err := decideRemote(tc.mode, tc.dockerHost, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got remote=%v nil", remote)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if remote != tc.wantRemote {
				t.Errorf("remote = %v, want %v", remote, tc.wantRemote)
			}
		})
	}
}

// TestDecideRemote_AutoProbe covers the "auto + non-unix DOCKER_HOST"
// branch using a stub daemonInfoer. The four cases the user-visible
// safety net depends on:
//
//   - hostname matches → local
//   - hostname differs → remote
//   - empty name (daemon ambiguous) → remote (safer for Phase 3)
//   - Info error → error returned to caller (no silent classification)
func TestDecideRemote_AutoProbe(t *testing.T) {
	clientHost, err := os.Hostname()
	if err != nil || clientHost == "" {
		t.Skip("os.Hostname unavailable")
	}

	t.Run("hostname matches -> local", func(t *testing.T) {
		got, err := decideRemote("auto", "tcp://127.0.0.1:2375", fakeInfoer{name: clientHost})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got {
			t.Errorf("remote = true, want false")
		}
	})

	t.Run("hostname differs -> remote", func(t *testing.T) {
		got, err := decideRemote("auto", "tcp://otherhost:2375", fakeInfoer{name: "some-other-daemon"})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !got {
			t.Errorf("remote = false, want true")
		}
	})

	t.Run("empty daemon Name -> remote", func(t *testing.T) {
		got, err := decideRemote("auto", "tcp://x:2375", fakeInfoer{name: ""})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !got {
			t.Errorf("remote = false, want true (empty Name treated as ambiguous)")
		}
	})

	t.Run("Info error -> propagated", func(t *testing.T) {
		_, err := decideRemote("auto", "tcp://x:2375", fakeInfoer{err: errors.New("network unreachable")})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "--remote-docker=on|off") {
			t.Errorf("err = %v, want hint about --remote-docker", err)
		}
	})

	t.Run("nil infoer + non-unix host -> remote", func(t *testing.T) {
		// Belt-and-suspenders: if decideRemote is ever called with a
		// nil cli (e.g. transport not yet established), fall to remote
		// rather than panicking.
		got, err := decideRemote("auto", "tcp://x:2375", nil)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !got {
			t.Errorf("remote = false, want true")
		}
	})
}
