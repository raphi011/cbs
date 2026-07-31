# Operator-Split API — Design

Sub-project 6a in `docs/expansion-roadmap.md`. The role-scoped web UI, specified
in `2026-07-31-role-scoped-web-ui-design.md`, becomes 6b and is built on this.

One server serves everybody. A bank's back office, the central bank and the
clearing house all reach the same `http.ServeMux` on `:8080`, and the only thing
distinguishing one operator from another is which URL a caller happens to type.
That is a faithful map of nothing: in the real thing there are three
institutions with three systems, and no cable between a bank's core and the
central bank's carries the request "list every payment in the network".

This design gives **each entity its own listener, bound to its own identity**.

## Goal

Make the scoping the rest of the repository already models *structural* rather
than navigational. A bank's API becomes a bank's API: it cannot name another
bank, because there is nowhere in it to put the name.

Three operator surfaces, from one binary over one store:

| Operator | Listeners | Surface |
|---|---|---|
| Central bank | 1 | reserves, its own audit, admission, settlement, reset |
| Clearing house | 1 | payments, cycles, settlements (read), mandates, schemes, the directory, the member roster |
| Member bank | one per participant | its ledger, deposits, products, lending, holds, end-of-day, its own audit — and its own payments |

### Why it lands before the web UI, not inside it

The role-scoped spec records that "the scoping is navigational, not enforced …
This is the decision that keeps this sub-project purely frontend." That decision
is now reversed, and the reversal is what forces the ordering: a persona that
maps to a *backend* is a different piece of software from a persona that maps to
a route prefix, so building the navigational one first would mean writing the
data layer twice.

It is a separate sub-project rather than a bigger 6 because the two halves fail
differently. This one changes what the system *does* — settling changes
operator, and `GET /payments` starts withholding rows — and is checked by Go
tests. The web restructure changes where things *are*, and is checked by
`tsc`, a route-integrity test and clicking. Bundling them puts a failing
conformance test and a dead React link in one review.

## Out of scope, deliberately

- **Splitting the store.** Every listener talks to the same `Store`. This is the
  constraint that makes the whole sub-project small, and lifting it is a much
  larger one: `SettleCycleTx` posts into the central bank's book *and* every
  participant's inside a single `Store.Update` (`payment/system.go:736`), so
  separate databases would replace one transaction with a distributed protocol
  and require a reconciliation-break concept the system does not have.
- **Splitting the call graph.** Each listener holds a full `payment.Network` and
  calls the domain directly. The clearing house does not ask the central bank to
  settle over HTTP; it simply has no settle route. Inter-operator *messaging* is
  sub-project 7b, which specifies it properly as ISO 20022 documents, and a
  lo-fi HTTP version now would be built twice.
- **Authentication and authorization.** Nothing verifies that the caller on a
  bank's port is that bank. The port *is* the claim. What changes is that a
  central-bank operator can no longer reach a bank's ledger by editing a URL,
  because that URL does not exist on the port they are talking to — which is
  structural scoping without an identity system, and is the honest half.
- **Dynamic port allocation.** See *Admission is not provisioning*.
- **`pain.001`.** A customer instruction arriving at its bank is an HTTP call
  here, not a message. 7a records the same boundary.
- **TLS, mutual auth, and any transport security.** Nothing in this repository
  models an untrusted counterparty.

## Decisions

### One binary, several ports

Stated first because it is the thing most easily read the wrong way: this is
**one binary — `cmd/server` — and by default one process**, opening one
`http.Server` per entity on a port of its own. There is no per-entity binary, no
`cmd/bank`, no build matrix, and no new package under `cmd/`. What multiplies is
listeners, not artefacts.

`make dev` and `make run` therefore still start exactly one Go process, the way
they do today; it just answers on six ports instead of one.

`store/mem` is a map behind a mutex in one process's memory (`store/mem/mem.go`).
Four bank processes each calling `mem.New` are four disconnected universes: a
payment from Aurora to Verde would post into an Aurora that Verde has never
heard of. Nothing short of putting the mem store behind a network protocol fixes
that, and that is a database.

Postgres-optional is load-bearing (`CLAUDE.md`: `go test ./...`, `make dev` and
`make run` must all work with no `DATABASE_URL`), so the split cannot require
one. Therefore:

**One binary starts N `http.Server`s over one shared `*payment.Network`.** The
default, and what `make dev`, `make run` and `make test` use, is a single process
holding every listener:

```
:8081  central bank
:8082  clearing house
:8083  Aurora Bank
:8084  Banca Verde
:8085  Nordhaven Bank
:8086  Crédit Soleil
```

`-entity` is an **optional second mode of the same binary**, not a second
binary: `cbs -entity aurora -addr :8083` starts one process holding one
listener, which is the real topology and what a `docker compose` of six services
would run. It is worth the twenty lines because it is what makes the shared-store
constraint visible — it **refuses to start without a DSN**:

```
cbs: -entity requires -database. Separate processes cannot share the in-memory
store: each would hold its own, and a payment between two banks would post into
two systems that cannot see each other. Start with -database, or run every
entity in one process (the default).
```

That refusal is worth more than the split. It is the one place a reader is told,
by the software rather than by a comment, what "shared store" actually means.

The alternative considered and rejected was making `store/mem` shareable across
processes. It is a database with the serial numbers filed off, and it would make
the reference implementation — the thing `store/pg` has to match — the more
complicated of the two.

### The three surfaces

The 84 routes re-home with no behaviour change except where noted. The bank's 58
lose their `/participants/{pid}` prefix outright; that segment is the port now.

**Central bank** (`:8081`)

```
GET  /reserves                 GET  /reserves/{pid}
GET  /audit
POST /members                  admission: opens reserve + settlement accounts
POST /settlements              body {cycleId} — was POST /cycles/{cid}/settle
POST /admin/reset
GET  /assets
```

**Clearing house** (`:8082`)

```
GET  /members                  the routing roster
GET  /payments                 every leg in the network
GET  /payments/{payid}         POST /payments
POST /payments/{payid}/reject  POST /payments/{payid}/return
GET  /payments/audit
GET  /cycles                   POST /cycles
GET  /cycles/{cid}             POST /cycles/{cid}/close
GET  /settlements              GET  /settlements/{sid}
GET  /mandates                 POST /mandates
GET  /mandates/{mid}           POST /mandates/{mid}/revoke
GET  /schemes                  GET  /directory
GET  /assets
```

**Member bank** (`:8083`…), every path relative to its own identity

```
GET  /me                                    was GET /participants/{pid}
GET|POST /ledgers                           GET  /ledgers/{lid}
GET|POST /ledgers/{lid}/subledgers          GET  /subledgers/{sid}
GET|POST /subledgers/{sid}/accounts         GET  /accounts/{aid}
GET  /accounts/{aid}/balance
GET|POST /transactions                      GET  /transactions/{tid}
POST /transactions/{tid}/reversal           GET  /audit
GET|POST /deposit-accounts                  GET|DELETE /deposit-accounts/{did}
…/{did}/balance …/holds …/identifiers …/identifiers/{scheme}/{value}
…/{did}/overdraft-terms …/overdraft-limit …/overdraft-pricing …/product
…/{did}/snapshots …/status …/interest-charge
GET  /holds/{hid}    POST /holds/{hid}/capture    POST /holds/{hid}/release
POST /deposits       POST /end-of-day             GET  /deposit-audit
GET  /totals         GET  /interest-refunds-payable
GET|POST /products   GET /products/{prid}         GET|POST /products/{prid}/versions
POST /products/{prid}/versions/{day}/publish       POST /products/{prid}/retire
GET|POST /facilities GET|DELETE /facilities/{fid}
…/{fid}/schedule …/disbursement …/draws …/interest-charge …/interest-refunds …/repayments
GET  /payments                              its own legs only          — new
GET  /payments/{payid}                      404 unless it is a party   — new
POST /payments                              202 Accepted + {paymentId} — new
GET  /directory                             resolve a payee            — new
GET  /assets
```

`GET /directory` is on the bank as well as the clearing house, and that is not
an oversight. A bank is a scheme participant with directory access; validating a
payee's address before accepting an instruction is exactly what it uses that
access for. The alternative — the customer's browser querying the CSM directly —
would give a retail app a clearing-house connection that no retail app has.

