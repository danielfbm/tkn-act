package refresolver

import (
	"archive/tar"
	"bytes"
	"strings"
	"testing"
)

// TestLoopbackHostHelpers (B6): only real loopback IPs and "localhost"
// count as loopback — never a DNS name that merely starts with "127.".
func TestLoopbackHostHelpers(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.1:8080", true},
		{"127.5.6.7:1", true},
		{"localhost", true},
		{"LOCALHOST:5000", true},
		{"[::1]:1234", true},
		{"::1", true},
		{"127.evil.example", false},
		{"127.evil.example:443", false},
		{"127.0.0.1.evil.example", false},
		{"localhost.evil.example", false},
		{"example.com", false},
		{"203.0.113.1:80", false},
		{"", false},
	} {
		if got := isLoopbackHost(tc.host); got != tc.want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
		if got := isLoopbackRegistryHost(tc.host); got != tc.want {
			t.Errorf("isLoopbackRegistryHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// TestRedactURL (B4): userinfo and query strings are masked; URLs
// without secrets pass through; unparsable "://" URLs still lose their
// userinfo.
func TestRedactURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://example.com/a.yaml", "https://example.com/a.yaml"},
		{"https://u:p@example.com/a.yaml", "https://***@example.com/a.yaml"},
		{"https://example.com/a.yaml?token=x", "https://example.com/a.yaml?REDACTED"},
		{"git@github.com:org/repo.git", "git@github.com:org/repo.git"},
	} {
		if got := redactURL(tc.in); got != tc.want {
			t.Errorf("redactURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	got := redactURL("https://u:SECRET@exa mple.com/%zz?t=SECRET")
	if strings.Contains(got, "SECRET") {
		t.Errorf("unparsable URL leaked secret: %q", got)
	}
}

// TestReadFirstTarFileLimit (B7): a decompressed layer file larger than
// MaxResolvedBytes is refused rather than buffered without bound.
func TestReadFirstTarFileLimit(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	size := int64(MaxResolvedBytes + 1)
	if err := tw.WriteHeader(&tar.Header{Name: "big.yaml", Mode: 0o644, Size: size}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(bytes.Repeat([]byte("a"), int(size))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := readFirstTarFile(&buf)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want size-limit error", err)
	}
}
