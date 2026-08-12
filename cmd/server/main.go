package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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

// The two institutions this process plays, by address.
const (
	centralBankBIC   = "CBSEDEFFXXX"
	clearingHouseBIC = "CSMXFRPPXXX"
)

func main() {
	basePort := flag.Int("base-port", defaultBasePort(), "first listen port; the central bank takes it, the clearing house the next, then one per bank")
	database := flag.String("database", os.Getenv("DATABASE_URL"), "directory holding one SQLite database per institution; empty uses ephemeral in-memory ones")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// The clock comes FIRST, because everything below is dated from it: the
	// stores, the networks and the sample dataset all read this one function, and
	// time.Now appears nowhere under any of them.
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

	// The two URLs are derived here rather than configured, because hostPlan below
	// gives the central bank the base port and the clearing house the next: a
	// second source for those two numbers would be a second thing to keep in step.
	cfg := Config{
		CentralBankBIC:   centralBankBIC,
		ClearingHouseBIC: clearingHouseBIC,
		CentralBankURL:   ebicsURL(*basePort),
		ClearingHouseURL: ebicsURL(*basePort + 1),
	}
	dep, err := NewDeployment(context.Background(), nets, stores, clock, cfg, data.Populate, log)
	if err != nil {
		log.Error("building the deployment", "error", err)
		os.Exit(1)
	}

	// The two HOSTS first, and the sample dataset between them and the banks.
	shutdownHosts, err := serve(context.Background(), hostPlan(*basePort), dep, log)
	if err != nil {
		log.Error("starting the institutions' listeners", "error", err)
		os.Exit(1)
	}

	if err := data.Populate(context.Background(), nets, dep); err != nil {
		log.Error("seeding the sample dataset", "error", err)
		os.Exit(1)
	}

	banks, err := bankPlan(context.Background(), stores, nets, *basePort)
	if err != nil {
		log.Error("planning the banks' listeners", "error", err)
		os.Exit(1)
	}
	shutdownBanks, err := serve(context.Background(), banks, dep, log)
	if err != nil {
		log.Error("starting the banks' listeners", "error", err)
		os.Exit(1)
	}
	shutdown := func(ctx context.Context) error {
		return errors.Join(shutdownBanks(ctx), shutdownHosts(ctx))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	// There is nothing to drain and nothing to join. No goroutine runs under this
	// process but the listeners' own: work is queued by requests and performed by
	// AdvanceDay, on the caller's goroutine.
	log.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(shutCtx); err != nil {
		log.Error("graceful shutdown failed", "error", err)
	}
}

// ebicsURL is the file-transfer endpoint on one institution's listener.
func ebicsURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, ebicsPath)
}

// openStores opens every institution's database under dir, or an ephemeral set
// when dir is empty. OpenSet applies each shape's embedded migrations either
// way, so a fresh directory is usable straight away.
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
