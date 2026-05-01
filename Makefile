.PHONY: build test lint clean integration

GO ?= go
BINARY := tkn-act
LDFLAGS := -s -w -X main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	CGO_ENABLED=0 $(GO) build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/tkn-act

test:
	$(GO) test -race -coverprofile=coverage.out ./...

integration:
	$(GO) test -tags=integration -race ./...

lint:
	golangci-lint run

clean:
	rm -f $(BINARY) coverage.out coverage.html
	rm -rf dist/
