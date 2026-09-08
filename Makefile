# TwoPlacePaste — root Makefile.
#
# SHARED TOUCHPOINT (ROADMAP §3): this file is owned by Phase 0. Feature phases
# must not edit it; they open a `contract-change` issue instead.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

# Go modules in the workspace. Keep in sync with go.work.
GO_MODULES := server pkg/tppclient desktop

# Toolchain 1.26 is declared in every go.mod; `auto` lets an older local `go`
# fetch it rather than failing with a version error.
export GOTOOLCHAIN ?= auto

GO             ?= go
GOLANGCI_LINT  ?= golangci-lint

.PHONY: help
help: ## List available targets
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

## ---------------------------------------------------------------------------
## Go
## ---------------------------------------------------------------------------

.PHONY: build
build: ## Compile every Go module
	@for m in $(GO_MODULES); do \
		echo "==> build $$m"; \
		( cd "$$m" && $(GO) build ./... ); \
	done

.PHONY: test
test: ## Run tests for every Go module
	@for m in $(GO_MODULES); do \
		echo "==> test $$m"; \
		( cd "$$m" && $(GO) test -race ./... ); \
	done

.PHONY: vet
vet: ## Run go vet across every Go module
	@for m in $(GO_MODULES); do \
		echo "==> vet $$m"; \
		( cd "$$m" && $(GO) vet ./... ); \
	done

.PHONY: lint
lint: vet ## Run go vet and golangci-lint across every Go module
	@if ! command -v $(GOLANGCI_LINT) >/dev/null 2>&1; then \
		echo "golangci-lint not found; install it from https://golangci-lint.run/welcome/install/" >&2; \
		exit 1; \
	fi
	@for m in $(GO_MODULES); do \
		echo "==> lint $$m"; \
		( cd "$$m" && $(GOLANGCI_LINT) run ./... ); \
	done

.PHONY: fmt
fmt: ## Format Go sources
	@$(GO) run mvdan.cc/gofumpt@latest -l -w $(GO_MODULES) 2>/dev/null || gofmt -l -w $(GO_MODULES)

.PHONY: fmt-check
fmt-check: ## Fail if any Go source is unformatted
	@out=$$(gofmt -l $(GO_MODULES)); \
	if [ -n "$$out" ]; then echo "unformatted files:"; echo "$$out"; exit 1; fi

.PHONY: tidy
tidy: ## go mod tidy every module and sync the workspace
	@for m in $(GO_MODULES); do \
		echo "==> tidy $$m"; \
		( cd "$$m" && $(GO) mod tidy ); \
	done
	@$(GO) work sync

## ---------------------------------------------------------------------------
## Protobuf (schema lands in Phase 1; this target is the stable entry point)
## ---------------------------------------------------------------------------

.PHONY: proto
proto: ## Regenerate protobuf bindings for Go and TypeScript
	@if [ -x proto/generate.sh ]; then \
		./proto/generate.sh; \
	elif [ -d proto ]; then \
		echo "proto/ exists but proto/generate.sh is missing or not executable" >&2; \
		exit 1; \
	else \
		echo "no proto/ directory yet — the schema lands in Phase 1 (ROADMAP P1)"; \
	fi

.PHONY: proto-check
proto-check: ## Fail if regenerating protobuf bindings changes any committed file
	@$(MAKE) proto
	@if [ -n "$$(git status --porcelain)" ]; then \
		echo "generated protobuf output is stale; run 'make proto' and commit the result" >&2; \
		git --no-pager diff --stat; \
		exit 1; \
	fi

## ---------------------------------------------------------------------------
## Running
## ---------------------------------------------------------------------------

.PHONY: run-server
run-server: ## Run the relay server from source
	@if [ ! -d server/cmd/tpp ]; then \
		echo "server/cmd/tpp does not exist yet — the entrypoint lands in Phase 3e (ROADMAP P3e)" >&2; \
		exit 1; \
	fi
	@cd server && $(GO) run ./cmd/tpp

.PHONY: clean
clean: ## Remove build output
	@rm -rf dist
	@for m in $(GO_MODULES); do ( cd "$$m" && $(GO) clean ./... ); done
