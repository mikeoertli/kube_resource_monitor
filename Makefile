BINARY  := krm
PKG     := github.com/mikeoertli/kube_resource_monitor
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(PKG)/internal/kube.version=$(VERSION) \
	-X $(PKG)/internal/kube.commit=$(COMMIT) \
	-X $(PKG)/internal/kube.date=$(DATE)

.PHONY: all build install test test-race cover check fmt fmt-check vet clean demo tidy deps

all: check build

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/$(BINARY)

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/$(BINARY)

test:
	go test ./...

# Race detection matters here: watch mode collects on a background goroutine
# while the UI reads the previous snapshot.
test-race:
	go test -race ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

fmt:
	gofmt -w ./cmd ./internal

vet:
	go vet ./...

check: fmt-check vet test

fmt-check:
	@out="$$(gofmt -l ./cmd ./internal)"; \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

tidy deps:
	go mod tidy

demo: build
	./bin/$(BINARY) --demo -A

clean:
	rm -rf bin coverage.out
