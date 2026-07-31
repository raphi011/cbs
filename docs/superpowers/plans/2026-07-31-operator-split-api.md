# Operator-Split API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give each entity its own HTTP listener bound to its own identity — one
per member bank, one for the central bank, one for the clearing house — so a
bank's API cannot name another bank, because there is nowhere in it to put the
name.

**Architecture:** One binary, one process by default, one `http.Server` per
entity over one shared `*payment.Network`. `Server.Routes()` splits into
`CentralBankRoutes()`, `ClearingHouseRoutes()` and `BankRoutes(pid)`. A bank's
listener carries its participant id on a shallow copy of `Server`, so
`s.participant(w, r)` resolves the bound identity instead of a path parameter —
which is why all 58 bank handler bodies are untouched by the split.

**Tech Stack:** Go 1.22+ (`net/http` method+path patterns) · `store/mem` /
`store/pg` behind `store/storetest` · Next.js 16 for the proxy change.

Spec: [`../specs/2026-07-31-operator-split-api-design.md`](../specs/2026-07-31-operator-split-api-design.md)

## Global Constraints

- **Postgres stays optional, and it is the property this whole design rests on.**
  `go test ./...` with no `DATABASE_URL`, `make dev` and `make run` must all work
  with no database. `TEST_DATABASE_URL=… go test ./...` must stay green too.
  Both runs, every task.
- **One binary.** No new package under `cmd/`. What multiplies is listeners.
- **`store/storetest` is not touched.** Nothing here changes the store. If a task
  finds itself editing `store/`, it has gone wrong.
- **No new dependency.** The repository depends on `pgx` and nothing else.
- **`make test` is the gate**: `go build ./... && go vet ./...`, `gofmt -l .`
  empty, `TEST_DATABASE_URL= go test ./...`, then the web suite.
- **Handler bodies do not change** except in the four tasks that say so (4, 5, 6).
  A task that finds itself editing a handler body outside those has misread the
  bound-identity mechanism.
- **Domain facts are duplicated across four layers on purpose** (`CLAUDE.md`):
  `README.md` is authoritative, then `web/src/components/hint-content.ts`, the
  quiz chapters, and the schema comments. This sub-project changes no domain
  fact — it changes where an API lives — so only `README.md`'s API section moves.
  Task 9 covers it.
- **Ports**: `:8081` central bank, `:8082` clearing house, `:8083`+ banks in
  registration order. Not `:8080` (the old single server) and not `:3000` (the
  web app).

## File Structure

**Created**

| File | Responsibility |
|---|---|
| `api/router.go` | `router`, a `*http.ServeMux` that records the patterns registered on it |
| `api/surface.go` | the three route-set constructors and the bound-identity copy |
| `api/surface_test.go` | completeness and disjointness of the three surfaces |
| `api/handlers_bank_payment.go` | a bank's own three payment routes |
| `cmd/server/listeners.go` | the entity table, and starting N `http.Server`s |
| `web/src/lib/api/operator.ts` | `cb()`, `csm()`, `bank()` path helpers |

**Modified**: `api/server.go` (`Routes()` → three constructors, `participant`
reads the bound id), the nine `register*Routes` functions, `api/server_test.go`,
`api/handlers_payment.go` (settle moves), `api/handlers_participant.go`
(`/participants` → `/members`, `GET /participants/{pid}` → `GET /me`),
`api/handlers_directory.go` (directory on two operators),
`cmd/server/main.go`, `cmd/server/main_test.go`, `Makefile`, `README.md`,
`web/src/app/api/[...path]/route.ts`, `web/src/lib/api/endpoints.ts`,
`web/CLAUDE.md`.

**Deleted**: nothing. `Routes()` is replaced, not removed from history.

---

## Task 1: Make the route table enumerable

`http.ServeMux` cannot be asked what is registered on it, so nothing can assert
that an 84-route re-home lost nothing. This task fixes that first, because every
later task leans on it.

**Files:**
- Create: `api/router.go`
- Test: `api/router_test.go`
- Modify: `api/server.go:96-108` and the nine `register*Routes` signatures

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type router struct{ mux *http.ServeMux; patterns []string }`
  - `func newRouter() *router`
  - `func (r *router) HandleFunc(pattern string, h http.HandlerFunc)`
  - `func (r *router) Patterns() []string` — sorted copy
  - `func (s *Server) RoutePatterns() []string` — what `Routes()` registers

- [ ] **Step 1: Write the failing test**

Create `api/router_test.go`:

```go
package api

import (
	"net/http"
	"testing"
)

// TestRouterRecordsEveryPattern pins the property the surface tests rest on:
// http.ServeMux cannot be asked what is registered on it, so registration goes
// through a wrapper that remembers.
func TestRouterRecordsEveryPattern(t *testing.T) {
	r := newRouter()
	r.HandleFunc("GET /b", func(http.ResponseWriter, *http.Request) {})
	r.HandleFunc("GET /a", func(http.ResponseWriter, *http.Request) {})

	got := r.Patterns()
	if len(got) != 2 || got[0] != "GET /a" || got[1] != "GET /b" {
		t.Fatalf("Patterns() = %v, want them sorted", got)
	}
}

// TestRouteInventoryIsComplete is a golden list. It exists so that the operator
// split in the next tasks cannot silently drop a route: this is what the API
// serves today, and the three-surface test that replaces it asserts the same
// set arrives on exactly one operator each.
func TestRouteInventoryIsComplete(t *testing.T) {
	got := newServer(t, nil).RoutePatterns()
	if len(got) != 84 {
		t.Fatalf("the API serves %d routes, want 84:\n%s", len(got), strings.Join(got, "\n"))
	}
	for _, want := range []string{
		"POST /participants",
		"GET /participants/{pid}/deposit-accounts",
		"POST /cycles/{cid}/settle",
		"GET /payments",
		"GET /directory",
		"POST /admin/reset",
		"GET /assets",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("route %q is missing from the inventory", want)
		}
	}
}
```

Add `"slices"` and `"strings"` to the imports.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./api/ -run 'TestRouter|TestRouteInventory' -v`
Expected: FAIL to compile — `undefined: newRouter`, `undefined: RoutePatterns`.

- [ ] **Step 3: Write the router**

Create `api/router.go`:

