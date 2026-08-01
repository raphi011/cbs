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

# The first listen port. Each entity gets a listener of its own, from here
# upward: the central bank, the clearing house, then one per member bank in
# registration order. One binary, one process — what multiplies is listeners.
BASE_PORT    ?= 8081

# The docker-compose Postgres. Only the `-pg` targets and `db-up`/`db-down` use
# it; everything else runs on the in-memory store and needs no database at all.
DB_URL       ?= postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable

# The DSN the server is started with. Empty — the default — is the in-memory
# store, which is the whole point: `make dev` and `make run` need no database.
# It is passed to the binary as an explicit -database flag rather than left to
# the environment so that `make dev DATABASE_URL=…` works the same way whether
# the value arrives from your shell or from the command line.
DATABASE_URL ?=

# Pick the OS default-browser opener.
UNAME := $(shell uname -s)
ifeq ($(UNAME),Darwin)
OPEN := open
else
OPEN := xdg-open
endif

.PHONY: help install build run run-pg dev dev-pg dev-split clean test test-pg test-schemas db-up db-down

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
	./bin/cbs -base-port "$(BASE_PORT)" -database "$(DATABASE_URL)" & BACK=$$!
	trap 'kill $$BACK 2>/dev/null || true' EXIT INT TERM
	$(open_when_ready)
	cd $(WEB) && npm run start

dev: install ## Run backend + frontend in watch mode, open browser
	set -euo pipefail
	go run ./cmd/server -base-port "$(BASE_PORT)" -database "$(DATABASE_URL)" & BACK=$$!
	trap 'kill $$BACK 2>/dev/null || true' EXIT INT TERM
	$(open_when_ready)
	cd $(WEB) && npm run dev

# The two above, against the docker-compose Postgres instead of store/mem: start
# the container, wait for it to accept connections, then hand off with a DSN.
#
# These exist for convenience only, and the convenience is the whole risk: the
# property worth protecting is that `dev` and `run` need no database, so the
# database belongs in a target you have to ask for by name rather than in a
# default anyone can trip over.
#
# `db-up` is a prerequisite rather than a step, so the container is already
# healthy before the server dials it; the recursive call is what exports
# DATABASE_URL into the recipe.
dev-pg: db-up ## Like `dev`, but on the docker-compose Postgres (state persists)
	$(MAKE) dev DATABASE_URL="$(DB_URL)"

run-pg: db-up ## Like `run`, but on the docker-compose Postgres (state persists)
	$(MAKE) run DATABASE_URL="$(DB_URL)"

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

# The one check that iso20022's golden documents are really schema-valid rather
# than merely round-trip-stable. It is not part of `test`, because it needs
# xmllint and the official XSDs, which are registration-walled and not this
# repository's to vendor — see iso20022/testdata/README.md.
#
# ISO20022_REQUIRE_SCHEMAS is what makes it a check rather than an aspiration:
# with it set, an absent tool or an absent schema FAILS instead of skipping. A
# skip is not a pass, and without this target there was no way for anyone who
# had the schemas to say so.
test-schemas: ## Run the iso20022 golden-file schema check, requiring xmllint and testdata/xsd
	set -euo pipefail
	ISO20022_REQUIRE_SCHEMAS=1 go test ./iso20022/ -run TestGoldenFilesValidateAgainstTheSchema -v

# The entities dev-split starts. Names, not ids: ids are generated (bank_1,
# bank_3, …) and -entity matches on a slugified name too. This list is the
# seeded scenario's; a different dataset needs its own.
ENTITIES     ?= central-bank clearing-house aurora banca-verde nordhaven credit-soleil

# One entity per process, which is the real topology and the mode -entity
# exists for. It needs a database: separate processes cannot share store/mem —
# each would hold its own — and the binary refuses rather than letting that
# fail later as a mystery. Each entity keeps the port the whole-system plan
# gave it, so the addresses are the same as `make dev`.
dev-split: db-up ## Run every entity as its own process against the container
	set -euo pipefail
	trap 'kill 0' EXIT INT TERM
	for e in $(ENTITIES); do
		go run ./cmd/server -entity "$$e" -database "$(DB_URL)" &
	done
	wait

db-up: ## Start the Postgres container and wait until it accepts connections
	set -euo pipefail
	docker compose up -d --wait postgres

db-down: ## Stop the Postgres container and delete its data
	docker compose down -v

clean: ## Remove build outputs
	rm -rf bin $(WEB)/.next
