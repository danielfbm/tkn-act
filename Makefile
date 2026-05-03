# tkn-act — repository root Makefile.
#
# Conveniences for contributors. The canonical CI gates live in
# .github/workflows/{ci,docker-integration,cluster-integration}.yml; this
# file just packages the same commands behind short, memorable targets.
#
# Quick path for a fresh checkout:
#
#   make quickstart   # doctor -> build -> cluster-up -> hello-cluster
#
# See `make help` for the full menu and AGENTS.md for the agent / JSON
# contract.

GO         ?= go
BIN_DIR    ?= bin
BINARY     ?= $(BIN_DIR)/tkn-act
LDFLAGS    := -s -w -X main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# K3D / kubectl versions pinned to match
# .github/workflows/cluster-integration.yml so contributors run the same
# versions CI does. Bump both places together.
K3D_VERSION     ?= v5.7.4
KUBECTL_VERSION ?= v1.31.0

HELLO_FIXTURE := testdata/e2e/hello/pipeline.yaml

.DEFAULT_GOAL := help

.PHONY: help build agentguide check-agentguide doctor cluster-up cluster-status \
        cluster-down hello-cluster quickstart test integration vet lint clean

## help: Show this help (default target).
help:
	@echo "tkn-act — make targets"
	@echo ""
	@awk 'BEGIN{FS=":.*## "} /^## [a-z]/ {sub(/^## /,"",$$0); split($$0,a,": "); printf "  %-18s %s\n", a[1], a[2]}' $(MAKEFILE_LIST)
	@echo ""
	@echo "First time? Run: make quickstart"

## build: Build bin/tkn-act (refreshes the embedded agent guide first).
build: agentguide
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/tkn-act

## agentguide: Refresh cmd/tkn-act/agentguide_data.md from AGENTS.md.
agentguide:
	@cp AGENTS.md cmd/tkn-act/agentguide_data.md

## check-agentguide: Fail if the embedded agent guide drifted from AGENTS.md.
check-agentguide:
	@cmp -s AGENTS.md cmd/tkn-act/agentguide_data.md || \
	  (echo "cmd/tkn-act/agentguide_data.md is out of sync with AGENTS.md; run 'make agentguide'" >&2; exit 1)

## doctor: Check docker/k3d/kubectl on PATH and run `tkn-act doctor`.
doctor: build
	@K3D_VERSION=$(K3D_VERSION) KUBECTL_VERSION=$(KUBECTL_VERSION) \
	  sh scripts/check-cluster-deps.sh
	@echo ""
	@./$(BINARY) doctor || { \
	  echo ""; \
	  echo "tkn-act doctor reports the environment is not ready."; \
	  echo "If k3d / kubectl are missing, see the install hints above."; \
	  exit 3; \
	}

## cluster-up: Boot the local k3d cluster + install Tekton (idempotent).
cluster-up: build
	@./$(BINARY) cluster up

## cluster-status: Show local k3d cluster status.
cluster-status: build
	@./$(BINARY) cluster status

## cluster-down: Tear down the local k3d cluster (-y, no prompt).
cluster-down: build
	@./$(BINARY) cluster down -y

## hello-cluster: Run testdata/e2e/hello/pipeline.yaml against the cluster.
hello-cluster: cluster-up
	@echo "==> running $(HELLO_FIXTURE) on the cluster backend"
	@./$(BINARY) run --cluster -f $(HELLO_FIXTURE) && echo "ok"

## quickstart: doctor -> build -> cluster-up -> hello-cluster (with next steps).
quickstart: doctor build cluster-up hello-cluster
	@echo ""
	@echo "Quickstart complete. Next steps:"
	@echo "  - Browse targets:        make help"
	@echo "  - Try other commands:    ./$(BINARY) {run,validate,list} --help"
	@echo "  - JSON / agent contract: see AGENTS.md (or ./$(BINARY) agent-guide)"
	@echo "  - When done, tear down:  make cluster-down"

## test: Run unit tests (race detector, no caching).
test:
	$(GO) test -race -count=1 ./...

## integration: Run -tags integration tests (requires Docker).
integration:
	$(GO) test -tags=integration -race -count=1 ./...

## vet: Run `go vet` against default + integration + cluster build tags.
vet:
	$(GO) vet ./...
	$(GO) vet -tags integration ./...
	$(GO) vet -tags cluster ./...

## lint: Run golangci-lint (if installed).
lint:
	golangci-lint run

## clean: Remove built binaries and coverage artifacts.
clean:
	@rm -rf $(BIN_DIR)
	@rm -f tkn-act coverage.out coverage.html
	@rm -rf dist/