```go
package api

import (
	"net/http"
	"slices"
)

// router is an http.ServeMux that remembers what was registered on it.
//
// ServeMux has no introspection: once a pattern is handed to HandleFunc it is
// unreachable. That is fine for serving and useless for proving that an API
// split across three operators kept every route exactly once, which is the one
// claim a re-home of this size has to be able to make. Recording the patterns
// on the way in costs a slice and makes that claim testable.
type router struct {
	mux      *http.ServeMux
	patterns []string
}

func newRouter() *router {
	return &router{mux: http.NewServeMux()}
}

func (r *router) HandleFunc(pattern string, h http.HandlerFunc) {
	r.patterns = append(r.patterns, pattern)
	r.mux.HandleFunc(pattern, h)
}

// Patterns returns the registered patterns, sorted, as a copy — so a caller
// cannot reorder the router's own record by sorting the result again.
func (r *router) Patterns() []string {
	out := slices.Clone(r.patterns)
	slices.Sort(out)
	return out
}
```

- [ ] **Step 4: Route registration through it**

Change the nine `register*Routes(mux *http.ServeMux)` signatures to
`register*Routes(mux *router)` — the call sites inside them are already
`mux.HandleFunc(…)` and do not change. The files:
`handlers_participant.go:12`, `handlers_ledger.go:10`, `handlers_deposit.go:14`,
`handlers_product.go:16`, `handlers_lending.go:15`, `handlers_payment.go:115`,
`handlers_directory.go:15`, `handlers_audit.go:17`, `handlers_admin.go:14`.

Then in `api/server.go`, rewrite `Routes()` and add `RoutePatterns()`:

```go
// Routes builds the HTTP handler: an enhanced ServeMux (Go 1.22+ method+path
// patterns) wrapped in the middleware chain (CORS, logging, recover).
func (s *Server) Routes() http.Handler {
	return s.withMiddleware(s.routes().mux)
}

// RoutePatterns is every pattern Routes registers, sorted. It exists for the
// tests that hold the operator split honest; nothing in serving uses it.
func (s *Server) RoutePatterns() []string { return s.routes().Patterns() }

func (s *Server) routes() *router {
	mux := newRouter()
	s.registerParticipantRoutes(mux)
	s.registerLedgerRoutes(mux)
	s.registerDepositRoutes(mux)
	s.registerProductRoutes(mux)
	s.registerLendingRoutes(mux)
	s.registerPaymentRoutes(mux)
	s.registerDirectoryRoutes(mux)
	s.registerAuditRoutes(mux)
	s.registerAdminRoutes(mux)
	return mux
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./api/ -run 'TestRouter|TestRouteInventory' -v`
Expected: PASS. If the count is not 84, do not adjust the constant — find out
what changed, because the number in this plan was measured from `main`.

- [ ] **Step 6: Run the full gate**

Run: `make test`
Expected: green. Then `TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...`
Expected: green. (Start the container with `make db-up` if it is not running.
Prefer setting `TEST_DATABASE_URL` directly over `make test-pg`, whose
`docker compose up` can collide with an already-running `cbs-pg`.)

- [ ] **Step 7: Commit**

```bash
git add api/router.go api/router_test.go api/server.go api/handlers_*.go
git commit -m "test(api): make the route table something a test can read"
```

---

## Task 2: Three surfaces, alongside the old one

Purely additive. `Routes()` keeps working and `cmd/server` is untouched, so this
task cannot break the running app; Task 3 switches over and removes the old
path.

**Files:**
- Create: `api/surface.go`, `api/surface_test.go`
- Modify: `api/server.go` (add `boundPID`, `participant` prefers it), the nine
  `register*Routes` (split by operator), `api/handlers_participant.go`

**Interfaces:**
- Consumes: `router` (Task 1).
- Produces:
  - `func (s *Server) CentralBankRoutes() http.Handler`
  - `func (s *Server) ClearingHouseRoutes() http.Handler`
  - `func (s *Server) BankRoutes(pid payment.ParticipantID) http.Handler`
  - `func (s *Server) centralBankRouter() *router` and siblings, for the tests
  - `func (s *Server) forBank(pid payment.ParticipantID) *Server`

- [ ] **Step 1: Write the failing test**

Create `api/surface_test.go`:

```go
package api

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

// The two deliberate overlaps, as an allowlist rather than a tolerance, so a
// third accidental one fails.
//
//   - /assets is a compiled-in constant every operator needs to render money at
//     the right scale. Duplicating a constant is not duplicating state.
//   - GET /directory is on the bank as well as the clearing house because a bank
//     is a scheme participant with directory access, and the alternative — a
//     customer's browser querying the CSM — gives a retail app a
//     clearing-house connection no retail app has.
var allowedOverlaps = []string{"GET /assets", "GET /directory"}

func surfaces(t *testing.T) map[string][]string {
	t.Helper()
	s := newServer(t, nil)
	return map[string][]string{
		"central-bank":   s.centralBankRouter().Patterns(),
		"clearing-house": s.clearingHouseRouter().Patterns(),
		"bank":           s.forBank("bank_1").bankRouter().Patterns(),
	}
}

// TestSurfacesAreDisjoint is the guard against a route quietly appearing on two
// operators — which would put a bank's ledger back within reach of a
// central-bank operator with a URL bar.
func TestSurfacesAreDisjoint(t *testing.T) {
	seen := map[string]string{}
	for op, patterns := range surfaces(t) {
		for _, p := range patterns {
			if other, dup := seen[p]; dup && !slices.Contains(allowedOverlaps, p) {
				t.Errorf("%q is on both %s and %s", p, other, op)
			}
			seen[p] = op
		}
	}
}

// TestEveryRouteLandsSomewhere is the guard against losing one. Every pattern
// the single server served must arrive on some operator, with its
// /participants/{pid} prefix stripped where the port now carries it.
func TestEveryRouteLandsSomewhere(t *testing.T) {
	landed := map[string]bool{}
	for _, patterns := range surfaces(t) {
		for _, p := range patterns {
			landed[p] = true
		}
	}

	for _, old := range newServer(t, nil).RoutePatterns() {
		want := movedTo(old)
		if want == "" {
			continue // handled by a later task; see the switch below
		}
		if !landed[want] {
			t.Errorf("%q should have become %q, which no operator serves", old, want)
		}
	}
}

// movedTo maps a pre-split pattern to the pattern that replaces it, or "" for
// the handful this task deliberately does not move yet.
func movedTo(old string) string {
	method, path, _ := strings.Cut(old, " ")
	switch {
	case path == "/participants" && method == "POST":
		return "POST /members"
	case path == "/participants" && method == "GET":
		return "GET /members"
	case path == "/participants/{pid}":
		return "GET /me"
	case strings.HasPrefix(path, "/participants/{pid}/"):
		return method + " /" + strings.TrimPrefix(path, "/participants/{pid}/")
	case path == "/central-bank/reserves":
		return "GET /reserves"
	case path == "/central-bank/reserves/{pid}":
		return "GET /reserves/{pid}"
	case path == "/central-bank/audit":
		return "GET /audit"
	case path == "/cycles/{cid}/settle":
		return "" // Task 4 turns this into POST /settlements
	default:
		return old
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./api/ -run TestSurfaces -v`
Expected: FAIL to compile — `undefined: centralBankRouter` and friends.

