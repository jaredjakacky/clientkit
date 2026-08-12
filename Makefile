SHELL := /bin/sh
.ONESHELL:
.SHELLFLAGS := -eu -c

GO ?= go
GO_MODULE ?= env GOWORK=off $(GO)
GOFMT ?= gofmt

PKGS ?= ./...
COVER_PKGS ?= $(shell $(GO_MODULE) list $(PKGS))
GOFILES := $(filter-out $(shell git ls-files --deleted -- '*.go'),$(shell git ls-files -- '*.go'))
GOVULNCHECK_VERSION ?= v1.6.0
RELEASE_CHECK_DIR := tools/releasecheck

# Keep build cache inside the repo so local runs are reproducible and do not
# depend on a writable global cache path.
export GOCACHE ?= $(CURDIR)/.cache/go-build

.DEFAULT_GOAL := help

.PHONY: \
	help \
	fmt \
	fmt-check \
	vet \
	test \
	test-race \
	coverage \
	root-deps-check \
	tidy \
	tidy-check \
	govulncheck \
	verify \
	clean

help: ## Show available targets.
	@printf "Available targets:\n"
	awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_.-]+:.*##/ {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: ## Format tracked Go source files.
	@echo "==> formatting"
	$(GOFMT) -w $(GOFILES)

fmt-check: ## Verify tracked Go source files are formatted.
	@echo "==> checking formatting"
	out="$$( $(GOFMT) -l $(GOFILES) )"
	if [ -n "$$out" ]; then
		echo "The following files are not formatted:"
		echo "$$out"
		exit 1
	fi

vet: ## Run go vet on all packages.
	@echo "==> vet"
	$(GO_MODULE) vet $(PKGS)
	$(GO_MODULE) -C $(RELEASE_CHECK_DIR) vet ./...

test: ## Run tests for all packages.
	@echo "==> test"
	$(GO_MODULE) test $(PKGS)
	$(GO_MODULE) -C $(RELEASE_CHECK_DIR) test ./...

test-race: ## Run tests with the race detector enabled.
	@echo "==> test race"
	$(GO_MODULE) test -race $(PKGS)
	$(GO_MODULE) -C $(RELEASE_CHECK_DIR) test -race ./...

coverage: ## Run library package tests with coverage output written to coverage.out.
	@echo "==> coverage"
	$(GO_MODULE) test -coverprofile=coverage.out $(COVER_PKGS)
	$(GO_MODULE) tool cover -func=coverage.out | tail -1

root-deps-check: ## Verify the root package compiles only Clientkit, Opskit, and the standard library.
	@echo "==> checking root dependency boundary"
	raw_deps="$$( $(GO_MODULE) list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' . )"
	deps="$$( printf '%s\n' "$$raw_deps" | sed '/^$$/d' | sort )"
	expected="$$( printf '%s\n' \
		'github.com/jaredjakacky/clientkit' \
		'github.com/jaredjakacky/opskit' )"
	if [ "$$deps" != "$$expected" ]; then
		echo "Unexpected non-standard-library packages in the root build:"
		printf '%s\n' "$$deps"
		exit 1
	fi

tidy: ## Synchronize module files for all verified modules.
	@echo "==> tidy"
	$(GO_MODULE) mod tidy
	$(GO_MODULE) -C $(RELEASE_CHECK_DIR) mod tidy

tidy-check: ## Verify go.mod/go.sum are already tidy.
	@echo "==> checking tidy"
	$(GO_MODULE) mod tidy -diff
	$(GO_MODULE) -C $(RELEASE_CHECK_DIR) mod tidy -diff

govulncheck: ## Run the pinned govulncheck tool against all verified modules.
	@echo "==> govulncheck"
	$(GO_MODULE) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) $(PKGS)
	$(GO_MODULE) -C $(RELEASE_CHECK_DIR) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

verify: fmt-check root-deps-check vet test tidy-check ## Run the local verification suite.
	@echo "==> verification passed"

clean: ## Remove local build outputs and caches.
	@echo "==> clean"
	rm -rf .cache coverage.out
