# OmniToken build & release helpers. Pure Go (no CGO): cross-compiles anywhere.

# Derived from git so a binary always reports what it was actually built from,
# instead of a literal somebody has to remember to bump. `--always` falls back
# to a commit hash before the first tag; `--dirty` marks uncommitted builds.
# Outside a git checkout (source tarball) this yields "dev".
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BIN     := omnitoken
DIST    := dist
LDFLAGS := -X main.version=$(VERSION)

GOSRC   := ./cmd ./internal

.PHONY: build test vet fmt fmt-check cover check clean release desktop desktop-check \
        desktop-sync desktop-sync-check

# Files the web panel and the menubar popover share verbatim (ADR-0014).
# web/ is the source of truth; desktop/ui/ holds copies.
SHARED_UI := tokens.css format-core.js

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/omnitoken

test:
	go test ./...

vet:
	go vet ./...

# Rewrite anything gofmt disagrees with.
fmt:
	gofmt -w $(GOSRC)

# Fail if any file is unformatted. Part of `check` so "gofmt clean" is an
# enforced gate rather than an aspiration nobody runs.
fmt-check:
	@unformatted="$$(gofmt -l $(GOSRC))"; \
	if [ -n "$$unformatted" ]; then \
		echo "以下文件不合 gofmt,运行 make fmt 修复:"; \
		echo "$$unformatted" | sed 's/^/  /'; \
		exit 1; \
	fi
	@echo "gofmt: clean"

# Runs the full suite and enforces a coverage floor on the packages that
# generate event_id. See scripts/coverage-gate.sh for why only those.
cover:
	@./scripts/coverage-gate.sh

# The gate every change must pass, locally and in CI. Keep this target and the
# CI workflow in sync by having CI call this target — never by duplicating the
# commands. fmt-check runs first: it is the cheapest and most mechanical.
check: fmt-check vet cover build

# Menubar client (ADR-0008). Kept out of `check` on purpose: the server is
# pure Go and a contributor without a Rust toolchain should still be able to
# run the full gate. Run these when touching desktop/.
desktop:
	cd desktop/src-tauri && cargo build

desktop-check: desktop-sync-check
	cd desktop/src-tauri && cargo fmt --check && cargo clippy -- -D warnings && cargo test

# Copy the shared design tokens and formatters into the popover.
# Run after editing the web/ originals; never edit the copies.
desktop-sync:
	@for f in $(SHARED_UI); do cp web/$$f desktop/ui/$$f && echo "  synced $$f"; done

# Fail if a copy has drifted from its source. The problem with a copy was never
# that it exists — it is that nobody notices when it changes: desktop/ui's
# stylesheet claimed to mirror web/style.css for a whole milestone after it had
# stopped doing so. Same stance as fmt-check: a convention that can be checked
# mechanically should not rely on anyone remembering it.
desktop-sync-check:
	@drift=""; \
	for f in $(SHARED_UI); do \
		if ! cmp -s web/$$f desktop/ui/$$f; then drift="$$drift $$f"; fi; \
	done; \
	if [ -n "$$drift" ]; then \
		echo "以下共享文件与 web/ 下的来源不一致,运行 make desktop-sync 修复:"; \
		for f in $$drift; do echo "  desktop/ui/$$f"; done; \
		exit 1; \
	fi
	@echo "shared ui: in sync"

# Cross-compile the common personal-fleet targets into dist/.
release: clean
	@mkdir -p $(DIST)
	GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "-s -w $(LDFLAGS)" -o $(DIST)/$(BIN)-darwin-arm64  ./cmd/omnitoken
	GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags "-s -w $(LDFLAGS)" -o $(DIST)/$(BIN)-darwin-amd64  ./cmd/omnitoken
	GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "-s -w $(LDFLAGS)" -o $(DIST)/$(BIN)-linux-amd64   ./cmd/omnitoken
	GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "-s -w $(LDFLAGS)" -o $(DIST)/$(BIN)-linux-arm64   ./cmd/omnitoken
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w $(LDFLAGS)" -o $(DIST)/$(BIN)-windows-amd64.exe ./cmd/omnitoken
	@ls -lh $(DIST)

clean:
	rm -rf $(DIST) $(BIN)
