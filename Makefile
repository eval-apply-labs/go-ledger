GO ?= go
BIN := bin
# The container commands: make COMPOSE=docker-compose for the older Compose, or CONTAINER=podman.
COMPOSE ?= docker compose
CONTAINER ?= docker
QUICKSTART := deploy/quickstart
# The quickstart's compose command: network.env names the engines' record files for the fetchers.
QUICKSTART_COMPOSE = $(COMPOSE) -f deploy/compose.yaml --env-file $(QUICKSTART)/network.env

.PHONY: build test demo clean images quickstart quickstart-clean

# build compiles the node, the fetcher, the key generator, the demo launcher, and the wallet UI into
# ./bin, each a static binary: the code uses only the standard library, so CGO_ENABLED=0 costs
# nothing. (test keeps cgo, which the race detector needs.)
build:
	CGO_ENABLED=0 $(GO) build -o $(BIN)/server ./cmd/server
	CGO_ENABLED=0 $(GO) build -o $(BIN)/fetcher ./cmd/fetcher
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

# images builds the container images: go-ledger, with the engine, the fetcher and keygen
# (deploy/Dockerfile), and go-ledger-ui, the wallet (deploy/ui.Dockerfile).
images:
	$(CONTAINER) build -f deploy/Dockerfile -t go-ledger .
	$(CONTAINER) build -f deploy/ui.Dockerfile -t go-ledger-ui .

# quickstart starts four banks on this machine (deploy/compose.yaml). The first time, it writes their
# keys, the genesis and the node configs into deploy/quickstart; later it starts the same network again.
quickstart: images $(QUICKSTART)/network.env
	$(QUICKSTART_COMPOSE) up -d

$(QUICKSTART)/network.env:
	$(GO) run ./cmd/quickstart -folder $(QUICKSTART)

# quickstart-clean removes the quickstart network: its containers, its Docker network, its volumes
# with the chain, and deploy/quickstart with the keys.
quickstart-clean:
	if [ -f $(QUICKSTART)/network.env ]; then $(QUICKSTART_COMPOSE) down --volumes; fi
	rm -rf $(QUICKSTART)

clean:
	rm -rf $(BIN)
