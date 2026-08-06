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
// Without DATABASE_URL (or -database) the server runs on an ephemeral SQLite
// database: zero setup, and every restart starts from the seeded scenario again.
// With one, -database is a FILE PATH and the data outlives the process.
//
// The flag keeps its name and its meaning barely changes, but what it takes
// does: it used to be a Postgres DSN, and the store behind it used to be
// store/mem when it was empty. Neither the server nor the developer needs a
// database server now — nothing in `make dev`, `make run` or `go test ./...`
// ever did, and now nothing anywhere does.
//
// Seeding is idempotent, so the same wiring serves both: Populate creates the
// scenario against an empty store and returns without touching a populated one,
// which is what makes a restart against a file a no-op rather than a second copy
// of every bank.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/mesh"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/seed"
	"github.com/raphi011/cbs/store/sqlite"
)

// meshConfig names the two institutions this process plays.
//
// They are configured rather than read, because neither has a participant row:
// member banks are admitted and carry their own BIC, and the central bank and
// the clearing house ARE the configuration. The two values are real-shaped and
// deliberately different from each other and from every seeded bank — one BIC in
// two roles would be one routing-table entry, which mesh.Config.validate
// refuses.
var meshConfig = mesh.Config{
	CentralBankBIC:   "CBSEDEFFXXX",
	ClearingHouseBIC: "CSMXFRPPXXX",
}

// meshShutdown bounds the drain and the stop at shutdown.
//
// It is generous, and the reason is in mesh.Stop's own doc: the deadline covers
// the handler in flight PLUS the whole depth of every inbox at the moment the
// inboxes close, because queued messages are DELIVERED rather than discarded.
// Sizing it against one handler would be sizing it against the optimistic case.
const meshShutdown = 30 * time.Second

// store is the store, narrowed to what this command needs.
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
	basePort := flag.Int("base-port", defaultBasePort(), "first listen port; the central bank takes it, the clearing house the next, then one per bank")
	database := flag.String("database", os.Getenv("DATABASE_URL"), "SQLite file path; empty uses an ephemeral in-memory database")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

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

	nets := payment.NewNetworks(st.Payment(), data.Now)

	// The mesh starts BEFORE the seed, and the order is load-bearing — it is the
	// reverse of the order this process used until admission became a
	// conversation. The seed admits its banks through the mesh's own door, the
	// same door POST /members goes through, so the two institutions that answer
	// an application have to be running before there is anything to answer.
	//
	// What that costs is Start's roster read, which finds an empty roster on a
	// fresh store: the banks it would have registered are the ones the seed is
	// about to admit, and each of those gets its actor from Mesh.Admit as it is
	// founded. Against a database file that already holds the scenario the
	// read finds the whole roster and the seed builds nothing, which is the same
	// division of labour seen from the other side.
	msh, err := mesh.New(nets, meshConfig, log)
	if err != nil {
		log.Error("building the mesh", "error", err)
		os.Exit(1)
	}
	// The mesh's lifetime is the process's, not any request's: every handler runs
	// with this context and Stop is what cancels it.
	if err := msh.Start(context.Background()); err != nil {
		log.Error("starting the mesh", "error", err)
		os.Exit(1)
	}

	if err := data.Populate(context.Background(), nets, msh); err != nil {
		log.Error("seeding the sample dataset", "error", err)
		os.Exit(1)
	}

	srv := api.NewServer(nets, msh, data.Populate, log)

	entities, err := plan(context.Background(), nets.ClearingHouse(), *basePort)
	if err != nil {
		log.Error("planning the listeners", "error", err)
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

	// Drain FIRST, then Stop, and neither return value is thrown away.
	//
	// Stop closes every inbox in one step before it joins anybody, so a
	// conversation still in flight when it runs is CUT — the payer debited and the
	// pacs.002 that would have told their bank never sent. Draining is the only
	// way one finishes, and it leaves Stop nothing to do but join. The listeners
	// are already down by this point, so nothing new can arrive while it runs.
	//
	// Both hand back what handlers had nobody to report to. A shutdown that
	// swallowed those would be the silent failure the whole dead-letter design
	// exists to prevent, reintroduced at the last line of the process.
	// A deadline each, and not one shared between them. A Drain that used the
	// whole budget would hand Stop an already-expired context, so the step that
	// JOINS the goroutines would fail instantly — turning a slow drain into a
	// process that exits with actors still running, which is the outcome the
	// ordering above exists to prevent.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), meshShutdown)
	defer drainCancel()
	if err := msh.Drain(drainCtx); err != nil {
		log.Error("mesh: dead letters at shutdown", "error", err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), meshShutdown)
	defer stopCancel()
	if err := msh.Stop(stopCtx); err != nil {
		log.Error("mesh: stopping", "error", err)
	}
}

// openStore opens the database at path, or an ephemeral one when path is empty.
// Open applies the embedded migrations either way, so a fresh file is usable
// straight away.
//
// There is nothing to redact from the log line any more, and that is why the
// function that used to do it is gone. A Postgres DSN routinely arrived from the
// environment carrying a real credential, and the line recording which database
// was opened was the easiest place in this process to leak one; a filesystem
// path carries no secret, so the guard has nothing left to guard and saying so
// is better than keeping a function nothing needs.
func openStore(ctx context.Context, path string, clock func() time.Time, log *slog.Logger) (store, error) {
	st, err := sqlite.Open(ctx, path, clock)
	if err != nil {
		return nil, err
	}
	if path == "" {
		log.Info("using an ephemeral in-memory database; state resets on restart")
	} else {
		log.Info("using the database file", "path", path)
	}
	return st, nil
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
