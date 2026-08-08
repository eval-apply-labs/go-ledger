GO ?= go
BIN := bin

.PHONY: build test demo clean

# build compiles the node, the key generator, the demo launcher, and the wallet UI into ./bin.
build:
	$(GO) build -o $(BIN)/server ./cmd/server
	$(GO) build -o $(BIN)/keygen ./cmd/keygen
	$(GO) build -o $(BIN)/demo ./cmd/demo
	$(GO) build -C ui -o ../$(BIN)/go_ledger_ui .

# test runs the engine suite (with the race detector) and the wallet module's tests.
test:
	$(GO) test -race ./...
	cd ui && $(GO) test ./...

# demo brings up a self-contained network on this machine — a single-validator node with funded,
# named accounts, plus the wallet + admin UI pointed at it. Open the URL it prints; Ctrl-C to stop.
demo: build
	$(BIN)/demo

clean:
	rm -rf $(BIN)
