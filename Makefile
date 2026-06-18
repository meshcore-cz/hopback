SHELL := /bin/bash

HOST ?= 127.0.0.1
PORT ?= 5173

.PHONY: help config agent-env env install dev server start agent stack stack-web check test lint build verify clean

help:
	@echo "Hopback targets:"
	@echo "  make config              Create config.yaml for backend/web when missing"
	@echo "  make agent-env           Create .env for the lightweight agent when missing"
	@echo "  make install             Install npm dependencies"
	@echo "  make dev                 Run Vite frontend on HOST/PORT"
	@echo "  make server              Run Go backend on 127.0.0.1:3000"
	@echo "  make agent               Run the meshcore-go IPC agent"
	@echo "  make stack               Run backend, frontend, and agent together"
	@echo "  make stack-web           Run frontend only"
	@echo "  make verify              Format, check, lint, test, and build"

config:
	@test -f config.yaml || cp config.yaml.example config.yaml

agent-env:
	@test -f .env || cp .env.example .env

env: config agent-env

install:
	npm install

dev:
	npm run dev -- --host $(HOST) --port $(PORT)

server:
	npm run build:server
	npm run dev:server

start:
	HOST=$(HOST) PORT=$(PORT) npm run start

agent:
	npm run build:agent
	npm run agent

stack:
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
	rm -rf .svelte-kit build web/build
