// Command server runs the core-banking REST API.
//
// It builds a single in-memory payment.Network (with the SEPA Credit Transfer
// and SEPA Direct Debit schemes pre-registered) and serves it over HTTP. State
// lives only in memory, so it resets on restart — this is a learning and
// prototyping tool, not a production service.
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

	"github.com/raphi011/ledger/api"
	"github.com/raphi011/ledger/payment"
)

func main() {
	addr := flag.String("addr", defaultAddr(), "listen address (host:port)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	net := payment.NewNetwork()
	srv := api.NewServer(net, log)

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
