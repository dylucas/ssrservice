SHELL := /bin/bash

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILT   ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X github.com/ydong/ssrservice/internal/version.Version=$(VERSION) \
	-X github.com/ydong/ssrservice/internal/version.Commit=$(COMMIT) \
	-X github.com/ydong/ssrservice/internal/version.BuildTime=$(BUILT)

GO      ?= go
NPM     ?= npm
# Optional alternate npm cache directory (useful in sandboxes or CI where the
# global cache is not writable).
NPM_CACHE ?=
NPM_FLAGS := --no-audit --no-fund
ifneq ($(NPM_CACHE),)
NPM_FLAGS += --cache $(NPM_CACHE)
endif
BIN     := bin/sspanel

.PHONY: all build build-go web test test-web race vet check bench clean run smoke dist

all: build

build: web
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/sspanel

# Backend only, using the currently embedded frontend (or the placeholder).
build-go:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/sspanel

web:
	cd web && $(NPM) ci $(NPM_FLAGS) && $(NPM) run build

test:
	$(GO) test ./...

test-web:
	cd web && $(NPM) test

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

check: vet race
	cd web && $(NPM) test && $(NPM) run typecheck

# Micro-benchmarks for the data plane and accounting hot paths.
bench:
	$(GO) test -run '^$$' -bench . -benchmem ./internal/ssserver

run: build
	./$(BIN) run --config ./config.yaml

smoke: build
	./scripts/smoke.sh

# Cross-compile static binaries for common Linux targets.
dist: web
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/sspanel-linux-amd64 ./cmd/sspanel
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/sspanel-linux-arm64 ./cmd/sspanel

clean:
	rm -rf bin dist
