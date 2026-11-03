package docker

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// TestSSHDialer_HandshakeDeadline: a host that accepts TCP but never
// speaks SSH used to hang ssh.NewClientConn forever (Timeout only
// covers the TCP connect). The handshake is now bounded.
func TestSSHDialer_HandshakeDeadline(t *testing.T) {
	old := sshHandshakeTimeout
	sshHandshakeTimeout = 200 * time.Millisecond
	defer func() { sshHandshakeTimeout = old }()

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv(envSSHInsecure, "1")
	_, priv := mustGenEd25519(t)
	if err := os.MkdirAll(filepath.Join(tmp, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".ssh", "id_ed25519"), mustMarshalPrivateKey(t, priv), 0o600); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		// Hold every connection open and say nothing; release them
		// when the listener closes at test end.
		var held []net.Conn
		defer func() {
			for _, c := range held {
				_ = c.Close()
			}
		}()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held = append(held, c)
		}
	}()

	dial, err := newSSHDialer("ssh://u@" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := dial(context.Background(), "tcp", "")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "ssh handshake") {
			t.Errorf("err = %v, want handshake timeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SSH handshake against a silent host did not time out")
	}

	// Cancellation aborts promptly even with a long handshake budget.
	sshHandshakeTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	go func() {
		_, err := dial(ctx, "tcp", "")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("want error on cancelled handshake")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled handshake did not abort")
	}
}

// TestKnownHostKeyAlgorithms: the client must request a host-key
// algorithm known_hosts lists for the host (golang/go#29286).
func TestKnownHostKeyAlgorithms(t *testing.T) {
	edPub, _ := mustGenEd25519(t)
	edKey, err := ssh.NewPublicKey(edPub)
	if err != nil {
		t.Fatal(err)
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ssh.NewPublicKey(&ec.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	kh := filepath.Join(t.TempDir(), "known_hosts")
	lines := knownhosts.Line([]string{"[build.example]:2222"}, ecKey) + "\n" +
		knownhosts.Line([]string{"[build.example]:2222"}, edKey) + "\n" +
		knownhosts.Line([]string{"other.example"}, edKey) + "\n"
	if err := os.WriteFile(kh, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	cb, err := knownhosts.New(kh)
	if err != nil {
		t.Fatal(err)
	}
	got := knownHostKeyAlgorithms(cb, "build.example:2222", &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 2222})
	want := []string{ssh.KeyAlgoECDSA256, ssh.KeyAlgoED25519}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("algos = %v, want %v", got, want)
	}
	if got := knownHostKeyAlgorithms(cb, "unknown.example:22", nil); got != nil {
		t.Errorf("unknown host algos = %v, want nil", got)
	}
	if got := knownHostKeyAlgorithms(ssh.InsecureIgnoreHostKey(), "x:22", nil); got != nil {
		t.Errorf("insecure algos = %v, want nil", got)
	}
}

func TestAlgorithmsForKeyType(t *testing.T) {
	if got := algorithmsForKeyType(ssh.KeyAlgoRSA); !reflect.DeepEqual(got, []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}) {
		t.Errorf("rsa = %v", got)
	}
	if got := algorithmsForKeyType(ssh.CertAlgoRSAv01); len(got) != 3 {
		t.Errorf("rsa cert = %v", got)
	}
	if got := algorithmsForKeyType(ssh.KeyAlgoED25519); !reflect.DeepEqual(got, []string{ssh.KeyAlgoED25519}) {
		t.Errorf("ed25519 = %v", got)
	}
	var p probeKey
	if p.Type() == "" || len(p.Marshal()) == 0 || p.Verify(nil, nil) == nil {
		t.Error("probeKey methods")
	}
}
