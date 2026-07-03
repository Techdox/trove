# Trove — build and dev tasks.
# Pure Go (modernc.org/sqlite, no CGO), so cross-compilation is trivial and
# every binary is static.

SERVER := trove-server
AGENT  := trove-agent-docker
BIN    := bin

# Static, reproducible-ish builds.
GOFLAGS   := -trimpath
LDFLAGS   := -s -w
export CGO_ENABLED := 0

# Cross-compile matrix required by the Definition of Done.
PLATFORMS := linux/amd64 linux/arm64

.DEFAULT_GOAL := build

## build: cross-compile both binaries for linux/amd64 and linux/arm64
.PHONY: build
build:
	@mkdir -p $(BIN)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "  building $(SERVER) $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS)" \
			-o $(BIN)/$(SERVER)-$$os-$$arch ./cmd/$(SERVER); \
		echo "  building $(AGENT) $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS)" \
			-o $(BIN)/$(AGENT)-$$os-$$arch ./cmd/$(AGENT); \
	done
	@echo "built -> $(BIN)/"

## native: build both binaries for the host platform
.PHONY: native
native:
	@mkdir -p $(BIN)
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN)/$(SERVER) ./cmd/$(SERVER)
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN)/$(AGENT)  ./cmd/$(AGENT)

## run: run the server locally (TROVE_DB=./trove.db)
.PHONY: run
run:
	go run ./cmd/$(SERVER)

## test: run all tests
.PHONY: test
test:
	go test ./...

## vet: static checks
.PHONY: vet
vet:
	go vet ./...

## fmt: gofmt the tree
.PHONY: fmt
fmt:
	gofmt -l -w .

## tidy: sync go.mod/go.sum
.PHONY: tidy
tidy:
	go mod tidy

## docker: build both container images
.PHONY: docker
docker:
	docker build -f Dockerfile.server -t trove-server:dev .
	docker build -f Dockerfile.agent  -t trove-agent-docker:dev .

## up: bring up the local dev stack (server + agent)
.PHONY: up
up:
	docker compose up --build

## down: tear down the dev stack and its volume
.PHONY: down
down:
	docker compose down -v

## clean: remove build artifacts and the local dev database
.PHONY: clean
clean:
	rm -rf $(BIN) trove.db trove.db-shm trove.db-wal

## help: list targets
.PHONY: help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'
