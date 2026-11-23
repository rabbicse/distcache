# DistCache developer tooling. Run `make help` for targets.
.PHONY: build test test-e2e test-all lint cover bench microbench docker docker-multiarch dist docs-commands help

help:
	@echo "targets: build test test-e2e test-all lint cover bench microbench docker docker-multiarch dist docs-commands"

build:
	go build -o bin/distcache-server ./cmd/distcache-server
	go build -o bin/distcache-bench ./cmd/distcache-bench

test:
	go test -race -count=1 ./...

# test/e2e is a separate module (it depends on go-redis; the server stays stdlib-only).
test-e2e:
	cd test/e2e && go test -race -count=1 ./...

test-all: test test-e2e

lint:
	golangci-lint run ./...

cover:
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

bench:
	./scripts/bench.sh

microbench:
	go test -run '^$$' -bench . -benchmem ./internal/server ./internal/resp ./internal/storage

docker:
	docker build -f deploy/docker/Dockerfile -t distcache:dev .

# Multi-architecture image (linux/amd64 + linux/arm64). Needs docker buildx;
# add "--push" through DOCKER_FLAGS to publish, for example:
#   make docker-multiarch IMAGE=ghcr.io/rabbicse/distcache:1.0.0 DOCKER_FLAGS=--push
IMAGE ?= distcache:dev
DOCKER_FLAGS ?=
docker-multiarch:
	docker buildx build --platform linux/amd64,linux/arm64 -f deploy/docker/Dockerfile -t $(IMAGE) $(DOCKER_FLAGS) .

# Release archives for Linux, macOS and Windows in dist/ (see scripts/release.sh).
dist:
	./scripts/release.sh

# Regenerate docs/commands.md from the command table.
docs-commands:
	go run ./scripts/gencommands > docs/commands.md
