# Role-Scoped Web UI — Design

Sub-project 6b in `docs/expansion-roadmap.md`, built on **6a, the operator-split
API** (`2026-07-31-operator-split-api-design.md`). Read that one first: it makes
each entity its own listener, and this spec's personas map to those listeners
rather than to route prefixes. Sections marked *revised for 6a* replace what
this document said when the API was a single open server.

The web app is one unified dashboard. Every screen in the system is reachable
from a single left nav (`web/src/components/app-shell.tsx:47-56`), a bank is a
*section* of that app rather than a place you are, and a customer does not exist
as a point of view at all — a deposit account is a row in a back-office table
and nothing else.

That is a faithful map of the API and a poor map of the domain. In the real
thing there is no observer who sees all of it. A back-office user sees one
bank's customers; a customer sees one account; the central bank sees reserves
and settlement instructions and not a single individual payment's description.
The unified dashboard flattens exactly the boundaries the rest of this
repository takes care to model.

This design replaces it with **identities you switch between**.

## Goal

Make the experience realistic and intuitive by making *who you are* the app's
top-level structure, and by giving each identity the software its real-world
counterpart uses rather than a filtered view of everybody's.

*Revised for 6a.* **Four** personas ship, and three of them are a backend:

| Persona | Backend | Scope | Sees |
|---|---|---|---|
| Central bank operator | central bank | the settlement layer | reserves, its audit, admission, settling a closed cycle |
| Clearing house operator | clearing house | the network | payments, clearing cycles, settlements, schemes, mandates, the directory |
| Bank back office | that bank | one member bank | its customers, accounts, GL, transactions, lending, audit, its own payments |
| Bank customer | their bank | one deposit account | balance, activity, sending money |

A customer is not a backend of their own: they talk to their bank's listener,
which is what a retail app does. That asymmetry is the point — the customer is a
*view onto* a bank, and the other three are institutions.

### Out of scope, deliberately

Recorded rather than dropped, because each is a real thing someone will ask for:

- **Any authentication or authorization.** *Revised for 6a.* The scoping is
  structural now — a persona reaches a listener that has no route for another
  operator's data, so a URL edit cannot cross the boundary — but nothing
  verifies that the caller on a bank's port *is* that bank. The port is the
  claim. Real authz remains a sub-project of its own; what 6a removed is the
  weaker statement this bullet used to make, that every endpoint stays reachable
  from every persona.
- ~~**A scheme-operator (clearing house) persona.**~~ *Superseded by 6a.* It
  ships. Splitting the API gave the clearing house a backend, and leaving its
  screens on the central bank's console would have put a visible boundary
  underneath a fiction. Payments, cycles, settlements, schemes, mandates and the
  directory move to it, and the central bank stops seeing every individual
  payment — which a real one does not. The README's clearing-versus-settlement
  distinction becomes visible in the UI for the first time.
- **A card-processor persona.** There is no card scheme in the backend
  (`README.md`, *Next Work*). `2026-07-31-account-addressing-design.md` is its
  groundwork: a PAN is an account identifier.
- **Customer mandates and customer credit.** No direct-debit list, no revoke, no
  overdraft terms card, no loan schedule or repayment in the customer view. All
  four exist in the back-office view already. The customer view ships with
  accounts, activity and sending, and nothing else.
- **A bank-scoped mandates view.** Mandates span two banks and stay
  network-level.
- **A products screen.** The product catalogue has an API and no web page today;
  this sub-project does not add one.
- **Multiple accounts per person.** There is no party master — the roadmap's
  sub-project 1 settled that a customer entity is its own sub-project. A
  customer identity therefore **is** one deposit account. "Alice Andersson" the
  identity is `(Aurora, that account)`, and a second account would be a second
  identity.

## Decisions

### Identity is derived from the URL, and persisted nowhere

```ts
type Identity =
  | { persona: "central-bank" }
  | { persona: "clearing-house" }
  | { persona: "bank"; pid: string }
  | { persona: "customer"; pid: string; did: string }
```

`web/src/lib/identity.ts` owns the type, `useIdentity()` (which derives it from
the pathname and returns `null` on `/` and `/learn/*`), `homeFor(identity)` and
`navFor(identity)`.

