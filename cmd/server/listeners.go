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

	bankapi "github.com/raphi011/cbs/api/bank"
	cbapi "github.com/raphi011/cbs/api/centralbank"
	csmapi "github.com/raphi011/cbs/api/csm"
	"github.com/raphi011/cbs/payment"
)

// Entity keys for the two institutions. A bank's key is its address, which is
// also its participant id and the name of its database, so these two are
// spelled out and the banks are not.
const (
	centralBankKey   = "central-bank"
	clearingHouseKey = "clearing-house"
)

// And what each is called wherever a deployment names one. A bank's name is its
// own row's.
const (
	centralBankName   = "Central bank"
	clearingHouseName = "Clearing house"
)

// ebicsPath is where a host's file-transfer endpoint sits on its own listener.
const ebicsPath = "/ebics"

// An entity is one listener: which operator it serves, on which address, and —
// for a bank — which participant it is.
type entity struct {
	key  string
	name string
	addr string
	pid  payment.ParticipantID // empty for the two institutions
}

// hostPlan is the two institutions' listeners, on the base port and the next.
func hostPlan(base int) []entity {
	return []entity{
		{key: centralBankKey, name: centralBankName, addr: addrFor(base)},
		{key: clearingHouseKey, name: clearingHouseName, addr: addrFor(base + 1)},
	}
}

// bankPlan is one listener per BANK from base+2 upward — every bank that HAS A
// DATABASE, and not only the ones the scheme has admitted.
func bankPlan(ctx context.Context, stores payment.Stores, nets *payment.Networks, base int) ([]entity, error) {
	bics, err := stores.Banks(ctx)
	if err != nil {
		return nil, err
	}
	var out []entity
	for i, bic := range bics {
		pid := payment.ParticipantID(bic)
		net, err := nets.Bank(ctx, pid)
		if err != nil {
			return nil, err
		}
		bank, err := net.GetBank(ctx, pid)
		if err != nil {
			return nil, fmt.Errorf("planning a listener for %s: %w", bic, err)
		}
		out = append(out, entity{
			key:  string(bic),
			name: bank.Name,
			addr: addrFor(base + 2 + i),
			pid:  pid,
		})
	}
	return out, nil
}

func addrFor(port int) string { return ":" + strconv.Itoa(port) }

// handlerFor picks the surface an entity serves, and builds the institution
// behind it. A bank's needs a context and can fail, because binding it opens
// that bank's own database.
func handlerFor(ctx context.Context, dep *Deployment, e entity, log *slog.Logger) (http.Handler, error) {
	switch e.key {
	case centralBankKey:
		cb := dep.CentralBank()
		return withEBICS(cbapi.Routes(cb, operator{dep}).Handler(log), cb.EBICS()), nil
	case clearingHouseKey:
		csm := dep.ClearingHouse()
		return withEBICS(csmapi.Routes(csm, operator{dep}).Handler(log), csm.EBICS()), nil
	default:
		b, err := dep.Bank(ctx, e.pid)
		if err != nil {
			return nil, err
		}
		return bankapi.Routes(b).Handler(log), nil
	}
}

// withEBICS puts a host's file-transfer endpoint on the same listener as its
// console, and OUTSIDE the console's middleware chain.
func withEBICS(console, ebics http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(ebicsPath, ebics)
	mux.Handle("/", console)
	return mux
}

// serve starts every listener and returns a function that shuts them all down.
func serve(ctx context.Context, entities []entity, dep *Deployment, log *slog.Logger) (func(context.Context) error, error) {
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
		// The surface is bound BEFORE the socket, so a bank whose database will
		// not open costs no listener rather than an open one serving nothing.
		h, err := handlerFor(ctx, dep, e, log)
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("binding the surface for %s: %w", e.key, err)
		}
		ln, err := net.Listen("tcp", e.addr)
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("listening for %s on %s: %w", e.key, e.addr, err)
		}
		listeners = append(listeners, bound{
			e:  e,
			ln: ln,
			hs: &http.Server{
				Handler:           h,
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
