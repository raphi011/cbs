# Role-Scoped Web UI — Design

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

Three personas ship:

| Persona | Scope | Sees |
|---|---|---|
| Central bank operator | the network | reserves, net positions, settlements, clearing cycles, payments, schemes, mandates |
| Bank back office | one member bank | its customers, accounts, GL, transactions, lending, audit |
| Bank customer | one deposit account | balance, activity, sending money |

### Out of scope, deliberately

Recorded rather than dropped, because each is a real thing someone will ask for:

- **Any authentication or authorization.** The Go API stays open and unchanged.
  Every endpoint remains reachable by URL from every persona; the scoping is
  navigational, not enforced. Real authz is a backend sub-project of its own and
  would touch every handler, test and store. This is the decision that keeps
  this sub-project purely frontend.
- **A scheme-operator (clearing house) persona.** Clearing and settlement are
  genuinely different jobs done by different institutions, and splitting them
  would make the README's central distinction visible in the UI. Until then,
  cycles and payments live under the central bank — which does mean the central
  bank sees every individual payment, and a real one does not. Named here so the
  compromise is a decision rather than an oversight.
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
  | { persona: "bank"; pid: string }
  | { persona: "customer"; pid: string; did: string }
```

`web/src/lib/identity.ts` owns the type, `useIdentity()` (which derives it from
the pathname and returns `null` on `/` and `/learn/*`), `homeFor(identity)` and
`navFor(identity)`.

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

```
/                                          lobby
/learn, /learn/[chapter], /learn/mixed     unchanged, outside the persona system

/central-bank                              network overview  (today's /)
/central-bank/reserves                     (today's /central-bank)
/central-bank/payments, /payments/[payid], /payments/audit
/central-bank/mandates
/central-bank/cycles, /cycles/[cid]
/central-bank/settlements, /settlements/[sid]
/central-bank/schemes
/central-bank/audit

/bank/[pid]                                customer accounts + totals
/bank/[pid]/deposit-accounts/[did], …/statement
/bank/[pid]/ledger, /accounts/[aid], /transactions
/bank/[pid]/facilities, /facilities/[fid]
/bank/[pid]/audit, /bank/[pid]/deposit-audit

/customer/[pid]/[did]                      overview
/customer/[pid]/[did]/activity
/customer/[pid]/[did]/send
```

A catch-all at `app/participants/[...rest]/page.tsx` redirects to the matching
`/bank/…` route, so existing links and bookmarks do not 404. Roughly 18 link
sites across `web/src` move with the tree.

### Three shells, because the layouts genuinely differ

`app-shell.tsx` is 430 lines doing the resizable-panel machinery, the nav
config, the brand, the mobile sheets and the concept-panel bridging at once. It
splits into `components/shell/`:

- `shell-frame.tsx` — the `ResizablePanelGroup`, the collapse bridging and the
  `ResizeObserver` reverse-direction logic, persona-agnostic, taking its sidebar
  as a prop.
- `sidebar-nav.tsx` — renders a nav config.
- `identity-picker.tsx` — the switcher.
- `central-bank-shell.tsx`, `bank-shell.tsx` — the frame plus their nav.
- `customer-shell.tsx` — **no left panel.** A top tab strip (Accounts · Send ·
  Activity) and a `max-w-2xl` content column, which is what retail banking
  actually looks like and what makes the switch unmistakable.

`ConceptPanelProvider` moves up to the root layout so the concepts panel and its
state survive a persona switch.

**The concepts panel stays in all three shells, including the customer's.** A
retail bank app has no concepts rail, so this costs a little realism — and buys
the thing the repository exists for. A customer screen that cannot explain
`[[available-balance]]` is a worse trade.

### Each identity carries an accent, and a customer inherits its bank's

A CSS variable set on the shell root: institutional for the central bank, a
stable per-`pid` colour for each member bank, and for a customer **the accent of
their bank**. You are a customer *of* Aurora, and the screen should say so
without a label.

### The switcher is one flat list of complete identities

A searchable dropdown in the topbar, grouped Central bank / Banks / Customers
(customers grouped under their bank), showing the current identity. Selecting
one navigates to `homeFor(it)`.

One control rather than a persona toggle plus a context picker, because a
persona without its context is not an identity — "customer" alone addresses
nothing, and a two-control design has a state where the persona has changed and
the context has not.

Frozen and Closed accounts are listed and selectable. Seeing the customer view
of a frozen account is one of the better lessons available here.

### The lobby is always the root

`/` never redirects. A first-time visitor is shown the cast — the central bank,
the four member banks with their reserves, and the customers grouped by bank
with their account status badged, plus an entry to Learn — and picks one.

The alternative, remembering the last identity, saves a click for a repeat
visitor and costs the newcomer the one screen that makes the app's structure
obvious. For a teaching system that is the wrong trade.

Today's dashboard (`web/src/app/page.tsx`, 285 lines: network stats and member
bank cards) moves wholesale to `/central-bank`.

### Back office is mostly a re-home

The bank's sub-nav is currently a tab strip *inside* a page
(`web/src/app/participants/[pid]/layout.tsx:26-34`), which is what "a section of
the network app" looks like. Those tabs become the shell's sidebar. That
promotion, plus a bank home that lists customer accounts with balances instead
of the current thin overview, is most of the persona; freezing, closing, holds,
GL, lending and audit all exist already.

### Customer is the only genuinely new surface

- **Overview** — balance, available balance, overdraft headroom, the account's
  IBAN, status, and a short recent-activity list.
- **Activity** — the existing statement components
  (`components/statement/*`), retail-framed.
- **Send** — amount, reference, and a payee entered as an **IBAN**, resolved
  live through `GET /directory` to a name and a bank before the customer
  confirms, then `POST /payments` as an SCT. This is the screen that requires
  `2026-07-31-account-addressing-design.md` and the reason that sub-project
  comes first.
- A **Frozen** account shows the debit block and disables sending. Cheap, and
  the best single lesson in the persona: money can still arrive.

## Testing

- `identity.test.ts` — pathname → `Identity` for all three personas and the two
  null cases; `homeFor`; `navFor`.
- A nav-integrity test asserting every `href` in every nav config corresponds to
  a real route file. This is what catches a dead link after the route move, and
  the move is the riskiest mechanical part of the work.
- `concept-links.test.ts` and `quiz/diversity.test.ts` are unaffected but must
  stay green; per `CLAUDE.md`, a `[[wiki-link]]` to a missing key takes every
  route down at runtime while `next build` stays green, so a page must be loaded
  in each of the three shells before this is called done.

## Failure modes

- **The route move breaks a link.** Caught by the nav-integrity test for nav
  links and by the `/participants/[...rest]` redirect for anything external.
  In-page links are the residual risk and are what the per-persona page load is
  for.
- **A customer identity points at a deleted or closed account.** Closed accounts
  are deliberately reachable; a `did` that does not exist renders the existing
  not-found treatment, as `/participants/[pid]` does today.
- **The identity picker fetches deposit accounts per bank.** Four parallel
  queries, cached by react-query, and the lobby needs the same data — so the
  picker and the lobby share it rather than each fetching.
- **The concepts panel in a `max-w-2xl` customer shell has little room.** It
  keeps its own resizable panel and collapse strip; the constraint is on the
  content column, not the viewport.
