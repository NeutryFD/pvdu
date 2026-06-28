.PHONY: build clean scanner pvdu run

BIN_DIR := build

# Build both scanner and pvdu
build: scanner embed-pvdu

# Build scanner binary to BIN_DIR
scanner:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -o $(BIN_DIR)/dirwalker github.com/neutry/dirwalker/cmd/dirwalker

# Build pvdu (scanner binary must be in cmd/pvdu/ for go:embed)
embed-pvdu:
	cp $(BIN_DIR)/dirwalker cmd/pvdu/dirwalker
	CGO_ENABLED=0 go build -o $(BIN_DIR)/pvdu ./cmd/pvdu/
	@rm -f cmd/pvdu/dirwalker

# Quick rebuild (scanner + pvdu, uses local build)
quick:
	CGO_ENABLED=0 go build -o cmd/pvdu/dirwalker github.com/neutry/dirwalker/cmd/dirwalker
	CGO_ENABLED=0 go build -o $(BIN_DIR)/pvdu ./cmd/pvdu/
	@rm -f cmd/pvdu/dirwalker

# Run pvdu with args
run: build
	$(BIN_DIR)/pvdu $(ARGS)

clean:
	rm -rf $(BIN_DIR) cmd/pvdu/dirwalker

test:
	go test ./...
