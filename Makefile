# Ferry build, check and cross-compilation helpers.
# Keep this file compatible with GNU Make 3.81 (the version shipped with macOS).

SHELL := /bin/sh

GO ?= go
BINARY := ferry
MAIN_PKG := ./cmd/ferry
DIST_DIR := dist

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

# darwin / linux / windows, amd64 and arm64 each: six artifacts.
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

.PHONY: all fmt-check vet test i18n cross ci-local build clean help

all: ci-local

## fmt-check: fail when any file is not gofmt-clean
fmt-check:
	@unformatted=`gofmt -l .`; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed for:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi; \
	echo "fmt ok"

## vet: run the standard static checks
vet:
	$(GO) vet ./...

## test: run the whole test suite
test:
	$(GO) test ./...

## i18n: check the locale files are in sync with the code
i18n:
	$(GO) run ./scripts/check-i18n

## build: build the host binary into dist/
build:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/$(BINARY) $(MAIN_PKG)

## cross: build every released platform into dist/
cross:
	@rm -rf $(DIST_DIR)
	@mkdir -p $(DIST_DIR)
	@for platform in $(PLATFORMS); do \
		os=`echo $$platform | cut -d/ -f1`; \
		arch=`echo $$platform | cut -d/ -f2`; \
		ext=''; \
		if [ "$$os" = "windows" ]; then ext='.exe'; fi; \
		out="$(DIST_DIR)/$(BINARY)_$${os}_$${arch}$${ext}"; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o "$$out" $(MAIN_PKG) || exit 1; \
	done
	@ls -1 $(DIST_DIR)

## ci-local: everything CI runs, in one command
ci-local: fmt-check vet test i18n cross
	@echo "ci-local ok"

## clean: remove build output
clean:
	@rm -rf $(DIST_DIR)

## help: list the targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed -e 's/^## //'
