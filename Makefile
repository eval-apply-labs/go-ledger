GO ?= go
BIN := bin

.PHONY: build test demo clean

# build compiles the node, the key generator, the demo launcher, and the wallet UI into ./bin, each
# a static binary: the code uses only the standard library, so CGO_ENABLED=0 costs nothing. (test
# keeps cgo, which the race detector needs.)
build:
	CGO_ENABLED=0 $(GO) build -o $(BIN)/server ./cmd/server
	CGO_ENABLED=0 $(GO) build -o $(BIN)/keygen ./cmd/keygen
	CGO_ENABLED=0 $(GO) build -o $(BIN)/demo ./cmd/demo
	CGO_ENABLED=0 $(GO) build -C ui -o ../$(BIN)/go_ledger_ui .

# test runs the engine suite and the wallet module's suite, both with the race detector, as CI does.
test:
	$(GO) test -race ./...
	$(GO) test -C ui -race ./...

# demo brings up a self-contained network on this machine — a single-validator node with funded,
# named accounts, plus the wallet + admin UI pointed at it. Open the URL it prints; Ctrl-C to stop.
demo: build
	$(BIN)/demo

clean:
	rm -rf $(BIN)
