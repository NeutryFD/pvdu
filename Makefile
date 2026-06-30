.PHONY: build clean scanner run test

BIN_DIR := build

build: scanner
	CGO_ENABLED=0 go build -o $(BIN_DIR)/pvdu ./cmd/pvdu/
	rm -f cmd/pvdu/dirwalker

scanner:
	@mkdir -p cmd/pvdu
	CGO_ENABLED=0 go build -o cmd/pvdu/dirwalker github.com/NeutryFD/dirwalker/cmd/dirwalker

run: build
	$(BIN_DIR)/pvdu $(ARGS)

test: scanner
	go test ./internal/... ./testing/...
	rm -f cmd/pvdu/dirwalker

clean:
	rm -rf $(BIN_DIR) cmd/pvdu/dirwalker
