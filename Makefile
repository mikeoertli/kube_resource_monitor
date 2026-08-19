BINARY  := krm
PKG     := github.com/mikeoertli/kube_resource_monitor
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(PKG)/internal/buildinfo.version=$(VERSION) \
	-X $(PKG)/internal/buildinfo.commit=$(COMMIT) \
	-X $(PKG)/internal/buildinfo.date=$(DATE)

.PHONY: all build install test test-race cover check fmt fmt-check vet clean demo tidy deps \
        version release-check tag

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

# ---------------------------------------------------------------------------
# Releasing
#
# The version comes from git tags, so releasing is: write the changelog entry,
# tag, push. Nothing is generated into the tree.
# ---------------------------------------------------------------------------

# What a build right now would report. Useful for confirming a tag took effect
# before you push it.
version:
	@echo "version: $(VERSION)"
	@echo "commit:  $(COMMIT)"
	@echo "date:    $(DATE)"

# Refuse to tag a release the changelog does not describe, and refuse to tag a
# dirty tree. Both produce releases nobody can reconstruct later.
release-check:
ifndef V
	$(error set V to the version being released, e.g. make tag V=0.2.0)
endif
	@case "$(V)" in v*) echo "V should not start with v (got $(V)); the tag gets the v"; exit 1;; esac
	@if [ -n "$$(git status --porcelain)" ]; then 		echo "working tree is dirty; commit or stash first"; exit 1; fi
	@if ! grep -q "^## \[$(V)\]" CHANGELOG.md; then 		echo "CHANGELOG.md has no '## [$(V)]' section."; 		echo "Move the Unreleased entries under a new heading first:"; 		echo ""; 		echo "  ## [$(V)] - $$(date -u +%Y-%m-%d)"; 		exit 1; fi
	@if git rev-parse "v$(V)" >/dev/null 2>&1; then 		echo "tag v$(V) already exists"; exit 1; fi
	@echo "ready to tag v$(V)"

# Tag a release. Push it yourself once you are happy:  git push origin v$(V)
tag: release-check check
	git tag -a "v$(V)" -m "krm v$(V)"
	@echo ""
	@echo "tagged v$(V). To publish:"
	@echo "    git push origin main --follow-tags"
	@echo ""
	@echo "Afterwards, go install $(PKG)/cmd/$(BINARY)@v$(V) reports v$(V)."
