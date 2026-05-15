package docker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// TestSSHTransport_BackendNew_RoundTripsAPI is the higher-level
// integration of Phase 1 (SSH dialer) + the moby SDK: a full
// Backend.New(Options{Host: "ssh://..."}) call must complete the
// dial, complete the docker API handshake (Ping + version
// negotiation), and the resulting *client.Client must round-trip a
// docker API request over the SSH transport. Without this test the
// individual pieces are covered (TestSSHDialer_HandshakeAndForward
// proves the dialer transports bytes; TestNew_HostOptionRoutesSSH
// proves Backend.New reaches the dialer) but nothing exercises the
// composed path end-to-end.
//
// Shape:
//
//	moby client ──HTTP──► [ssh channel] ──tcp──► in-process http.Server
//	                                              serving /_ping etc
//
// The SSH server accepts `direct-streamlocal@openssh.com` channels
// (the channel type that ssh.Client.Dial("unix", ...) opens) and
// pipes their bytes into a connection on the HTTP server's listener.
// To the moby client this is indistinguishable from talking to a
// dockerd unix socket over ssh.
func TestSSHTransport_BackendNew_RoundTripsAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ssh transport round-trip in -short")
	}

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv(envSSHInsecure, "1")
	t.Setenv(envRemoteDockerSocket, "/var/run/docker.sock")
	t.Setenv("DOCKER_HOST", "")
	// USER pinned even though the URL below carries an explicit
	// "test@" — defensively prevents the test from silently depending
	// on the runner's $USER if the URL is ever simplified to
	// ssh://<addr> (which would exercise parseSSHDockerHost's USER
	// fallback path).
	t.Setenv("USER", "test")

	clientPub, clientPriv := mustGenEd25519(t)
	sshDir := filepath.Join(tmp, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	clientKeyPEM := mustMarshalPrivateKey(t, clientPriv)
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519"), clientKeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
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

	// In-process HTTP server emulating the docker daemon API surface
	// that Backend.New invokes: Ping for liveness + Info for the
	// auto-detect remote-daemon probe (decideRemote). Listens on its
	// own TCP port — the SSH stub bridges channel bytes here.
	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Api-Version", "1.45")
		w.Header().Set("Ostype", "linux")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	// /info is hit by auto-detect when Options.Remote is "" or "auto".
	// Return a daemon Name that won't match os.Hostname(), so
	// decideRemote classifies remote=true. The body shape is a
	// subset of system.Info that the moby SDK decodes happily.
	mux.HandleFunc("/info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Name":"tkn-act-test-stub-daemon"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// The moby SDK negotiates an API version and may prefix
		// paths with /v1.NN/. Strip the prefix and dispatch.
		if strings.HasPrefix(r.URL.Path, "/v") {
			if i := strings.Index(r.URL.Path[1:], "/"); i > 0 {
				r.URL.Path = r.URL.Path[1+i:]
				mux.ServeHTTP(w, r)
				return
			}
		}
		http.NotFound(w, r)
	})
	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer httpLn.Close()
	httpSrv := &http.Server{
		Handler: mux,
		// Suppress "http: Server closed" on Close() so it doesn't
		// noise up test output when the goroutine drains.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go func() { _ = httpSrv.Serve(httpLn) }() // ErrServerClosed expected on teardown
	defer httpSrv.Close()

	sshLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sshLn.Close()

	// SSH accept loop. One connection is enough for this test —
	// Backend.New will Ping once then return.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, err := sshLn.Accept()
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
			// Bridge: open a TCP conn to the in-process HTTP server
			// and pipe bytes both directions, then close. The moby
			// client speaks plain HTTP over this; the HTTP server
			// answers /_ping.
			upstream, err := net.Dial("tcp", httpLn.Addr().String())
			if err != nil {
				_ = ch.Close()
				continue
			}
			go func() {
				_, _ = io.Copy(upstream, ch)
				_ = upstream.(*net.TCPConn).CloseWrite()
			}()
			go func() {
				_, _ = io.Copy(ch, upstream)
				_ = ch.CloseWrite()
			}()
		}
	}()

	dockerHost := "ssh://test@" + sshLn.Addr().String()
	be, err := New(Options{Host: dockerHost})
	if err != nil {
		t.Fatalf("Backend.New(ssh://) failed end-to-end: %v", err)
	}

	// Re-Ping through the live client to confirm the transport remains
	// usable after New's internal Ping. A regression that closed the
	// channel after the first request would fail here. Accesses the
	// unexported be.cli directly because Backend has no public method
	// that takes a ctx and re-pings — this test lives in `package
	// docker` precisely so the in-process round-trip can be asserted
	// without growing the public API surface.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := be.cli.Ping(ctx); err != nil {
		t.Fatalf("Ping over ssh transport: %v", err)
	}

	// Close the moby client so the keep-alive idle timeout doesn't
	// hold the SSH conn open; that lets the accept-loop goroutine
	// exit promptly when we close the listener. Without this, the
	// test would still pass but wait ~30s for the idle to expire.
	_ = be.cli.Close()
	_ = sshLn.Close()
	wg.Wait()
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