In code this is three constructors on the existing `Server` — `CentralBankRoutes()`,
`ClearingHouseRoutes()`, `BankRoutes(pid)` — replacing the single `Routes()`.
The mechanical core of the change is one helper: `s.participant(w, r)`, which
reads `{pid}` off the path (`api/server.go:113`), becomes a lookup of the
listener's bound identity. Every one of the 58 bank handlers is otherwise
untouched.

### Settling is the central bank's act

`POST /cycles/{cid}/settle` sits on the clearing house today because there is
only one server to put it on. Settlement moves reserves between accounts in the
central bank's own book; a clearing house that could do that would be a central
bank. It becomes `POST /settlements` on the central bank, taking the cycle id in
the body — the resource created is a settlement, and the cycle is its input.

The clearing house keeps `GET /settlements` and `GET /settlements/{sid}`: it
needs to know whether the cycle it closed has settled, and reading is not doing.

What is *not* modelled is the instruction — the message in which the clearing
house tells the central bank which cycle to settle and at what net positions.
Today it is out-of-band: an operator closes a cycle on one console and settles it
on another. That is honest for a teaching system and is exactly the gap 7b fills
with a real message.

### A bank sees its own payments and no others

`GET /payments` returns every payment in the network. A bank sees its
competitors' customers, their counterparties and their amounts. This has always
been wrong and has never been fixable, because narrowing it needs a caller
identity that a single shared server does not have.

On a bank's listener, `GET /payments` returns payments where the bank is the
debtor's or the creditor's participant. `GET /payments/{payid}` is 404 — not
403 — for a payment it is not party to: a payment it cannot see is a payment
that, as far as its API is concerned, does not exist, and a 403 would confirm the
id.

The clearing house keeps the unnarrowed list. It is the CSM: seeing every
payment is its job, not a leak.

### A customer instructs their bank, and the answer arrives later

A customer's browser must never call the clearing house. Retail payment
submission therefore lands on the bank: `POST /payments` on a bank's listener
accepts an instruction whose debtor is one of its own accounts (422 otherwise)
and returns

```
202 Accepted
{ "paymentId": "pay_…" }
```

with the outcome read back from `GET /payments/{paymentId}`.

`202` rather than `201` even though today's handler is synchronous, because
sub-project 7b converts submission to exactly this shape — a real CSM answers
with a `pacs.002` later, not by return value. A client built against a
synchronous "payment created" response would be rewritten; a client built
against "here is an identifier, ask again" would not. The cost of shaping it now
is one extra request in 6b's send form.

The clearing house keeps its own `POST /payments` for interbank submission and
for driving the system as an operator.

### Admission is not provisioning

Ports are statically configured. A participant created at runtime via
`POST /members` gets a store row, a chart of accounts, reserve and settlement
accounts, and **no listener until the process restarts**.

This is a decision, not a limitation to apologise for. Admitting a member to a
payment network is an operational act — a scheme agreement, a settlement account,
an operator provisioning a connection — and modelling it as an API call that
instantly yields a running bank would teach the wrong thing. 6b renders such a
bank as *awaiting provisioning* rather than hiding it.

Dynamic allocation was considered: the process would bind a fresh port on
`POST /members` and announce it. It is maybe forty lines, and it buys a
demonstration that a real network does not perform.

### Seeding and reset belong to the central bank

Both are process-wide acts that clear and rebuild every entity, so they need a
single owner. `Server.Reset`'s own doc comment already records that its mutex is
per-process and that "two servers sharing a database could still race"
(`api/server.go:60`); giving reset to more than one operator would make that race
routine rather than theoretical.

The central bank gets both: it is the operator that admits members, so "the
system starts with these four banks in it" is its act. In the default
single-process mode there is one process and the question is academic; in
multi-process mode only the central bank is started with `-seed`.

The seed builder keeps driving `payment.Network` directly rather than the
per-entity HTTP APIs. It is called in-process at boot and again by `Reset`, it
predates the split, and routing it through HTTP would make the sample dataset
depend on the listeners being up — turning a function call into a startup
ordering problem for no gain.

### `/assets` is on every listener

Asset definitions are compiled into `ledger`, not stored per book. Every operator
needs them to render money at the right scale, and a client that has to call a
different host to learn that EUR has two decimal places has learned nothing.
Duplicating a constant is not duplicating state.

