# Developer entry points. `make check` is the same gate CI runs
# (.github/workflows/ci.yml calls these targets step by step).
# Written for GNU Make 3.81, the version macOS ships.

.DEFAULT_GOAL := help

GO ?= go
# ci.yml runs `make vuln`, so this is the only govulncheck version pin.
GOVULNCHECK_VERSION ?= v1.9.0
# Package directories, which leaves out dot-directories such as nested
# worktrees under .claude/worktrees/ that `gofmt -l .` would descend into.
GO_DIRS = $(shell $(GO) list -e -f '{{.Dir}}' ./... | sed 's|^$(CURDIR)/||')
NODE_TESTS = $(wildcard .github/scripts/*.test.mjs .claude/hooks/*.test.mjs apps/console/jstest/*.test.mjs apps/console/tools/ui-audit/core/*.test.mjs apps/console/tools/ui-audit/core/lib/*.test.mjs apps/site/*.test.mjs deploy/install/*.test.mjs deploy/packaging/*.test.mjs)

# Build identity stamped into internal/buildinfo (GET /api/v1/system). Each
# falls back quietly when git or the .git directory is missing, such as in a
# source tarball: version reads dev and the commit stays empty.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS = -X amdl/internal/buildinfo.Version=$(VERSION) -X amdl/internal/buildinfo.Commit=$(COMMIT) -X amdl/internal/buildinfo.BuildDate=$(BUILD_DATE)

DEV_DIR := data/dev
DEV_LISTEN := 127.0.0.1:18080
DOCKER_IMAGE ?= amdl-backend:dev

.PHONY: help build run dev fmt fmt-check vet tui-web test race vuln node-test check temaridiff docker dist dist-check desktop desktop-web site

help: ## List the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{ printf "  %-10s %s\n", $$1, $$2 }'

build: ## Compile every package, then bin/amdl with the version stamped from git
	$(GO) build ./...
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/amdl ./apps/amdl

run: ## Run the server with configs/config.yaml (needs a wrapper for real downloads)
	$(GO) run -ldflags "$(LDFLAGS)" ./apps/amdl serve

dev: ## Run the server in simulate mode on 127.0.0.1:18080 with a throwaway config and data under data/dev/
	@mkdir -p $(DEV_DIR)
	@printf 'catalog:\n  language: "en-US"\ndownload:\n  downloads_dir: "%s"\n  temp_dir: "%s"\n  album_path_format: "albums/{ArtistName}/{AlbumName}/{SongNumber:02d}. {SongName}"\n  artist_path_format: "artists/{ArtistName}/{AlbumName}/{SongNumber:02d}. {SongName}"\nsimulate:\n  enabled: true\n' \
		$(DEV_DIR)/downloads $(DEV_DIR)/tmp > $(DEV_DIR)/config.yaml
	@test -f $(DEV_DIR)/hooks.yaml || cp configs/hooks.yaml $(DEV_DIR)/hooks.yaml
	AMDL_CONFIG=$(DEV_DIR)/config.yaml \
	AMDL_HOOKS_CONFIG=$(DEV_DIR)/hooks.yaml \
	AMDL_SERVER_LISTEN=$(DEV_LISTEN) \
	AMDL_DATABASE_PATH=$(DEV_DIR)/amdl.db \
	$(GO) run -ldflags "$(LDFLAGS)" ./apps/amdl serve

fmt: ## Rewrite Go files with gofmt
	@test -n "$(strip $(GO_DIRS))" || { echo "fmt: go list found no packages" >&2; exit 1; }
	gofmt -w $(GO_DIRS)

fmt-check: ## Fail if any Go file is not gofmt-formatted
	@test -n "$(strip $(GO_DIRS))" || { echo "fmt-check: go list found no packages" >&2; exit 1; }
	@out="$$(gofmt -l $(GO_DIRS))"; \
	if [ -n "$$out" ]; then \
		echo "These files are not gofmt-formatted; run make fmt:" >&2; \
		echo "$$out" >&2; \
		exit 1; \
	fi

vet: ## go vet every package, and the desktop app's browser build
	$(GO) vet ./...
	GOOS=js GOARCH=wasm $(GO) vet ./apps/desktop/...

tui-web: ## Build the terminal interface's browser demo into apps/amdl/build/web/
	$(GO) run ./apps/amdl/web/build -out apps/amdl/build/web -ldflags "-s -w $(LDFLAGS)"

test: ## Run the test suite
	$(GO) test ./...

race: ## Run the test suite under the race detector
	$(GO) test -race ./...

vuln: ## Scan dependencies with govulncheck
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

node-test: ## Run the Node tests for release tooling, the guard hook, console JS and the website build
	@command -v node >/dev/null 2>&1 || { echo "node-test: node is not installed; install Node.js 26 or newer (CI uses 26)" >&2; exit 1; }
	@if [ -z "$(strip $(NODE_TESTS))" ]; then echo "node-test: no *.test.mjs files found"; exit 1; fi
	node --test $(NODE_TESTS)

check: fmt-check build vet tui-web test race vuln node-test ## The CI gate: everything above, in CI's order

temaridiff: ## Compare internal/fairplay with upstream Temari (own CI job; downloads Temari's cdylib)
	cd tools/temaridiff && $(GO) test -count=1 ./...

docker: ## Build the server image locally (single arch, not pushed)
	docker build -f deploy/server/Dockerfile -t $(DOCKER_IMAGE) .

# The platforms the release attaches an amdl binary for. modernc.org/sqlite is
# pure Go, so CGO_ENABLED=0 cross-compiles each from any host.
DIST_TARGETS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

dist: ## Cross-compile amdl for every release platform into dist/, one archive each plus SHA256SUMS
	@rm -rf dist && mkdir -p dist
	@for t in $(DIST_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; name=amdl_$(VERSION)_$${os}_$${arch}; ext=; \
		if [ "$$os" = windows ]; then ext=.exe; fi; \
		echo "dist: $$name"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "-s -w $(LDFLAGS)" -o dist/$$name/amdl$$ext ./apps/amdl || exit 1; \
		cp LICENSE README.md dist/$$name/ || exit 1; \
		if [ "$$os" = windows ]; then (cd dist && zip -qr $$name.zip $$name) || exit 1; \
		else tar -C dist -czf dist/$$name.tar.gz $$name || exit 1; fi; \
		rm -rf dist/$$name; \
	done
	@cd dist && { sha256sum *.tar.gz *.zip 2>/dev/null || shasum -a 256 *.tar.gz *.zip; } > SHA256SUMS

# What a release attaches: five archives, SHA256SUMS, amdl.rb and amdl.json.
DIST_FILES = 8

dist-check: ## Build and render the release files as a release does, for a made-up version, and count them
	@$(MAKE) --no-print-directory dist VERSION=v0.0.0-ci
	@node deploy/packaging/render.mjs v0.0.0-ci dist/SHA256SUMS dist
	@n=$$(ls dist | wc -l); if [ $$n -ne $(DIST_FILES) ]; then echo "dist-check: $$n files in dist/, want $(DIST_FILES)"; ls dist; exit 1; fi

# The desktop app's targets, as GOOS/GOARCH pairs or web. MyGo's CLI (the
# go.mod tool) reads apps/desktop/mygo.config.ts, which needs Node.js, and
# writes apps/desktop/build/<os>-<arch>/. Disk images need macOS and the
# Windows installer NSIS; elsewhere the CLI skips them with a note.
DESKTOP_PLATFORMS ?= $(shell $(GO) env GOOS)/$(shell $(GO) env GOARCH)
DESKTOP_MYGO = cd apps/desktop && DESKTOP_VERSION=$(patsubst v%,%,$(VERSION)) MYGO_LDFLAGS="$(LDFLAGS)" $(GO) tool mygo build

desktop: ## Package the desktop app for DESKTOP_PLATFORMS (default: this machine) into apps/desktop/build/
	$(DESKTOP_MYGO) -platform $(DESKTOP_PLATFORMS)

desktop-web: ## Build the desktop app's browser demo into apps/desktop/build/web/
	$(DESKTOP_MYGO) -platform web

# The website as GitHub Pages publishes it (.github/workflows/pages.yml): the
# desktop demo, a frame of the terminal interface from its snapshot test, the
# pages, and the Open Graph card, which needs Chrome (apps/site/og.mjs).
SITE_OUT ?= apps/site/dist
SITE_BASE ?= https://amdl.lyjw.me/

site: desktop-web ## Build the website with its demos into SITE_OUT (default apps/site/dist/)
	mkdir -p apps/site/build
	AMDL_TUI_SNAPSHOTS=$(CURDIR)/apps/site/build $(GO) test -count=1 -run '^TestEveryLineFitsTheTerminal$$' ./apps/amdl/internal/tui/
	node apps/site/build.mjs --base $(SITE_BASE) --out $(SITE_OUT) --desktop apps/desktop/build/web --tui apps/site/build/tui-100.ansi
	node apps/site/og.mjs --out $(SITE_OUT)