- [ ] **Step 3: Bind a listener to a participant**

In `api/server.go`, add the field to `Server` with its reasoning, and change
`participant`:

```go
type Server struct {
	net *payment.Network

	// boundPID is the participant this listener belongs to, empty on the
	// central bank's and the clearing house's.
	//
	// It is what replaces the {pid} path segment: a bank's port already names
	// the bank, so a bank's routes have nowhere to put another bank's id. The
	// value lives on a shallow copy of the Server (see forBank) rather than in
	// the request context, which is what leaves all 58 bank handlers untouched
	// — they call s.participant(w, r) exactly as before.
	boundPID payment.ParticipantID

	populate func(context.Context, *payment.Network) error
	resetMu  sync.Mutex
	log      *slog.Logger
}

// forBank returns a view of this Server bound to one participant. The copy is
// shallow on purpose: every listener shares the one Network, the one populate
// func and the one log.
//
// It is built field by field rather than as *s because a sync.Mutex copied by
// value is a second, independent lock. resetMu therefore stays behind, on the
// original Server — which is the one the central bank's listener uses, and the
// central bank is the only operator with a reset route. So there is exactly one
// resetMu in the process, guarding the only surface that can reach it.
func (s *Server) forBank(pid payment.ParticipantID) *Server {
	return &Server{
		net:      s.net,
		boundPID: pid,
		populate: s.populate,
		log:      s.log,
	}
}

// participant resolves the listener's own participant. On failure it writes the
// appropriate error response and returns false, so callers can simply `return`
// when ok is false.
//
// Transitional: until Task 3 switches cmd/server over, an unbound Server falls
// back to the {pid} path parameter so the combined Routes() keeps working.
func (s *Server) participant(w http.ResponseWriter, r *http.Request) (*payment.Participant, bool) {
	pid := s.boundPID
	if pid == "" {
		pid = payment.ParticipantID(r.PathValue("pid"))
	}
	p, err := s.network().GetParticipant(r.Context(), pid)
	if err != nil {
		writeError(w, err)
		return nil, false
	}
	return p, true
}
```

- [ ] **Step 4: Split the registrations**

Create `api/surface.go`. Each operator's router is built from the existing
register functions, which are split by operator rather than by domain package.
The bank's registrations drop the `/participants/{pid}` prefix; nothing else
about them changes.

```go
package api

import (
	"net/http"

	"github.com/raphi011/cbs/payment"
)

// The three operator surfaces.
//
// One binary, one process by default, one listener per entity. What a caller
// can reach is decided by which port they are talking to, and a bank's routes
// have nowhere to name another bank — the port carries the identity.
//
// This is scoping, not authorization: nothing verifies that the caller on a
// bank's port is that bank.

func (s *Server) CentralBankRoutes() http.Handler {
	return s.withMiddleware(s.centralBankRouter().mux)
}

func (s *Server) ClearingHouseRoutes() http.Handler {
	return s.withMiddleware(s.clearingHouseRouter().mux)
}

// BankRoutes is one member bank's surface, bound to its identity.
func (s *Server) BankRoutes(pid payment.ParticipantID) http.Handler {
	b := s.forBank(pid)
	return b.withMiddleware(b.bankRouter().mux)
}

func (s *Server) centralBankRouter() *router {
	mux := newRouter()
	mux.HandleFunc("GET /reserves", s.handleListReserves)
	mux.HandleFunc("GET /reserves/{pid}", s.handleGetReserve)
	mux.HandleFunc("GET /audit", s.handleCentralBankAudit)
	mux.HandleFunc("POST /members", s.handleAddParticipant)
	mux.HandleFunc("GET /assets", s.handleListAssets)
	s.registerAdminRoutes(mux)
	return mux
}

func (s *Server) clearingHouseRouter() *router {
	mux := newRouter()
	mux.HandleFunc("GET /members", s.handleListParticipants)
	mux.HandleFunc("GET /assets", s.handleListAssets)
	mux.HandleFunc("GET /schemes", s.handleListSchemes)
	mux.HandleFunc("GET /directory", s.handleResolveIdentifier)
	s.registerPaymentRoutes(mux)
	s.registerPaymentAuditRoutes(mux)
	return mux
}

func (s *Server) bankRouter() *router {
	mux := newRouter()
	mux.HandleFunc("GET /me", s.handleGetBoundParticipant)
	mux.HandleFunc("GET /assets", s.handleListAssets)
	mux.HandleFunc("GET /directory", s.handleResolveIdentifier)
	mux.HandleFunc("POST /deposits", s.handleFundDeposit)
	s.registerLedgerRoutes(mux)
	s.registerDepositRoutes(mux)
	s.registerProductRoutes(mux)
	s.registerLendingRoutes(mux)
	s.registerBankAuditRoutes(mux)
	return mux
}
```

Then, mechanically, in each `register*Routes`: strip `/participants/{pid}` from
every pattern. `handlers_ledger.go` (14 routes), `handlers_deposit.go` (18),
`handlers_product.go` (7), `handlers_lending.go` (13). For example

```go
mux.HandleFunc("GET /participants/{pid}/deposit-accounts", s.handleListDepositAccounts)
```

becomes

```go
mux.HandleFunc("GET /deposit-accounts", s.handleListDepositAccounts)
```

Split `registerAuditRoutes` (4 routes) into `registerBankAuditRoutes`
(`GET /audit`, `GET /deposit-audit`) and `registerPaymentAuditRoutes`
(`GET /payments/audit`); `GET /central-bank/audit` moves to
`centralBankRouter` as `GET /audit`, so the handler name
`handleCentralBankAudit` stays even though the path no longer says so — the
handler reads the central bank's log, and that is what the name means.

Delete `registerParticipantRoutes` and `registerDirectoryRoutes`: their routes
are now distributed by hand above, which is the point. Keep
`handlers_participant.go`'s and `handlers_directory.go`'s handler functions.

Add `handleGetBoundParticipant` to `api/handlers_participant.go`, replacing
`handleGetParticipant`'s path-parameter read (`:130` is `handleGetReserve` and
keeps its `{pid}` — the central bank legitimately asks about a member):