*Revised for 6a.* It also owns `backendFor(identity)`, which is the same
switch and returns the operator key the proxy routes on — `"central-bank"`,
`"clearing-house"`, or `` `bank/${pid}` `` for both a bank and its customers.
One function, because an identity that named a persona and a backend separately
could name a pair that does not exist.

Persona-prefixed routes rather than a client-side flag, because a view that is
not addressable is not a view: "the customer's version of this account" has to
be something you can link to, refresh into, and go back out of. A `?as=customer`
query parameter would be addressable too but would keep the route tree
back-office-shaped, so every customer screen would be a branch inside a
back-office page.

Nothing is stored in `localStorage`. `/` is always the lobby, so there is no
last-identity to remember, and `participant-switcher.tsx` — with its
`ledger.lastParticipant` key (`:22`) — is deleted rather than adapted.

### Routes

*Revised for 6a* — the network screens split across two operators.

```
/                                          lobby
/learn, /learn/[chapter], /learn/mixed     unchanged, outside the persona system

/central-bank                              reserves + settling a closed cycle
/central-bank/audit
                                           (today's / — the network overview —
                                            becomes the clearing house's home)

/clearing-house                            network overview  (today's /)
/clearing-house/payments, /payments/[payid], /payments/audit
/clearing-house/mandates
/clearing-house/cycles, /cycles/[cid]
/clearing-house/settlements, /settlements/[sid]
/clearing-house/schemes
/clearing-house/directory                  resolve an address — new, and the
                                           only screen the split adds

/bank/[pid]                                customer accounts + totals
/bank/[pid]/deposit-accounts/[did], …/statement
/bank/[pid]/ledger, /accounts/[aid], /transactions
/bank/[pid]/facilities, /facilities/[fid]
/bank/[pid]/payments                       its own legs only — new
/bank/[pid]/audit, /bank/[pid]/deposit-audit

/customer/[pid]/[did]                      overview
/customer/[pid]/[did]/activity
/customer/[pid]/[did]/send
```

A catch-all at `app/participants/[...rest]/page.tsx` redirects to the matching
`/bank/…` route, so existing links and bookmarks do not 404.

**The link surface is larger than this spec first claimed.** It said "roughly 18
link sites"; it is about 25 in components and pages, *plus* roughly 35
`explore.href` values across seven quiz chapter files, governed by the
`EXPLORE_ROUTES` allowlist in `web/src/lib/quiz/index.ts` that
`quiz/index.test.ts` already enforces. Nothing today checks that an allowlisted
route corresponds to a real page; the nav-integrity test under *Testing* now
does, which is what makes the quiz links safe to move.

### The proxy routes by operator

*New for 6a.* `web/src/app/api/[...path]/route.ts` forwards everything to one
`BACKEND_URL`. It becomes `/api/<operator>/<path>`, resolving the operator key
from `backendFor(identity)` against a registry, which keeps the
no-CORS-by-construction property `web/CLAUDE.md` calls out: the browser still
only ever talks to its own origin.

The port table is Next-side configuration — an env-driven JSON map, defaulting
to 6a's `:8081`–`:8086` block. It is not served by any backend and appears in no
DTO: deployment topology is not domain data, and a `GET /members` that returned
base URLs would make the roster a deployment manifest.

A bank admitted at runtime has a store row and no listener (6a, *Admission is not
provisioning*). The lobby and the picker render it as **awaiting provisioning**
and refuse to enter it, rather than offering a console whose every request 502s.

Two controls stop following the shell they live in. `ResetButton` sits in every
sidebar but `POST /admin/reset` is the central bank's, and
`CreateParticipantDialog` writes through `POST /members`, also the central
bank's. Both address that backend explicitly regardless of which persona is on
screen — which is correct rather than awkward: resetting the system and
admitting a member are the central bank's acts wherever you happen to be
standing.

### Four shells, because the layouts genuinely differ

`app-shell.tsx` is 430 lines doing the resizable-panel machinery, the nav
config, the brand, the mobile sheets and the concept-panel bridging at once. It
splits into `components/shell/`:

