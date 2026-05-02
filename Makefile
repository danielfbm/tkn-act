.PHONY: build test lint clean integration agentguide check-agentguide

GO ?= go
BINARY := tkn-act
LDFLAGS := -s -w -X main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build: agentguide
	CGO_ENABLED=0 $(GO) build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/tkn-act

# Refresh the embedded copy of AGENTS.md used by `tkn-act agent-guide`.
agentguide:
	cp AGENTS.md cmd/tkn-act/agentguide_data.md

# Fail if the embedded copy has drifted from AGENTS.md.
check-agentguide:
	@cmp -s AGENTS.md cmd/tkn-act/agentguide_data.md || \
	  (echo "cmd/tkn-act/agentguide_data.md is out of sync with AGENTS.md; run 'make agentguide'" >&2; exit 1)

test:
	$(GO) test -race -coverprofile=coverage.out ./...

integration:
	$(GO) test -tags=integration -race ./...

lint:
	golangci-lint run

clean:
	rm -f $(BINARY) coverage.out coverage.html
	rm -rf dist/
