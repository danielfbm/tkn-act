package main

// The user-facing agent guide lives under docs/agent-guide/. `go:embed`
// cannot escape this source directory, so a small generator mirrors
// the tree into cmd/tkn-act/agentguide_data/ where agentguide.go
// embeds it via //go:embed all:agentguide_data.
//
// Run `make agentguide` (or `go generate ./cmd/tkn-act/`) after editing
// any file under docs/agent-guide/. The `agentguide-freshness` test
// fails CI on drift.

//go:generate go run ./internal/agentguide-gen -src ../../docs/agent-guide -dst ./agentguide_data