- `shell-frame.tsx` — the `ResizablePanelGroup`, the collapse bridging and the
  `ResizeObserver` reverse-direction logic, persona-agnostic, taking its sidebar
  as a prop.
- `sidebar-nav.tsx` — renders a nav config.
- `identity-picker.tsx` — the switcher.
- `central-bank-shell.tsx`, `clearing-house-shell.tsx`, `bank-shell.tsx` — the
  frame plus their nav. Three consoles with the same layout and different
  contents; the shells differ only in what they hand `sidebar-nav.tsx`, which is
  why they are three thin files rather than one taking a persona prop.
- `customer-shell.tsx` — **no left panel.** A top tab strip (Accounts · Send ·
  Activity) and a `max-w-2xl` content column, which is what retail banking
  actually looks like and what makes the switch unmistakable.

`ConceptPanelProvider` moves up to the root layout so the concepts panel and its
state survive a persona switch.

A fifth arrangement falls out of `useIdentity()` returning `null`: the lobby and
`/learn/*` get `plain-shell.tsx`, the frame with no sidebar — the same
two-panel arrangement the customer uses. `ShellFrame` therefore takes its
sidebar as an optional prop and renders two panels when there is none.

**The concepts panel stays in every shell, including the customer's.** A retail
bank app has no concepts rail, so this costs a little realism — and buys the
thing the repository exists for. A customer screen that cannot explain
`[[balance-available]]` is a worse trade.

### Each identity carries an accent, and a customer inherits its bank's

A CSS variable set on the shell root: two distinct institutional colours for the
central bank and the clearing house, a stable per-`pid` colour for each member
bank, and for a customer **the accent of their bank**. You are a customer *of*
Aurora, and the screen should say so without a label.

The two institutions get *different* accents rather than sharing one, because
telling them apart at a glance is exactly the lesson the split exists to teach.

### The switcher is one flat list of complete identities

A searchable dropdown in the topbar, grouped Institutions / Banks / Customers
(customers grouped under their bank), showing the current identity. Selecting
one navigates to `homeFor(it)`. The central bank and the clearing house are the
two rows under Institutions.

One control rather than a persona toggle plus a context picker, because a
persona without its context is not an identity — "customer" alone addresses
nothing, and a two-control design has a state where the persona has changed and
the context has not.

Frozen and Closed accounts are listed and selectable. Seeing the customer view
of a frozen account is one of the better lessons available here.

### The lobby is always the root

`/` never redirects. A first-time visitor is shown the cast — the central bank
and the clearing house, the four member banks with their reserves, and the
customers grouped by bank with their account status badged, plus an entry to
Learn — and picks one.

The alternative, remembering the last identity, saves a click for a repeat
visitor and costs the newcomer the one screen that makes the app's structure
obvious. For a teaching system that is the wrong trade.

Today's dashboard (`web/src/app/page.tsx`, 285 lines: network stats and member
bank cards) moves wholesale to `/clearing-house` — *revised for 6a*; it counts
payments, cycles and settlements, which are the CSM's, not the central bank's.
The central bank's home is the reserves table it already has.

### The two institutions are a re-home and a subtraction

*New for 6a.* Splitting the central bank's console in two adds almost no
screens. Payments, cycles, settlements, schemes and mandates move to the
clearing house exactly as they are; reserves and the central-bank audit stay.
What the central bank *gains* is the settle action, moved off the cycle detail
page and onto its own console, where a closed cycle becomes a settleable one.
What it loses is every screen that shows an individual payment — which is the
whole point, and is a subtraction rather than work.

The one genuinely new screen is `/clearing-house/directory`: type an address,
see which bank and account holds it. `GET /directory` has had no UI since
sub-project 5 shipped it, and the CSM is the operator whose job that question
is.

### Back office is mostly a re-home

The bank's sub-nav is currently a tab strip *inside* a page
(`web/src/app/participants/[pid]/layout.tsx:26-34`), which is what "a section of
the network app" looks like. Those tabs become the shell's sidebar. That
promotion, plus a bank home that lists customer accounts with balances instead
of the current thin overview, is most of the persona; freezing, closing, holds,
GL, lending and audit all exist already.

