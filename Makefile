# DistCache developer tooling. Run `make help` for targets.
.PHONY: build test test-e2e test-all lint cover bench microbench docker help

help:
	@echo "targets: build test test-e2e test-all lint cover bench microbench docker"

build:
	go build -o bin/distcache-server ./cmd/distcache-server
# The bench binary is added in Task 15.

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
	@echo "added in Task 15"

microbench:
	@echo "added in Task 16"

docker:
	docker build -t distcache:dev .
