.PHONY: build build-linux lint test clean run-once

BINARY_NAME=loki-prune
MODULE=github.com/blackbirdworks/loki-prune
VERSION_PKG=$(MODULE)/pkg/version
BUILD_VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE=$(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
LDFLAGS=-w -s -X $(VERSION_PKG).BuildVersion=$(BUILD_VERSION) -X $(VERSION_PKG).Commit=$(COMMIT) -X $(VERSION_PKG).BuildDate=$(BUILD_DATE)

build:
	go build \
		-trimpath \
		-ldflags "$(LDFLAGS)" \
		-o bin/$(BINARY_NAME) ./cmd/loki-prune

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
		-tags 'netgo osusergo static_build' \
		-trimpath \
		-ldflags "$(LDFLAGS) -extldflags '-static'" \
		-o bin/$(BINARY_NAME)-linux-amd64 ./cmd/loki-prune

test:
	go test -v -race ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/ dist/

run-once: build
	./bin/$(BINARY_NAME) --config config.example.yaml --once --dry-run
