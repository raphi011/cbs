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

# The database file the server is started with. Empty — the default — is an
# ephemeral in-memory database, which is the whole point: `make dev` and
# `make run` need no setup and leave nothing behind. Give it a path and the state
# outlives the process.
#
# It is passed to the binary as an explicit -database flag rather than left to
# the environment so that `make dev DATABASE_URL=…` works the same way whether
# the value arrives from your shell or from the command line. It kept its name
# through the store swap; what changed is that the value is a PATH rather than a
# Postgres DSN, and that there is no longer a pair of `-pg` targets beside it,
# because there is no longer a second store to point them at.
DATABASE_URL ?=

# Pick the OS default-browser opener.
UNAME := $(shell uname -s)
ifeq ($(UNAME),Darwin)
OPEN := open
else
OPEN := xdg-open
endif

.PHONY: help install build run dev clean test test-schemas

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

# `dev-pg` and `run-pg` used to sit here, starting a docker-compose Postgres and
# handing the server its DSN. They are gone with the store they pointed at, and
# so is the reason they were kept out of `dev` and `run`: the property worth
# protecting was that the ordinary targets need no database, and it is now
# unconditional rather than defended.

test: ## Run the Go and web suites (no setup)
	set -euo pipefail
	go build ./... && go vet ./...
	test -z "$$(gofmt -l .)" || { echo "gofmt: $$(gofmt -l .)"; exit 1; }
	# One run, and nothing to clear from the environment before it. This recipe
	# used to unset TEST_DATABASE_URL explicitly, because store/testenv read it and
	# an inherited value would have turned this target into a second copy of
	# `make test-pg` for exactly the developers who needed the two runs to differ.
	# Nothing reads it now.
	go test ./...
	cd $(WEB) && npm run typecheck && npm run lint && npm run test

# The one check that iso20022's golden documents are really schema-valid rather
# than merely round-trip-stable. It is not part of `test`, because it needs
# xmllint and the official XSDs, which are registration-walled and not this
# repository's to vendor — see iso20022/testdata/README.md.
#
# ISO20022_REQUIRE_SCHEMAS is what makes it a check rather than an aspiration:
# with it set, an absent tool or an absent schema FAILS instead of skipping. A
# skip is not a pass, and without this target there was no way for anyone who
# had the schemas to say so.
test-schemas: ## Run every XSD check, requiring xmllint and iso20022/testdata/xsd
	set -euo pipefail
	ISO20022_REQUIRE_SCHEMAS=1 go test ./iso20022/ -run TestGoldenFilesValidateAgainstTheSchema -v
	ISO20022_REQUIRE_SCHEMAS=1 go test ./payment/ -run TestTheAdmissionMessagesThisSystemEmitsValidateAgainstTheSchema -v

clean: ## Remove build outputs
	rm -rf bin $(WEB)/.next
