// Package runstore manages the on-disk record of past tkn-act runs:
// the state directory, the index of runs, per-run metadata, and the
// JSON event stream replayed by `tkn-act logs`.
package runstore

import (
	"os"
	"path/filepath"
)

// ResolveStateDir returns the directory where tkn-act stores per-run
// state. Precedence: flag override > TKN_ACT_STATE_DIR env >
// $XDG_DATA_HOME/tkn-act > $HOME/.local/share/tkn-act. The returned
// path is not created.
func ResolveStateDir(flag string) string {
	if flag != "" {
		return flag
	}
	if env := os.Getenv("TKN_ACT_STATE_DIR"); env != "" {
		return env
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "tkn-act")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "tkn-act")
	}
	return filepath.Join(os.TempDir(), "tkn-act")
}