```go
// handleGetBoundParticipant answers GET /me: the bank this listener is. It
// replaces GET /participants/{pid}, which had to be told which bank it was.
func (s *Server) handleGetBoundParticipant(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toParticipantDTO(p))
}
```

- [ ] **Step 5: Run the surface tests**

Run: `go test ./api/ -run 'TestSurfaces|TestEveryRoute' -v`
Expected: PASS. A failure names the exact pattern that went missing or doubled.

- [ ] **Step 6: Run the full gate, both stores**

Run: `make test`, then `TEST_DATABASE_URL=… go test ./...`
Expected: green — the existing `api/server_test.go` still drives `Routes()`,
which still exists and still serves the old paths.

- [ ] **Step 7: Commit**

```bash
git add api/surface.go api/surface_test.go api/server.go api/handlers_*.go
git commit -m "feat(api): give each operator a surface of its own"
```

---

## Task 3: Switch over — N listeners, and the old mux goes

**Files:**
- Create: `cmd/server/listeners.go`
- Modify: `cmd/server/main.go`, `api/server.go` (delete `Routes()` and the
  fallback), `api/server_test.go`, `api/router_test.go`

**Interfaces:**
- Consumes: the three constructors (Task 2).
- Produces:
  - `type entity struct{ key, addr string; pid payment.ParticipantID }`
  - `func plan(ctx context.Context, net *payment.Network, base int) ([]entity, error)`
  - `func serve(entities []entity, srv *api.Server, log *slog.Logger) (stop func(context.Context) error, err error)`

- [ ] **Step 1: Write the failing test**

Add to `api/server_test.go` — the helpers already take an `http.Handler`, so
re-pointing the suite is a matter of which handler each test asks for:

```go
// newTestServer is the bank's surface, bound to the first participant the test
// creates. The great majority of this suite exercises a bank.
func newTestServer(t *testing.T) http.Handler { … }

func newCentralBankServer(t *testing.T) http.Handler
func newClearingHouseServer(t *testing.T) http.Handler
func newBankServer(t *testing.T, pid payment.ParticipantID) http.Handler
```

and a new test pinning the property the split exists for:

```go
// TestABankCannotNameAnotherBank is the whole point of the split. The old API
// took the bank as a path segment, so any caller could ask for any bank's
// ledger. There is now nowhere in a bank's API to put another bank's id.
func TestABankCannotNameAnotherBank(t *testing.T) {
	s := newServer(t, nil)
	aurora := addParticipant(t, s, "Aurora Bank")
	verde := addParticipant(t, s, "Banca Verde")

	h := s.BankRoutes(aurora)
	// The old shape is simply not a route any more.
	do(t, h, "GET", "/participants/"+string(verde)+"/deposit-accounts", "").
		expectStatus(t, http.StatusNotFound)

	// And the bank's own list is its own, whichever bank asks.
	var got []map[string]any
	getJSON(t, h, "/deposit-accounts", &got)
}
```

Adapt `expectStatus`/`addParticipant` to whatever the suite's existing helpers
are named; the assertion that matters is the 404 on the old shape.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./api/ -run TestABankCannotNameAnotherBank -v`
Expected: FAIL — `newServer(...).BankRoutes` exists, but the suite's helpers do
not yet compile against it.

- [ ] **Step 3: Start N listeners**

Create `cmd/server/listeners.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/payment"
)

// An entity is one listener: which operator it serves, on which address, and —
// for a bank — which participant it is.
type entity struct {
	key  string // "central-bank", "clearing-house", or the participant id
	addr string
	pid  payment.ParticipantID // empty for the two institutions
}

// plan builds the listener table: the two institutions, then one listener per
// member bank in registration order, from base+2 upward.
//
// Ports are static. A participant admitted at runtime gets a store row, a chart
// of accounts and reserve accounts — and no listener until the process
// restarts. That is a decision about what admission means: admitting a member
// to a payment network is an operational act, and an API call that instantly
// yielded a running bank would teach the wrong thing.
func plan(ctx context.Context, net *payment.Network, base int) ([]entity, error) {
	parts, err := net.ListParticipants(ctx)
	if err != nil {
		return nil, err
	}
	out := []entity{
		{key: "central-bank", addr: ":" + strconv.Itoa(base)},
		{key: "clearing-house", addr: ":" + strconv.Itoa(base+1)},
	}
	for i, p := range parts {
		out = append(out, entity{
			key:  string(p.ID),
			addr: ":" + strconv.Itoa(base+2+i),
			pid:  p.ID,
		})
	}
	return out, nil
}

// handlerFor picks the surface an entity serves.
func handlerFor(srv *api.Server, e entity) http.Handler {
	switch e.key {
	case "central-bank":
		return srv.CentralBankRoutes()
	case "clearing-house":
		return srv.ClearingHouseRoutes()
	default:
		return srv.BankRoutes(e.pid)
	}
}

