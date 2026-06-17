SHELL := /bin/bash

HOST ?= 127.0.0.1
PORT ?= 5173
MESHPKT_DIR ?= ../meshpkt/js

.PHONY: help config agent-env env install meshpkt-build meshpkt-use-local dev start agent stack stack-web check test lint build verify clean

help:
	@echo "Hopback targets:"
	@echo "  make config              Create config.yaml for backend/web when missing"
	@echo "  make agent-env           Create .env for the lightweight agent when missing"
	@echo "  make install             Install npm dependencies"
	@echo "  make meshpkt-build       Build local ../meshpkt/js TypeScript package"
	@echo "  make meshpkt-use-local   Install local ../meshpkt/js into Hopback"
	@echo "  make dev                 Run SvelteKit dev gateway on HOST/PORT"
	@echo "  make agent               Run the meshcore-go IPC agent"
	@echo "  make stack               Run dev gateway and agent together"
	@echo "  make stack-web           Run dev gateway only"
	@echo "  make verify              Format, check, lint, test, and build"

config:
	@test -f config.yaml || cp config.yaml.example config.yaml

agent-env:
	@test -f .env || cp .env.example .env

env: config agent-env

install:
	npm install

meshpkt-build:
	cd $(MESHPKT_DIR) && npm install && npm run build:ts

meshpkt-use-local: meshpkt-build
	npm install $(MESHPKT_DIR)

dev:
	npm run dev -- --host $(HOST) --port $(PORT)

start:
	HOST=$(HOST) PORT=$(PORT) npm run start

agent:
	npm run agent

stack:
	@echo "Starting Hopback web gateway at http://$(HOST):$(PORT)"
	@echo "Starting Hopback agent from .env"
	@trap 'kill 0' INT TERM EXIT; \
		$(MAKE) --no-print-directory dev & web_pid=$$!; \
		$(MAKE) --no-print-directory agent & agent_pid=$$!; \
		while kill -0 $$web_pid 2>/dev/null && kill -0 $$agent_pid 2>/dev/null; do sleep 1; done; \
		kill $$web_pid $$agent_pid 2>/dev/null; \
		wait $$web_pid $$agent_pid

stack-web:
	$(MAKE) --no-print-directory dev

check:
	npm run check

test:
	npm run test

lint:
	npm run lint

build:
	npm run build

verify:
	npm run format
	npm run check
	npm run lint
	npm run test
	npm run build

clean:
	rm -rf .svelte-kit build