*Revised for 6a:* it gains one screen it could not have had. `/bank/[pid]/payments`
lists the bank's **own** legs — the payments it sent and received and nothing
else — which is 6a's narrowed `GET /payments`. Today's payments table renders
this data already; the screen is the existing component pointed at a listener
that withholds everybody else's rows.

### Customer is no longer the only new surface

- **Overview** — balance, available balance, overdraft headroom, the account's
  IBAN, status, and a short recent-activity list.
- **Activity** — the existing statement components
  (`components/statement/*`), retail-framed.
- **Send** — amount, reference, and a payee entered as an **IBAN**, resolved
  live to a name and a bank before the customer confirms. This is the screen
  that requires `2026-07-31-account-addressing-design.md` and the reason that
  sub-project came first.

  *Revised for 6a, twice over.* The lookup no longer calls `GET /directory`
  from the browser: that route is the clearing house's, and a customer's browser
  must not talk to the CSM. It goes to **their bank's** listener, which resolves
  on their behalf — which is also what a real retail app does, since a customer
  has no CSM connection.

  And submission is **`POST /payments` on the bank, answering `202 Accepted`
  with a `{paymentId}`**, whose outcome the form reads back from
  `GET /payments/{id}`. Not because today's handler is asynchronous — it is not
  — but because 7b converts it, a real CSM answering with a `pacs.002` later
  rather than by return value. A form built around a synchronous "payment
  created" response would be rewritten in 7b; this one will not be. The cost is
  one extra request.
- A **Frozen** account shows the debit block and disables sending. Cheap, and
  the best single lesson in the persona: money can still arrive.

## Testing

- `identity.test.ts` — pathname → `Identity` for all four personas and the two
  null cases; `homeFor`; `navFor`; `backendFor`, including that a customer
  resolves to their bank's backend and not one of their own.
- A nav-integrity test asserting every `href` in every nav config corresponds to
  a real route file, **and that every `EXPLORE_ROUTES` entry does too**. This is
  what catches a dead link after the route move, and the move is the riskiest
  mechanical part of the work — larger than this spec first estimated, since the
  quiz's ~35 deep-links all move.
- `concept-links.test.ts` and `quiz/diversity.test.ts` are unaffected but must
  stay green; per `CLAUDE.md`, a `[[wiki-link]]` to a missing key takes every
  route down at runtime while `next build` stays green, so a page must be loaded
  in each of the five shell arrangements before this is called done.

## Failure modes

- **The route move breaks a link.** Caught by the nav-integrity test for nav
  links and by the `/participants/[...rest]` redirect for anything external.
  In-page links are the residual risk and are what the per-persona page load is
  for.
- **A customer identity points at a deleted or closed account.** Closed accounts
  are deliberately reachable; a `did` that does not exist renders the existing
  not-found treatment, as `/participants/[pid]` does today.
- **The identity picker fetches deposit accounts per bank.** *Revised for 6a:*
  four parallel queries **to four different backends**, cached by react-query,
  and the lobby needs the same data — so the picker and the lobby share it
  rather than each fetching. The roster itself comes from the clearing house's
  `GET /members`, so the picker touches five backends to draw one list. That is
  the honest cost of the split and is why the shared hook is a requirement
  rather than an optimisation.
- **A bank in the roster has no listener.** Admitted at runtime, not yet
  provisioned (6a). Its per-bank query would 502 forever. The picker and lobby
  read the port registry, badge such a bank *awaiting provisioning*, skip its
  query and refuse selection — so an un-provisioned bank costs one row of
  explanation rather than four failing requests and a dead console.
- **A persona's shell renders before its backend answers.** Each console now
  depends on a listener that can be individually down, where previously one
  backend served everything. A 502 from one operator must not read as an empty
  system: the existing `ErrorState` already distinguishes transport failure via
  `describeError`, and the proxy's "Backend unreachable at …" message gains the
  operator key so the message names *which* one.
- **The concepts panel in a `max-w-2xl` customer shell has little room.** It
  keeps its own resizable panel and collapse strip; the constraint is on the
  content column, not the viewport.
