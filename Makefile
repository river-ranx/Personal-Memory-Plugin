GO     ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
PREFIX ?= $(HOME)/.local
AGENTS ?= claude codex antigravity

.PHONY: build test install uninstall

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
