package reporter

import (
	"fmt"
	"strings"
)

// ColorMode is how the user requested coloring be decided.
type ColorMode int

const (
	ColorAuto   ColorMode = iota // honor TTY + env
	ColorAlways                  // always emit ANSI
	ColorNever                   // never emit ANSI
)

// ParseColorMode accepts auto|always|never (case-insensitive). Empty string
// maps to ColorAuto.
func ParseColorMode(s string) (ColorMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return ColorAuto, nil
	case "always", "on", "true":
		return ColorAlways, nil
	case "never", "off", "false":
		return ColorNever, nil
	}
	return ColorAuto, fmt.Errorf("invalid color mode %q (want auto|always|never)", s)
}

// ResolveColor decides whether to emit ANSI codes.
//
// Precedence (highest first):
//  1. mode = Always or Never — explicit user choice wins.
//  2. NO_COLOR env (any non-empty value) — disables color, per https://no-color.org.
//  3. FORCE_COLOR or CLICOLOR_FORCE (any non-empty value) — enables color.
//  4. mode = Auto — color iff isTerminal.
//
// env is the lookup function (typically os.LookupEnv) so this can be tested
// without touching process state.
func ResolveColor(mode ColorMode, isTerminal bool, env func(string) (string, bool)) bool {
	switch mode {
	case ColorAlways:
		return true
	case ColorNever:
		return false
	}
	if v, ok := env("NO_COLOR"); ok && v != "" {
		return false
	}
	if v, ok := env("FORCE_COLOR"); ok && v != "" {
		return true
	}
	if v, ok := env("CLICOLOR_FORCE"); ok && v != "" {
		return true
	}
	return isTerminal
}

// palette is the set of ANSI codes used by the pretty reporter. Indirecting
// through a struct keeps NewPretty free of magic strings and makes it easy to
// disable color (just substitute a zero-value palette).
type palette struct {
	reset     string
	dim       string
	bold      string
	green     string
	red       string
	yellow    string
	cyan      string
	gray      string
	magenta   string
}

func newPalette(on bool) palette {
	if !on {
		return palette{}
	}
	return palette{
		reset:   "\033[0m",
		dim:     "\033[2m",
		bold:    "\033[1m",
		green:   "\033[32m",
		red:     "\033[31m",
		yellow:  "\033[33m",
		cyan:    "\033[36m",
		gray:    "\033[90m",
		magenta: "\033[35m",
	}
}

// wrap returns code+s+reset, or s if color is disabled.
func (p palette) wrap(code, s string) string {
	if code == "" {
		return s
	}
	return code + s + p.reset
}
