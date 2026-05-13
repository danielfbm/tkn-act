package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	defaultRemoteDockerSocket = "/var/run/docker.sock"
	envRemoteDockerSocket     = "TKN_ACT_DOCKER_SOCKET"
	envSSHInsecure            = "TKN_ACT_SSH_INSECURE"
	sshDialTimeout            = 10 * time.Second
)

// remoteSocketPath returns the daemon's unix socket path on the remote
// host. Default /var/run/docker.sock; override with $TKN_ACT_DOCKER_SOCKET.
func remoteSocketPath() string {
	if s := os.Getenv(envRemoteDockerSocket); s != "" {
		return s
	}
	return defaultRemoteDockerSocket
}

// newSSHDialer parses a DOCKER_HOST=ssh://[user@]host[:port] URL and
// returns a DialContext suitable for moby's client.WithDialContext. The
// returned function dials the remote unix socket over an SSH connection.
//
// Authentication is publickey-only. Methods tried, in order:
//  1. SSH_AUTH_SOCK (ssh-agent)
//  2. ~/.ssh/id_ed25519
//  3. ~/.ssh/id_rsa
//
// Password auth is intentionally not attempted — it would either hang on
// a TTY-less daemon or surface as an opaque "permission denied." When no
// key is available the dialer returns an actionable error pointing the
// user at ssh-add.
//
// Host-key verification reads ~/.ssh/known_hosts by default. Set
// TKN_ACT_SSH_INSECURE=1 to disable (CI / dind use case where the
// daemon's host key is ephemeral).
func newSSHDialer(dockerHost string) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	user, sshAddr, err := parseSSHDockerHost(dockerHost)
	if err != nil {
		return nil, err
	}

	auths, err := loadSSHAuthMethods()
	if err != nil {
		return nil, err
	}
	if len(auths) == 0 {
		return nil, fmt.Errorf("no SSH authentication available for %s: try `ssh-add <key>` or set SSH_AUTH_SOCK", dockerHost)
	}

	hostKey, err := loadHostKeyCallback()
	if err != nil {
		return nil, err
	}

	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            auths,
		HostKeyCallback: hostKey,
		Timeout:         sshDialTimeout,
	}
	sock := remoteSocketPath()

	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		d := net.Dialer{Timeout: cfg.Timeout}
		rawConn, err := d.DialContext(ctx, "tcp", sshAddr)
		if err != nil {
			return nil, fmt.Errorf("ssh tcp dial %s: %w", sshAddr, err)
		}
		sshConn, chans, reqs, err := ssh.NewClientConn(rawConn, sshAddr, cfg)
		if err != nil {
			_ = rawConn.Close()
			return nil, fmt.Errorf("ssh handshake %s: %w", sshAddr, err)
		}
		sshClient := ssh.NewClient(sshConn, chans, reqs)
		unixConn, err := sshClient.Dial("unix", sock)
		if err != nil {
			_ = sshClient.Close()
			return nil, fmt.Errorf("ssh dial remote unix socket %s: %w", sock, err)
		}
		return &sshUnixConn{Conn: unixConn, ssh: sshClient}, nil
	}, nil
}

// parseSSHDockerHost extracts the SSH user and host:port from a
// DOCKER_HOST=ssh://[user@]host[:port] URL.
//
// Errors quote the URL with userinfo redacted (url.URL.Redacted) so a
// password or token in `ssh://user:secret@host` does not surface in
// logs / shell history. The dialer is publickey-only so a password
// URL never authenticates anyway, but echoing it back is dodgy
// — Phase 4 made `--docker-host` a CLI flag, which puts URLs in
// shell history and `ps`, so the redaction is worth the cost here.
func parseSSHDockerHost(dockerHost string) (user, hostport string, err error) {
	u, err := url.Parse(dockerHost)
	if err != nil {
		return "", "", fmt.Errorf("parse DOCKER_HOST %q: %w", redactURL(dockerHost), err)
	}
	if u.Scheme != "ssh" {
		return "", "", fmt.Errorf("not an ssh URL: %q", u.Redacted())
	}
	if u.Hostname() == "" {
		return "", "", fmt.Errorf("DOCKER_HOST %q: missing host", u.Redacted())
	}
	user = u.User.Username()
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		return "", "", fmt.Errorf("DOCKER_HOST %q: no SSH user (set user@host or $USER)", u.Redacted())
	}
	port := u.Port()
	if port == "" {
		port = "22"
	}
	return user, net.JoinHostPort(u.Hostname(), port), nil
}

// redactURL returns the input with any URL userinfo replaced by
// "xxxxx" (matches net/url.URL.Redacted). Used for the parse-failure
// branch where url.Parse itself returned an error so we can't call
// u.Redacted(); falls through to the raw string when redaction
// can't safely apply.
func redactURL(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		return u.Redacted()
	}
	return raw
}

// loadSSHAuthMethods builds publickey AuthMethods from the agent (if
// SSH_AUTH_SOCK is set and reachable) and from common key files under
// ~/.ssh. Encrypted keys without a passphrase are skipped silently —
// the agent path covers that case.
func loadSSHAuthMethods() ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return methods, nil
	}
	for _, name := range []string{"id_ed25519", "id_rsa"} {
		path := filepath.Join(home, ".ssh", name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			continue
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	return methods, nil
}

// loadHostKeyCallback returns ssh.InsecureIgnoreHostKey when
// $TKN_ACT_SSH_INSECURE=1, otherwise wraps ~/.ssh/known_hosts.
func loadHostKeyCallback() (ssh.HostKeyCallback, error) {
	if os.Getenv(envSSHInsecure) == "1" {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate home dir for known_hosts: %w", err)
	}
	khPath := filepath.Join(home, ".ssh", "known_hosts")
	cb, err := knownhosts.New(khPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("known_hosts %s not found: ssh into the host once or set %s=1 to bypass", khPath, envSSHInsecure)
		}
		return nil, fmt.Errorf("load known_hosts %s: %w (set %s=1 to bypass)", khPath, err, envSSHInsecure)
	}
	return cb, nil
}

// sshUnixConn wraps the remote unix socket connection so that closing
// the docker client's connection also closes the underlying SSH client.
type sshUnixConn struct {
	net.Conn
	ssh *ssh.Client
}

func (c *sshUnixConn) Close() error {
	err := c.Conn.Close()
	_ = c.ssh.Close()
	return err
}
