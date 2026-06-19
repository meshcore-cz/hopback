SHELL := /bin/bash

HOST ?= 127.0.0.1
PORT ?= 5173
BIN_DIR ?= bin
JS_DIR ?= .
REMOTE ?= origin
RELEASE_BRANCH ?= main
APP_VERSION ?= $(shell sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' package.json | head -n 1)
GO_LDFLAGS := -X github.com/meshcore-cz/hopback/internal/buildinfo.Version=$(APP_VERSION)
RELEASE_VERSION := $(patsubst v%,%,$(VERSION))

.PHONY: help version config agent-env env install dev server start agent stack stack-web format check lint test test-js test-go build build-frontend build-server build-agent release verify clean

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "Hopback targets:\n"} /^[a-zA-Z0-9_-]+:.*##/ {printf "  make %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

version: ## Show the app version from package.json
	@echo $(APP_VERSION)

config: ## Create config.yaml for backend/web when missing
	@test -f config.yaml || cp config.yaml.example config.yaml

agent-env: ## Create .env for the lightweight agent when missing
	@test -f .env || cp .env.example .env

env: config agent-env ## Create local config and agent env files when missing

install: ## Install npm dependencies
	npm install

dev: ## Run Vite frontend on HOST/PORT
	npm run dev -- --host $(HOST) --port $(PORT)

server: ## Run Go backend on 127.0.0.1:3000
	go run -ldflags "$(GO_LDFLAGS)" ./cmd/hopbackd

start: ## Run compiled Go backend from BIN_DIR
	./$(BIN_DIR)/hopbackd

agent: ## Run the meshcore-go IPC agent
	go run -ldflags "$(GO_LDFLAGS)" ./cmd/hopback-agent

stack: ## Run backend, frontend, and agent together
	@echo "Starting Hopback Go backend at http://127.0.0.1:3000"
	@echo "Starting Hopback frontend at http://$(HOST):$(PORT)"
	@echo "Starting Hopback agent from .env"
	@trap 'kill 0' INT TERM EXIT; \
		$(MAKE) --no-print-directory server & server_pid=$$!; \
		$(MAKE) --no-print-directory dev & web_pid=$$!; \
		$(MAKE) --no-print-directory agent & agent_pid=$$!; \
		while kill -0 $$server_pid 2>/dev/null && kill -0 $$web_pid 2>/dev/null && kill -0 $$agent_pid 2>/dev/null; do sleep 1; done; \
		kill $$server_pid $$web_pid $$agent_pid 2>/dev/null; \
		wait $$server_pid $$web_pid $$agent_pid

stack-web: ## Run frontend only
	$(MAKE) --no-print-directory dev

format: ## Format frontend files
	npm run format

check: ## Run frontend type checks
	npm run check

lint: ## Run frontend lint checks
	npm run lint

test-js: ## Run JS unit tests
	npm run test

test-go: ## Run Go tests
	go test ./cmd/...

test: test-js test-go ## Run JS and Go tests

build-frontend: ## Build embedded frontend assets
	npm run build:frontend:embed

build-server: build-frontend ## Build Go backend with embedded frontend
	mkdir -p $(BIN_DIR)
	go build -ldflags "$(GO_LDFLAGS)" -o $(BIN_DIR)/hopbackd ./cmd/hopbackd

build-agent: ## Build Go agent
	mkdir -p $(BIN_DIR)
	go build -ldflags "$(GO_LDFLAGS)" -o $(BIN_DIR)/hopback-agent ./cmd/hopback-agent

build: build-server build-agent ## Build embedded frontend, backend, and agent

release: ## Check, commit, tag, and push a release, for example make release VERSION=v0.9.1
	@test -n "$(VERSION)" || { \
		echo "Missing VERSION. Example: make release VERSION=v0.9.1"; \
		exit 1; \
	}
	@echo "$(VERSION)" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$$' || { \
		echo "Invalid VERSION: $(VERSION). Expected format: v0.9.1"; \
		exit 1; \
	}
	@test "$$(git branch --show-current)" = "$(RELEASE_BRANCH)" || { \
		echo "Release must be created from the $(RELEASE_BRANCH) branch"; \
		exit 1; \
	}
	@test -z "$$(git status --porcelain)" || { \
		echo "Working tree is not clean"; \
		exit 1; \
	}
	@git fetch --quiet $(REMOTE) $(RELEASE_BRANCH) --tags
	@test "$$(git rev-parse HEAD)" = "$$(git rev-parse $(REMOTE)/$(RELEASE_BRANCH))" || { \
		echo "Local $(RELEASE_BRANCH) is not synchronized with $(REMOTE)/$(RELEASE_BRANCH)"; \
		exit 1; \
	}
	@! git rev-parse "$(VERSION)" >/dev/null 2>&1 || { \
		echo "Tag $(VERSION) already exists"; \
		exit 1; \
	}
	cd $(JS_DIR) && npm version --no-git-tag-version "$(RELEASE_VERSION)"
	$(MAKE) --no-print-directory check test-go
	git add $(JS_DIR)/package.json
	git commit -m "chore: release $(VERSION)"
	git tag -a "$(VERSION)" -m "hopback $(VERSION)"
	git push $(REMOTE) $(RELEASE_BRANCH) "$(VERSION)"
	@echo "Released $(VERSION)"

verify: format check lint test build ## Format, check, lint, test, and build

clean: ## Remove generated frontend and binary artifacts
	rm -rf .svelte-kit build web/build $(BIN_DIR)
