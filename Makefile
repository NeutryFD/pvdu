.PHONY: build clean scanner pvdu run test

BIN_DIR := build

# Build both scanner and pvdu
build: scanner pvdu

# Build scanner binary to BIN_DIR
scanner:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -o $(BIN_DIR)/dirwalker github.com/neutry/dirwalker/cmd/dirwalker

# Build pvdu (reads scanner binary from build/ at runtime)
pvdu:
	CGO_ENABLED=0 go build -o $(BIN_DIR)/pvdu ./cmd/pvdu/

# Run pvdu with args
run: build
	$(BIN_DIR)/pvdu $(ARGS)

clean:
	rm -rf $(BIN_DIR)

test: scanner
	go test ./internal/... ./testing/...

# Quick rebuild (scanner + pvdu)
quick:
	CGO_ENABLED=0 go build -o $(BIN_DIR)/dirwalker github.com/neutry/dirwalker/cmd/dirwalker
	CGO_ENABLED=0 go build -o $(BIN_DIR)/pvdu ./cmd/pvdu/
