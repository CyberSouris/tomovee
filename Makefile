SHELL := /bin/sh

BIN := bin/tomovee
PKG := ./cmd/tomovee

.PHONY: all build frontend test vet fmt fmt-check clean run

all: frontend build

build:
	go build -o $(BIN) $(PKG)

# Rebuild the embedded Svelte SPA (requires Node.js and npm).
frontend:
	cd internal/webui && npm ci && npm run build

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
