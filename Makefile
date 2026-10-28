.PHONY: build clean scanner run test test-unit test-integration

BIN_DIR := build
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
ifeq ($(strip $(VERSION)),)
override VERSION := dev
endif
LDFLAGS := -ldflags "-X github.com/neutry/pvdu/internal/cmd.version=$(VERSION)"

build: scanner
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BIN_DIR)/pvdu ./cmd/pvdu/

scanner:
	@mkdir -p cmd/pvdu/scanners
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o cmd/pvdu/scanners/dirwalker-linux-amd64 github.com/NeutryFD/dirwalker/cmd/dirwalker
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o cmd/pvdu/scanners/dirwalker-linux-arm64 github.com/NeutryFD/dirwalker/cmd/dirwalker

run: build
	$(BIN_DIR)/pvdu $(ARGS)

test: test-unit test-integration

test-unit:
	go test ./internal/...
	go test ./testing/ -run 'Pod|ScannerExecCommand'

test-integration: scanner
	go test ./testing/...

clean:
	rm -rf $(BIN_DIR) cmd/pvdu/dirwalker cmd/pvdu/scanners
