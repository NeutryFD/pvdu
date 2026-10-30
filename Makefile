.DEFAULT_GOAL := help
.PHONY: help build scanner run fmt vet lint tidy test test-unit test-integration clean

BIN_DIR := build
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
ifeq ($(strip $(VERSION)),)
override VERSION := dev
endif
LDFLAGS := -ldflags "-X github.com/neutry/pvdu/internal/cli.version=$(VERSION)"

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: scanner ## Build the pvdu binary with embedded scanners
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BIN_DIR)/pvdu ./cmd/pvdu/

scanner: ## Build the Linux amd64 and arm64 scanner payloads
	@mkdir -p cmd/pvdu/scanners
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o cmd/pvdu/scanners/dirwalker-linux-amd64 github.com/NeutryFD/dirwalker/cmd/dirwalker
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o cmd/pvdu/scanners/dirwalker-linux-arm64 github.com/NeutryFD/dirwalker/cmd/dirwalker

run: build ## Run pvdu (pass flags with ARGS="...")
	$(BIN_DIR)/pvdu $(ARGS)

fmt: ## Format Go sources
	gofmt -w $$(git ls-files '*.go')

vet: ## Run go vet
	go vet ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

tidy: ## Tidy go.mod and go.sum
	go mod tidy

test: test-unit test-integration ## Run all tests

test-unit: ## Run tests that need no cluster
	go test ./internal/...
	go test ./testing/ -run 'Pod|ScannerExecCommand'

test-integration: scanner ## Run the full test suite
	go test ./testing/...

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) cmd/pvdu/dirwalker cmd/pvdu/scanners