// serve starts every listener and returns a function that shuts them all down.
//
// A failure to bind is fatal rather than survivable: a system missing one of its
// banks is not a degraded system, it is a wrong one, and a payment routed to the
// missing member would fail somewhere far from the cause.
func serve(entities []entity, srv *api.Server, log *slog.Logger) (func(context.Context) error, error) {
	servers := make([]*http.Server, 0, len(entities))
	errs := make(chan error, len(entities))

	for _, e := range entities {
		hs := &http.Server{
			Addr:              e.addr,
			Handler:           handlerFor(srv, e),
			ReadHeaderTimeout: 10 * time.Second,
		}
		ln, err := net.Listen("tcp", e.addr)
		if err != nil {
			for _, started := range servers {
				_ = started.Close()
			}
			return nil, fmt.Errorf("listening for %s on %s: %w", e.key, e.addr, err)
		}
		servers = append(servers, hs)
		log.Info("listening", "entity", e.key, "addr", e.addr)
		go func() {
			if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}()
	}

	return func(ctx context.Context) error {
		var first error
		for _, hs := range servers {
			if err := hs.Shutdown(ctx); err != nil && first == nil {
				first = err
			}
		}
		return first
	}, nil
}
```

Add `"net"` to the imports. Binding with `net.Listen` before `Serve` is what
makes a port collision a startup error rather than a goroutine that logs and
dies after `main` has already reported success.

Rewrite the serving half of `cmd/server/main.go` to call `plan` and `serve`, and
replace the `-addr` flag with `-base-port` (default 8081, or `PORT` when set).
Keep the signal handling and the graceful-shutdown block, calling the returned
stop function.

- [ ] **Step 4: Delete the old surface**

In `api/server.go`, delete `Routes()`, `routes()` and `RoutePatterns()`'s
dependence on them — `RoutePatterns()` goes too; the three-surface tests replace
it. Remove the `boundPID == ""` fallback from `participant`, leaving:

```go
func (s *Server) participant(w http.ResponseWriter, r *http.Request) (*payment.Participant, bool) {
	p, err := s.network().GetParticipant(r.Context(), s.boundPID)
	if err != nil {
		writeError(w, err)
		return nil, false
	}
	return p, true
}
```

An unbound `Server` reaching this asks the network for the participant `""`,
which is a clean not-found rather than another bank's data — the failure mode
worth having.

In `api/router_test.go`, delete `TestRouteInventoryIsComplete`: its golden list
described a single mux that no longer exists, and `TestEveryRouteLandsSomewhere`
is its successor. Keep `TestRouterRecordsEveryPattern`.

Update `movedTo` in `api/surface_test.go` to read from a hard-coded list of the
84 pre-split patterns rather than from `RoutePatterns()`, since the latter is
gone. Paste the list from `git show HEAD~2:api/…` or from this plan's spec.

- [ ] **Step 5: Re-point the API suite**

`api/server_test.go` is ~2500 lines. Its helpers already take an `http.Handler`
(`do`, `doJSON`, `getJSON`), so the work is per-test and mechanical: each test
asks for the surface that serves its routes, and its paths lose the
`/participants/{pid}` prefix. Work file-section by file-section, running
`go test ./api/` after each, rather than in one sweep.

- [ ] **Step 6: Run the full gate, both stores**

Run: `make test`, then `TEST_DATABASE_URL=… go test ./...`
Expected: green.

- [ ] **Step 7: Start it and look**

```bash
go run ./cmd/server
curl -s localhost:8081/reserves | head -c 200      # central bank
curl -s localhost:8082/members   | head -c 200      # clearing house
curl -s localhost:8083/me        | head -c 200      # Aurora
curl -s -o /dev/null -w '%{http_code}\n' localhost:8083/members   # 404: not a bank's route
```

Expected: the first three answer; the fourth is 404. That last one is the whole
sub-project in a single command.

- [ ] **Step 8: Commit**

```bash
git add -A api cmd
git commit -m "feat(api): one listener per entity, and the shared mux retires"
```

---

## Task 4: Settling is the central bank's act

**Files:**
- Modify: `api/handlers_payment.go:115-135` (registration) and `:317` (handler),
  `api/surface.go`, `api/surface_test.go`, `api/server_test.go`

**Interfaces:**
- Consumes: `Network.SettleCycle` (unchanged).
- Produces: `POST /settlements` on the central bank, body `{"cycleId": "…"}`.

- [ ] **Step 1: Write the failing test**

In `api/server_test.go`:

```go
// TestSettlingIsTheCentralBanksAct pins the operator, not the mechanism.
// Settlement moves reserves between accounts in the central bank's own book; a
// clearing house that could do that would be a central bank. Before the split
// the CSM settled directly, because there was only one server to put the route
// on.
func TestSettlingIsTheCentralBanksAct(t *testing.T) {
	s := newServer(t, nil)
	cid := openAndCloseACycle(t, s) // existing helper shape

	csm := s.ClearingHouseRoutes()
	do(t, csm, "POST", "/cycles/"+cid+"/settle", "").expectStatus(t, http.StatusNotFound)

	cb := s.CentralBankRoutes()
	doJSON(t, cb, "POST", "/settlements", `{"cycleId":"`+cid+`"}`, http.StatusOK)
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./api/ -run TestSettlingIsTheCentralBank -v`
Expected: FAIL — 404 on `POST /settlements`, and 200 on the CSM's settle route.

- [ ] **Step 3: Move the route**

Delete `mux.HandleFunc("POST /cycles/{cid}/settle", s.handleSettleCycle)` from
`registerPaymentRoutes`. Add to `centralBankRouter()`:

```go
	mux.HandleFunc("POST /settlements", s.handleSettleCycle)
```

Change the handler to read the cycle from the body, and add the request DTO
beside the others in `api/dto_payment.go`:

```go
// settleCycleRequest names the cycle to settle. The cycle is the input; the
// resource created is a settlement, which is why this is POST /settlements and
// not an action on a cycle. It is also why the central bank serves it: the
// clearing house closed the cycle, and moving reserves in the central bank's
// own book is not something a clearing house can do.
type settleCycleRequest struct {
	CycleID string `json:"cycleId"`
}

func (s *Server) handleSettleCycle(w http.ResponseWriter, r *http.Request) {
	var req settleCycleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	settlement, err := s.network().SettleCycle(r.Context(), payment.CycleID(req.CycleID))
	if err != nil {
		writeError(w, err)
		return
	}
	asset, err := s.settlementAsset(r.Context(), settlement)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSettlementDTO(settlement, asset))
}
```

The clearing house keeps `GET /settlements` and `GET /settlements/{sid}`: it
needs to know whether the cycle it closed has settled, and reading is not doing.
Note the `POST` and the `GET` on the same path now live on *different*
operators, which is legal and is exactly the shape the split is for.

- [ ] **Step 4: Update the surface test's mapping**

In `movedTo`, replace the `""` case:

```go
	case path == "/cycles/{cid}/settle":
		return "POST /settlements"
