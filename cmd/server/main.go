// Command server runs the core-banking REST API.
//
// It opens a store, builds a payment.Network over it, seeds it with a
// comprehensive sample dataset (multiple banks, accounts, payments, clearing
// cycles and settlements) and serves it over HTTP. It can also be reset to the
// sample dataset at runtime via POST /admin/reset. This is a learning and
// prototyping tool, not a production service.
//
// # Which store
//
// Without DATABASE_URL (or -database) the server runs on store/mem: zero setup,
// and every restart starts from the seeded scenario again. With one, it runs on
// store/pg and the data outlives the process. Postgres is strictly optional —
// nothing in `make dev`, `make run` or `go test ./...` needs it.
//
// Seeding is idempotent, so the same wiring serves both: Populate creates the
// scenario against an empty store and returns without touching a populated one,
// which is what makes a restart against Postgres a no-op rather than a second
// copy of every bank.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/seed"
	"github.com/raphi011/cbs/store/mem"
	"github.com/raphi011/cbs/store/pg"
)

// store is the shape both backends share, narrowed to what this command needs.
//
// There is no shared store.Store type: the three layers each declare their own
// Store interface, and Go allows a type only one Update method, so one concrete
// store presents the narrower views as adapters. Payment() is the widest of the
// three — payment.Tx embeds deposit.Tx, which embeds ledger.Tx — so handing the
// network that one view is enough for all three layers to address the same data
// inside the same unit of work.
type store interface {
	Payment() payment.Store
	Close() error
}

func main() {
	basePort := flag.Int("base-port", defaultBasePort(), "first listen port; the central bank takes it, the clearing house the next, then one per member bank")
	database := flag.String("database", os.Getenv("DATABASE_URL"), "Postgres DSN; empty uses the in-memory store")
	only := flag.String("entity", "", "serve one entity in this process (a bank id or name, `central-bank`, or `clearing-house`); requires -database")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	if err := checkEntityMode(*only, *database); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}

	data := seed.New()
	st, err := openStore(context.Background(), *database, data.Now, log)
	if err != nil {
		log.Error("opening the store", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Error("closing the store", "error", err)
		}
	}()

	net := payment.NewNetwork(st.Payment(), data.Now)
	if err := data.Populate(context.Background(), net); err != nil {
		log.Error("seeding the sample dataset", "error", err)
		os.Exit(1)
	}

	srv := api.NewServer(net, data.Populate, log)

	entities, err := plan(context.Background(), net, *basePort)
	if err != nil {
		log.Error("planning the listeners", "error", err)
		os.Exit(1)
	}
	entities, err = resolveEntities(entities, *only)
	if err != nil {
		log.Error("selecting the entity", "error", err)
		os.Exit(1)
	}

	shutdown, err := serve(entities, srv, log)
	if err != nil {
		log.Error("starting the listeners", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	log.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(shutCtx); err != nil {
		log.Error("graceful shutdown failed", "error", err)
	}
}

// openStore returns the in-memory store when dsn is empty and a migrated
// Postgres store otherwise. pg.Open connects and then applies the embedded
// migrations, so a fresh database is usable straight away.
func openStore(ctx context.Context, dsn string, clock func() time.Time, log *slog.Logger) (store, error) {
	if dsn == "" {
		log.Info("using the in-memory store; state resets on restart")
		return mem.New(clock), nil
	}
	st, err := pg.Open(ctx, dsn, clock)
	if err != nil {
		return nil, err
	}
	log.Info("using the postgres store", "dsn", redact(dsn))
	return st, nil
}

// redact removes the password from a DSN so a connection string can be logged.
// A DSN routinely arrives from the environment with a real credential in it,
// and a log line is the easiest place in a system to leak one.
//
// Two places carry it: the userinfo (which url.Redacted replaces with "xxxxx")
// and a "password" query parameter, which url.Redacted does not touch.
//
// Anything that does not parse as a URL is reported as "<redacted>" rather than
// echoed: libpq also accepts the keyword form ("host=… password=…"), and
// guessing at its shape here would risk printing the very thing this exists to
// hide.
func redact(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		return "<redacted>"
	}
	if q := u.Query(); q.Has("password") {
		q.Set("password", "xxxxx")
		u.RawQuery = q.Encode()
	}
	return u.Redacted()
}

// defaultBasePort reads the PORT environment variable (the common convention
// for hosted environments) and falls back to 8081 — one above the single
// server's old :8080, so an 8081-8086 block cannot collide with a stray one.
func defaultBasePort() int {
	if p := os.Getenv("PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			return n
		}
	}
	return 8081
}
