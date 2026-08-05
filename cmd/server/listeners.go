package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/payment"
)

// Entity keys for the two institutions. A member bank's key is its participant
// id, which is why these two are spelled out and the banks are not.
const (
	centralBankKey   = "central-bank"
	clearingHouseKey = "clearing-house"
)

// An entity is one listener: which operator it serves, on which address, and —
// for a bank — which participant it is.
type entity struct {
	key  string
	name string
	addr string
	pid  payment.ParticipantID // empty for the two institutions
}

// plan builds the listener table: the two institutions, then one listener per
// member bank in registration order, from base+2 upward.
//
// Ports are static. A participant admitted at runtime gets a store row, a chart
// of accounts and reserve accounts — and no listener until the process
// restarts. That is a decision about what admission means rather than a
// limitation to apologise for: admitting a member to a payment network is an
// operational act — a scheme agreement, a settlement account, an operator
// provisioning a connection — and an API call that instantly yielded a running
// bank would teach the wrong thing.
func plan(ctx context.Context, net *payment.Network, base int) ([]entity, error) {
	parts, err := net.ListBanks(ctx)
	if err != nil {
		return nil, err
	}
	out := []entity{
		{key: centralBankKey, name: "Central bank", addr: addrFor(base)},
		{key: clearingHouseKey, name: "Clearing house", addr: addrFor(base + 1)},
	}
	for i, p := range parts {
		out = append(out, entity{
			key:  string(p.ID),
			name: p.Name,
			addr: addrFor(base + 2 + i),
			pid:  p.ID,
		})
	}
	return out, nil
}

func addrFor(port int) string { return ":" + strconv.Itoa(port) }

// handlerFor picks the surface an entity serves.
func handlerFor(srv *api.Server, e entity) http.Handler {
	switch e.key {
	case centralBankKey:
		return srv.CentralBankRoutes()
	case clearingHouseKey:
		return srv.ClearingHouseRoutes()
	default:
		return srv.BankRoutes(e.pid)
	}
}

// serve starts every listener and returns a function that shuts them all down.
//
// Each socket is bound before any of them starts serving, so a port collision
// is a startup error the caller can report rather than a goroutine that logs
// and dies after main has already announced success. A failure to bind is fatal
// rather than survivable: a network missing one of its banks is not a degraded
// system but a wrong one, and a payment routed to the missing member would fail
// somewhere far from the cause.
func serve(entities []entity, srv *api.Server, log *slog.Logger) (func(context.Context) error, error) {
	type bound struct {
		e  entity
		ln net.Listener
		hs *http.Server
	}

	var listeners []bound
	closeAll := func() {
		for _, b := range listeners {
			_ = b.ln.Close()
		}
	}

	for _, e := range entities {
		ln, err := net.Listen("tcp", e.addr)
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("listening for %s on %s: %w", e.key, e.addr, err)
		}
		listeners = append(listeners, bound{
			e:  e,
			ln: ln,
			hs: &http.Server{
				Handler:           handlerFor(srv, e),
				ReadHeaderTimeout: 10 * time.Second,
			},
		})
	}

	for _, b := range listeners {
		log.Info("listening", "entity", b.e.key, "name", b.e.name, "addr", b.e.addr)
		go func() {
			if err := b.hs.Serve(b.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("server error", "entity", b.e.key, "error", err)
			}
		}()
	}

	return func(ctx context.Context) error {
		var first error
		for _, b := range listeners {
			if err := b.hs.Shutdown(ctx); err != nil && first == nil {
				first = err
			}
		}
		return first
	}, nil
}
