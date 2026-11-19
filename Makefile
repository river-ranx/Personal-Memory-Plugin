GO     ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
PREFIX ?= $(HOME)/.local
AGENTS ?= claude codex antigravity

# The platforms a release carries; install.sh picks one by `uname`.
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64

.PHONY: build test install uninstall dist

build:
	$(GO) build -o bin/memory ./cmd/memory

test:
	$(GO) vet ./... && $(GO) test ./...

# Builds into $(PREFIX)/bin and installs the skill and hooks for $(AGENTS).
install:
	$(GO) build -o $(PREFIX)/bin/memory ./cmd/memory
	$(PREFIX)/bin/memory install $(AGENTS)

uninstall:
	$(PREFIX)/bin/memory uninstall $(AGENTS)
	rm -f $(PREFIX)/bin/memory

# dist/memory-<os>-<arch> for each platform, and SHA256SUMS: what a release
# uploads. The SQLite driver is pure Go, so no C toolchain is needed.
dist:
	rm -rf dist && mkdir -p dist
	for p in $(PLATFORMS); do \
		CGO_ENABLED=0 GOOS=$${p%/*} GOARCH=$${p#*/} $(GO) build -trimpath -ldflags "-s -w" \
			-o dist/memory-$${p%/*}-$${p#*/} ./cmd/memory || exit 1; \
	done
	cd dist && shasum -a 256 memory-* > SHA256SUMS