### The roster splits, and the split is the point

`POST /members` is the central bank's, because admission opens central-bank
accounts. `GET /members` is the clearing house's, because routing a payment is
what needs to know who is reachable, and because it is what 6b's identity picker
asks.

They are two different questions that today's `POST /participants` and
`GET /participants` make look like one.

## Testing

- **Surface completeness and disjointness.** One test asserting that the union
  of the three route sets is exactly the 84 routes plus the four new ones, that
  none is lost, and that no route appears on two operators except the two
  deliberate overlaps — `/assets` on all three, `GET /directory` on the bank and
  the clearing house — which the test holds as an explicit allowlist rather than
  a tolerance, so a third accidental overlap fails. This is the analogue of 6b's
  nav-integrity test, and it is what makes an 84-route re-home reviewable.
- **`api/server_test.go` splits by operator.** Its helpers already take an
  `http.Handler` (`doJSON`, `getJSON`, `api/server_test.go:65`), so the existing
  ~2500 lines re-point at whichever operator's handler serves the route, and
  `newTestServer` grows siblings returning each surface.
- **The narrowing has its own tests**, because it is the one behaviour change a
  re-home can silently lose: a bank sees its own debtor legs and its own creditor
  legs, does not see a payment between two other banks, and gets 404 rather than
  403 for one.
- **`-entity` without `-database` exits non-zero with the explanatory message.**
  A test in `cmd/server`, because it is the guard rail for the property the whole
  design rests on.
- **`store/storetest` is untouched.** Nothing here changes the store.
- Both store runs stay green: `go test ./...` with no `DATABASE_URL`, and
  `TEST_DATABASE_URL=… go test ./...`.

## Failure modes

- **A route is lost or double-registered in the re-home.** The completeness test
  is the whole answer; it is written first.
- **A bank handler still reads `{pid}` from the path.** It would compile and
  return the wrong bank's data, since the segment is simply absent and
  `PathValue` returns `""`. The bound-identity helper returns an error for an
  empty id rather than falling through to a lookup, so this fails loudly on the
  first request instead of quietly on the first mismatch.
- **Six listeners on one process share `Server`'s reset mutex.** Correct, and
  worth stating: a reset blocks every operator, which is what a reset of a shared
  store should do.
- **`make dev` becomes six listeners and one frontend.** Ports must not collide
  with the existing `:8080` or the web app's `:3000`, which is why the block
  starts at `:8081`. `docker-compose.yml` is untouched — it holds only Postgres.
- **6b's proxy has to route by operator.** `web/src/app/api/[...path]/route.ts`
  forwards everything to one `BACKEND_URL`. It becomes `/api/<operator>/<path>`
  over a registry, keeping the no-CORS-by-construction property that
  `web/CLAUDE.md` calls out. The port table is Next-side configuration, because
  deployment topology is not domain data and does not belong in a DTO.

## What this makes wrong elsewhere

Recorded so the corrections are a task rather than a discovery:

- **`2026-07-31-role-scoped-web-ui-design.md`** — its *Out of scope* bullet on
  authn/authz ("the API stays open and unchanged … keeps this sub-project purely
  frontend") is false, as is the scheme-operator deferral and the route table
  that follows from it. Revised alongside this spec.
- **`handoff-2026-07-31-role-scoped-web-ui.md`** — "this sub-project is
  frontend-only" and the `GET /directory` paragraph.
- **`docs/superpowers/plans/2026-07-31-role-scoped-web-ui.md`** (`c91c092`) — the
  identity module, shell split and picker survive; the route move gains a fourth
  persona and the send task is rewritten around `202`. It is re-planned after
  this lands rather than patched now.
- **7a/7b (`spec/iso20022-messages`)** — 7b specifies "one goroutine per
  participant bank, one for the clearing house, one for the central bank,
  exchanging marshalled bytes over channels". After this sub-project each of
  those actors already has an address, so the channel wants to be an HTTP POST to
  a counterparty's message endpoint, and the mesh is the listener table rather
  than a `map[ParticipantID]chan []byte`. That is a better 7b and a real revision
  to its spec; it is noted here and made there.
