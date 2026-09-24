SHELL := /bin/sh

BIN := bin/tomovee
PKG := ./cmd/tomovee

# Version embedded into the binary. Release builds get the release tag (e.g.
# 0.2.0); a git snapshot gets "<last released tag>-<commits since>-g<short sha>"
# (plus -dirty when the tree has uncommitted changes). Override with
# `make VERSION=x.y.z build`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build frontend test vet fmt fmt-check clean version run e2e coverage

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

# Instrumented binary and coverage scratch dirs. GOCOVERDIR flushes counter
# data when the daemon shuts down gracefully (SIGTERM) at the end of the smoke
# run, so the real-binary e2e specs contribute to the report.
BIN_CVR := bin/tomovee-cov
COVER_DIR := tmp/coverage

coverage: $(BIN_CVR)
	rm -rf $(COVER_DIR) coverage.out
	mkdir -p $(COVER_DIR)/unit $(COVER_DIR)/e2e $(COVER_DIR)/merged
	go test -cover ./... -args -test.gocoverdir=$(abspath $(COVER_DIR)/unit)
	cd internal/webui && GOCOVERDIR=$(abspath $(COVER_DIR)/e2e) TOMOVEE_BIN=$(abspath $(BIN_CVR)) npm run e2e:smoke
	go tool covdata merge -i=$(abspath $(COVER_DIR)/unit),$(abspath $(COVER_DIR)/e2e) -o=$(abspath $(COVER_DIR)/merged)
	go tool covdata textfmt -i=$(abspath $(COVER_DIR)/merged) -o=coverage.out
	rm -rf $(COVER_DIR)

$(BIN_CVR):
	go build -cover -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_CVR) $(PKG)

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
