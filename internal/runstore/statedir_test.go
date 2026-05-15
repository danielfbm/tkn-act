package runstore_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danielfbm/tkn-act/internal/runstore"
)

func TestResolveStateDir_PrecedenceFlag(t *testing.T) {
	t.Setenv("TKN_ACT_STATE_DIR", "/env/path")
	t.Setenv("XDG_DATA_HOME", "/xdg/data")
	got := runstore.ResolveStateDir("/flag/path")
	if got != "/flag/path" {
		t.Errorf("flag override: got %q, want /flag/path", got)
	}
}

func TestResolveStateDir_PrecedenceEnv(t *testing.T) {
	t.Setenv("TKN_ACT_STATE_DIR", "/env/path")
	t.Setenv("XDG_DATA_HOME", "/xdg/data")
	got := runstore.ResolveStateDir("")
	if got != "/env/path" {
		t.Errorf("env override: got %q, want /env/path", got)
	}
}

func TestResolveStateDir_XDGDataHome(t *testing.T) {
	t.Setenv("TKN_ACT_STATE_DIR", "")
	t.Setenv("XDG_DATA_HOME", "/xdg/data")
	got := runstore.ResolveStateDir("")
	want := filepath.Join("/xdg/data", "tkn-act")
	if got != want {
		t.Errorf("xdg: got %q, want %q", got, want)
	}
}

func TestResolveStateDir_HomeFallback(t *testing.T) {
	t.Setenv("TKN_ACT_STATE_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	got := runstore.ResolveStateDir("")
	want := filepath.Join(home, ".local", "share", "tkn-act")
	if got != want {
		t.Errorf("home fallback: got %q, want %q", got, want)
	}
}
