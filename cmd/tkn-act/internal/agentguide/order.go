// Package agentguide holds the curated order of the user-facing guide
// sections shipped by `tkn-act agent-guide`. Both the generator
// (internal/agentguide-gen) and the embedding command (agentguide.go)
// import this list so the two cannot drift.
package agentguide

// Order is the curated concatenation order for the user-guide files
// under docs/agent-guide/. Each entry is the base name without the
// `.md` suffix; "overview" is the alias for README.md.
//
// When concatenated into the default `tkn-act agent-guide` output,
// adjacent files are separated by a horizontal-rule block ("---")
// matching the existing in-section separators in the source files.
var Order = []string{
	"overview",
	"docker-backend",
	"step-template",
	"sidecars",
	"step-actions",
	"matrix",
	"pipeline-results",
	"display-name",
	"timeouts",
	"resolvers",
	"debug",
}

// FileName returns the on-disk file name for a section in Order.
// The alias "overview" maps to README.md; every other section maps
// to `<section>.md`.
func FileName(section string) string {
	if section == "overview" {
		return "README.md"
	}
	return section + ".md"
}

// Sections returns a copy of Order so callers can iterate without
// risking mutation of the package-level slice.
func Sections() []string {
	out := make([]string, len(Order))
	copy(out, Order)
	return out
}
