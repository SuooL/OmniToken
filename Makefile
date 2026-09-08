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
        desktop-sync desktop-sync-check desktop-install

# Files the web panel and the menubar popover share verbatim (ADR-0014).
# web/ is the source of truth; desktop/ui/ holds copies.
SHARED_UI := tokens.css format-core.js

# Where the menubar app runs from on macOS, and where the autostart job points.
DESKTOP_APP    := /Applications/OmniToken.app
DESKTOP_BUNDLE := desktop/src-tauri/target/release/bundle/macos/OmniToken.app
DESKTOP_PLIST  := $(HOME)/Library/LaunchAgents/OmniToken.plist

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
	node --check desktop/ui/app.js
	node --test desktop/ui/app.test.js
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

# Build the menubar app and put the result where it actually runs from.
#
# This target exists because `cargo tauri build` writes only to
# target/release/bundle — it does NOT touch /Applications. Upgrading by hand
# therefore has a silent failure mode: build succeeds, app keeps running the old
# code, and nothing anywhere says so. That happened — the bundle in use was four
# hours older than a commit that changed desktop/ui, and it went unnoticed for
# two weeks. Same stance as desktop-sync-check: a step that can be mechanised
# should not depend on anyone remembering it.
#
# ditto rather than rm -rf + cp: it overwrites in place, which keeps the app's
# code-signature metadata intact and avoids the window where /Applications holds
# no app at all.
#
# `--bundles app` skips the .dmg. Nothing here installs from a disk image, and
# building one is not free of consequences: bundle_dmg.sh mounts a volume, and a
# mount left behind by an earlier build makes the next one fail. That is exactly
# how this target failed the first time it ran, with two stale /Volumes/dmg.*
# entries. A DMG belongs to distribution, not to installing on this machine.
#
# The verification at the end is the point of the target, not decoration. It
# fails loudly on the two states that look fine but are not: a second instance
# still alive, and an instance running from somewhere other than DESKTOP_APP.
desktop-install: desktop-check
	@[ "$$(uname)" = "Darwin" ] || { echo "desktop-install 只支持 macOS"; exit 1; }
	@[ -f "$(DESKTOP_PLIST)" ] || { echo "找不到自启配置 $(DESKTOP_PLIST)"; exit 1; }
	cd desktop/src-tauri && cargo tauri build --bundles app
	@[ -d "$(DESKTOP_BUNDLE)" ] || { echo "构建产物不存在: $(DESKTOP_BUNDLE)"; exit 1; }
	ditto "$(DESKTOP_BUNDLE)" "$(DESKTOP_APP)"
	@echo "--- 重启菜单栏 ---"
	@# Both are no-ops when nothing is running, and a no-op is a success here:
	@# without `|| true` make prints "Error 1 (ignored)" on a perfectly good run.
	@launchctl bootout gui/$$(id -u)/OmniToken 2>/dev/null || true
	@pkill -f "omnitoken-desktop" 2>/dev/null || true
	@for i in $$(seq 1 10); do \
		[ -z "$$(pgrep -f omnitoken-desktop)" ] && break; sleep 1; \
	done
	@launchctl bootstrap gui/$$(id -u) "$(DESKTOP_PLIST)"
	@for i in $$(seq 1 15); do \
		[ -n "$$(pgrep -f omnitoken-desktop)" ] && break; sleep 1; \
	done
	@echo "--- 验证 ---"; \
	n=$$(pgrep -f omnitoken-desktop | wc -l | tr -d ' '); \
	if [ "$$n" != "1" ]; then \
		echo "菜单栏实例数 = $$n,应为 1(两份同时在跑会出现两个托盘图标)"; exit 1; \
	fi; \
	pid=$$(pgrep -f omnitoken-desktop); \
	running=$$(ps -p $$pid -o comm=); \
	case "$$running" in \
		$(DESKTOP_APP)/*) ;; \
		*) echo "跑的是 $$running,不是 $(DESKTOP_APP)"; exit 1;; \
	esac; \
	echo "  实例数 1,PID $$pid"; \
	echo "  路径   $$running"; \
	echo "desktop-install: 完成"

# Cross-compile the common personal-fleet targets into dist/.
release: clean
	@mkdir -p $(DIST)
	GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "-s -w $(LDFLAGS)" -o $(DIST)/$(BIN)-darwin-arm64  ./cmd/omnitoken
	GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags "-s -w $(LDFLAGS)" -o $(DIST)/$(BIN)-darwin-amd64  ./cmd/omnitoken
	GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "-s -w $(LDFLAGS)" -o $(DIST)/$(BIN)-linux-amd64   ./cmd/omnitoken
	GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "-s -w $(LDFLAGS)" -o $(DIST)/$(BIN)-linux-arm64   ./cmd/omnitoken
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w $(LDFLAGS)" -o $(DIST)/$(BIN)-windows-amd64.exe ./cmd/omnitoken
	# Checksums for out-of-band distribution (install.sh verifies against this).
	# CI regenerates the same file; sha256sum on Linux, shasum on macOS.
	cd $(DIST) && { command -v sha256sum >/dev/null 2>&1 && sha256sum $(BIN)-* || shasum -a 256 $(BIN)-*; } > SHA256SUMS
	@ls -lh $(DIST)

clean:
	rm -rf $(DIST) $(BIN)
