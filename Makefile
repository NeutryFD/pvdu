.PHONY: build clean scanner run test test-unit test-integration

BIN_DIR := build

build: scanner
	CGO_ENABLED=0 go build -o $(BIN_DIR)/pvdu ./cmd/pvdu/
	rm -f cmd/pvdu/dirwalker

scanner:
	@mkdir -p cmd/pvdu
	CGO_ENABLED=0 go build -o cmd/pvdu/dirwalker github.com/NeutryFD/dirwalker/cmd/dirwalker

run: build
	$(BIN_DIR)/pvdu $(ARGS)

test: test-unit test-integration

test-unit:
	go test ./internal/...
	go test ./testing/ -run 'Pod|ScannerExecCommand'

test-integration: scanner
	go test ./testing/...
	rm -f cmd/pvdu/dirwalker

clean:
	rm -rf $(BIN_DIR) cmd/pvdu/dirwalker
