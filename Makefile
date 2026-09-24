SHELL := /bin/sh

BIN := bin/tomovee
PKG := ./cmd/tomovee

# Version embedded into the binary. Release builds get the release tag (e.g.
# 0.2.0); a git snapshot gets "<last released tag>-<commits since>-g<short sha>"
# (plus -dirty when the tree has uncommitted changes). Override with
# `make VERSION=x.y.z build`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build frontend test vet fmt fmt-check clean version run e2e

all: frontend build

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)

version:
	@echo $(VERSION)

# Rebuild the embedded Svelte SPA (requires Node.js and npm).
frontend:
	cd internal/webui && npm ci && npm run build

# End-to-end tests for the web UI. Requires Node.js, the Cypress browser
# dependencies (Xvfb on headless machines), and a freshly built binary; runs
# the mocked-API rendering specs and then the real-binary smoke specs.
e2e: build
	cd internal/webui && npm run e2e:mocked && npm run e2e:smoke

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w ./cmd ./internal

fmt-check:
	@files=$$(gofmt -l ./cmd ./internal); \
	if [ -n "$$files" ]; then \
		echo "gofmt needed on:"; echo "$$files"; exit 1; \
	fi

clean:
	rm -rf bin

run: build
	$(BIN) serve
