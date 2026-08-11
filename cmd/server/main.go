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

	"github.com/raphi011/cbs/calendar"
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
// two roles would be one routing-table entry, which MeshConfig.validate
// refuses.
var meshConfig = MeshConfig{
	CentralBankBIC:   "CBSEDEFFXXX",
	ClearingHouseBIC: "CSMXFRPPXXX",
}

// meshShutdown bounds the drain and the stop at shutdown.
//
// It is generous, and the reason is in Mesh.Stop's own doc: the deadline covers
// the handler in flight PLUS the whole depth of every inbox at the moment the
// inboxes close, because queued messages are DELIVERED rather than discarded.
// Sizing it against one handler would be sizing it against the optimistic case.
const meshShutdown = 30 * time.Second

func main() {
	basePort := flag.Int("base-port", defaultBasePort(), "first listen port; the central bank takes it, the clearing house the next, then one per bank")
	database := flag.String("database", os.Getenv("DATABASE_URL"), "directory holding one SQLite database per institution; empty uses ephemeral in-memory ones")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// The clock comes FIRST, because everything below is dated from it: the
	// stores, the networks and the sample dataset all read this one function, and
	// time.Now appears nowhere under any of them.
	//
	// It is read out of the database directory, so a deployment restarted
	// mid-timeline resumes on the day it left rather than rewinding to the
	// anchor. With no directory there is nowhere to keep it and it starts at the
	// anchor every time, which is the same bargain the ephemeral store makes.
	clock, err := calendar.OpenClock(*database, seed.BaseDate)
	if err != nil {
		log.Error("opening the business date", "error", err)
		os.Exit(1)
	}

	data := seed.New(clock)
	stores, err := openStores(context.Background(), *database, clock.Now, log)
	if err != nil {
		log.Error("opening the stores", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := stores.Close(); err != nil {
			log.Error("closing the stores", "error", err)
		}
	}()

	nets := payment.NewNetworks(stores, clock.Now)

	// The mesh starts BEFORE the seed, and the order is load-bearing. The seed
	// gives each bank it provisions an actor and pulls each one's routing
	// directory, both through the mesh's own doors, so the transport has to be
	// running before there is anything to run against.
	//
	// What that costs is Start's roster read, which finds an empty roster on a
	// fresh store: the banks it would have registered are the ones the seed is
	// about to provision, and each of those gets its actor from Mesh.AddBank as
	// it is built. Against a database file that already holds the scenario the
	// read finds the whole roster and the seed builds nothing, which is the same
	// division of labour seen from the other side.
	msh, err := NewMesh(nets, meshConfig, log)
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

	dep := NewDeployment(nets, msh, data.Populate, log)

	entities, err := plan(context.Background(), stores, nets, *basePort)
	if err != nil {
		log.Error("planning the listeners", "error", err)
		os.Exit(1)
	}

	shutdown, err := serve(context.Background(), entities, dep, log)
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

// openStores opens every institution's database under dir, or an ephemeral set
// when dir is empty. OpenSet applies each shape's embedded migrations either
// way, so a fresh directory is usable straight away.
//
// There is nothing to redact from the log line: a filesystem path carries no
// secret, where a database DSN routinely arrives from the environment carrying a
// real credential.
func openStores(ctx context.Context, dir string, clock func() time.Time, log *slog.Logger) (*sqlite.Set, error) {
	set, err := sqlite.OpenSet(ctx, dir, clock)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		log.Info("using ephemeral in-memory databases, one per institution; state resets on restart")
	} else {
		log.Info("using the database directory", "path", dir)
	}
	return set, nil
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
