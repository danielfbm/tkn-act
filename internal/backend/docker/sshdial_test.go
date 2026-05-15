package docker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestParseSSHDockerHost covers URL parsing rules: scheme, user fallback,
// default port, missing host.
func TestParseSSHDockerHost(t *testing.T) {
	t.Setenv("USER", "fallback-user")
	tests := []struct {
		name     string
		host     string
		wantUser string
		wantAddr string
		wantErr  string
	}{
		{name: "explicit-user-and-port", host: "ssh://root@dockerd.example:2222", wantUser: "root", wantAddr: "dockerd.example:2222"},
		{name: "default-port", host: "ssh://root@dockerd.example", wantUser: "root", wantAddr: "dockerd.example:22"},
		{name: "user-fallback", host: "ssh://dockerd.example", wantUser: "fallback-user", wantAddr: "dockerd.example:22"},
		{name: "wrong-scheme", host: "tcp://dockerd.example", wantErr: "not an ssh URL"},
		{name: "missing-host", host: "ssh://", wantErr: "missing host"},
		{name: "garbage", host: "::not-a-url::", wantErr: "parse DOCKER_HOST"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			user, addr, err := parseSSHDockerHost(tc.host)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substr %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if user != tc.wantUser {
				t.Errorf("user = %q, want %q", user, tc.wantUser)
			}
			if addr != tc.wantAddr {
				t.Errorf("addr = %q, want %q", addr, tc.wantAddr)
			}
		})
	}
}

// TestNewSSHDialer_NoAuth verifies the dialer fails fast with an
// actionable message when no SSH key material is available, and that
// any userinfo present in DOCKER_HOST is redacted out of the error
// (the URL goes through shell history / `ps` once Phase 4 made
// `--docker-host` a CLI flag).
func TestNewSSHDialer_NoAuth(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv(envSSHInsecure, "1")

	_, err := newSSHDialer("ssh://user:s3cret@host:22")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ssh-add") {
		t.Errorf("err = %v, want substr about ssh-add", err)
	}
	if strings.Contains(msg, "s3cret") {
		t.Errorf("err = %v leaks userinfo password", err)
	}
}

// TestNewSSHDialer_BadURL returns errors for non-ssh / malformed hosts.
func TestNewSSHDialer_BadURL(t *testing.T) {
	for _, host := range []string{"tcp://x", "ssh://"} {
		if _, err := newSSHDialer(host); err == nil {
			t.Errorf("newSSHDialer(%q): expected error", host)
		}
	}
}

// TestLoadHostKeyCallback_Insecure short-circuits when env is set.
func TestLoadHostKeyCallback_Insecure(t *testing.T) {
	t.Setenv(envSSHInsecure, "1")
	cb, err := loadHostKeyCallback()
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if cb == nil {
		t.Fatal("callback nil")
	}
}

// TestLoadHostKeyCallback_MissingKnownHosts returns a guiding error.
func TestLoadHostKeyCallback_MissingKnownHosts(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv(envSSHInsecure, "")
	_, err := loadHostKeyCallback()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "TKN_ACT_SSH_INSECURE") {
		t.Errorf("err = %v, want hint about TKN_ACT_SSH_INSECURE", err)
	}
}

// TestSSHDialer_HandshakeAndForward starts an in-process SSH server,
// authenticates with a temp ed25519 key, opens a direct-streamlocal@openssh.com
// channel (the channel type ssh.Client.Dial("unix", ...) uses), and asserts
// the dialer round-trips bytes end-to-end.
func TestSSHDialer_HandshakeAndForward(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping SSH handshake test in -short")
	}

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv(envSSHInsecure, "1")
	t.Setenv(envRemoteDockerSocket, "/var/run/docker.sock")

	// Generate client key and place it in $HOME/.ssh/id_ed25519.
	clientPub, clientPriv := mustGenEd25519(t)
	sshDir := filepath.Join(tmp, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	clientKeyPEM := mustMarshalPrivateKey(t, clientPriv)
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519"), clientKeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	// Generate server host key.
	_, serverPriv := mustGenEd25519(t)
	serverSigner, err := ssh.NewSignerFromKey(serverPriv)
	if err != nil {
		t.Fatal(err)
	}

	clientSSHPub, err := ssh.NewPublicKey(clientPub)
	if err != nil {
		t.Fatal(err)
	}
	authorizedFP := ssh.FingerprintSHA256(clientSSHPub)

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if ssh.FingerprintSHA256(k) != authorizedFP {
				return nil, errors.New("unauthorized key")
			}
			return nil, nil
		},
	}
	cfg.AddHostKey(serverSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Accept loop runs until the test closes the listener.
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
		if err != nil {
			t.Logf("server handshake: %v", err)
			return
		}
		go ssh.DiscardRequests(reqs)
		for newCh := range chans {
			if newCh.ChannelType() != "direct-streamlocal@openssh.com" {
				_ = newCh.Reject(ssh.UnknownChannelType, "only direct-streamlocal supported")
				continue
			}
			ch, chReqs, err := newCh.Accept()
			if err != nil {
				return
			}
			go ssh.DiscardRequests(chReqs)
			// Echo what the client sends back, then close. The test
			// only needs to prove the bytes flow end-to-end.
			go func() {
				defer ch.Close()
				_, _ = io.Copy(ch, ch)
			}()
		}
	}()

	dockerHost := "ssh://test@" + ln.Addr().String()
	dial, err := newSSHDialer(dockerHost)
	if err != nil {
		t.Fatalf("newSSHDialer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dial(ctx, "unix", "/var/run/docker.sock")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	want := []byte("ping-from-dialer")
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("echo = %q, want %q", got, want)
	}
}

func mustGenEd25519(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// mustMarshalPrivateKey serializes an ed25519 key in the OpenSSH
// private-key format that ssh.ParsePrivateKey accepts.
func mustMarshalPrivateKey(t *testing.T, priv ed25519.PrivateKey) []byte {
	t.Helper()
	block, err := ssh.MarshalPrivateKey(priv, "tkn-act-test")
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block)
}