```

- [ ] **Step 5: Run the tests**

Run: `go test ./api/ -v`
Expected: PASS, including `TestSurfacesAreDisjoint` — `POST /settlements` on the
central bank and `GET /settlements` on the clearing house are different
patterns, so no overlap is reported.

- [ ] **Step 6: Full gate, both stores, then commit**

```bash
make test && TEST_DATABASE_URL=… go test ./...
git add -A api
git commit -m "fix(api): settling is the central bank's act, not the clearing house's"
```

---

## Task 5: A bank sees its own payments and no others

**Files:**
- Create: `api/handlers_bank_payment.go`
- Modify: `api/surface.go`, `api/server_test.go`

**Interfaces:**
- Consumes: `Network.ListPayments`, `Network.GetPayment`, `payment.PartyRef`.
- Produces: `GET /payments` and `GET /payments/{payid}` on a bank's surface.

- [ ] **Step 1: Write the failing test**

```go
// TestABankSeesOnlyItsOwnPayments pins the narrowing the single server could
// not express. Before the split GET /payments listed every bank's payments to
// every caller — competitors' customers, counterparties and amounts.
func TestABankSeesOnlyItsOwnPayments(t *testing.T) {
	s := newServer(t, nil)
	// aurora → verde, and a third payment between two other banks.
	mine, theirs := twoPaymentsAcrossThreeBanks(t, s)

	h := s.BankRoutes(auroraID)

	var list []map[string]any
	getJSON(t, h, "/payments", &list)
	if len(list) != 1 || list[0]["id"] != mine {
		t.Fatalf("a bank's payment list = %v, want only its own leg %q", list, mine)
	}

	doJSON(t, h, "GET", "/payments/"+mine, "", http.StatusOK)

	// 404 and not 403: a payment this bank is not party to does not exist as
	// far as its API is concerned, and a 403 would confirm the id.
	doJSON(t, h, "GET", "/payments/"+theirs, "", http.StatusNotFound)

	// The clearing house is the CSM. Seeing every payment is its job.
	var all []map[string]any
	getJSON(t, s.ClearingHouseRoutes(), "/payments", &all)
	if len(all) != 2 {
		t.Fatalf("the clearing house sees %d payments, want both", len(all))
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./api/ -run TestABankSeesOnlyItsOwn -v`
Expected: FAIL — 404 on `/payments`, which a bank does not serve yet.

- [ ] **Step 3: Write the narrowed handlers**

Create `api/handlers_bank_payment.go`:

```go
package api

import (
	"net/http"

	"github.com/raphi011/cbs/payment"
)

// A bank's view of the payment network: its own legs and nothing else.
//
// The single server could not express this. GET /payments listed every payment
// to every caller, because narrowing needs a caller identity and there was none
// — the port is that identity now.

// isParty reports whether this listener's bank is one end of the payment.
func (s *Server) isParty(p payment.Payment) bool {
	return p.Debtor.Participant == s.boundPID || p.Creditor.Participant == s.boundPID
}

func (s *Server) handleListBankPayments(w http.ResponseWriter, r *http.Request) {
	payments, err := s.network().ListPayments(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	schemes := s.network().ListSchemes()
	out := make([]paymentDTO, 0, len(payments))
	for _, p := range payments {
		if s.isParty(p) {
			out = append(out, toPaymentDTO(p, schemes))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetBankPayment answers 404 — not 403 — for a payment this bank is not
// party to. A payment it cannot see does not exist as far as its API is
// concerned, and a 403 would confirm that the id names something real.
func (s *Server) handleGetBankPayment(w http.ResponseWriter, r *http.Request) {
	p, err := s.network().GetPayment(r.Context(), payment.PaymentID(r.PathValue("payid")))
	if err != nil {
		writeError(w, err)
		return
	}
	if !s.isParty(p) {
		writeError(w, payment.ErrPaymentNotFound)
		return
	}
	writeJSON(w, http.StatusOK, toPaymentDTO(p, s.network().ListSchemes()))
}
```

Register both in `bankRouter()`:

```go
	mux.HandleFunc("GET /payments", s.handleListBankPayments)
	mux.HandleFunc("GET /payments/{payid}", s.handleGetBankPayment)
```

Add them to `allowedOverlaps`? **No** — they are genuinely different handlers on
different operators serving the same pattern string, which `TestSurfacesAreDisjoint`
will flag. Extend the allowlist with a comment saying why these two are the
deliberate case where one pattern means two different things:

```go
	// A bank and the clearing house both serve GET /payments, and they answer
	// differently on purpose: the bank's is narrowed to its own legs. Same
	// pattern, different operator, different meaning — which is what the split
	// is for.
	"GET /payments", "GET /payments/{payid}",
```

- [ ] **Step 4: Run the tests, full gate, commit**

```bash
go test ./api/ -v && make test && TEST_DATABASE_URL=… go test ./...
git add -A api
git commit -m "fix(api): a bank sees its own payments, not everybody's"
```

---

## Task 6: A customer instructs their bank

**Files:**
- Modify: `api/handlers_bank_payment.go`, `api/surface.go`, `api/dto_payment.go`,
  `api/server_test.go`

**Interfaces:**
- Produces: `POST /payments` on a bank → `202 Accepted`, `{"paymentId": "…"}`.

- [ ] **Step 1: Write the failing test**

```go
// TestABankAcceptsItsOwnCustomersInstruction pins both halves: the debtor must
// be one of this bank's accounts, and the answer is an identifier rather than
// the payment.
//
// 202 and not 201 even though the handler is synchronous today: sub-project 7b
// converts submission to exactly this shape, because a real CSM answers with a
// pacs.002 later and not by return value. A client built against a synchronous
// "payment created" response would be rewritten; this one will not be.
func TestABankAcceptsItsOwnCustomersInstruction(t *testing.T) {
	s := newServer(t, nil)
	h := s.BankRoutes(auroraID)

	got := doJSON(t, h, "POST", "/payments", sctFromAuroraToVerde, http.StatusAccepted)
	id, ok := got["paymentId"].(string)
	if !ok || id == "" {
		t.Fatalf("response = %v, want a paymentId", got)
	}
	// The outcome arrives from a second request, which is the shape 7b needs.
	doJSON(t, h, "GET", "/payments/"+id, "", http.StatusOK)
}

// A bank may not submit a payment drawn on somebody else's customer.
func TestABankRefusesAnInstructionItIsNotTheDebtorFor(t *testing.T) {
	s := newServer(t, nil)
	doJSON(t, s.BankRoutes(auroraID), "POST", "/payments",
		sctFromVerdeToNord, http.StatusUnprocessableEntity)
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./api/ -run TestABankAccepts -v`
Expected: FAIL — 404; a bank has no `POST /payments`.

- [ ] **Step 3: Write the handler**

Append to `api/handlers_bank_payment.go`:

```go
// acceptedPaymentDTO is what a bank answers a customer instruction with: an
// identifier to ask about, not an outcome. See the 202 reasoning on the test.
type acceptedPaymentDTO struct {
	PaymentID string `json:"paymentId"`
}

// handleSubmitPayment accepts a payment instruction from this bank's own
// customer. A customer's client must never talk to the clearing house — it has
// no CSM connection in the real thing either — so submission lands here, and
// the bank is what forwards it.
func (s *Server) handleSubmitPayment(w http.ResponseWriter, r *http.Request) {
	var req initiatePaymentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	dom := req.toDomain()
	// A bank submits on behalf of its own customer and nobody else's. This is
	// scoping, not authorization: it says which instructions this listener is
	// for, and verifies nothing about who is calling it.
	if dom.Debtor.Participant != s.boundPID {
		writeUnprocessable(w, "the debtor must be an account at this bank")
		return
	}
	p, err := s.network().InitiatePayment(r.Context(), dom)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, acceptedPaymentDTO{PaymentID: string(p.ID)})
}
```

Use whatever the codebase's existing 422 writer is called — check
`api/errors.go` for the helper that `ErrParticipantAssetNotFound` maps through,
and match it rather than adding a second one.

Register in `bankRouter()`:

```go
	mux.HandleFunc("POST /payments", s.handleSubmitPayment)
```

and add `"POST /payments"` to `allowedOverlaps` with the other two, under the
same comment.

- [ ] **Step 4: Run the tests, full gate, commit**

```bash
go test ./api/ -v && make test && TEST_DATABASE_URL=… go test ./...
git add -A api
git commit -m "feat(api): a customer instructs their bank, and asks again for the answer"
```

---

## Task 7: One entity per process, and the refusal that explains why

**Files:**
- Modify: `cmd/server/main.go`, `cmd/server/listeners.go`, `cmd/server/main_test.go`

**Interfaces:**
- Produces: `func selectEntity(entities []entity, key string) (entity, error)`,
  and `-entity` / `-base-port` flags.

- [ ] **Step 1: Write the failing test**

Add to `cmd/server/main_test.go`:

```go
// TestEntityWithoutADSNIsRefused is the guard rail for the property the whole
// design rests on.
//
// store/mem is one process's memory. Four bank processes each calling mem.New
// hold four disconnected universes, and a payment from Aurora to Verde would
// post into an Aurora that Verde has never heard of. Postgres-optional is
// load-bearing (CLAUDE.md), so the split cannot require a database — the
// default is every listener in one process — but -entity, which is the mode
// that genuinely splits processes, cannot work without one.
//
// The message is the teaching, so it is asserted rather than merely the error.
func TestEntityWithoutADSNIsRefused(t *testing.T) {
	err := checkEntityMode("aurora", "")
	if err == nil {
		t.Fatal("-entity with no -database was accepted; it cannot work")
	}
	for _, want := range []string{"in-memory", "-database"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, err)
		}
	}
	if err := checkEntityMode("aurora", "postgres://…"); err != nil {
		t.Errorf("-entity with a DSN was refused: %v", err)
	}
	if err := checkEntityMode("", ""); err != nil {
		t.Errorf("the default (every listener in one process) was refused: %v", err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./cmd/server/ -run TestEntityWithoutADSN -v`
Expected: FAIL to compile — `undefined: checkEntityMode`.

- [ ] **Step 3: Implement**

In `cmd/server/main.go`:

```go
// checkEntityMode refuses -entity without a DSN.
//
// Running one entity per process is the real topology and the reason -entity
// exists. It is also the one configuration store/mem cannot serve: separate
// processes would each hold their own map, so the banks could not see each
// other or the central bank. Rather than let that fail later as a mystery — a
// payment to a participant that "does not exist" — it fails here, saying why.
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
```

Add the flags, call `checkEntityMode` immediately after `flag.Parse()` and exit
non-zero on error, and add `selectEntity` to `listeners.go` so `-entity` narrows
the planned table to one row before `serve`. Match a bank by participant id
*or* by a slugified name, so `-entity aurora` works — the id is generated and
not something a reader can type from memory.

- [ ] **Step 4: Run the tests and try it**

```bash
go test ./cmd/server/ -v
go run ./cmd/server -entity aurora            # expect the refusal, exit 1
make db-up
go run ./cmd/server -entity aurora -database "postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable"
```

Expected: the first exits 1 with the message; the second serves one listener.

- [ ] **Step 5: Full gate, both stores, commit**

```bash
make test && TEST_DATABASE_URL=… go test ./...
git add -A cmd
git commit -m "feat(server): one entity per process, and the refusal that explains the store"
```

---

## Task 8: Point the web app at the split API

Without this, 6a ships a backend the existing frontend cannot talk to. This is
the data layer only — the persona restructure is 6b.

**Files:**
- Create: `web/src/lib/api/operator.ts`
- Modify: `web/src/app/api/[...path]/route.ts`, `web/src/lib/api/endpoints.ts`,
  `web/CLAUDE.md`

**Interfaces:**
- Produces: `cb(path)`, `csm(path)`, `bank(pid, path)` — operator-prefixed paths
  the proxy routes on.

- [ ] **Step 1: Write the path helpers**

Create `web/src/lib/api/operator.ts`:

```ts
// Each entity has a listener of its own (see the operator-split API spec), so a
// request has to say which one it is for. The first segment after /api is the
// operator key; the proxy strips it and forwards the rest.
//
// The port table is deployment configuration and lives in the proxy, not here:
// a base URL in a DTO would make the member roster a deployment manifest.

export const cb = (path: string) => `/central-bank${path}`;
export const csm = (path: string) => `/clearing-house${path}`;
export const bank = (pid: string, path: string) => `/bank/${pid}${path}`;
```

- [ ] **Step 2: Route the proxy by operator**

In `web/src/app/api/[...path]/route.ts`, replace the single `BACKEND` constant
with a registry and resolve per request:

```ts
// operator key → base URL. Defaults match the spec's :8081– block; override
// with BACKENDS as JSON for a deployment that binds elsewhere.
const BASE_PORT = Number(process.env.BASE_PORT ?? 8081);
const BACKENDS: Record<string, string> = process.env.BACKENDS
  ? JSON.parse(process.env.BACKENDS)
  : {};

function resolve(segments: string[]): { base: string; rest: string[] } | null {
  const [head, ...tail] = segments;
  if (head === "central-bank") return { base: backendFor("central-bank"), rest: tail };
  if (head === "clearing-house") return { base: backendFor("clearing-house"), rest: tail };
  if (head === "bank") {
    const [pid, ...rest] = tail;
    return pid ? { base: backendFor(pid), rest } : null;
  }
  return null;
}
```

`backendFor(key)` returns `BACKENDS[key]` when present. Its fallback covers the
two institutions only — `central-bank` → `BASE_PORT`, `clearing-house` →
`BASE_PORT + 1` — because their ports are fixed by the spec. A **bank** must be
present in `BACKENDS`: its port depends on registration order, which the proxy
cannot know and must not guess.

`BACKENDS` is written by `make dev`/`make run` from the same participant order
the server plans from (Task 9). Deployment configuration flows from the
deployment, not from the API: a `GET /backends` returning base URLs would make
the member roster a deployment manifest, which the spec forbids under *The
proxy routes by operator*.

An unresolvable operator is a 502 whose message names the key, so a bank that
was admitted at runtime and never provisioned reads as "no backend configured
for bank_5" rather than as a dead screen.

- [ ] **Step 3: Prefix every endpoint**

`web/src/lib/api/endpoints.ts` is one typed function per route. Each gains the
helper for its operator; the change is one line per function. Examples:

```ts
export function listParticipants(): Promise<Participant[]> {
  return request("GET", csm("/members"));
}

export function listDepositAccounts(pid: string): Promise<DepositAccount[]> {
  return request("GET", bank(pid, "/deposit-accounts"));
}

export function listReserves(): Promise<Reserve[]> {
  return request("GET", cb("/reserves"));
}

export function settleCycle(cid: string): Promise<Settlement> {
  return request("POST", cb("/settlements"), { cycleId: cid });
}
```

Work section by section — the file already has `// --- <area> ---` banners that
map almost exactly onto the operators. The exceptions to check by hand:
`getParticipant` becomes `bank(pid, "/me")`; `addParticipant` becomes
`cb("/members")`; `resetState` becomes `cb("/admin/reset")`; `resolveIdentifier`
takes the operator from its caller (the clearing house's directory screen versus
a bank's payee lookup) so it gains a `pid?: string` parameter and uses
`bank(pid, "/directory")` when given one.

- [ ] **Step 4: Update `web/CLAUDE.md`**

Its **Proxy / no CORS by construction** paragraph describes one `BACKEND_URL`.
Replace with the operator registry, keeping the no-CORS claim, which still holds:
the browser still only ever talks to its own origin.

- [ ] **Step 5: Run the web gate and drive it**

```bash
cd web && npm run typecheck && npm run lint && npm run test && npm run build
```

Then with `go run ./cmd/server` and `npm run dev`, load `/`, a participant page,
its deposit accounts, a payment, a cycle, `/central-bank` and `/schemes`. Every
screen still works; only the URLs behind them moved.

- [ ] **Step 6: Commit**

```bash
git add -A web
git commit -m "feat(web): talk to each operator's own listener"
```

---

## Task 9: Make, docs, and the log

**Files:**
- Modify: `Makefile`, `README.md`, `docs/expansion-roadmap.md`

- [ ] **Step 1: Makefile**

`BACKEND_ADDR ?= :8080` becomes `BASE_PORT ?= 8081`, and `run`/`dev` pass
`-base-port "$(BASE_PORT)"`. They still start **one** Go process — the change is
the flag, not the process count. Export `BACKENDS` for the frontend from the
same table. `docker-compose.yml` is untouched: it holds only Postgres.

Add a `make dev-split` target that starts one process per entity against the
container, since that is the mode `-entity` exists for and it is otherwise
undiscoverable.

- [ ] **Step 2: README**

The API section documents routes under `/participants/{pid}/…`. Rewrite it as
three tables, one per operator, and add a short section on the split: one
binary, listeners not processes, why `-entity` needs a DSN, and that admission
is not provisioning. This is the authoritative layer (`CLAUDE.md`), so it is
where the reasoning goes.

No other documentation layer changes: `hint-content.ts`, the quiz chapters and
the schema comments carry *domain* facts, and this sub-project moved an API, not
a fact.

- [ ] **Step 3: Roadmap**

Mark 6a `done` and append a log row in the established style — what was settled
and why, deviations from this plan, and anything that cost rework. Cover at
minimum: whether the bound-identity copy really left all 58 handlers untouched,
what the `api/server_test.go` re-point actually cost, and whether the
`allowedOverlaps` list grew beyond the four this plan predicts.

- [ ] **Step 4: Final verification**

```bash
make test
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
go run ./cmd/server &
curl -s -o /dev/null -w 'cb %{http_code}\n' localhost:8081/reserves
curl -s -o /dev/null -w 'csm %{http_code}\n' localhost:8082/payments
curl -s -o /dev/null -w 'bank %{http_code}\n' localhost:8083/deposit-accounts
curl -s -o /dev/null -w 'leak %{http_code}\n' localhost:8083/members
```

Expected: both suites green; `200 200 200 404`.

- [ ] **Step 5: Commit**

```bash
git add Makefile README.md docs/expansion-roadmap.md
git commit -m "docs: three operators, one binary"
```

---

## Self-review notes

Checked against the spec:

- **One binary, several ports** — Task 3 (`plan`/`serve`), Task 7 (`-entity`),
  Task 9 (`make`). No new package under `cmd/` in any task.
- **The three surfaces** — Task 2, with the full route table from the spec.
- **Settle → central bank** — Task 4. **Bank payments narrowed** — Task 5.
  **202 submission** — Task 6. **Bank directory** — Task 2 (`bankRouter`).
- **Static ports / admission is not provisioning** — Task 3's `plan`, documented
  on the function.
- **Seeding and reset own by the central bank** — Task 2 registers
  `registerAdminRoutes` on `centralBankRouter` only; seeding stays in
  `cmd/server` at boot, driving `payment.Network` directly, unchanged.
- **`/assets` everywhere** — Task 2, registered on all three, allowlisted in the
  disjointness test.
- **Roster splits** — Task 2: `POST /members` on the central bank, `GET /members`
  on the clearing house.
- **Testing** — the completeness and disjointness tests (Tasks 1–2), the
  narrowing tests (Task 5), the `-entity` refusal (Task 7). `store/storetest` is
  untouched in every task, as the spec requires.
- **Failure modes** — a lost route is caught by `TestEveryRouteLandsSomewhere`;
  a handler still reading `{pid}` is caught by `TestABankCannotNameAnotherBank`
  and by the removal of the fallback in Task 3; the shared `resetMu` is
  addressed explicitly in `forBank`; port collisions are a startup error because
  Task 3 binds with `net.Listen` before `Serve`.

Two things this plan decides that the spec left open, both flagged where they
occur:

1. **`allowedOverlaps` grows to four**, not two. `GET /payments`,
   `GET /payments/{payid}` and `POST /payments` exist on a bank *and* the
   clearing house with different handlers and different meanings. The spec named
   `/assets` and `GET /directory`; the payment routes are the same phenomenon and
   are allowlisted with a comment rather than silently tolerated.
2. **The web app moves in 6a, not 6b.** The spec assigns the proxy change to 6b's
   *Failure modes*, but leaving the frontend unable to reach any endpoint for a
   whole sub-project would mean 6a does not end in working software. Task 8
   takes the data layer only; the persona restructure stays 6b's.
