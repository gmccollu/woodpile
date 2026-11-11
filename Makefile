# Woodpile developer tasks. Build output goes to dist/ (gitignored).
#
#   make web        build the frontend into web/dist (skipped until web/package.json exists)
#   make build      web + the woodpile binary for this machine -> dist/woodpile
#   make test       go vet + go test -race
#   make fuzz       run every fuzz target for FUZZTIME (default 30s)
#   make snapshot   goreleaser release --snapshot (archives, deb, rpm; nothing published)
#   make check      goreleaser check + packaging lint
#   make pkg        macOS .pkg (macOS only)          make msi: see packaging/windows/build-msi.ps1

GO                 ?= go
GORELEASER_VERSION ?= v2.20.1
GORELEASER         ?= $(GO) run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)
FUZZTIME           ?= 30s

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
EXE     := $(shell $(GO) env GOEXE)
LDFLAGS := -s -w -X main.version=$(patsubst v%,%,$(VERSION)) -X main.commit=$(COMMIT)

# -race needs cgo; turn it off with RACE= if there's no C toolchain.
RACE ?= -race

.PHONY: all web build test vet fuzz snapshot check lint-packaging pkg clean help

all: build

help:
	@sed -n '1,10p' Makefile | sed 's/^# \{0,1\}//'

web:
	@if [ -f web/package.json ]; then \
		cd web && npm ci && npm run build; \
	else \
		echo "web/package.json not found; skipping the frontend build"; \
	fi

build: web
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/woodpile$(EXE) ./cmd/woodpile

vet:
	$(GO) vet ./...

test: vet
	$(GO) test $(RACE) ./...

fuzz:
	scripts/fuzz.sh $(FUZZTIME) ./internal/...

snapshot:
	$(GORELEASER) release --snapshot --clean --skip=publish

check: lint-packaging
	$(GORELEASER) check

lint-packaging:
	scripts/lint-packaging.sh

pkg: web
	packaging/macos/build-pkg.sh --version $(VERSION) --commit $(COMMIT)

clean:
	rm -rf dist
