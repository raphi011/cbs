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

# The docker-compose Postgres. Only `db-up`, `db-down` and `test-pg` use it;
# everything else runs on the in-memory store and needs no database at all.
DB_URL       ?= postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable

# Pick the OS default-browser opener.
UNAME := $(shell uname -s)
ifeq ($(UNAME),Darwin)
OPEN := open
else
OPEN := xdg-open
endif

.PHONY: help install build run dev clean test test-pg db-up db-down

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

test: ## Run the Go and web suites against the in-memory store (no setup)
	set -euo pipefail
	go build ./... && go vet ./...
	test -z "$$(gofmt -l .)" || { echo "gofmt: $$(gofmt -l .)"; exit 1; }
	# TEST_DATABASE_URL is cleared, not merely unset by default: store/testenv
	# reads it from the environment, and the README tells anyone working on
	# store/pg to export it. Inheriting it here would silently turn this target
	# into a second copy of `make test-pg` — for exactly the developers who need
	# the two runs to differ — and "both stores stay green" would become one
	# store, twice.
	TEST_DATABASE_URL= go test ./...
	cd $(WEB) && npm run typecheck && npm run lint && npm run test

# The same suites, on the other store. TEST_DATABASE_URL is what store/testenv
# reads; every test then runs in a Postgres schema of its own. This is the only
# run in which "these writes commit or roll back together" is a claim about the
# code — under store/mem's single process-wide mutex it is true either way.
test-pg: db-up ## Run the Go suite against the docker-compose Postgres
	set -euo pipefail
	TEST_DATABASE_URL="$(DB_URL)" go test ./...

db-up: ## Start the Postgres container and wait until it accepts connections
	set -euo pipefail
	docker compose up -d --wait postgres

db-down: ## Stop the Postgres container and delete its data
	docker compose down -v

clean: ## Remove build outputs
	rm -rf bin $(WEB)/.next
