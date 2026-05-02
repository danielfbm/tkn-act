package main

// agentguide_data.md is a build-time copy of the repo-root AGENTS.md so that
// `go:embed` (which cannot escape the source file's directory) can read it.
// Run `make agentguide` (or `go generate ./cmd/tkn-act`) to refresh after
// editing AGENTS.md.

//go:generate sh -c "cp ../../AGENTS.md ./agentguide_data.md"
