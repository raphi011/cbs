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
// also its participant id and the name of its database, so these two are spelled
// out and the banks are not.
//
// They are the two institutions' database file names without the suffix, and
// that is not a coincidence to be tidied away: one process, one directory, one
// name per institution. See store/sqlite.Set.
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
// BANK from base+2 upward — every bank that HAS A DATABASE, and not only the
// ones the scheme has admitted. The paragraph below is why: a listener is
// provisioning, and a bank whose admission has not finished still has a book and
// customers to serve.
//
// The bank list comes from the store set rather than from a ListBanks at the
// clearing house, which is where it came from until the store split and which
// was a crossing: a clearing house holds a roster and no bank rows at all. The
// set answers the same question more honestly — the set of banks is the set of
// databases — and it keeps the founded-but-unadmitted bank the roster omits. See
// payment.Stores.Banks.
//
// The ORDER is by address rather than by registration, because a directory has
// no registration order. Ports therefore move if a bank is founded whose address
// sorts before an existing one. That is a fixture-level fact rather than an
// operational one — the seeded scenario is built in one order every time — and
// it is the price of the set of banks being a fact about the file system instead
// of a list somebody keeps.
//
// Each bank's NAME comes from that bank's own row, in that bank's own database.
// It is the one thing here that costs a read per bank, and it is the composition
// root doing it rather than any institution: nothing in the domain asks another
// bank what it is called.
//
// Ports are static, and so is the set of banks: this plan is made once, from the
// databases that exist when the process starts. A bank provisioned into a
// running deployment gets its three rows and no listener until the next restart.
// That is a decision about what joining a network means rather than a limitation
// to apologise for: it is an operational act — a scheme agreement, an account
// another institution has to open, an operator provisioning a connection — and a
// deployment that instantly yielded a running bank would teach the wrong thing.
func plan(ctx context.Context, stores payment.Stores, nets *payment.Networks, base int) ([]entity, error) {
	bics, err := stores.Banks(ctx)
	if err != nil {
		return nil, err
	}
	out := []entity{
		{key: centralBankKey, name: "Central bank", addr: addrFor(base)},
		{key: clearingHouseKey, name: "Clearing house", addr: addrFor(base + 1)},
	}
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
//
// Each of the three surface packages declares the interface it is driven by and
// the deployment's institution satisfies it, so this is the one place in the
// process that knows which of the three a listener is.
func handlerFor(ctx context.Context, dep *Deployment, e entity, log *slog.Logger) (http.Handler, error) {
	switch e.key {
	case centralBankKey:
		return cbapi.Routes(dep.CentralBank()).Handler(log), nil
	case clearingHouseKey:
		return csmapi.Routes(dep.ClearingHouse()).Handler(log), nil
	default:
		b, err := dep.Bank(ctx, e.pid)
		if err != nil {
			return nil, err
		}
		return bankapi.Routes(b).Handler(log), nil
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
