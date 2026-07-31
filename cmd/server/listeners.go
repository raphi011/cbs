package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
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
	parts, err := net.ListParticipants(ctx)
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

// checkEntityMode refuses -entity without a DSN.
//
// Running one entity per process is the real topology and the reason -entity
// exists. It is also the one configuration store/mem cannot serve: separate
// processes would each hold their own map, so the banks could not see each
// other or the central bank, and a payment between two of them would post into
// two systems with no knowledge of one another.
//
// Rather than let that fail later as a mystery — a participant that "does not
// exist", a settlement that balances against nothing — it fails here, saying
// why. This is the one place a reader is told by the software, rather than by a
// comment, what "shared store" actually means.
func checkEntityMode(entity, dsn string) error {
	if entity == "" || dsn != "" {
		return nil
	}
	return errors.New(
		"-entity requires -database. Separate processes cannot share the in-memory " +
			"store: each would hold its own, and a payment between two banks would post " +
			"into two systems that cannot see each other. Start with -database, or run " +
			"every entity in one process (the default).")
}

// resolveEntities is the whole listener table, or just the one entity named on
// the command line.
//
// It exists as a function rather than as an `if` inside main because that `if`
// is exactly the wiring that can be present, compile, and do nothing — which is
// how it shipped broken once. Here a test can hold it.
func resolveEntities(entities []entity, only string) ([]entity, error) {
	if only == "" {
		return entities, nil
	}
	one, err := selectEntity(entities, only)
	if err != nil {
		return nil, err
	}
	// It keeps the port the whole-system plan gave it, so an entity is
	// reachable at the same address whichever mode started it.
	return []entity{one}, nil
}

// selectEntity narrows a planned table to the one entity named on the command
// line.
//
// A bank is matched by participant id or by name, because ids are generated
// (bank_1, bank_3, …) and nobody can type one from memory. The name match is
// slugified and case-insensitive, so `-entity aurora` finds "Aurora Bank" and
// `-entity credit-soleil` finds "Crédit Soleil".
func selectEntity(entities []entity, want string) (entity, error) {
	slug := slugify(want)
	for _, e := range entities {
		if e.key == want || slugify(e.name) == slug || strings.HasPrefix(slugify(e.name), slug) {
			return e, nil
		}
	}
	names := make([]string, 0, len(entities))
	for _, e := range entities {
		names = append(names, fmt.Sprintf("%s (%s)", e.key, e.name))
	}
	return entity{}, fmt.Errorf("no entity named %q; this system has %s",
		want, strings.Join(names, ", "))
}

// foldAccent maps the accented Latin letters to their base letter, so a name a
// reader would type without accents still matches the one the seed spells with
// them.
//
// A table rather than Unicode normalisation: NFD needs golang.org/x/text, and
// this repository takes no dependency beyond pgx. The table covers Latin-1,
// which is what bank names in this system are written in; a name outside it
// still matches by id, and by its own exact spelling.
var foldAccent = map[rune]rune{
	'à': 'a', 'á': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a',
	'ç': 'c',
	'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e',
	'ì': 'i', 'í': 'i', 'î': 'i', 'ï': 'i',
	'ñ': 'n',
	'ò': 'o', 'ó': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o', 'ø': 'o',
	'ù': 'u', 'ú': 'u', 'û': 'u', 'ü': 'u',
	'ý': 'y', 'ÿ': 'y',
}

// slugify reduces a display name to comparable letters and digits, folding
// accents and mapping anything else — spaces, punctuation — to a single hyphen,
// so "Crédit Soleil" and "credit-soleil" are the same handle.
func slugify(s string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(s) {
		if folded, ok := foldAccent[r]; ok {
			r = folded
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteRune('-')
				prevHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
