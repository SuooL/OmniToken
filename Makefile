# OmniToken build & release helpers. Pure Go (no CGO): cross-compiles anywhere.

VERSION ?= 0.2.0-m2
BIN     := omnitoken
DIST    := dist

GOSRC   := ./cmd ./internal

.PHONY: build test vet fmt fmt-check cover check clean release

build:
	go build -o $(BIN) ./cmd/omnitoken

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

# Cross-compile the common personal-fleet targets into dist/.
release: clean
	@mkdir -p $(DIST)
	GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o $(DIST)/$(BIN)-darwin-arm64  ./cmd/omnitoken
	GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o $(DIST)/$(BIN)-darwin-amd64  ./cmd/omnitoken
	GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o $(DIST)/$(BIN)-linux-amd64   ./cmd/omnitoken
	GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o $(DIST)/$(BIN)-linux-arm64   ./cmd/omnitoken
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o $(DIST)/$(BIN)-windows-amd64.exe ./cmd/omnitoken
	@ls -lh $(DIST)

clean:
	rm -rf $(DIST) $(BIN)
