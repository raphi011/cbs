// Command server runs the core-banking REST API.
//
// It opens a store, builds a payment.Network over it, seeds it with a
// comprehensive sample dataset (multiple banks, accounts, payments, clearing
// cycles and settlements) and serves it over HTTP. The store is store/mem, so
// state lives only in memory and is rebuilt on restart; it can also be reset to
// the sample dataset at runtime via POST /admin/reset. This is a learning and
// prototyping tool, not a production service.
//
// Seeding is idempotent, so the same wiring works against a store that outlives
// the process: a restart against a populated store leaves the data alone.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/seed"
	"github.com/raphi011/cbs/store/mem"
)

func main() {
	addr := flag.String("addr", defaultAddr(), "listen address (host:port)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// One store backs every layer: the network takes a payment.Store and
	// derives the ledger and deposit views from it, so the three cannot end up
	// addressing different data.
	data := seed.New()
	store := mem.New(data.Now)
	defer func() {
		if err := store.Close(); err != nil {
			log.Error("closing the store", "error", err)
		}
	}()

	net := payment.NewNetwork(store.Payment(), data.Now)
	if err := data.Populate(context.Background(), net); err != nil {
		log.Error("seeding the sample dataset", "error", err)
		os.Exit(1)
	}

	srv := api.NewServer(net, data.Populate, log)

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Run the server until an interrupt/terminate signal, then shut down
	// gracefully so in-flight requests can finish.
	go func() {
		log.Info("listening", "addr", *addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	log.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutCtx); err != nil {
		log.Error("graceful shutdown failed", "error", err)
	}
}

// defaultAddr reads the PORT environment variable (the common convention for
// hosted environments) and falls back to :8080.
func defaultAddr() string {
	if p := os.Getenv("PORT"); p != "" {
		return ":" + p
	}
	return ":8080"
}
