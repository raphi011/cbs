# Makefile for the CBS core-banking explorer.
#
# Fresh checkout → running app in your browser:
#
#     make run
#
# which installs dependencies, builds the backend + frontend, starts both, and
# opens the app once it is serving. See `make help` for all targets.

SHELL := /usr/bin/env bash
.ONESHELL:
.DEFAULT_GOAL := help

WEB          := web
APP_URL      ?= http://localhost:3000
BACKEND_ADDR ?= :8080

# Pick the OS default-browser opener.
UNAME := $(shell uname -s)
ifeq ($(UNAME),Darwin)
OPEN := open
else
OPEN := xdg-open
endif

.PHONY: help install build run dev clean

help: ## Show this help
	@echo "CBS — make targets:"
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk -F':.*?## ' '{ printf "  \033[1m%-10s\033[0m %s\n", $$1, $$2 }'

install: ## Install backend (Go) and frontend (npm) dependencies
	go mod download
	cd $(WEB) && npm ci

build: install ## Install deps, then build the backend binary and the frontend
	cd $(WEB) && npm run build
	go build -o bin/cbs ./cmd/server

# Wait for the frontend to start serving, then open it in the default browser.
# Backgrounded so it runs alongside the foreground server; never fatal.
define open_when_ready
	( for i in $$(seq 1 60); do \
		if curl -sf -o /dev/null "$(APP_URL)"; then $(OPEN) "$(APP_URL)"; break; fi; \
		sleep 1; \
	done ) &
endef

run: build ## Fresh checkout → build, start backend + frontend (prod), open browser
	set -euo pipefail
	./bin/cbs -addr "$(BACKEND_ADDR)" & BACK=$$!
	trap 'kill $$BACK 2>/dev/null || true' EXIT INT TERM
	$(open_when_ready)
	cd $(WEB) && npm run start

dev: install ## Run backend + frontend in watch mode, open browser
	set -euo pipefail
	go run ./cmd/server -addr "$(BACKEND_ADDR)" & BACK=$$!
	trap 'kill $$BACK 2>/dev/null || true' EXIT INT TERM
	$(open_when_ready)
	cd $(WEB) && npm run dev

clean: ## Remove build outputs
	rm -rf bin $(WEB)/.next
