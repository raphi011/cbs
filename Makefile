# Makefile for the CBS core-banking explorer.
#
# Fresh checkout → running app in your browser:
#
#     make run
#
# which installs dependencies, builds the backend + frontend, starts both, and
# opens the app once it is serving. See `make help` for all targets.

SHELL := /usr/bin/env bash
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

.PHONY: help install build run dev clean clean-db test test-schemas

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

# Everything that starts a server lives in ONE recipe line, joined by
# backslashes. That is not a style choice: the make macOS ships is 3.81 and
# `.ONESHELL` arrived in 3.82, so on a stock machine every recipe LINE is its own
# shell. A `trap` on one line and the pid it kills on another are in different
# processes, which is how a backgrounded server ends up orphaned to init the
# moment its line finishes.
#
# Three more things about stopping cleanly, none readable off the code:
#
# Neither server may be the LAST command, or bash execs into it and this shell —
# trap and all — is gone before the signal arrives. Hence the wait loop.
#
# Both are started DIRECTLY rather than through `npm run` and `go run`, because
# each of those is a parent that dies and leaves the real server behind: TERM to
# a `go run` orphans the compiled binary, still holding its port. Started
# directly, the pid held here is the pid that serves. Both scripts bypassed are
# bare `next` invocations (web/package.json); a flag added to either belongs here.
#
# The SIGKILL is not impatience. `next start` drains in-flight requests and
# latches a cleanup flag on the FIRST signal, discarding every later one
# (next/dist/server/lib/start-server.js), so a second Ctrl+C can neither hurry it
# nor give up on it. Ten seconds is the low end of the drain window Next's own
# self-hosting guide asks a platform to allow.
#
# $(1) is the `next` subcommand: the two targets differ in that and nothing else.
define serve
	set -euo pipefail; \
	BACK= FRONT= OPENER=; \
	stop() { \
		trap - EXIT INT TERM; \
		local deadline; \
		kill $$BACK $$FRONT $$OPENER 2>/dev/null || true; \
		deadline=$$((SECONDS + 10)); \
		while kill -0 $$FRONT 2>/dev/null && [ $$SECONDS -lt $$deadline ]; do sleep 0.25; done; \
		[ -n "$$FRONT" ] && pkill -9 -P $$FRONT 2>/dev/null || true; \
		kill -9 $$BACK $$FRONT 2>/dev/null || true; \
	}; \
	trap stop EXIT INT TERM; \
	./bin/cbs -base-port "$(BASE_PORT)" -database "$(DATABASE_URL)" & BACK=$$!; \
	( cd $(WEB) && exec ./node_modules/.bin/next $(1) ) & FRONT=$$!; \
	( for i in $$(seq 1 60); do \
		if curl -sf -o /dev/null "$(APP_URL)"; then $(OPEN) "$(APP_URL)"; break; fi; \
		sleep 1; \
	done ) & OPENER=$$!; \
	while kill -0 $$BACK 2>/dev/null && kill -0 $$FRONT 2>/dev/null; do sleep 0.25; done
endef

run: build ## Fresh checkout → build, start backend + frontend (prod), open browser
	$(call serve,start)

# `go run` is a build followed by a run, so building here loses nothing and costs
# one pid instead of two.
dev: install ## Run backend + frontend in watch mode, open browser
	go build -o bin/cbs ./cmd/server
	$(call serve,dev)

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
	ISO20022_REQUIRE_SCHEMAS=1 go test ./payment/ -run 'ValidateAgainstTheSchema' -v

clean: ## Remove build outputs
	rm -rf bin $(WEB)/.next

# The databases a run with a DATABASE_URL left behind, and the clock beside them.
#
# With no path there is nothing on disk to remove and this says so rather than
# succeeding quietly: the databases are in memory, the clock starts at the seed's
# base date every time, and a restart is already the reset. Exiting non-zero is
# the point — a target that shrugged would tell someone whose state survived that
# it had not.
#
# The CLOCK goes with them because it lives in the same directory and is what
# makes a deployment resume on the business date it left off (ADR-0001). Removing
# the databases alone would replay the seed's timeline under whatever date the
# last run reached, which is a deployment whose sample data is dated in its own
# future.
#
# ONE recipe line, for the reason the serve macro above is one: this make gives
# every line its own shell, so a guard on one line would not protect an `rm -rf`
# on the next.
clean-db: ## Remove the databases DATABASE_URL points at, and their clock
	@if [ -z "$(DATABASE_URL)" ]; then \
		echo "clean-db: DATABASE_URL is empty, so this deployment's databases are in memory and nothing is on disk to remove."; \
		echo "           Restart the server to reset it, or name the directory you ran with:"; \
		echo "               make clean-db DATABASE_URL=./cbs.db"; \
		exit 1; \
	elif [ ! -e "$(DATABASE_URL)" ]; then \
		echo "clean-db: $(DATABASE_URL) does not exist; nothing to remove"; \
	else \
		rm -rf -- "$(DATABASE_URL)" && echo "clean-db: removed $(DATABASE_URL)"; \
	fi
