# Role-Scoped Web UI (6b) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the web app's single unified dashboard with **four** identities you
switch between — central bank operator, clearing house operator, bank back
office, bank customer — each on persona-prefixed routes, in its own shell,
talking to its own operator's listener.

**Architecture:** Identity is derived from the URL and persisted nowhere
(`web/src/lib/identity.ts`), which also owns `backendFor` — the operator key the
proxy routes on, so an identity can never name a persona and a backend that do
not go together. Routes move under `/central-bank/…`, `/clearing-house/…`,
`/bank/[pid]/…` and `/customer/[pid]/[did]/…`, with a catch-all redirect keeping
old `/participants/…` links alive. The 430-line `app-shell.tsx` splits into
`components/shell/`: a persona-agnostic `ShellFrame` plus one shell per persona,
chosen by a `PersonaShell` dispatcher. `/` is always a lobby.

**Tech Stack:** Next.js 16 (App Router) · React 19 · Tailwind v4 · shadcn/ui on
Radix (unified `radix-ui` package) · TanStack Query v5 · lucide-react · vitest ·
Go 1.x (one small backend task only).

**Branch:** `spec/role-scoped-web-ui`, already created off `spec/operator-split-api`.

## Global Constraints

- **Every web commit must pass all four gates**, run from `web/`:
  `npm run typecheck` · `npm run lint` · `npm run test` · `npm run build`. All
  four clean. If `node_modules` is missing in a fresh worktree, `npm install`
  first.
- **`go test ./...` must stay green with no `DATABASE_URL` and against
  Postgres.** Only Task 1 touches Go; every later task must not break it.
  Postgres is a local Homebrew service, not the Docker container:
  `TEST_DATABASE_URL=postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable go test ./...`.
  Start it with `brew services start postgresql@18`. **Do not use `make test-pg`** —
  it starts docker-compose and will hit the `cbs-pg` name conflict.
- **A `[[wiki-link]]` to a key absent from `web/src/components/hint-content.ts`
  throws at runtime under `RootLayout` and takes every route in the dev app
  down, while `next build` stays green.** `npm run test` catches it, in hint
  bodies *and* quiz explanations. Every hint key this plan uses already exists —
  do not invent one. The available balance is **`balance-available`** (the spec's
  prose says `available-balance`, which is not a key), and the overdraft key is
  **`overdraft-interest`** (there is no `overdraft`). A `<Hint id>` typo *is*
  caught by `tsc` via the `HintKey` union; a `[[wiki-link]]` typo is not.
- **Vitest is node-environment, pure logic only**: `include: ["src/**/*.test.ts"]`
  — `.ts` only, no `.tsx`, no DOM, no component tests. `lucide-react` and
  `next/navigation` both import cleanly under it (verified in 6a). `next/server`
  is **not** exercised under it: keep route-handler logic in a plain `.ts` module
  and test that.
- **`web/src/lib/quiz/diversity.test.ts`** holds each chapter to 18–22 questions,
  ≥8 distinct `concept` tags, no tag more than 3×, all three difficulty tiers.
  Nothing in this plan changes question content, so it must stay green untouched.
- **Never hand-write a backend path.** Build them with `cb()`, `csm()` and
  `bank(pid, …)` from `web/src/lib/api/operator.ts`. The proxy resolves the first
  segment after `/api` to a listener.
- **All amounts are integers in the minor units of their asset.** Every formatter
  takes the asset it renders; there is no default scale. An unresolved asset code
  means render a `<Skeleton>` or `<UnresolvedAmount>`, never a number.
- **Backend `DisallowUnknownFields()`**: send only the exact keys a request DTO
  defines. A stray key is a 400.
- **Next 16 async params**: dynamic *pages* are client components using
  `useParams()`; a server component or route handler awaits its `params` promise.
- **Match the existing shadcn/Tailwind design system.** Refined and minimal — do
  not impose a new aesthetic.
- **Do not add a dependency.** Everything here is buildable from what
  `web/package.json` already has.
- **A scripted edit that matches nothing must fail, not pass silently.** This is
  6a's first lesson and 6b's route move is exactly where it bites. Every scripted
  replacement in Task 4 asserts on its match count, and the task ends with a grep
  that must print nothing.

## Two decisions taken before planning, and why

Both are places where the design spec does not survive contact with what 6a
actually shipped. Both were put to the author and answered; they are requirements
now, not suggestions.

1. **The central bank could not read the cycle it settles.** The spec gives it
   "settling a closed cycle" on its own console, but 6a's `centralBankRouter`
   serves only `POST /settlements` — no `GET /cycles`, no `GET /settlements`. A
   settle screen there would have nothing to list. **Task 1 adds those four read
   routes to the central bank's surface.** A central bank settles on
   instruction, and a closed cycle with its net positions *is* that instruction;
   being unable to read it was an omission rather than a boundary. This is the
   only Go in this plan.
2. **"Awaiting provisioning" had no registry to read.** The spec has the lobby
   and picker read a Next-side env-driven port map; 6a instead made the proxy
   *derive* ports from the clearing house's roster (roadmap deviation 6), so no
   such map exists and every roster member looks provisioned until its request
   502s. **Task 3 adds a Next-side `GET /api/operators`** that resolves each
   bank's derived URL and probes it once. This honours the spec's actual decision
   — deployment topology is Next's knowledge and appears in no backend DTO —
   while its stated mechanism has moved, and it answers for every bank in one
   request instead of one failure per bank.

## Three findings this plan carries in, with exact numbers

Counted against the tree, not estimated:

1. **The route move touches 13 `/participants/${…}` link sites plus 13
   network-route literals in components and pages, *and* 33 of the 40
   `explore.href` values across 8 quiz chapter files.** `explore.href` is typed
   `string` (`quiz/types.ts:17`), so `tsc` catches none of it; only
   `quiz/index.test.ts:73` (href ∈ `EXPLORE_ROUTES`) does, and **nothing checks
   that an allowlisted route names a real page.** Task 4 fixes both ends: it
   narrows `explore.href` to `(typeof EXPLORE_ROUTES)[number]` so the compiler
   holds the chapters to the allowlist, and adds `nav-integrity.test.ts` so the
   allowlist is held to the route tree.
2. **`StatementTable` needs a `retail` variant.** It renders an `AccountRef` per
   contra leg and an expandable "Underlying GL transaction" panel listing every
   GL entry — the bank's chart of accounts, linking into
   `/bank/[pid]/accounts/[aid]`. Reused whole in the customer's Activity screen
   it leaks the back office into the retail view and navigates out of the
   persona. Task 12 adds the variant.
3. **`errors.ts:26` still says "Is `go run ./cmd/server` running on :8080?"**
   There is no :8080 since 6a. Corrected in Task 3, where the operator key is
   already in hand.

## File Structure

**Created**

| File | Responsibility |
|---|---|
| `web/src/lib/identity.ts` | `Identity`, `identityFromPathname`, `useIdentity`, `homeFor`, `navFor`, `backendFor` |
| `web/src/lib/identity.test.ts` | pathname → identity for four personas; `homeFor`; `navFor`; `backendFor` |
| `web/src/lib/nav-integrity.test.ts` | every nav href, every persona home and every `EXPLORE_ROUTES` entry resolves to a real `page.tsx` |
| `web/src/lib/api/backend-url.ts` | pure port derivation shared by the proxy and the operator probe |
| `web/src/lib/api/backend-url.test.ts` | institution ports, bank-by-roster-position, `BACKENDS` overrides, unknown pid |
| `web/src/app/api/operators/route.ts` | probes each derived listener; answers which operator keys are live |
| `web/src/lib/accent.ts` + `.test.ts` | per-identity accent; a customer inherits its bank's |
| `web/src/lib/use-debounced-value.ts` | debounce for the send form's live IBAN lookup |
| `web/src/app/participants/[...rest]/page.tsx` | server redirect `/participants/…` → `/bank/…` |
| `web/src/app/central-bank/audit/page.tsx` | the central bank's own log (split out of today's `/central-bank`) |
| `web/src/app/clearing-house/directory/page.tsx` | resolve an address — the one genuinely new operator screen |
| `web/src/app/bank/[pid]/payments/page.tsx` | a bank's own legs — new, and impossible before 6a |
| `web/src/components/payments-table.tsx` | the payments table, shared by the CSM's list and a bank's |
| `web/src/components/shell/shell-frame.tsx` | `ResizablePanelGroup`, collapse bridging, `ResizeObserver`; sidebar optional |
| `web/src/components/shell/sidebar-nav.tsx` | renders a `NavItem[]` |
| `web/src/components/shell/topbar.tsx` | brand, identity picker, theme toggle, mobile triggers |
| `web/src/components/shell/central-bank-shell.tsx` | frame + the central bank's nav |
| `web/src/components/shell/clearing-house-shell.tsx` | frame + the clearing house's nav |
| `web/src/components/shell/bank-shell.tsx` | frame + one bank's nav |
| `web/src/components/shell/customer-shell.tsx` | **no left panel**; top tab strip + `max-w-2xl` column |
| `web/src/components/shell/plain-shell.tsx` | no persona: the lobby and `/learn/*` |
| `web/src/components/shell/persona-shell.tsx` | picks the shell from `useIdentity()` |
| `web/src/components/shell/identity-picker.tsx` | one flat grouped searchable list of identities |
| `web/src/app/customer/[pid]/[did]/{page,send/page,activity/page}.tsx` | the customer's three screens |

**Moved** (`git mv`, so history follows) — exact sequence in Task 4.

**Modified**: `web/src/app/layout.tsx`, `web/src/app/page.tsx`,
`web/src/app/api/[...path]/route.ts`, `web/src/lib/quiz/{index,types}.ts`, eight
quiz chapter files, `web/src/lib/api/{endpoints,query-keys,hooks,errors}.ts`,
`web/src/lib/types.ts`, `web/src/components/{account-ref,create-participant-dialog}.tsx`,
`web/src/components/statement/{statement-card,statement-table}.tsx`,
`api/surface.go`, `api/surface_test.go`.

**Deleted**: `web/src/components/app-shell.tsx`,
`web/src/components/participant-switcher.tsx`,
`web/src/app/bank/[pid]/deposit-accounts/page.tsx` (Task 11 folds it into the
bank home).

---

## Task 1: The central bank can read the cycle it settles

The only Go in this plan. A central bank settles on instruction; the closed
cycle and its net positions *are* that instruction, and 6a left it unable to read
them — so its console could offer a settle action with nothing to settle.

**Files:**
- Modify: `api/surface.go:40-59` (`centralBankRouter`)
- Modify: `api/surface_test.go:27-34` (`allowedOverlaps`) and add one test

**Interfaces:**
- Consumes: `handleListCycles`, `handleGetCycle`, `handleListSettlements`,
  `handleGetSettlement` — all four already exist in `handlers_payment.go` and
  read through `s.network()`, which is network-wide on every listener. No handler
  changes.
- Produces: `GET /cycles`, `GET /cycles/{cid}`, `GET /settlements`,
  `GET /settlements/{sid}` on the central bank's surface.

- [ ] **Step 1: Write the failing test**

Append to `api/surface_test.go`:

```go
// TestTheCentralBankCanReadTheCycleItSettles is the read half of
// TestSettlingIsTheCentralBanksAct.
//
// Settling is the central bank's act, and an operator who cannot see the closed
// cycle or its net positions cannot perform it — the console would offer a
// settle button with nothing to name. A settlement instruction in the real thing
// IS a closed cycle and its positions, so reading them is part of the act rather
// than a widening of the surface. What the central bank still cannot see is an
// individual payment, which is the boundary that matters.
func TestTheCentralBankCanReadTheCycleItSettles(t *testing.T) {
	h := newServer(t, nil)
	cid := closedCycle(t, h)

	var cycles []clearingCycleDTO
	getJSON(t, cb(h), "/cycles", &cycles)
	if len(cycles) != 1 || cycles[0].ID != cid {
		t.Fatalf("the central bank sees %v, want the one closed cycle %s", cycles, cid)
	}

	got := doJSON(t, cb(h), "GET", "/cycles/"+cid, "", http.StatusOK)
	if got["status"] != "Closed" {
		t.Fatalf("cycle status = %v, want Closed", got["status"])
	}

	sid := doJSON(t, cb(h), "POST", "/settlements",
		`{"cycleId":"`+cid+`"}`, http.StatusOK)["id"].(string)

	// And it can read back what it did, without asking the clearing house.
	var settlements []settlementDTO
	getJSON(t, cb(h), "/settlements", &settlements)
	if len(settlements) != 1 {
		t.Fatalf("the central bank sees %d settlements, want 1", len(settlements))
	}
	doJSON(t, cb(h), "GET", "/settlements/"+sid, "", http.StatusOK)

	// The boundary that matters is untouched: an individual payment is still
	// the clearing house's, and a central bank does not see one.
	assertStatus(t, cb(h), "GET", "/payments", "", http.StatusNotFound)
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd /Users/raphaelgruber/Git/cbs-account-addressing && go test ./api/ -run TestTheCentralBankCanReadTheCycleItSettles -v`
Expected: FAIL — the central bank's mux has no `/cycles`, so `getJSON` gets a 404.

If it fails instead on `clearingCycleDTO` or `settlementDTO` being undefined,
check their names in `api/dto_payment.go` and use the real ones; the point of the
test is the routes, not the type names.

- [ ] **Step 3: Add the routes**

Edit `api/surface.go`, in `centralBankRouter()`, immediately after the
`POST /settlements` registration:

```go
	// A central bank settles on instruction, and the instruction IS a closed
	// cycle and its net positions — so it has to be able to read them. What it
	// still cannot reach is an individual payment: GET /payments is the clearing
	// house's, and a real central bank does not see one.
	mux.HandleFunc("GET /cycles", s.handleListCycles)
	mux.HandleFunc("GET /cycles/{cid}", s.handleGetCycle)
	mux.HandleFunc("GET /settlements", s.handleListSettlements)
	mux.HandleFunc("GET /settlements/{sid}", s.handleGetSettlement)
```

- [ ] **Step 4: Record the four new overlaps**

`TestSurfacesAreDisjoint` will now fail: those four patterns are on the clearing
house too. That is deliberate, so it goes in the allowlist **with its reasoning**,
which is what the allowlist is for. Edit `api/surface_test.go`, adding to the
doc comment above `allowedOverlaps`:

```go
//   - The four cycle and settlement reads are on the central bank as well as the
//     clearing house, with the same handlers and the same answers. They are not
//     two views of one thing: the clearing house reads them because it closed the
//     cycle and wants to know whether it settled, and the central bank reads them
//     because a closed cycle and its net positions are the instruction it acts on.
```

and to the slice:

```go
	"GET /cycles",
	"GET /cycles/{cid}",
	"GET /settlements",
	"GET /settlements/{sid}",
```

- [ ] **Step 5: Run the Go suite on both stores**

```bash
cd /Users/raphaelgruber/Git/cbs-account-addressing
go build ./... && go vet ./... && go test ./...
TEST_DATABASE_URL=postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable go test ./...
```

Expected: both green. If the second cannot connect, run
`brew services start postgresql@18` and retry — do **not** fall back to
`make test-pg`, which starts docker-compose and hits the `cbs-pg` name conflict.

- [ ] **Step 6: Commit**

```bash
git add api/surface.go api/surface_test.go
git commit -m "feat(api): a central bank can read the cycle it is asked to settle"
```

---

## Task 2: The identity module

**Files:**
- Create: `web/src/lib/identity.ts`
- Test: `web/src/lib/identity.test.ts`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Identity = { persona: "central-bank" } | { persona: "clearing-house" } | { persona: "bank"; pid: string } | { persona: "customer"; pid: string; did: string }`
  - `type Persona = Identity["persona"]`
  - `interface NavItem { href: string; label: string; icon: LucideIcon; exact?: boolean }`
  - `function identityFromPathname(pathname: string): Identity | null`
  - `function useIdentity(): Identity | null`
  - `function homeFor(identity: Identity): string`
  - `function navFor(identity: Identity): NavItem[]`
  - `function backendFor(identity: Identity): string` — the operator key the proxy
    routes on: `"central-bank"`, `"clearing-house"`, or `` `bank/${pid}` `` for
    both a bank and its customers.

**A nav entry may not name a page that does not exist yet.** That is exactly what
Task 4's integrity test rejects, so three entries are deliberately absent here
and each arrives in the task that builds its screen, test-first:

| Entry | Arrives in |
|---|---|
| `/clearing-house/directory` | Task 10 |
| `/bank/[pid]/payments` | Task 11 |
| the whole customer nav (`navFor` returns `[]`) | Tasks 12 and 13 |

- [ ] **Step 1: Write the failing test**

Create `web/src/lib/identity.test.ts`:

```ts
import { describe, expect, it } from "vitest";

import {
  backendFor,
  homeFor,
  identityFromPathname,
  navFor,
  type Identity,
} from "./identity";

describe("identityFromPathname", () => {
  it("reads the two institutions off their prefixes", () => {
    expect(identityFromPathname("/central-bank")).toEqual({ persona: "central-bank" });
    expect(identityFromPathname("/central-bank/audit")).toEqual({ persona: "central-bank" });
    expect(identityFromPathname("/clearing-house")).toEqual({ persona: "clearing-house" });
    expect(identityFromPathname("/clearing-house/settlements/set_1")).toEqual({
      persona: "clearing-house",
    });
  });

  it("reads a bank and its pid", () => {
    expect(identityFromPathname("/bank/bank_1")).toEqual({ persona: "bank", pid: "bank_1" });
    expect(identityFromPathname("/bank/bank_1/ledger")).toEqual({
      persona: "bank",
      pid: "bank_1",
    });
  });

  it("reads a customer as a (bank, account) pair", () => {
    expect(identityFromPathname("/customer/bank_1/dep_9")).toEqual({
      persona: "customer",
      pid: "bank_1",
      did: "dep_9",
    });
    expect(identityFromPathname("/customer/bank_1/dep_9/send")).toEqual({
      persona: "customer",
      pid: "bank_1",
      did: "dep_9",
    });
  });

  // The two null cases the design names: the lobby and Learn sit outside the
  // persona system entirely.
  it("has no identity at the lobby or under Learn", () => {
    expect(identityFromPathname("/")).toBeNull();
    expect(identityFromPathname("/learn")).toBeNull();
    expect(identityFromPathname("/learn/12-sepa")).toBeNull();
    expect(identityFromPathname("/learn/mixed")).toBeNull();
  });

  // A prefix without its context addresses nothing, so it is not an identity.
  // Landing on /bank with no pid must not produce { pid: undefined }.
  it("refuses a persona prefix with its context missing", () => {
    expect(identityFromPathname("/bank")).toBeNull();
    expect(identityFromPathname("/customer/bank_1")).toBeNull();
  });

  it("ignores a trailing slash", () => {
    expect(identityFromPathname("/bank/bank_1/")).toEqual({ persona: "bank", pid: "bank_1" });
  });
});

const ALL: Identity[] = [
  { persona: "central-bank" },
  { persona: "clearing-house" },
  { persona: "bank", pid: "bank_1" },
  { persona: "customer", pid: "bank_1", did: "dep_9" },
];

describe("homeFor", () => {
  it("sends each identity to its own root", () => {
    expect(homeFor({ persona: "central-bank" })).toBe("/central-bank");
    expect(homeFor({ persona: "clearing-house" })).toBe("/clearing-house");
    expect(homeFor({ persona: "bank", pid: "bank_1" })).toBe("/bank/bank_1");
    expect(homeFor({ persona: "customer", pid: "bank_1", did: "dep_9" })).toBe(
      "/customer/bank_1/dep_9",
    );
  });

  // homeFor is what the identity picker navigates to, so it must round-trip:
  // the identity you pick is the identity the destination reads back.
  it("round-trips through identityFromPathname", () => {
    for (const it of ALL) {
      expect(identityFromPathname(homeFor(it))).toEqual(it);
    }
  });
});

describe("backendFor", () => {
  it("gives each institution its own listener", () => {
    expect(backendFor({ persona: "central-bank" })).toBe("central-bank");
    expect(backendFor({ persona: "clearing-house" })).toBe("clearing-house");
  });

  it("gives a bank its own", () => {
    expect(backendFor({ persona: "bank", pid: "bank_1" })).toBe("bank/bank_1");
  });

  // The asymmetry is the point. Three of the four personas are institutions with
  // a listener; a customer is a *view onto* a bank and talks to their bank's,
  // which is what a retail app does — it has no CSM connection and no backend of
  // its own.
  it("sends a customer to their bank's listener and not one of their own", () => {
    expect(backendFor({ persona: "customer", pid: "bank_1", did: "dep_9" })).toBe(
      backendFor({ persona: "bank", pid: "bank_1" }),
    );
    expect(backendFor({ persona: "customer", pid: "bank_1", did: "dep_9" })).not.toContain(
      "dep_9",
    );
  });
});

describe("navFor", () => {
  it("gives the central bank the settlement layer and nothing else", () => {
    expect(navFor({ persona: "central-bank" }).map((n) => n.href)).toEqual([
      "/central-bank",
      "/central-bank/audit",
    ]);
  });

  // Directory arrives in Task 10, with the screen it names.
  it("gives the clearing house the network", () => {
    expect(navFor({ persona: "clearing-house" }).map((n) => n.href)).toEqual([
      "/clearing-house",
      "/clearing-house/payments",
      "/clearing-house/mandates",
      "/clearing-house/cycles",
      "/clearing-house/settlements",
      "/clearing-house/schemes",
    ]);
  });

  // The whole point of the split: no screen showing an individual payment is on
  // the central bank's console, because a real central bank does not see one.
  it("keeps individual payments off the central bank's console", () => {
    for (const item of navFor({ persona: "central-bank" })) {
      expect(item.href).not.toContain("payments");
    }
  });

  it("scopes every bank entry to the bank's own pid", () => {
    const nav = navFor({ persona: "bank", pid: "bank_1" });
    expect(nav.length).toBeGreaterThan(0);
    for (const item of nav) {
      expect(item.href.startsWith("/bank/bank_1")).toBe(true);
    }
  });

  // Only the home entry may match exactly; every other entry is a section
  // prefix, so a detail page below it keeps its parent highlighted.
  it("marks exactly one entry per persona as an exact match", () => {
    for (const identity of ALL) {
      const nav = navFor(identity);
      if (nav.length === 0) continue;
      expect(nav.filter((n) => n.exact)).toHaveLength(1);
      expect(nav[0].exact).toBe(true);
      expect(nav[0].href).toBe(homeFor(identity));
    }
  });

  it("has no customer nav until the customer screens exist", () => {
    expect(navFor({ persona: "customer", pid: "bank_1", did: "dep_9" })).toEqual([]);
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && npx vitest run src/lib/identity.test.ts`
Expected: FAIL — `Failed to resolve import "./identity"`.

- [ ] **Step 3: Write the implementation**

Create `web/src/lib/identity.ts`:

```ts
"use client";

import { usePathname } from "next/navigation";
import {
  ArrowLeftRight,
  BookOpen,
  Building2,
  FileSignature,
  Landmark,
  LayoutDashboard,
  Network,
  RefreshCw,
  ScrollText,
  Users,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

// Who you are in the app. There is no observer who sees all of it: a back office
// sees one bank, a customer sees one account, the central bank sees reserves and
// settlement, the clearing house sees the network. The identity is derived from
// the URL and persisted nowhere — a view that is not addressable is not a view,
// so "the customer's version of this account" has to be something you can link
// to, refresh into and go back out of.
//
// A customer identity IS one deposit account: there is no party master, so
// "Alice Andersson" the identity is the pair (Aurora, that account), and a
// second account would be a second identity.
export type Identity =
  | { persona: "central-bank" }
  | { persona: "clearing-house" }
  | { persona: "bank"; pid: string }
  | { persona: "customer"; pid: string; did: string };

export type Persona = Identity["persona"];

export interface NavItem {
  href: string;
  label: string;
  icon: LucideIcon;
  // True only for a persona's home. Every other entry names a section, so a
  // detail page below it keeps its parent highlighted.
  exact?: boolean;
}

// A persona prefix without its context addresses nothing, so "/bank" with no pid
// is no identity at all rather than a bank with an undefined id.
export function identityFromPathname(pathname: string): Identity | null {
  const [prefix, pid, did] = pathname.split("/").filter(Boolean);
  if (prefix === "central-bank") return { persona: "central-bank" };
  if (prefix === "clearing-house") return { persona: "clearing-house" };
  if (prefix === "bank" && pid) return { persona: "bank", pid };
  if (prefix === "customer" && pid && did) return { persona: "customer", pid, did };
  return null;
}

// Null on `/` (always the lobby) and under `/learn/*`, which sit outside the
// persona system.
export function useIdentity(): Identity | null {
  return identityFromPathname(usePathname());
}

export function homeFor(identity: Identity): string {
  switch (identity.persona) {
    case "central-bank":
      return "/central-bank";
    case "clearing-house":
      return "/clearing-house";
    case "bank":
      return `/bank/${identity.pid}`;
    case "customer":
      return `/customer/${identity.pid}/${identity.did}`;
  }
}

// The operator key the proxy routes on — the first segment after /api. One
// function off the same switch as homeFor, because an identity that named a
// persona and a backend separately could name a pair that does not exist.
//
// Three of the four personas are institutions with a listener of their own. A
// customer is not: they reach their bank's, which is what a retail app does. A
// retail client has no clearing-house connection in the real thing either.
export function backendFor(identity: Identity): string {
  switch (identity.persona) {
    case "central-bank":
      return "central-bank";
    case "clearing-house":
      return "clearing-house";
    case "bank":
    case "customer":
      return `bank/${identity.pid}`;
  }
}

// The settlement layer, and nothing that shows an individual payment: a real
// central bank sees reserves move, not who paid whom. That subtraction is the
// point of the split rather than a gap.
const CENTRAL_BANK_NAV: NavItem[] = [
  { href: "/central-bank", label: "Reserves", icon: Landmark, exact: true },
  { href: "/central-bank/audit", label: "Audit", icon: ScrollText },
];

// The network. Clearing is the clearing house's and settlement is the central
// bank's; the CSM keeps the read side of settlements because it needs to know
// whether the cycle it closed has settled, and reading is not doing.
const CLEARING_HOUSE_NAV: NavItem[] = [
  { href: "/clearing-house", label: "Network", icon: LayoutDashboard, exact: true },
  { href: "/clearing-house/payments", label: "Payments", icon: ArrowLeftRight },
  { href: "/clearing-house/mandates", label: "Mandates", icon: FileSignature },
  { href: "/clearing-house/cycles", label: "Clearing cycles", icon: RefreshCw },
  { href: "/clearing-house/settlements", label: "Settlements", icon: Building2 },
  { href: "/clearing-house/schemes", label: "Schemes", icon: Network },
  // Directory arrives in Task 10, with the screen it names.
];

export function navFor(identity: Identity): NavItem[] {
  switch (identity.persona) {
    case "central-bank":
      return CENTRAL_BANK_NAV;
    case "clearing-house":
      return CLEARING_HOUSE_NAV;
    case "bank": {
      const base = `/bank/${identity.pid}`;
      return [
        { href: base, label: "Customers", icon: Users, exact: true },
        // Payments — this bank's own legs — arrives in Task 11.
        { href: `${base}/ledger`, label: "General ledger", icon: BookOpen },
        { href: `${base}/transactions`, label: "Transactions", icon: ArrowLeftRight },
        { href: `${base}/facilities`, label: "Facilities", icon: Landmark },
        { href: `${base}/audit`, label: "Ledger audit", icon: ScrollText },
        { href: `${base}/deposit-audit`, label: "Deposit audit", icon: ScrollText },
      ];
    }
    case "customer":
      // The customer's screens arrive with Task 12; a nav entry pointing at a
      // route with no file is what nav-integrity.test.ts exists to reject.
      return [];
  }
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd web && npx vitest run src/lib/identity.test.ts`
Expected: PASS — all cases green.

- [ ] **Step 5: Run the gate**

Run: `cd web && npm run typecheck && npm run lint && npm run test`
Expected: all clean. (`npm run build` is not needed yet — no route or component
references this module.)

- [ ] **Step 6: Commit**

```bash
git add web/src/lib/identity.ts web/src/lib/identity.test.ts
git commit -m "feat(web): derive who you are, and whose listener you talk to, from the URL"
```

---

## Task 3: Which listeners are actually there

The proxy already knows where every operator is bound; nothing else can ask it.
This extracts that knowledge into a testable module and puts one endpoint in
front of it, so the lobby and the picker can badge a bank *awaiting
provisioning* rather than firing four requests that will 502 forever.

**Files:**
- Create: `web/src/lib/api/backend-url.ts`, `web/src/lib/api/backend-url.test.ts`
- Create: `web/src/app/api/operators/route.ts`
- Modify: `web/src/app/api/[...path]/route.ts` (use the extracted module)
- Modify: `web/src/lib/api/endpoints.ts`, `query-keys.ts`, `hooks.ts`, `errors.ts`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `interface BackendConfig { host: string; basePort: number; overrides: Record<string, string> }`
  - `function backendConfig(env: Record<string, string | undefined>): BackendConfig`
  - `function institutionUrl(key: "central-bank" | "clearing-house", cfg: BackendConfig): string`
  - `function bankUrl(pid: string, roster: string[], cfg: BackendConfig): string` — throws when the pid is not in the roster
  - `interface OperatorStatus { operator: string; live: boolean }` — in
    `backend-url.ts`, so the route handler that produces it and the client
    function that consumes it share one definition
  - `function listOperators(): Promise<OperatorStatus[]>` (endpoints)
  - `function useOperators()` and `function useIsProvisioned(): (operatorKey: string) => boolean` (hooks)

A static `app/api/operators/route.ts` wins over the `app/api/[...path]` catch-all
in the App Router, and `operators` is not a legal operator key (`central-bank`,
`clearing-house`, `bank` are), so nothing collides.

- [ ] **Step 1: Write the failing test**

Create `web/src/lib/api/backend-url.test.ts`. Note this is a `.ts` module with no
`next/*` import, which is why it is testable at all — the route handler that uses
it is not.

```ts
import { describe, expect, it } from "vitest";

import { backendConfig, bankUrl, institutionUrl } from "./backend-url";

const DEFAULTS = backendConfig({});

describe("institutionUrl", () => {
  // Mirrors cmd/server's plan(): the central bank takes the base port and the
  // clearing house the next, so `make dev` needs no configuration at all.
  it("puts the two institutions at the base port and the next", () => {
    expect(institutionUrl("central-bank", DEFAULTS)).toBe("http://localhost:8081");
    expect(institutionUrl("clearing-house", DEFAULTS)).toBe("http://localhost:8082");
  });

  it("honours an override", () => {
    const cfg = backendConfig({ BACKENDS: '{"central-bank":"http://cb.internal:9000"}' });
    expect(institutionUrl("central-bank", cfg)).toBe("http://cb.internal:9000");
    expect(institutionUrl("clearing-house", cfg)).toBe("http://localhost:8082");
  });

  it("honours a moved base port and host", () => {
    const cfg = backendConfig({ BASE_PORT: "9000", BACKEND_HOST: "http://api" });
    expect(institutionUrl("central-bank", cfg)).toBe("http://api:9000");
  });
});

describe("bankUrl", () => {
  // The seed's ids are bank_1, bank_3, bank_5, bank_7 — deliberately not
  // contiguous. Nothing may infer a port from an id, only from roster position.
  const roster = ["bank_1", "bank_3", "bank_5", "bank_7"];

  it("derives a bank's port from its roster position, never from its id", () => {
    expect(bankUrl("bank_1", roster, DEFAULTS)).toBe("http://localhost:8083");
    expect(bankUrl("bank_3", roster, DEFAULTS)).toBe("http://localhost:8084");
    expect(bankUrl("bank_5", roster, DEFAULTS)).toBe("http://localhost:8085");
    expect(bankUrl("bank_7", roster, DEFAULTS)).toBe("http://localhost:8086");
  });

  it("honours an override keyed by participant id", () => {
    const cfg = backendConfig({ BACKENDS: '{"bank_5":"http://verde:7000"}' });
    expect(bankUrl("bank_5", roster, cfg)).toBe("http://verde:7000");
  });

  // Admission is not provisioning: a bank created at runtime is in the roster
  // and has no listener until the server restarts. Saying so is the whole point
  // — the alternative is a console whose every request 502s.
  it("refuses a bank that is not in the roster, and says why", () => {
    expect(() => bankUrl("bank_99", roster, DEFAULTS)).toThrow(/admission is not provisioning/i);
  });
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd web && npx vitest run src/lib/api/backend-url.test.ts`
Expected: FAIL — `Failed to resolve import "./backend-url"`.

- [ ] **Step 3: Write the module**

Create `web/src/lib/api/backend-url.ts`:

```ts
// Where each operator's listener is bound. Server-side only: this is deployment
// topology, which is not domain data and appears in no DTO — a GET /members that
// returned base URLs would make the member roster a deployment manifest.
//
// It is a module rather than inline in the proxy because two things need it now:
// the proxy, which forwards to one of these, and /api/operators, which probes all
// of them so the lobby can tell an un-provisioned bank from a running one.

export interface BackendConfig {
  host: string;
  basePort: number;
  overrides: Record<string, string>;
}

export type Institution = "central-bank" | "clearing-house";

// The answer /api/operators gives. It lives here rather than in endpoints.ts so
// the route handler that produces it and the client function that consumes it
// share one definition instead of agreeing by hand.
export interface OperatorStatus {
  operator: string;
  live: boolean;
}

// Reads the environment into a value, so the derivation below is a pure function
// a test can hold. BACKENDS is a JSON object of operator key to base URL; a
// bank's key is its participant id.
export function backendConfig(env: Record<string, string | undefined>): BackendConfig {
  return {
    host: env.BACKEND_HOST ?? "http://localhost",
    basePort: Number(env.BASE_PORT ?? 8081),
    overrides: env.BACKENDS ? (JSON.parse(env.BACKENDS) as Record<string, string>) : {},
  };
}

// The two institutions sit at the base port and the next one, mirroring
// cmd/server's plan(). With no configuration at all, `make dev` works and the
// ports are predictable.
export function institutionUrl(key: Institution, cfg: BackendConfig): string {
  if (cfg.overrides[key]) return cfg.overrides[key];
  return `${cfg.host}:${cfg.basePort + (key === "central-bank" ? 0 : 1)}`;
}

// A bank's port depends on where it sits in the roster, which only the clearing
// house can answer — and on nothing about its id. The seed's ids are bank_1,
// bank_3, bank_5, bank_7: deliberately not contiguous, so an id-derived port
// would be wrong on the very first dataset.
export function bankUrl(pid: string, roster: string[], cfg: BackendConfig): string {
  if (cfg.overrides[pid]) return cfg.overrides[pid];
  const index = roster.indexOf(pid);
  if (index < 0) {
    throw new Error(
      `no listener for bank ${pid}. A participant admitted at runtime has no ` +
        `listener until the server restarts — admission is not provisioning.`,
    );
  }
  return `${cfg.host}:${cfg.basePort + 2 + index}`;
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `cd web && npx vitest run src/lib/api/backend-url.test.ts`
Expected: PASS.

- [ ] **Step 5: Point the proxy at the module**

Edit `web/src/app/api/[...path]/route.ts`. Delete the `BASE_PORT`, `HOST`,
`OVERRIDES`, `CENTRAL_BANK`, `CLEARING_HOUSE`, `institutionUrl` and `bankUrl`
declarations (everything from `const BASE_PORT =` down to the end of
`bankUrl`), keep `rosterCache`/`roster()` and `resolve()`/`handle()`, and add at
the top:

```ts
import {
  backendConfig,
  bankUrl,
  institutionUrl,
  type Institution,
} from "@/lib/api/backend-url";

// Read once per process: the environment does not change under a running server,
// and JSON.parse of BACKENDS on every request would be waste.
const CFG = backendConfig(process.env);
const CENTRAL_BANK = "central-bank";
const CLEARING_HOUSE = "clearing-house";
```

Then rewrite the three call sites:

- `roster()`'s fetch target becomes `` `${institutionUrl(CLEARING_HOUSE, CFG)}/members` ``.
- `resolve()`'s institution branch becomes
  `return { base: institutionUrl(head as Institution, CFG), rest: tail, key: head };`
- `resolve()`'s bank branch becomes
  `return { base: bankUrl(pid, await roster(), CFG), rest, key: \`bank/${pid}\` };`

Nothing else in the file changes: the 400 for an unknown operator, the 502 naming
the operator, the header handling and the verbatim status/body passthrough all
stay exactly as they are.

- [ ] **Step 6: Write the probe endpoint**

Create `web/src/app/api/operators/route.ts`:

```ts
import { NextResponse } from "next/server";

import {
  backendConfig,
  bankUrl,
  institutionUrl,
  type OperatorStatus,
} from "@/lib/api/backend-url";

// Which operators actually have a listener behind them.
//
// Ports are static by design: a bank admitted at runtime through POST /members
// gets reserve accounts and no listener until the server restarts, because
// admitting a member to a network is an operational act and modelling it as an
// API call that instantly yields a running bank teaches the wrong thing. The
// lobby and the identity picker need to tell those two states apart *before*
// offering a console, so this answers once for every bank rather than letting
// each one discover it through a 502.
//
// This is Next's own knowledge and is served by no backend: deployment topology
// is not domain data. A member of the roster with no listener is still a member.
export const dynamic = "force-dynamic";

const CFG = backendConfig(process.env);
const PROBE_TIMEOUT_MS = 1_500;

// A listener is live if it answers at all. GET /assets is the probe because
// every operator serves it — it is a compiled-in constant, which is exactly why
// it is on all three surfaces — so one request shape works for every key.
async function probe(base: string): Promise<boolean> {
  try {
    const res = await fetch(`${base}/assets`, {
      cache: "no-store",
      signal: AbortSignal.timeout(PROBE_TIMEOUT_MS),
    });
    return res.ok;
  } catch {
    return false;
  }
}

export async function GET() {
  const csm = institutionUrl("clearing-house", CFG);

  let roster: string[] = [];
  let csmLive = false;
  try {
    const res = await fetch(`${csm}/members`, {
      cache: "no-store",
      signal: AbortSignal.timeout(PROBE_TIMEOUT_MS),
    });
    csmLive = res.ok;
    if (res.ok) roster = ((await res.json()) as { id: string }[]).map((m) => m.id);
  } catch {
    // Leave the roster empty. With the clearing house down there is no roster to
    // read, and reporting every bank as dead would be a guess rather than an
    // answer — the caller sees clearing-house:false and knows why the list is
    // short.
  }

  const banks = await Promise.all(
    roster.map(async (pid): Promise<OperatorStatus> => {
      let base: string;
      try {
        base = bankUrl(pid, roster, CFG);
      } catch {
        // In the roster and outside the port plan: admitted, not provisioned.
        return { operator: `bank/${pid}`, live: false };
      }
      return { operator: `bank/${pid}`, live: await probe(base) };
    }),
  );

  const out: OperatorStatus[] = [
    { operator: "central-bank", live: await probe(institutionUrl("central-bank", CFG)) },
    { operator: "clearing-house", live: csmLive },
    ...banks,
  ];
  return NextResponse.json(out);
}
```

- [ ] **Step 7: Add it to the data layer**

`web/src/lib/api/endpoints.ts` — add a new section at the end, above `--- Admin ---`:

```ts
// --- Operators (Next-side, not a backend route) ----------------------------

// Which operators have a listener behind them. Served by the Next app itself:
// deployment topology is not domain data, and no backend knows where its
// siblings are bound. See app/api/operators/route.ts, which shares the type.
//
// It is the one path here that is not built with cb()/csm()/bank(), because it
// names no operator — it is the question "which of them are there?".
export function listOperators(): Promise<OperatorStatus[]> {
  return request("GET", "/operators");
}
```

Add `import type { OperatorStatus } from "./backend-url";` beside the existing
`./operator` import, and re-export it so callers have one place to look:
`export type { OperatorStatus } from "./backend-url";`

`web/src/lib/api/query-keys.ts` — add above `// Ledger layer`:

```ts
  // Next-side, not a backend area: which listeners are actually there.
  operators: () => ["operators"] as const,
```

`web/src/lib/api/hooks.ts` — add after the participant hooks:

```ts
// Which operators have a listener behind them, and a predicate over the answer.
//
// staleTime is Infinity because ports are static by design: a bank admitted at
// runtime gets no listener until the server restarts, so the answer cannot change
// under a running page. Re-probing six listeners on every mount would be waste.
export function useOperators() {
  return useQuery({
    queryKey: qk.operators(),
    queryFn: api.listOperators,
    staleTime: Infinity,
  });
}

// Answers `backendFor(identity)`. Optimistic while the probe is in flight and
// when it failed: an unknown answer must not make a working console look dead.
export function useIsProvisioned(): (operatorKey: string) => boolean {
  const { data } = useOperators();
  return (operatorKey: string) => {
    const row = data?.find((o) => o.operator === operatorKey);
    return row ? row.live : true;
  };
}
```

- [ ] **Step 8: Correct the stale port in the error copy**

`web/src/lib/api/errors.ts:26` still says the backend runs on `:8080`. There has
been no `:8080` since the operator split, and the proxy's own 502 already names
the operator it could not reach. Replace the 502 case with:

```ts
    case 502:
      return "That operator's backend is unreachable. Is `go run ./cmd/server` running?";
```

- [ ] **Step 9: Run the gate**

Run: `cd web && npm run typecheck && npm run lint && npm run test && npm run build`
Expected: all clean.

- [ ] **Step 10: Prove the probe against a running backend**

This is the step that catches an extracted-module refactor that compiles and does
nothing. From the repo root with `make dev` running (or `go run ./cmd/server` plus
`npm run dev` in `web/`):

```bash
curl -s localhost:3000/api/operators | python3 -m json.tool
curl -s localhost:3000/api/clearing-house/members | head -c 200
curl -s localhost:3000/api/bank/bank_1/me
```

Expected: six rows, all `"live": true`, with bank keys `bank/bank_1`,
`bank/bank_3`, `bank/bank_5`, `bank/bank_7` — and the two proxied calls still
answering, which is what proves the extraction did not change the proxy's
behaviour.

Then prove the un-provisioned case, which is the whole reason this task exists:

```bash
curl -s -X POST localhost:3000/api/central-bank/members \
  -H 'content-type: application/json' -d '{"name":"Late Bank"}'
curl -s localhost:3000/api/operators | python3 -m json.tool | tail -8
```

Expected: a seventh row for the new bank with `"live": false`. Reset afterwards
(`curl -s -X POST localhost:3000/api/central-bank/admin/reset`).

- [ ] **Step 11: Commit**

```bash
git add -A web/src
git commit -m "feat(web): ask which listeners are actually there"
```

---

## Task 4: Move the routes under their personas

The riskiest mechanical part of the work, and the one 6a's first lesson is aimed
at. Everything moves in one commit, because a half-moved tree is a broken app.

`/` is left as a one-line redirect to `/clearing-house` at the end of this task
and becomes the real lobby in Task 5. That is working software rather than a
placeholder: the design's "`/` never redirects" is a property of the finished
lobby, and shipping a 404 at the root between two tasks would be worse.

Two safety nets go in **before** the move, and between them they turn 33 silent
string replacements into two things a machine checks:

- `explore.href` is typed `string` today (`quiz/types.ts:17`), so `tsc` catches
  nothing. Narrowing it to `(typeof EXPLORE_ROUTES)[number]` makes the compiler
  hold every chapter to the allowlist.
- Nothing checks that an allowlisted route names a real page.
  `nav-integrity.test.ts` makes it, for nav entries *and* for the quiz's
  deep-links, which are the larger surface.

**Files:**
- Move: `web/src/app/page.tsx` → `web/src/app/clearing-house/page.tsx`
- Move: `web/src/app/{payments,mandates,cycles,settlements,schemes}` → `web/src/app/clearing-house/…`
- Move: `web/src/app/participants/[pid]` → `web/src/app/bank/[pid]`
- Create: `web/src/app/participants/[...rest]/page.tsx`, `web/src/app/page.tsx`, `web/scripts/repoint-routes.mjs` (deleted in the same commit)
- Modify: `web/src/lib/quiz/types.ts`, `web/src/lib/quiz/index.ts`, eight chapter files, `web/src/components/app-shell.tsx`, and the 27 route literals the script rewrites
- Test: `web/src/lib/nav-integrity.test.ts`

**Interfaces:**
- Consumes: `navFor`, `homeFor`, `type Identity`, `type NavItem` (Task 2).
- Produces: the route tree every later task builds on, and an `EXPLORE_ROUTES`
  the compiler enforces.

- [ ] **Step 1: Write the failing test**

Create `web/src/lib/nav-integrity.test.ts`:

```ts
import { existsSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import { homeFor, navFor, type Identity } from "./identity";
import { EXPLORE_ROUTES } from "./quiz";

// vitest's cwd is web/, so this is web/src/app.
const APP_DIR = join(process.cwd(), "src", "app");

// Sentinels navFor interpolates, mapped back to the dynamic segment they stand
// for. Real ids would name no file.
const PID = "__PID__";
const DID = "__DID__";

const IDENTITIES: Identity[] = [
  { persona: "central-bank" },
  { persona: "clearing-house" },
  { persona: "bank", pid: PID },
  { persona: "customer", pid: PID, did: DID },
];

// An App Router route renders src/app/<segments>/page.tsx. A dynamic segment is
// a directory literally named [pid] / [did].
function routeFileFor(href: string): string {
  const segments = href
    .split("/")
    .filter(Boolean)
    .map((s) => (s === PID ? "[pid]" : s === DID ? "[did]" : s));
  return join(APP_DIR, ...segments, "page.tsx");
}

describe("nav integrity", () => {
  // The route move is the riskiest mechanical part of the persona work, and a
  // dead nav link is invisible to typecheck, lint and `next build`.
  it("every nav href resolves to a real page", () => {
    for (const identity of IDENTITIES) {
      for (const item of navFor(identity)) {
        expect(
          existsSync(routeFileFor(item.href)),
          `${identity.persona} nav "${item.label}" → ${item.href}`,
        ).toBe(true);
      }
    }
  });

  it("every persona's home resolves to a real page", () => {
    for (const identity of IDENTITIES) {
      const href = homeFor(identity);
      expect(existsSync(routeFileFor(href)), `home for ${identity.persona} → ${href}`).toBe(
        true,
      );
    }
  });

  // The quiz deep-links into the explorer from 40 questions across eight
  // chapters, 33 of which move here. index.test.ts already pins that every
  // question's explore.href is in this allowlist, and narrowing the type makes
  // tsc do the same; nothing pinned that an allowlisted route *exists*.
  it("every allowlisted quiz explore route resolves to a real page", () => {
    for (const href of EXPLORE_ROUTES) {
      const file = href === "/" ? join(APP_DIR, "page.tsx") : routeFileFor(href);
      expect(existsSync(file), `EXPLORE_ROUTES entry ${href}`).toBe(true);
    }
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && npx vitest run src/lib/nav-integrity.test.ts`
Expected: FAIL — the clearing house's nav points at `/clearing-house/payments`
etc., none of which exist, and the bank's at `/bank/[pid]/…`.

- [ ] **Step 3: Move the tree**

Run from the repo root, in this order — `git mv` so history follows each file:

```bash
cd /Users/raphaelgruber/Git/cbs-account-addressing/web/src/app

# Today's dashboard counts payments, cycles and settlements. Those are the
# CSM's, not the central bank's, so the network overview becomes the clearing
# house's home. /central-bank stays where it is: the central bank's home is the
# reserves table it already has, and Task 9 lifts the audit half out of it.
mkdir -p clearing-house
git mv page.tsx clearing-house/page.tsx

# The network screens are the clearing house's section now — moved exactly as
# they are. This is a re-home, not work.
git mv payments clearing-house/payments
git mv mandates clearing-house/mandates
git mv cycles clearing-house/cycles
git mv settlements clearing-house/settlements
git mv schemes clearing-house/schemes

# The back office. `participants` keeps existing, holding only the redirect.
mkdir -p bank
git mv participants/'[pid]' bank/'[pid]'
mkdir -p participants/'[...rest]'
```

Verify the tree:

```bash
cd /Users/raphaelgruber/Git/cbs-account-addressing/web && find src/app -name page.tsx | sort
```

Expected: pages under `central-bank/`, `clearing-house/`, `bank/[pid]/` and
`learn/`, nothing left under `participants/[pid]`, and no `src/app/page.tsx`
(Step 6 puts one back).

- [ ] **Step 4: Write the asserting rewrite script**

Every rule asserts its match count. This is 6a's first lesson: the `-entity`
narrowing was written into `main`, compiled, and did nothing, because a Python
`.replace()` matched zero times after a variable had been renamed — and only a
live probe caught it. A rule that matches nothing here is a bug in the rule, not
a no-op.

Create `web/scripts/repoint-routes.mjs`:

```js
// One-shot route rewrite for the persona split. Deliberately NOT idempotent: a
// second run finds every rule matching zero times and throws, which is the
// assertion doing its job rather than a defect.
//
// Each rule states the count it expects. A mismatch fails loudly and names the
// rule, because a scripted edit that matches nothing must fail, not pass.

import { readFileSync, writeFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

const ROOT = new URL("../src", import.meta.url).pathname;

function walk(dir) {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name);
    return statSync(p).isDirectory() ? walk(p) : [p];
  });
}

const ALL = walk(ROOT).filter((f) => f.endsWith(".ts") || f.endsWith(".tsx"));
const under = (prefix) => ALL.filter((f) => f.startsWith(join(ROOT, prefix)));

// A file's contents are read once, rewritten by every rule that names it, and
// written once — so two rules touching one file cannot lose each other's edit.
const buffers = new Map(ALL.map((f) => [f, readFileSync(f, "utf8")]));

const rules = [
  // The back office moved out from under /participants. Every one of these is a
  // link in the app; the prose comments naming backend routes are untouched,
  // because they are matched on the template-literal opener `${.
  { files: ALL, from: "`/participants/${", to: "`/bank/${", expect: 14 },

  // The network screens are the clearing house's. Scoped to its own subtree so
  // a stray "/payments" elsewhere cannot be caught by accident.
  { files: under("app/clearing-house"), from: "`/cycles/${", to: "`/clearing-house/cycles/${", expect: 3 },
  { files: under("app/clearing-house"), from: "`/settlements/${", to: "`/clearing-house/settlements/${", expect: 3 },
  { files: under("app/clearing-house"), from: "`/payments/${", to: "`/clearing-house/payments/${", expect: 2 },
  { files: under("app/clearing-house"), from: '"/cycles"', to: '"/clearing-house/cycles"', expect: 1 },
  { files: under("app/clearing-house"), from: '"/settlements"', to: '"/clearing-house/settlements"', expect: 1 },
  // "/payments/audit" is not matched by "/payments" — the closing quote differs.
  { files: under("app/clearing-house"), from: '"/payments/audit"', to: '"/clearing-house/payments/audit"', expect: 1 },
  { files: under("app/clearing-house"), from: '"/payments"', to: '"/clearing-house/payments"', expect: 2 },

  // The quiz's deep-links: 33 of its 40 explore.href values move. `/` stays (it
  // is the lobby, and "See the ledger" still lands somewhere real) and
  // `/central-bank` stays (the central bank's home IS the reserves table those
  // questions mean).
  { files: under("lib/quiz/chapters"), from: 'href: "/payments"', to: 'href: "/clearing-house/payments"', expect: 7 },
  { files: under("lib/quiz/chapters"), from: 'href: "/mandates"', to: 'href: "/clearing-house/mandates"', expect: 3 },
  { files: under("lib/quiz/chapters"), from: 'href: "/cycles"', to: 'href: "/clearing-house/cycles"', expect: 10 },
  { files: under("lib/quiz/chapters"), from: 'href: "/settlements"', to: 'href: "/clearing-house/settlements"', expect: 5 },
  { files: under("lib/quiz/chapters"), from: 'href: "/schemes"', to: 'href: "/clearing-house/schemes"', expect: 8 },
];

let failed = false;
for (const rule of rules) {
  let hits = 0;
  for (const file of rule.files) {
    const before = buffers.get(file);
    const parts = before.split(rule.from);
    if (parts.length === 1) continue;
    hits += parts.length - 1;
    buffers.set(file, parts.join(rule.to));
  }
  const ok = hits === rule.expect;
  if (!ok) failed = true;
  console.log(`${ok ? "ok  " : "FAIL"}  ${hits}/${rule.expect}  ${rule.from} → ${rule.to}`);
}

if (failed) {
  console.error(
    "\nA rule did not match what it expected. Nothing was written. Fix the rule " +
      "or the count — a scripted edit that matches nothing must fail, not pass.",
  );
  process.exit(1);
}

for (const [file, text] of buffers) writeFileSync(file, text);
console.log(`\nrewrote ${rules.length} rules across ${ALL.length} files`);
```

- [ ] **Step 5: Run it**

Run: `cd web && node scripts/repoint-routes.mjs`

Expected: fourteen `ok` lines with counts `14/14`, `3/3`, `3/3`, `2/2`, `1/1`,
`1/1`, `1/1`, `2/2`, `7/7`, `3/3`, `10/10`, `5/5`, `8/8`, and a final
`rewrote 13 rules across N files`.

**If any line says FAIL, stop and read it — nothing was written.** A count that
came out low means a literal moved or was reformatted since this plan was
written; find it with the grep in Step 9 and correct the rule and the expected
count together. Do not lower an expectation to make the script pass.

- [ ] **Step 6: Narrow the explore type and re-point the allowlist**

`EXPLORE_ROUTES` moves into `quiz/types.ts` so `explore.href` can be typed
against it — `index.ts` imports the chapters, which import `types.ts`, so the
allowlist living in `index.ts` would be a cycle.

Edit `web/src/lib/quiz/types.ts`, adding above `BaseQuestion`:

```ts
/**
 * Explorer routes a question may deep-link to.
 *
 * It lives here rather than in index.ts so `explore.href` can be typed against
 * it: index.ts imports the chapters and the chapters import this file, so the
 * other direction would be a cycle. Typing it is what makes the compiler hold
 * every chapter to the allowlist — before this it was `string`, and only a
 * runtime test caught a stale one.
 */
export const EXPLORE_ROUTES = [
  // The lobby. "See the ledger" still lands somewhere real: the cast, and a
  // seat to pick.
  "/",
  // The central bank's home is the reserves table, which is what the questions
  // naming it mean — so this one did not move.
  "/central-bank",
  "/clearing-house",
  "/clearing-house/payments",
  "/clearing-house/mandates",
  "/clearing-house/cycles",
  "/clearing-house/settlements",
  "/clearing-house/schemes",
] as const;

export type ExploreRoute = (typeof EXPLORE_ROUTES)[number];
```

and change `BaseQuestion`'s `explore` (line 17):

```ts
  /** Optional deep-link to a relevant explorer page (operator-level routes only). */
  explore?: { href: ExploreRoute; label: string };
```

Edit `web/src/lib/quiz/index.ts`: delete the `EXPLORE_ROUTES` block (`:22-31`) and
re-export it so every existing importer keeps working:

```ts
export { EXPLORE_ROUTES, type ExploreRoute } from "./types";
```

Then confirm the compiler now carries the load:

```bash
cd /Users/raphaelgruber/Git/cbs-account-addressing/web && npm run typecheck
```

Expected: clean. **A `tsc` error here naming a chapter file is the good
outcome** — it means a `href` the script missed, and the error names the file and
the string. Fix the href, do not widen the type.

- [ ] **Step 7: Add the redirect and an interim root**

Create `web/src/app/participants/[...rest]/page.tsx` — a server component, so
existing links and bookmarks resolve before anything renders:

```tsx
import { redirect } from "next/navigation";

// The back office used to live at /participants/[pid]/…; it is /bank/[pid]/…
// now. Existing links and bookmarks land here and are forwarded rather than
// 404ing. A catch-all needs at least one segment, which is exactly right:
// /participants alone was never a page.
export default async function ParticipantsRedirect({
  params,
}: {
  params: Promise<{ rest: string[] }>;
}) {
  const { rest } = await params;
  redirect(`/bank/${rest.join("/")}`);
}
```

Create `web/src/app/page.tsx`:

```tsx
import { redirect } from "next/navigation";

// Interim root. The lobby lands here in the next task; until then the root
// forwards to the network overview rather than 404ing.
export default function RootPage() {
  redirect("/clearing-house");
}
```

- [ ] **Step 8: Point the existing nav at `navFor`**

`app-shell.tsx` still holds a hardcoded `NETWORK_NAV` pointing at the old routes.
It is deleted wholesale in Task 6, but leaving it broken now would mean a commit
with an unusable app. Edit `web/src/components/app-shell.tsx`:

- Delete the `NavItem` interface (`:42-46`) and the `NETWORK_NAV` array
  (`:48-59`), and the icon imports that go with them — `ArrowLeftRight`,
  `Building2`, `FileSignature`, `LayoutDashboard`, `Network`, `RefreshCw`,
  `Landmark` — keeping `GraduationCap`, `Menu`, `BookOpen`, `PanelRightOpen`,
  `PanelLeftOpen`, `PanelLeftClose`. Drop the now-unused `LucideIcon` type
  import.
- Add `import { navFor, useIdentity, type NavItem } from "@/lib/identity";`
- In `NavLinks`, replace the `NETWORK_NAV.map(…)` source and the active test:

```tsx
  const pathname = usePathname();
  const identity = useIdentity();
  // Learn sits outside the persona system, so it is appended rather than being
  // any persona's. Task 6 gives it a shell of its own.
  const items: NavItem[] = [
    ...(identity ? navFor(identity) : []),
    { href: "/learn", label: "Learn", icon: GraduationCap },
  ];
```

with the map over `items` and

```tsx
        const active = item.exact ? pathname === item.href : pathname.startsWith(item.href);
```

- [ ] **Step 9: Prove nothing was missed**

```bash
cd /Users/raphaelgruber/Git/cbs-account-addressing/web
grep -rn '"/participants\|`/participants/\|"/payments\|`/payments/\|"/cycles\|`/cycles/\|"/settlements\|`/settlements/\|"/schemes\|"/mandates' src --include="*.tsx" --include="*.ts" | grep -v 'src/lib/api/'
```

Expected: **no output.**

Prose comments naming the backend routes (`GET /participants/{pid}/…`) are not
matched by these patterns and are left alone, as are `src/lib/api/*` where the
paths are backend paths rather than app routes.
`src/components/concept-links.test.ts:22` asserts `conceptUrlTransform` passes
`/participants/p_1` through untouched — it is testing the URL sanitizer, not a
link in the app. Leave it.

- [ ] **Step 10: Delete the script and run the tests**

```bash
cd /Users/raphaelgruber/Git/cbs-account-addressing/web && rm scripts/repoint-routes.mjs && npm run test
```

The script goes because it is a one-shot migration and is not re-runnable by
design; it lives in this plan, which is where a reader would look for it.

Expected: PASS — `nav-integrity.test.ts` green (every nav href, every persona
home and every `EXPLORE_ROUTES` entry resolves to a file), `quiz/index.test.ts`
green, `concept-links.test.ts` and `quiz/diversity.test.ts` unchanged and green.

- [ ] **Step 11: Run the full gate and click through the app**

Run: `cd web && npm run typecheck && npm run lint && npm run build`
Expected: all clean.

Then, with `make dev` running from the repo root, load and click:

- `/` → forwards to `/clearing-house`
- a member bank card → `/bank/<pid>`, its tab strip, a deposit account, its
  statement, a GL account from the statement's Contra column
- `/clearing-house/payments` → a payment → its cycle → its settlement → back
- `/clearing-house/schemes`, `/clearing-house/mandates`
- `/central-bank` (unchanged), `/learn` and one chapter with an explore link on it
- `/participants/<pid>/ledger` → forwards to `/bank/<pid>/ledger`

In-page links are the residual risk the integrity test cannot see, and this is
what catches them.

- [ ] **Step 12: Commit**

```bash
git add -A web/src
git commit -m "refactor(web): give every screen a persona to belong to"
```

---

## Task 5: The lobby

`/` never redirects. A first-time visitor is shown the cast — the two
institutions, the member banks with their reserves, and the customers grouped by
bank with their account status badged, plus an entry to Learn — and picks one.

Remembering the last identity would save a repeat visitor a click and cost the
newcomer the one screen that makes the app's structure obvious. For a teaching
system that is the wrong trade, so nothing is persisted.

**Files:**
- Modify: `web/src/app/page.tsx` (replace the interim redirect)
- Modify: `web/src/lib/api/hooks.ts` (append `useIdentityDirectory`)

**Interfaces:**
- Consumes: `homeFor`, `backendFor`, `type Identity` (Task 2); `useOperators`,
  `useIsProvisioned` (Task 3); `useParticipants`, `useReserves`,
  `qk.depositAccounts`, `api.listDepositAccounts`.
- Produces:
  - `interface BankEntry { participant: Participant; accounts: DepositAccount[]; provisioned: boolean }`
  - `function useIdentityDirectory(): { banks: BankEntry[]; isLoading: boolean; error: unknown }`
    — the picker (Task 7) and the lobby share this rather than each fetching.

- [ ] **Step 1: Add the shared directory hook**

Add `useQueries` to the existing `@tanstack/react-query` import at the top of
`web/src/lib/api/hooks.ts`, then append after the deposit-account hooks:

```ts
// --- Identities -----------------------------------------------------------

// Every identity in the system, in one place: each member bank with its customer
// accounts. The lobby and the identity picker both need exactly this, so they
// share one set of queries rather than each fetching its own.
//
// The honest cost of the split is visible here. The roster comes from the
// clearing house's GET /members, and each bank's accounts come from that bank's
// own listener — so drawing one list touches five backends. That is why this is
// a shared hook rather than an optimisation.
//
// A bank admitted at runtime has a store row and no listener until the server
// restarts. Its query is not fired at all: it would 502 forever, and four
// failing requests plus a dead console is a worse answer than one row saying so.
export interface BankEntry {
  participant: Participant;
  accounts: DepositAccount[];
  provisioned: boolean;
}

export function useIdentityDirectory(): {
  banks: BankEntry[];
  isLoading: boolean;
  error: unknown;
} {
  const participants = useParticipants();
  const operators = useOperators();
  const isProvisioned = useIsProvisioned();
  const list = participants.data ?? [];

  const results = useQueries({
    queries: list.map((p) => ({
      queryKey: qk.depositAccounts(p.id),
      queryFn: () => api.listDepositAccounts(p.id),
      enabled: isProvisioned(`bank/${p.id}`),
    })),
  });

  const banks = list.map((participant, i) => ({
    participant,
    accounts: results[i]?.data ?? [],
    provisioned: isProvisioned(`bank/${participant.id}`),
  }));

  return {
    banks,
    // The probe is part of the load: until it answers, "provisioned" is a guess
    // and firing the per-bank queries on a guess is what this exists to avoid.
    isLoading:
      participants.isLoading || operators.isLoading || results.some((r) => r.isLoading),
    error: participants.error ?? results.find((r) => r.error)?.error ?? null,
  };
}
```

Add `DepositAccount` and `Participant` to the `@/lib/types` type import at the
top of the file if they are not already there (it currently imports `Asset` and
`AuditQuery`).

- [ ] **Step 2: Write the lobby**

Replace `web/src/app/page.tsx` entirely:

```tsx
"use client";

import Link from "next/link";
import { Building2, GraduationCap, Landmark, Network } from "lucide-react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Money } from "@/components/money";
import { EnumBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { Skeleton } from "@/components/ui/skeleton";
import { useAssetLookup, useIdentityDirectory, useReserves } from "@/lib/api/hooks";
import { homeFor } from "@/lib/identity";
import type { DepositAccount, Participant, Reserve } from "@/lib/types";

// The lobby. `/` never redirects: a first-time visitor is shown the cast and
// picks one. Remembering the last identity would save a repeat visitor a click
// and cost the newcomer the one screen that makes the app's structure obvious,
// which for a teaching system is the wrong trade — so nothing is persisted.
export default function Lobby() {
  const { banks, isLoading, error } = useIdentityDirectory();
  const { data: reserves } = useReserves();

  if (error) return <ErrorState error={error} />;

  return (
    <div className="mx-auto max-w-4xl space-y-8 py-4">
      <div className="space-y-2">
        <h1 className="text-3xl font-semibold tracking-tight">Who are you today?</h1>
        <p className="max-w-prose text-sm text-muted-foreground">
          There is no observer who sees all of this. A back office sees one
          bank&apos;s customers, a customer sees one account, the clearing house
          sees payments and the central bank sees reserves. Each of them talks to
          a different listener. Pick a seat.
        </p>
      </div>

      <section className="space-y-3">
        <h2 className="text-sm font-medium text-muted-foreground">Institutions</h2>
        <div className="grid gap-4 sm:grid-cols-2">
          <InstitutionCard
            href={homeFor({ persona: "central-bank" })}
            icon={<Landmark className="size-4" />}
            title="Central bank"
            hint="central-bank-reserves"
            body="Reserves, and settling a closed cycle by moving them. It never sees an individual payment."
          />
          <InstitutionCard
            href={homeFor({ persona: "clearing-house" })}
            icon={<Network className="size-4" />}
            title="Clearing house"
            hint="clearing-vs-settlement"
            body="Every payment in the network, the clearing cycles, the schemes, the mandates and the directory."
          />
        </div>
      </section>

      <section className="space-y-3">
        <h2 className="text-sm font-medium text-muted-foreground">Member banks</h2>
        {isLoading && banks.length === 0 ? (
          <div className="grid gap-4 sm:grid-cols-2">
            <Skeleton className="h-24" />
            <Skeleton className="h-24" />
          </div>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2">
            {banks.map(({ participant, provisioned }) => (
              <BankCard
                key={participant.id}
                participant={participant}
                provisioned={provisioned}
                reserves={(reserves ?? []).filter((r) => r.participant === participant.id)}
              />
            ))}
          </div>
        )}
      </section>

      <section className="space-y-3">
        <h2 className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground">
          Customers
          <Hint id="account-addressing" />
        </h2>
        <p className="text-sm text-muted-foreground">
          A customer identity is one deposit account — there is no party master
          here, so a second account would be a second identity.
        </p>
        {banks
          .filter((b) => b.provisioned)
          .map(({ participant, accounts }) => (
            <div key={participant.id} className="space-y-1">
              <p className="text-xs font-medium text-muted-foreground">{participant.name}</p>
              <div className="divide-y rounded-lg border">
                {accounts.length === 0 ? (
                  <p className="px-3 py-2.5 text-sm text-muted-foreground">
                    No customer accounts yet.
                  </p>
                ) : (
                  accounts.map((account) => (
                    <CustomerRow key={account.id} pid={participant.id} account={account} />
                  ))
                )}
              </div>
            </div>
          ))}
      </section>

      <section className="space-y-3">
        <h2 className="text-sm font-medium text-muted-foreground">Or just read</h2>
        <Link href="/learn">
          <Card className="transition-colors hover:border-foreground/30">
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <GraduationCap className="size-4" />
                Learn
              </CardTitle>
            </CardHeader>
            <CardContent>
              <p className="text-sm text-muted-foreground">
                Eighteen chapters, from double-entry bookkeeping to arrears.
              </p>
            </CardContent>
          </Card>
        </Link>
      </section>
    </div>
  );
}

function InstitutionCard({
  href,
  icon,
  title,
  hint,
  body,
}: {
  href: string;
  icon: React.ReactNode;
  title: string;
  hint: "central-bank-reserves" | "clearing-vs-settlement";
  body: string;
}) {
  return (
    <Link href={href}>
      <Card className="h-full transition-colors hover:border-foreground/30">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            {icon}
            {title}
            <Hint id={hint} />
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">{body}</p>
        </CardContent>
      </Card>
    </Link>
  );
}

// A bank admitted at runtime has reserve accounts and no listener until the
// server restarts — admission is an operational act, and modelling it as an API
// call that instantly yields a running bank teaches the wrong thing. It is shown
// and not offered: a console whose every request 502s is worse than a sentence.
function BankCard({
  participant,
  provisioned,
  reserves,
}: {
  participant: Participant;
  provisioned: boolean;
  reserves: Reserve[];
}) {
  const { byCode } = useAssetLookup();

  const body = (
    <Card
      className={
        provisioned ? "h-full transition-colors hover:border-foreground/30" : "h-full opacity-70"
      }
    >
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <Building2 className="size-4" />
          {participant.name}
        </CardTitle>
      </CardHeader>
      <CardContent>
        {provisioned ? (
          <>
            <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
              Reserves
              <Hint id="reserve-account" />
            </p>
            {reserves.length === 0 ? (
              <p className="text-sm text-muted-foreground">None yet.</p>
            ) : (
              reserves.map((r) => {
                const asset = byCode.get(r.asset);
                return asset ? (
                  <p key={r.asset} className="text-lg font-semibold">
                    <Money amount={r.reserve} asset={asset} />
                  </p>
                ) : (
                  <Skeleton key={r.asset} className="h-6 w-20" />
                );
              })
            )}
          </>
        ) : (
          <p className="text-sm text-muted-foreground">
            <span className="font-medium text-foreground">Awaiting provisioning.</span> It was
            admitted to the network and has its reserve accounts, but no listener of its own
            until the server restarts.
          </p>
        )}
      </CardContent>
    </Card>
  );

  return provisioned ? (
    <Link href={homeFor({ persona: "bank", pid: participant.id })}>{body}</Link>
  ) : (
    body
  );
}

// Frozen and Closed accounts are listed and selectable on purpose: seeing the
// customer view of a frozen account is one of the better lessons here.
function CustomerRow({ pid, account }: { pid: string; account: DepositAccount }) {
  const iban = account.identifiers.find((i) => i.scheme === "IBAN");
  return (
    <Link
      href={homeFor({ persona: "customer", pid, did: account.id })}
      className="flex items-center justify-between gap-3 px-3 py-2.5 transition-colors hover:bg-muted/50"
    >
      <span className="flex min-w-0 items-center gap-2">
        <span className="truncate text-sm font-medium">{account.name}</span>
        <EnumBadge value={account.status} />
      </span>
      {iban && <span className="font-mono text-xs text-muted-foreground">{iban.value}</span>}
    </Link>
  );
}
```

- [ ] **Step 3: Run the gate**

Run: `cd web && npm run typecheck && npm run lint && npm run test && npm run build`
Expected: all clean. `nav-integrity.test.ts` still passes — `/` is in
`EXPLORE_ROUTES` and `src/app/page.tsx` exists again.

- [ ] **Step 4: Load it**

With `make dev` running, open `/`. Expect both institutions, four member banks
with reserves, the customer accounts grouped by bank with their IBANs and a
status badge (Bianca Belli frozen, Annie Ahlberg dormant, Closed Account closed),
and the Learn card. Click a bank card and a customer row and confirm both land on
the right URL (the customer one 404s until Task 12 — expected here).

Then prove the provisioning branch, which is the part no test covers:

```bash
curl -s -X POST localhost:3000/api/central-bank/members \
  -H 'content-type: application/json' -d '{"name":"Late Bank"}'
```

Reload `/`. Late Bank appears greyed with *Awaiting provisioning*, is not a link,
and has no customer group. Reset afterwards
(`curl -s -X POST localhost:3000/api/central-bank/admin/reset`).

- [ ] **Step 5: Commit**

```bash
git add web/src/app/page.tsx web/src/lib/api/hooks.ts
git commit -m "feat(web): show the cast at the root and let you pick a seat"
```

---

## Task 6: Split the shell

`app-shell.tsx` is 430 lines doing the resizable-panel machinery, the nav config,
the brand, the mobile sheets and the concept-panel bridging at once. This splits
it by responsibility and gives each persona its own shell.

**Files:**
- Create: `web/src/components/shell/{shell-frame,sidebar-nav,topbar,central-bank-shell,clearing-house-shell,bank-shell,plain-shell,persona-shell}.tsx`
- Modify: `web/src/app/layout.tsx`, `web/src/app/bank/[pid]/layout.tsx`
- Delete: `web/src/components/app-shell.tsx`

**Interfaces:**
- Consumes: `navFor`, `homeFor`, `useIdentity`, `type NavItem` (Task 2).
- Produces:
  - `<ShellFrame topbar sidebar? mobileSidebar? accent?>{children}</ShellFrame>`
  - `<SidebarNav items home collapsed onToggle onNavigate? />`
  - `<Topbar mobile? />`
  - `<PersonaShell>{children}</PersonaShell>` — the root layout's only shell import.

`ShellFrame` takes its sidebar as a prop and renders **two** panels when there is
none, which is what both the customer shell (Task 12) and the lobby need. The two
arrangements persist their layouts under different ids (`app-shell-nav` and
`app-shell-plain`) so a two-panel layout can never be restored into a three-panel
group.

The `accent` prop is declared here and used in Task 8; declaring it now keeps
`ShellFrame` from being edited twice.

- [ ] **Step 1: Extract the frame**

Create `web/src/components/shell/shell-frame.tsx`. Lift `DesktopShell`,
`MobileShell`, `ConceptStrip`, `ConceptSheet`, `MobileNavSheet` and the
`useIsDesktop` gate out of `app-shell.tsx` **unchanged in behaviour** — the
collapse bridging and the `ResizeObserver` reverse-direction logic are subtle and
were got right once. Move their comments with them, including the one explaining
why a `ResizeObserver` is used rather than rrp's `onResize`, and the one
explaining why the `h-screen overflow-hidden` wrapper is load-bearing (rrp's
group hard-codes `height:100%`, which only resolves against a definite-height
ancestor). Keep `NAV_COLLAPSED_KEY = "nav-collapsed"`.

The changes:

- `sidebar` and `mobileSidebar` become optional props; `topbar` and `accent` are
  new props.
- `ConceptPanelProvider` is **not** rendered here any more — the frame consumes
  the context, and Step 4 moves the provider to the root layout so the panel and
  its state survive a persona switch.
- When `sidebar` is absent, render a two-panel group (`main` | `concepts`), use
  `useDefaultLayout({ id: "app-shell-plain", panelIds: ["main", "concepts"] })`,
  and skip the nav panel, `navRef`, `navElRef`, the `navCollapsed` flag, its
  bridge effect, its localStorage effect and its half of the `ResizeObserver`.
  When present, everything is as today with `id: "app-shell-nav"`.
- The `ResizeObserver` effect observes `conceptEl` always and `navEl` only when
  there is one. Keep the `initial` skip exactly as written — it is what stops the
  first callback clobbering the storage-restored flag.

```tsx
export function ShellFrame({
  children,
  sidebar,
  mobileSidebar,
  topbar,
  accent,
}: {
  children: React.ReactNode;
  // Rendered inside the collapsible left panel, given the panel's collapsed
  // state. Absent means no left panel at all — the customer shell and the lobby,
  // which are content columns rather than consoles.
  sidebar?: (collapsed: boolean, toggle: () => void) => React.ReactNode;
  mobileSidebar?: React.ReactNode;
  topbar: React.ReactNode;
  // The identity's colour, set as --identity-accent on the outermost element of
  // both arrangements. Undefined outside the persona system.
  accent?: string;
}) { /* … */ }
```

with, on the outermost element of each of the desktop and mobile arrangements:

```tsx
  style={accent ? ({ "--identity-accent": accent } as React.CSSProperties) : undefined}
```

- [ ] **Step 2: Extract the nav and the topbar**

Create `web/src/components/shell/sidebar-nav.tsx` — `Brand`, `NavLinks` and
`NavSidebar` from `app-shell.tsx`, with the items passed in:

```tsx
export function SidebarNav({
  items,
  home,
  collapsed,
  onToggle,
  onNavigate,
}: {
  items: NavItem[];
  // Where the brand links to: this persona's own home, not "/".
  home: string;
  collapsed: boolean;
  onToggle: () => void;
  onNavigate?: () => void;
}) { /* … */ }
```

Active-state test:
`item.exact ? pathname === item.href : pathname.startsWith(item.href)`. Keep the
collapsed icon-only rendering with `title`/`aria-label`, and keep
`<ResetButton collapsed={collapsed} />` in the footer — resetting the system is
the central bank's act, and `ResetButton` already addresses `cb("/admin/reset")`
explicitly, which is correct rather than awkward wherever you happen to be
standing.

Create `web/src/components/shell/topbar.tsx` — `Topbar`, `ConceptTrigger` and
the mobile brand wordmark from `app-shell.tsx`, taking `mobileSidebar` as a prop
where it rendered `<MobileNavSheet />`. Where it rendered
`<ParticipantSwitcher />` it renders nothing for now; Task 7 puts the identity
picker there.

- [ ] **Step 3: Write the four shells and the dispatcher**

Create `web/src/components/shell/central-bank-shell.tsx`:

```tsx
"use client";

import { homeFor, navFor, type Identity } from "@/lib/identity";
import { ShellFrame } from "./shell-frame";
import { SidebarNav } from "./sidebar-nav";
import { Topbar } from "./topbar";

const IDENTITY: Identity = { persona: "central-bank" };

export function CentralBankShell({ children }: { children: React.ReactNode }) {
  const items = navFor(IDENTITY);
  const home = homeFor(IDENTITY);
  return (
    <ShellFrame
      topbar={<Topbar />}
      sidebar={(collapsed, toggle) => (
        <SidebarNav items={items} home={home} collapsed={collapsed} onToggle={toggle} />
      )}
      mobileSidebar={
        <SidebarNav items={items} home={home} collapsed={false} onToggle={() => {}} />
      }
    >
      {children}
    </ShellFrame>
  );
}
```

Create `web/src/components/shell/clearing-house-shell.tsx` — identical with
`const IDENTITY: Identity = { persona: "clearing-house" };`.

Create `web/src/components/shell/bank-shell.tsx` — identical, taking `{ pid }`
and building `const identity: Identity = { persona: "bank", pid };` inside the
component.

Three consoles with the same layout and different contents. They are three thin
files rather than one taking a persona prop because they differ only in what they
hand `SidebarNav`, and a prop would hide that.

Create `web/src/components/shell/plain-shell.tsx` — `ShellFrame` with no
`sidebar`, so the lobby and Learn get a content column, the concepts rail and
nothing else. This fifth arrangement falls out of `useIdentity()` returning null.

Create `web/src/components/shell/persona-shell.tsx`:

```tsx
"use client";

import { useIdentity } from "@/lib/identity";
import { BankShell } from "./bank-shell";
import { CentralBankShell } from "./central-bank-shell";
import { ClearingHouseShell } from "./clearing-house-shell";
import { PlainShell } from "./plain-shell";

// Who you are decides which software you get. The customer's shell arrives with
// the customer's screens in Task 12; until then a customer URL has no page to
// render and falls through to the plain shell like the lobby does.
export function PersonaShell({ children }: { children: React.ReactNode }) {
  const identity = useIdentity();
  switch (identity?.persona) {
    case "central-bank":
      return <CentralBankShell>{children}</CentralBankShell>;
    case "clearing-house":
      return <ClearingHouseShell>{children}</ClearingHouseShell>;
    case "bank":
      return <BankShell pid={identity.pid}>{children}</BankShell>;
    default:
      return <PlainShell>{children}</PlainShell>;
  }
}
```

- [ ] **Step 4: Move the provider up and delete the old shell**

Edit `web/src/app/layout.tsx`: `ConceptPanelProvider` moves to the root layout so
the concepts panel and its state survive a persona switch, and `AppShell` becomes
`PersonaShell`:

```tsx
import { ConceptPanelProvider } from "@/components/concept-panel-provider";
import { PersonaShell } from "@/components/shell/persona-shell";

// …

      <body className="min-h-full">
        <Providers>
          <ConceptPanelProvider>
            <PersonaShell>{children}</PersonaShell>
          </ConceptPanelProvider>
        </Providers>
      </body>
```

Then:

```bash
git rm web/src/components/app-shell.tsx
```

- [ ] **Step 5: Promote the bank's tabs into its sidebar**

The bank's sub-nav is a tab strip *inside* a page, which is what "a section of
the network app" looks like. It is the shell's sidebar now. Edit
`web/src/app/bank/[pid]/layout.tsx`: delete the `base` and `tabs` locals, the
`<nav>` block and the now-unused `usePathname`/`cn`/`Link` imports. Keep the
`useParticipant` call — a bad pid must still surface the friendly not-found — and
the bank header:

```tsx
"use client";

import { useParams } from "next/navigation";

import { useParticipant } from "@/lib/api/hooks";
import { IdText } from "@/components/id-text";
import { ErrorState } from "@/components/error-state";
import { Skeleton } from "@/components/ui/skeleton";

// The back office of one member bank. Its sections are the shell's sidebar now,
// not tabs inside a page: a bank is a place you are, not a section of a network
// app. This layout validates the pid and names the bank.
export default function BankLayout({ children }: { children: React.ReactNode }) {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const { data, isLoading, error } = useParticipant(pid);

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        {isLoading ? (
          <Skeleton className="h-8 w-48" />
        ) : (
          <div className="flex items-center gap-3">
            <h1 className="text-2xl font-semibold tracking-tight">
              {data?.name ?? "Participant"}
            </h1>
            {data && <IdText id={data.id} />}
          </div>
        )}
        <p className="text-sm text-muted-foreground">Member bank</p>
      </div>
      {error ? <ErrorState error={error} /> : children}
    </div>
  );
}
```

- [ ] **Step 6: Run the gate**

Run: `cd web && npm run typecheck && npm run lint && npm run test && npm run build`
Expected: all clean.

- [ ] **Step 7: Load all four surfaces**

With `make dev`: open `/` (plain shell — no left nav, concepts rail present),
`/central-bank` (Reserves + Audit in the sidebar), `/clearing-house` (the network
sections), `/bank/<pid>` (the bank's own sections in the sidebar, and **no tab
strip in the page**), and `/learn/12-sepa`.

In each console: collapse and expand both the nav and the concepts panel, drag
both handles past their minimum to confirm the reverse direction still flips the
content mode, and reload to confirm the collapse states persist. On `/` and
`/learn`, confirm the concepts panel still collapses and expands with only two
panels. Then open a `[[wiki-link]]` in the concepts panel and switch persona, to
confirm the panel and its state survive the switch — which is the point of moving
the provider up.

- [ ] **Step 8: Commit**

```bash
git add -A web/src
git commit -m "refactor(web): one shell per persona, not one app with filters"
```

---

## Task 7: The identity picker

One control, not a persona toggle plus a context picker: a persona without its
context is not an identity — "customer" alone addresses nothing, and a
two-control design has a state where the persona has changed and the context has
not.

**Files:**
- Create: `web/src/components/shell/identity-picker.tsx`
- Modify: `web/src/components/shell/topbar.tsx`
- Delete: `web/src/components/participant-switcher.tsx`

**Interfaces:**
- Consumes: `useIdentityDirectory` (Task 5); `homeFor`, `useIdentity`,
  `type Identity` (Task 2); `Popover`, `Command*` from `@/components/ui/*`.
- Produces: `<IdentityPicker />`.

Built directly on Popover + cmdk rather than the generic `Combobox`, because
`Combobox` renders one flat option list and this needs groups.

- [ ] **Step 1: Write the picker**

Create `web/src/components/shell/identity-picker.tsx`:

```tsx
"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { ChevronsUpDown } from "lucide-react";

import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Button } from "@/components/ui/button";
import { EnumBadge } from "@/components/enum-badge";
import { useIdentityDirectory } from "@/lib/api/hooks";
import { homeFor, useIdentity, type Identity } from "@/lib/identity";

// The switcher: one flat searchable list of complete identities, grouped
// Institutions / Banks / Customers, with customers under their bank. Selecting
// one navigates to its home.
//
// One control rather than a persona toggle plus a context picker, because a
// persona without its context is not an identity — "customer" alone addresses
// nothing, and two controls have a state where the persona has changed and the
// context has not.
//
// Frozen and Closed accounts are listed and selectable: seeing the customer view
// of a frozen account is one of the better lessons available here. A bank with
// no listener is not — it is shown as awaiting provisioning and cannot be
// chosen, because entering it would mean a console whose every request 502s.
export function IdentityPicker() {
  const [open, setOpen] = useState(false);
  const router = useRouter();
  const identity = useIdentity();
  const { banks, isLoading } = useIdentityDirectory();

  const bankName = (pid: string) =>
    banks.find((b) => b.participant.id === pid)?.participant.name ?? pid;

  const currentLabel = (() => {
    if (!identity) return "Choose an identity";
    if (identity.persona === "central-bank") return "Central bank";
    if (identity.persona === "clearing-house") return "Clearing house";
    if (identity.persona === "bank") return bankName(identity.pid);
    const account = banks
      .find((b) => b.participant.id === identity.pid)
      ?.accounts.find((a) => a.id === identity.did);
    return account ? `${account.name} · ${bankName(identity.pid)}` : "Customer";
  })();

  function go(next: Identity) {
    setOpen(false);
    router.push(homeFor(next));
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          role="combobox"
          aria-expanded={open}
          className="w-[240px] justify-between"
        >
          <span className="truncate">{currentLabel}</span>
          <ChevronsUpDown className="size-4 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-[320px] p-0" align="end">
        <Command>
          <CommandInput placeholder="Search identities…" />
          <CommandList>
            <CommandEmpty>{isLoading ? "Loading…" : "No identity found."}</CommandEmpty>

            <CommandGroup heading="Institutions">
              <CommandItem
                value="central bank reserves settlement"
                onSelect={() => go({ persona: "central-bank" })}
              >
                Central bank
              </CommandItem>
              <CommandItem
                value="clearing house csm payments cycles schemes directory"
                onSelect={() => go({ persona: "clearing-house" })}
              >
                Clearing house
              </CommandItem>
            </CommandGroup>

            <CommandGroup heading="Banks">
              {banks.map(({ participant, provisioned }) => (
                <CommandItem
                  key={participant.id}
                  value={`bank ${participant.name} ${participant.id}`}
                  disabled={!provisioned}
                  onSelect={() =>
                    provisioned && go({ persona: "bank", pid: participant.id })
                  }
                >
                  <span className="truncate">{participant.name}</span>
                  {!provisioned && (
                    <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                      awaiting provisioning
                    </span>
                  )}
                </CommandItem>
              ))}
            </CommandGroup>

            {banks
              .filter((b) => b.provisioned)
              .map(({ participant, accounts }) => (
                <CommandGroup key={participant.id} heading={`Customers · ${participant.name}`}>
                  {accounts.map((account) => (
                    <CommandItem
                      key={account.id}
                      value={`customer ${account.name} ${participant.name} ${account.identifiers
                        .map((i) => i.value)
                        .join(" ")}`}
                      onSelect={() =>
                        go({ persona: "customer", pid: participant.id, did: account.id })
                      }
                    >
                      <span className="truncate">{account.name}</span>
                      <EnumBadge value={account.status} />
                    </CommandItem>
                  ))}
                </CommandGroup>
              ))}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
```

- [ ] **Step 2: Put it in the topbar and delete the old switcher**

Edit `web/src/components/shell/topbar.tsx`: render `<IdentityPicker />` where
`<ParticipantSwitcher />` used to be, so it is in every shell.

Then:

```bash
git rm web/src/components/participant-switcher.tsx
```

Nothing else imports it — `app-shell.tsx` is already gone. Its
`ledger.lastParticipant` localStorage key goes with it: `/` is always the lobby,
so there is no last identity to remember.

`CreateParticipantDialog` was the switcher's `+` button as well as the
dashboard's action. It moves to the central bank's console in Task 9, where
admitting a member belongs. Until then it stays only on
`/clearing-house/page.tsx`, where the move in Task 4 left it — confirm that page
still renders it and drop nothing.

- [ ] **Step 3: Run the gate**

Run: `cd web && npm run typecheck && npm run lint && npm run test && npm run build`
Expected: all clean.

- [ ] **Step 4: Load it**

Open the picker from `/central-bank`, `/clearing-house`, `/bank/<pid>` and `/`.
Confirm: three group headings with customers nested under their bank; typing
"aurora", "alice", "clearing" and an IBAN fragment (`1001`) each filter to the
right rows; the trigger shows the current identity; selecting a customer
navigates to `/customer/<pid>/<did>` (which 404s until Task 12 — expected here);
selecting a bank and each institution land correctly. Admit a Late Bank as in
Task 5 and confirm its row is present, labelled *awaiting provisioning*, and does
nothing when clicked. Reset afterwards.

- [ ] **Step 5: Commit**

```bash
git add -A web/src
git commit -m "feat(web): switch identities, not filters"
```

---

## Task 8: An accent per identity

**Files:**
- Create: `web/src/lib/accent.ts`, `web/src/lib/accent.test.ts`
- Modify: `web/src/components/shell/shell-frame.tsx`,
  `web/src/components/shell/{central-bank,clearing-house,bank}-shell.tsx`

**Interfaces:**
- Consumes: `type Identity` (Task 2); `ShellFrame`'s `accent` prop (Task 6).
- Produces: `function accentFor(identity: Identity | null): string | undefined`
  — an `oklch()` string for `--identity-accent`.

- [ ] **Step 1: Write the failing test**

Create `web/src/lib/accent.test.ts`:

```ts
import { describe, expect, it } from "vitest";

import { accentFor } from "./accent";

describe("accentFor", () => {
  // Telling the two institutions apart at a glance is exactly the lesson the
  // split exists to teach, so they get different accents rather than sharing one.
  it("gives the two institutions distinct accents", () => {
    const cb = accentFor({ persona: "central-bank" });
    const csm = accentFor({ persona: "clearing-house" });
    expect(cb).toMatch(/^oklch\(/);
    expect(csm).toMatch(/^oklch\(/);
    expect(cb).not.toBe(csm);
  });

  it("is stable for a given bank", () => {
    expect(accentFor({ persona: "bank", pid: "bank_1" })).toBe(
      accentFor({ persona: "bank", pid: "bank_1" }),
    );
  });

  it("distinguishes the four seeded banks", () => {
    const accents = ["bank_1", "bank_3", "bank_5", "bank_7"].map((pid) =>
      accentFor({ persona: "bank", pid }),
    );
    expect(new Set(accents).size).toBe(4);
  });

  // You are a customer *of* Aurora, and the screen should say so without a
  // label — so a customer carries their bank's accent, not one of their own.
  it("gives a customer their bank's accent", () => {
    expect(accentFor({ persona: "customer", pid: "bank_1", did: "dep_9" })).toBe(
      accentFor({ persona: "bank", pid: "bank_1" }),
    );
  });

  it("has no accent without an identity", () => {
    expect(accentFor(null)).toBeUndefined();
  });
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd web && npx vitest run src/lib/accent.test.ts`
Expected: FAIL — `Failed to resolve import "./accent"`.

- [ ] **Step 3: Write the implementation**

Create `web/src/lib/accent.ts`:

```ts
import type { Identity } from "./identity";

// Each identity carries an accent, set as --identity-accent on the shell root. A
// customer inherits their bank's: you are a customer *of* Aurora, and the screen
// should say so without a label.
//
// The two institutions get different accents rather than sharing one, because
// telling them apart at a glance is exactly the lesson the split exists to
// teach.
//
// A bank's hue is picked from a fixed palette by hashing its pid, so its colour
// is stable across reloads and needs nothing persisted. Chroma and lightness are
// held constant so no bank is louder than another and the same values read in
// both themes.
const CENTRAL_BANK_ACCENT = "oklch(0.55 0.02 260)";
const CLEARING_HOUSE_ACCENT = "oklch(0.58 0.10 150)";
const BANK_HUES = [25, 265, 330, 60, 200, 100];

function hueFor(pid: string): number {
  let h = 0;
  for (let i = 0; i < pid.length; i++) {
    h = (h * 31 + pid.charCodeAt(i)) >>> 0;
  }
  return BANK_HUES[h % BANK_HUES.length];
}

export function accentFor(identity: Identity | null): string | undefined {
  if (!identity) return undefined;
  if (identity.persona === "central-bank") return CENTRAL_BANK_ACCENT;
  if (identity.persona === "clearing-house") return CLEARING_HOUSE_ACCENT;
  return `oklch(0.58 0.14 ${hueFor(identity.pid)})`;
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `cd web && npx vitest run src/lib/accent.test.ts`
Expected: PASS.

**If "distinguishes the four seeded banks" fails**, two of `bank_1`, `bank_3`,
`bank_5`, `bank_7` collided on the six-hue palette. Reorder `BANK_HUES` or add a
seventh hue until they do not — do not weaken the assertion to `> 1`. Four banks
that cannot be told apart is the failure this test is for.

- [ ] **Step 5: Use it**

`ShellFrame` already takes `accent` (Task 6) and already puts it on the outermost
element of both arrangements. Use it in two places inside `ShellFrame`, so every
persona gets it for free:

- The topbar grows a 2px top rule:
  `<header className="… border-t-2 border-t-[color:var(--identity-accent,transparent)]">`
- The sidebar's brand block takes the accent as its text colour:
  `<span className="text-base font-semibold tracking-tight text-[color:var(--identity-accent,inherit)]">`

Pass `accent={accentFor(IDENTITY)}` from `central-bank-shell.tsx` and
`clearing-house-shell.tsx`, and `accent={accentFor(identity)}` from
`bank-shell.tsx`. `plain-shell.tsx` passes nothing, and the `transparent` /
`inherit` fallbacks in the arbitrary values are what make that render correctly.

- [ ] **Step 6: Run the gate**

Run: `cd web && npm run typecheck && npm run lint && npm run test && npm run build`
Expected: all clean.

- [ ] **Step 7: Load it**

Switch between the four member banks, the central bank and the clearing house.
Confirm the top rule changes colour, that the two institutions are visibly
different from each other, and that a given bank keeps its colour across a
reload. Check both light and dark theme. On `/` and `/learn` confirm there is no
rule at all rather than a black one.

- [ ] **Step 8: Commit**

```bash
git add -A web/src
git commit -m "feat(web): let each identity carry a colour, and a customer their bank's"
```

---

## Task 9: The central bank's console

The two institutions are a re-home and a subtraction. Task 4 did the re-home;
this does the rest of the central bank. Its home is the reserves table it already
has, plus the one thing it *gains* — the settle action, moved off the cycle
detail page onto the console of the operator whose act it is. What it loses is
every screen showing an individual payment, which is the point and is a
subtraction rather than work.

**Files:**
- Modify: `web/src/app/central-bank/page.tsx` (drop the audit half, add settlement instructions and admission)
- Create: `web/src/app/central-bank/audit/page.tsx`
- Modify: `web/src/app/clearing-house/cycles/[cid]/page.tsx` (drop the Settle action)
- Modify: `web/src/components/create-participant-dialog.tsx`
- Modify: `web/src/lib/api/{endpoints,query-keys,hooks}.ts`

**Interfaces:**
- Consumes: `cb()` (operator.ts); `useReserves`, `useCentralBankAudit`,
  `useSettleCycle`, `useAssetLookup`, `AuditTable`, `useAuditPager`, `DataTable`,
  `NetPositionsTable`, `ConfirmAction`.
- Produces:
  - `function centralBankCycles(): Promise<ClearingCycle[]>` (endpoints)
  - `qk.centralBankCycles()` (query keys)
  - `useCentralBankCycles()` (hooks)

Task 1 also put `GET /settlements` and `GET /settlements/{sid}` on the central
bank's surface, and the web does **not** wrap them. That is deliberate: the
surfaces are defined by what an operator's role is, not by what the current
frontend happens to call — `GET /assets` is on all three listeners for the same
reason. `POST /settlements` already returns the settlement it created, and the
central bank's own record of a reserve movement is its audit log. Adding an
uncalled hook to match would be dead code.

- [ ] **Step 1: Add the central bank's reads to the data layer**

These are Task 1's four new routes reaching the web. They are separate functions
and separate keys from the clearing house's, even though the bytes are identical:
the two operators read them for different reasons and are individually reachable
or down, so one persona's cache must not answer for the other's.

`web/src/lib/api/endpoints.ts` — add to the `--- Central bank ---` section:

```ts
// The cycles the central bank is asked to settle, read from its own listener.
//
// A settlement instruction in the real thing IS a closed cycle and its net
// positions, so this is part of the act rather than a widening. What the central
// bank still cannot read is an individual payment — GET /payments is the
// clearing house's, and a real central bank does not see one.
export function centralBankCycles(): Promise<ClearingCycle[]> {
  return request("GET", cb("/cycles"));
}
```

`web/src/lib/api/query-keys.ts` — beside `centralBankAudit`:

```ts
  // Keyed under the central bank rather than shared with the clearing house's
  // cycles(): the same rows read from a different listener, which can be
  // individually down, and for a different reason.
  centralBankCycles: () => ["central-bank", "cycles"] as const,
```

`web/src/lib/api/hooks.ts` — beside `useCentralBankAudit`:

```ts
export function useCentralBankCycles() {
  return useQuery({
    queryKey: qk.centralBankCycles(),
    queryFn: api.centralBankCycles,
  });
}
```

and extend `invalidateNetwork` (the helper above `usePayments`) with the new key,
so settling refreshes the console it was performed on:

```ts
  qc.invalidateQueries({ queryKey: qk.centralBankCycles() });
```

`ClearingCycle` is already in `endpoints.ts`'s type import — check before adding.

- [ ] **Step 2: Rewrite the central bank's home**

Replace `web/src/app/central-bank/page.tsx`. `ReserveAmountCell` and
`reserveColumns` are kept exactly as they are; the audit section is lifted out to
its own route in Step 3, and a settlement-instructions section and the admission
dialog take its place.

```tsx
"use client";

import { toast } from "sonner";

import { PageHeader } from "@/components/page-header";
import { CreateParticipantDialog } from "@/components/create-participant-dialog";
import { DataTable, type Column } from "@/components/data-table";
import { AmountCell, UnresolvedAmount } from "@/components/money";
import { IdText } from "@/components/id-text";
import { EnumBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { NetPositionsTable } from "@/components/net-positions-table";
import { ConfirmAction } from "@/components/forms/confirm-action";
import {
  useAssetLookup,
  useCentralBankCycles,
  useReserves,
  useSettleCycle,
} from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";
import type { ClearingCycle, Reserve } from "@/lib/types";

// A reserve's amount can only be rendered once its asset's scale is known.
// Reserves in different assets are different things, so each row resolves its
// own code rather than the page assuming one.
function ReserveAmountCell({ reserve }: { reserve: Reserve }) {
  const { byCode, isLoading } = useAssetLookup();
  const asset = byCode.get(reserve.asset);
  if (!asset) {
    return (
      <UnresolvedAmount
        code={reserve.asset}
        isLoading={isLoading}
        className="ml-auto block text-right"
      />
    );
  }
  return <AmountCell amount={reserve.reserve} asset={asset} />;
}

const reserveColumns: Column<Reserve>[] = [
  { key: "participant", header: "Participant", render: (r) => <IdText id={r.participant} /> },
  { key: "asset", header: "Asset", render: (r) => r.asset },
  {
    key: "reserve",
    header: "Reserve",
    align: "right",
    hint: "reserve-account",
    render: (r) => <ReserveAmountCell reserve={r} />,
  },
];

// The settlement layer. Banks meet only here, and what the central bank does is
// move reserves between them — it never sees an individual payment, which is
// what the operator split made expressible for the first time.
//
// Admission is the central bank's act too: opening a member's reserve and
// settlement accounts happens in the central bank's own book, which is why
// POST /members is its route and not the clearing house's.
export default function CentralBankPage() {
  const reserves = useReserves();
  const cycles = useCentralBankCycles();

  return (
    <div className="space-y-8">
      <PageHeader
        title="Central bank"
        hint="central-bank-reserves"
        description="Banks meet only here. The central bank holds one reserve account per participant and asset, and settlement is reserves moving between them."
        actions={<CreateParticipantDialog />}
      />

      <SettlementInstructions
        cycles={(cycles.data ?? []).filter((c) => c.status === "Closed")}
        isLoading={cycles.isLoading}
        error={cycles.error}
        onRetry={() => cycles.refetch()}
      />

      <section className="space-y-3">
        <h2 className="text-sm font-medium text-muted-foreground">Reserves</h2>
        {reserves.error ? (
          <ErrorState error={reserves.error} onRetry={() => reserves.refetch()} />
        ) : (
          <DataTable
            columns={reserveColumns}
            rows={reserves.data}
            rowKey={(r) => `${r.participant}:${r.asset}`}
            isLoading={reserves.isLoading}
            empty="No participants yet. Admit one to see its reserve account."
          />
        )}
      </section>
    </div>
  );
}

// A closed cycle is a settlement instruction: the clearing house netted the
// payments and computed each bank's position, and discharging those positions is
// the central bank's act. That is why the settle button is here and not on the
// cycle page — a clearing house that could move reserves would be a central bank.
//
// The cycle's payment ids are deliberately not rendered. The central bank
// settles a net figure per bank and has no business with who paid whom.
function SettlementInstructions({
  cycles,
  isLoading,
  error,
  onRetry,
}: {
  cycles: ClearingCycle[];
  isLoading: boolean;
  error: unknown;
  onRetry: () => void;
}) {
  return (
    <section className="space-y-3">
      <h2 className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground">
        Settlement instructions
        <Hint id="clearing-vs-settlement" />
      </h2>
      {error ? (
        <ErrorState error={error} onRetry={onRetry} />
      ) : isLoading ? (
        <Card>
          <CardContent className="py-8 text-sm text-muted-foreground">Loading…</CardContent>
        </Card>
      ) : cycles.length === 0 ? (
        <Card>
          <CardContent className="py-8 text-sm text-muted-foreground">
            Nothing to settle. A cycle becomes settleable once the clearing house
            closes it and nets the positions.
          </CardContent>
        </Card>
      ) : (
        cycles.map((c) => <SettleCard key={c.id} cycle={c} />)
      )}
    </section>
  );
}

function SettleCard({ cycle }: { cycle: ClearingCycle }) {
  const settle = useSettleCycle();
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-2 text-base">
          Cycle {cycle.scheme}
          <IdText id={cycle.id} />
          <EnumBadge value={cycle.status} />
        </CardTitle>
        <ConfirmAction
          trigger={<Button size="sm">Settle</Button>}
          title="Settle cycle"
          description="Moves the net amounts across central-bank reserves, discharging the obligations."
          confirmLabel="Settle"
          pending={settle.isPending}
          onConfirm={async () => {
            await settle.mutateAsync(cycle.id, {
              onError: (err) => toast.error(describeError(err)),
            });
            toast.success("Cycle settled — reserves moved");
          }}
        />
      </CardHeader>
      <CardContent>
        <NetPositionsTable positions={cycle.netPositions} asset={cycle.asset} />
      </CardContent>
    </Card>
  );
}
```

The settle action does **not** navigate to the settlement afterwards: the
settlement detail page is the clearing house's, and following it would walk the
operator out of their own console. The card disappears from the list instead,
which is the honest signal that the instruction was discharged.

- [ ] **Step 3: Give the central bank's audit its own route**

Create `web/src/app/central-bank/audit/page.tsx`:

```tsx
"use client";

import { PageHeader } from "@/components/page-header";
import { AuditTable, useAuditPager } from "@/components/audit-table";
import { useCentralBankAudit } from "@/lib/api/hooks";

// The central bank's own log: reserve movements and settlements, the events
// produced by the layer the banks meet at. A bank's ledger and deposit logs are
// its own and live in its back office — GET /audit means "this operator's own
// log" on every operator, which is the split being consistent rather than
// colliding.
export default function CentralBankAuditPage() {
  const pager = useAuditPager();
  const audit = useCentralBankAudit(pager.query);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Audit"
        hint="audit-trail"
        description="Every mutation the central-bank layer produced, append-only and in order."
      />
      <AuditTable
        events={audit.data}
        isLoading={audit.isLoading}
        error={audit.error}
        onRetry={() => audit.refetch()}
        pager={pager}
        empty="No central-bank activity yet. Fund a participant or settle a cycle to see reserve movements."
      />
    </div>
  );
}
```

- [ ] **Step 4: Take Settle off the cycle page**

Edit `web/src/app/clearing-house/cycles/[cid]/page.tsx`:

- Delete the whole `{c.status === "Closed" && (<ConfirmAction … Settle …/>)}`
  block and the `const settle = useSettleCycle();` line.
- Drop `useSettleCycle` from the `@/lib/api/hooks` import, and `useRouter` plus
  its `const router = useRouter();` if nothing else in the file uses them (the
  settle handler was the only `router.push`).
- Add, where the Settle button was, a line explaining the absence rather than
  leaving a silent gap:

```tsx
              {c.status === "Closed" && !c.settlementId && (
                <p className="text-xs text-muted-foreground">
                  Netted and awaiting settlement. Moving reserves is the central
                  bank&apos;s act, not the clearing house&apos;s.
                </p>
              )}
```

Keep `Close & net` exactly as it is: closing a cycle and netting the positions is
the clearing house's own act. Keep the settlement link in the Timeline card —
reading whether the cycle it closed has settled is the CSM's business, which is
why 6a left it `GET /settlements`.

- [ ] **Step 5: Stop the admission dialog walking into a dead console**

`create-participant-dialog.tsx:60` pushes to the new bank's console. A bank
admitted at runtime has no listener until the server restarts, so that push lands
on a console whose every request 502s — exactly what the lobby and picker were
taught to refuse. Replace the navigation:

```tsx
          toast.success(
            `${p.name} admitted. Its reserve accounts are open; it gets a listener ` +
              `of its own when the server restarts — admission is not provisioning.`,
          );
```

Delete the `router.push(…)` line and, if nothing else in the file uses it, the
`useRouter` import and its call.

- [ ] **Step 6: Run the gate**

Run: `cd web && npm run typecheck && npm run lint && npm run test && npm run build`
Expected: all clean, `nav-integrity.test.ts` included — `/central-bank/audit` is
in the central bank's nav and now has a file.

- [ ] **Step 7: Settle something**

With `make dev`:

- `/central-bank` — reserves for four banks, and "Nothing to settle."
- `/clearing-house/cycles` — open a cycle if none is open, send a payment into it
  from `/clearing-house/payments`, then Close & net it. Confirm the cycle page
  now says settlement is the central bank's act and offers no Settle button.
- Back to `/central-bank` — the closed cycle appears as a settlement instruction
  with its net positions. Settle it. Confirm the card disappears, the reserves
  table moves, and `/central-bank/audit` shows the settlement.
- Confirm `/clearing-house/cycles/<cid>` now shows the settlement link.
- Admit a bank from `/central-bank`: expect the toast, no navigation, and the new
  bank appearing on `/` as awaiting provisioning. Reset afterwards.

- [ ] **Step 8: Commit**

```bash
git add -A web/src
git commit -m "feat(web): settling is the central bank's act, on the central bank's console"
```

---

## Task 10: The clearing house's directory

The one genuinely new operator screen. `GET /directory` has had no UI since
sub-project 5 shipped it, and the CSM is the operator whose job that question is:
resolving an address is exactly "which bank?", so a route that already named the
bank would answer nothing.

**Files:**
- Create: `web/src/app/clearing-house/directory/page.tsx`
- Create: `web/src/lib/use-debounced-value.ts`
- Modify: `web/src/lib/types.ts`, `web/src/lib/api/{endpoints,query-keys,hooks}.ts`

**Interfaces:**
- Consumes: `csm()` (operator.ts); `useParticipants`, `useAssetLookup`,
  `describeError`.
- Produces:
  - `interface DirectoryEntry { participant: string; account: string; name: string; asset: string; identifier: AccountIdentifier }`
  - `function resolveIdentifierAtCsm(scheme: string, value: string): Promise<DirectoryEntry>`
  - `function useCsmDirectory(scheme: string, value: string)`
  - `function useDebouncedValue<T>(value: T, delayMs: number): T`

The customer's lookup in Task 13 is a *different* function against a *different*
listener, and deliberately so — a customer's browser must never talk to the CSM.
Both are added rather than one being reused with an operator argument, because
"which listener" is the whole distinction and a parameter would hide it.

- [ ] **Step 1: Add the directory to the data layer**

`web/src/lib/types.ts` — add beside `AccountIdentifier`:

```ts
// What GET /directory answers: enough to tell a caller who an address belongs to
// before they pay it. `identifier` is echoed back so a client that fired several
// lookups at once can tell the answers apart. See api/handlers_directory.go's
// directoryEntryDTO.
export interface DirectoryEntry {
  participant: string;
  account: string;
  name: string;
  asset: string;
  identifier: AccountIdentifier;
}
```

`web/src/lib/api/endpoints.ts` — add `DirectoryEntry` to the type import and a
section after `// --- Schemes ---`:

```ts
// --- Directory ------------------------------------------------------------

// Resolving an address on the clearing house's listener: the operator whose job
// "which bank holds this?" is. 404 when nobody holds it, 409 when two banks do —
// an ambiguous address is an error rather than a first hit, following the
// settlement rule about not defaulting quietly.
//
// A customer's lookup is NOT this function. It goes to their own bank's listener
// (see resolveIdentifierAtBank), because a retail client has no CSM connection.
export function resolveIdentifierAtCsm(
  scheme: string,
  value: string,
): Promise<DirectoryEntry> {
  return request("GET", csm(`/directory${qs({ scheme, value })}`));
}
```

`web/src/lib/api/query-keys.ts` — above the payment-network block:

```ts
  // Keyed by the listener that answered as well as the address: the same
  // question asked of the clearing house and of a bank are two different
  // requests, and only one of them is a customer's to make.
  csmDirectory: (scheme: string, value: string) =>
    ["clearing-house", "directory", scheme, value] as const,
```

`web/src/lib/api/hooks.ts` — beside the scheme hooks:

```ts
// Resolves an external address to the account that holds it. `retry: false`
// because a 404 here is an answer — nobody holds that IBAN — and retrying it
// three times only delays saying so.
export function useCsmDirectory(scheme: string, value: string) {
  return useQuery({
    queryKey: qk.csmDirectory(scheme, value),
    queryFn: () => api.resolveIdentifierAtCsm(scheme, value),
    enabled: scheme !== "" && value !== "",
    retry: false,
  });
}
```

- [ ] **Step 2: Add the debounce hook**

Create `web/src/lib/use-debounced-value.ts`:

```ts
"use client";

import { useEffect, useState } from "react";

// Settles a value that changes per keystroke. Both directory screens resolve a
// typed address as you type; without this they would fire a request per
// character, and each miss along the way would be cached under its own key.
export function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setSettled(value), delayMs);
    return () => clearTimeout(t);
  }, [value, delayMs]);
  return settled;
}
```

- [ ] **Step 3: Write the screen**

Create `web/src/app/clearing-house/directory/page.tsx`:

```tsx
"use client";

import { useState } from "react";

import { PageHeader } from "@/components/page-header";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { FieldLabel } from "@/components/field-label";
import { IdText } from "@/components/id-text";
import { useCsmDirectory, useParticipants } from "@/lib/api/hooks";
import { useDebouncedValue } from "@/lib/use-debounced-value";
import { describeError } from "@/lib/api/errors";

// Type an address, see who holds it.
//
// Routing is by id and an IBAN is not one, so somebody has to answer "which
// bank?" before a payment can be built — and that is the clearing house's job.
// The register stops at the bank: a bank-issued identifier is globally unique by
// construction, and an address two banks both claim is refused rather than
// resolved to the first hit.
export default function DirectoryPage() {
  const [value, setValue] = useState("");
  const settled = useDebouncedValue(value.trim(), 350);
  const entry = useCsmDirectory(settled ? "IBAN" : "", settled);
  const { data: participants } = useParticipants();

  const bankName = (pid: string) => participants?.find((p) => p.id === pid)?.name ?? pid;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Directory"
        hint="account-addressing"
        description="An account's id is its bank's own key and never leaves it. An address is what a counterparty quotes — and resolving one to the account behind it is the network's question, not any single bank's."
      />

      <Card>
        <CardContent className="space-y-4">
          <div className="space-y-1.5">
            <FieldLabel htmlFor="directory-iban">IBAN</FieldLabel>
            <Input
              id="directory-iban"
              value={value}
              placeholder="SE89-AURORA-1001"
              className="font-mono"
              onChange={(e) => setValue(e.target.value)}
            />
          </div>

          {!settled ? (
            <p className="text-sm text-muted-foreground">
              Nothing typed yet. The seeded dataset addresses every customer
              account, so any of them resolves.
            </p>
          ) : entry.isLoading ? (
            <Skeleton className="h-16 w-full" />
          ) : entry.error ? (
            <p className="text-sm text-destructive">{describeError(entry.error)}</p>
          ) : entry.data ? (
            <dl className="grid gap-3 text-sm sm:grid-cols-2">
              <div>
                <dt className="text-xs text-muted-foreground">Holder</dt>
                <dd className="font-medium">{entry.data.name}</dd>
              </div>
              <div>
                <dt className="text-xs text-muted-foreground">Bank</dt>
                <dd className="font-medium">{bankName(entry.data.participant)}</dd>
              </div>
              <div>
                <dt className="text-xs text-muted-foreground">Account</dt>
                <dd>
                  <IdText id={entry.data.account} />
                </dd>
              </div>
              <div>
                <dt className="text-xs text-muted-foreground">Asset</dt>
                <dd>{entry.data.asset}</dd>
              </div>
            </dl>
          ) : null}
        </CardContent>
      </Card>

      <p className="max-w-prose text-xs text-muted-foreground">
        The account id is shown because this is an operator&apos;s screen. It is
        the holding bank&apos;s own key and a counterparty never sees it — which
        is exactly why an address exists.
      </p>
    </div>
  );
}
```

- [ ] **Step 4: Put Directory in the clearing house's nav**

The screen exists now, so the nav entry may. Edit `web/src/lib/identity.test.ts`,
appending to the clearing-house expectation:

```ts
      "/clearing-house/directory",
```

Run `cd web && npx vitest run src/lib/identity.test.ts` — expect FAIL. Then edit
`web/src/lib/identity.ts`, replacing the `// Directory arrives in Task 10` comment
in `CLEARING_HOUSE_NAV` with:

```ts
  { href: "/clearing-house/directory", label: "Directory", icon: Search },
```

and adding `Search` to the lucide import. Re-run: expect PASS on both
`identity.test.ts` and `nav-integrity.test.ts`, the latter because the page now
exists — which is the pairing the two tests are for.

- [ ] **Step 5: Run the gate**

Run: `cd web && npm run typecheck && npm run lint && npm run test && npm run build`
Expected: all clean.

- [ ] **Step 6: Load it**

`/clearing-house/directory`: type `SE89-AURORA-1001` → "Alice Andersson at Aurora
Bank". Type `IT60-VERDE-2003` → "Bianca Belli at Banca Verde". Type
`NOBODY-0001` → the 404 surfaces as a plain miss rather than a spinner. Confirm
one request per settled value rather than one per keystroke (Network tab), and
that Directory is in the sidebar.

- [ ] **Step 7: Commit**

```bash
git add -A web/src
git commit -m "feat(web): let the clearing house answer which bank holds an address"
```

---

## Task 11: The bank's home is its customers, and its payments are its own

Two things. A back office opens on its customers, not on a thin card of internal
account ids — that plus the tab-strip promotion in Task 6 is most of the persona.
And the bank gains one screen it could not have had before 6a: its **own** legs,
the payments it sent and received and nothing else. The component that renders
them already exists; what is new is a listener that withholds everybody else's
rows.

**Files:**
- Create: `web/src/components/payments-table.tsx`, `web/src/app/bank/[pid]/payments/page.tsx`
- Modify: `web/src/app/clearing-house/payments/page.tsx` (use the extracted table)
- Modify: `web/src/app/bank/[pid]/page.tsx` (rewrite)
- Modify: `web/src/lib/identity.ts`, `web/src/lib/identity.test.ts` (the nav entry)
- Modify: `web/src/lib/api/{endpoints,query-keys,hooks}.ts`
- Modify: `web/src/app/bank/[pid]/deposit-accounts/[did]/page.tsx` (back-link)
- Delete: `web/src/app/bank/[pid]/deposit-accounts/page.tsx`

**Interfaces:**
- Consumes: `bank()` (operator.ts); `useDepositAccounts`, `useDepositBalance`,
  `useTotals`, `useReserve`, `useAssetLookup`, `OpenDepositAccountForm`,
  `DataTable`.
- Produces:
  - `function bankPayments(pid: string): Promise<Payment[]>` (endpoints)
  - `qk.bankPayments(pid)` (query keys), `useBankPayments(pid)` (hooks)
  - `<PaymentsTable rows isLoading onRowClick? />`

- [ ] **Step 1: Add a bank's own payments to the data layer**

`web/src/lib/api/endpoints.ts` — in the `--- Payment: payments ---` section,
beside `listPayments`:

```ts
// One bank's own legs: the payments it sent and the ones it received, and
// nothing else. Same pattern as the clearing house's GET /payments, different
// operator, different answer — the port is the caller identity the single server
// did not have, which is why it listed every payment to everybody.
//
// GET /payments/{id} on a bank answers 404, not 403, for a payment it is not
// party to: a 403 would confirm that the id names something real.
export function bankPayments(pid: string): Promise<Payment[]> {
  return request("GET", bank(pid, "/payments"));
}
```

`web/src/lib/api/query-keys.ts` — with the other participant-nested keys:

```ts
  // Nested under the participant, because these are that bank's legs and not
  // the network's list filtered.
  bankPayments: (pid: string) => ["participants", pid, "payments"] as const,
```

`web/src/lib/api/hooks.ts` — beside `usePayments`:

```ts
export function useBankPayments(pid: string) {
  return useQuery({
    queryKey: qk.bankPayments(pid),
    queryFn: () => api.bankPayments(pid),
    enabled: pid !== "",
  });
}
```

and add `qc.invalidateQueries({ queryKey: ["participants"] })` — already in
`invalidateNetwork`, so a settled or initiated payment refreshes a bank's list
too. Confirm that line is present rather than adding a second.

- [ ] **Step 2: Extract the payments table**

Create `web/src/components/payments-table.tsx`, lifting `PaymentAmountCell` and
the `columns` array out of `web/src/app/clearing-house/payments/page.tsx`
unchanged:

```tsx
"use client";

import { ArrowRight } from "lucide-react";

import { DataTable, type Column } from "@/components/data-table";
import { EnumBadge } from "@/components/enum-badge";
import { IdText } from "@/components/id-text";
import { Money, UnresolvedAmount } from "@/components/money";
import { useAssetLookup } from "@/lib/api/hooks";
import type { Payment } from "@/lib/types";

// A payment carries its own asset code, fixed by its scheme. The scale that code
// implies lives only in the network-wide asset list, which every caller shares
// through useAssetLookup (one GET /assets). Until the code resolves there is no
// scale to render at, and guessing one is the bug the whole asset dimension
// exists to prevent — so withhold the number instead.
function PaymentAmountCell({ payment }: { payment: Payment }) {
  const { byCode, isLoading } = useAssetLookup();
  const asset = byCode.get(payment.asset);
  if (!asset) {
    return (
      <UnresolvedAmount
        code={payment.asset}
        isLoading={isLoading}
        className="ml-auto block text-right"
      />
    );
  }
  return <Money amount={payment.amount} asset={asset} />;
}

const columns: Column<Payment>[] = [
  { key: "id", header: "ID", render: (p) => <IdText id={p.id} /> },
  { key: "scheme", header: "Scheme", render: (p) => p.scheme },
  {
    key: "flow",
    header: "Debtor → Creditor",
    render: (p) => (
      <span className="flex items-center gap-1.5">
        <IdText id={p.debtor.participant} />
        <ArrowRight className="size-3.5 text-muted-foreground" />
        <IdText id={p.creditor.participant} />
      </span>
    ),
  },
  { key: "amount", header: "Amount", align: "right", render: (p) => <PaymentAmountCell payment={p} /> },
  { key: "status", header: "Status", render: (p) => <EnumBadge value={p.status} /> },
];

// Shared by the clearing house's list of every payment and a bank's list of its
// own legs. The same rows either way; what differs is which listener answered,
// and therefore how many there are.
//
// onRowClick is optional because only the clearing house has a payment detail
// page. A bank's rows go nowhere: the spec gives it a list and no drill-down, and
// a row that looks clickable and is not would be worse than one that does not.
export function PaymentsTable({
  rows,
  isLoading,
  onRowClick,
  empty,
}: {
  rows: Payment[] | undefined;
  isLoading: boolean;
  onRowClick?: (p: Payment) => void;
  empty: string;
}) {
  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(p) => p.id}
      isLoading={isLoading}
      onRowClick={onRowClick}
      empty={empty}
    />
  );
}
```

Then rewrite `web/src/app/clearing-house/payments/page.tsx` to use it: delete
`PaymentAmountCell`, the `columns` array and the now-unused `ArrowRight`,
`DataTable`, `Column`, `EnumBadge`, `IdText`, `Money`, `UnresolvedAmount`,
`useAssetLookup` and `Payment` imports, and replace the `<DataTable …/>` with:

```tsx
        <PaymentsTable
          rows={data}
          isLoading={isLoading}
          onRowClick={(p) => router.push(`/clearing-house/payments/${p.id}`)}
          empty="No payments yet. Initiate one between two funded participants."
        />
```

- [ ] **Step 3: Add the bank's payments screen, nav first**

Edit `web/src/lib/identity.test.ts`'s bank-nav test, replacing the loose
assertion with the exact list now that it is stable:

```ts
  it("scopes every bank entry to the bank's own pid", () => {
    const nav = navFor({ persona: "bank", pid: "bank_1" });
    expect(nav.map((n) => n.href)).toEqual([
      "/bank/bank_1",
      "/bank/bank_1/payments",
      "/bank/bank_1/ledger",
      "/bank/bank_1/transactions",
      "/bank/bank_1/facilities",
      "/bank/bank_1/audit",
      "/bank/bank_1/deposit-audit",
    ]);
    for (const item of nav) {
      expect(item.href.startsWith("/bank/bank_1")).toBe(true);
    }
  });
```

Run `cd web && npx vitest run src/lib/identity.test.ts` — expect FAIL. Then edit
`web/src/lib/identity.ts`, replacing the `// Payments … arrives in Task 11`
comment with:

```ts
        { href: `${base}/payments`, label: "Payments", icon: ArrowLeftRight },
```

Re-run: `identity.test.ts` passes and `nav-integrity.test.ts` now fails, because
the page does not exist. That is the test doing its job. Create
`web/src/app/bank/[pid]/payments/page.tsx`:

```tsx
"use client";

import { useParams } from "next/navigation";

import { PageHeader } from "@/components/page-header";
import { ErrorState } from "@/components/error-state";
import { PaymentsTable } from "@/components/payments-table";
import { useBankPayments } from "@/lib/api/hooks";

// A bank's own legs — the payments it sent and the ones it received, and nothing
// else. This screen could not have existed before the operator split: the single
// server's GET /payments listed every payment in the network to every caller,
// because narrowing needs a caller identity and there was none. The port is that
// identity now.
//
// There is no drill-down. A payment's detail page is the clearing house's, and
// following one would walk the back office out of its own console.
export default function BankPaymentsPage() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const { data, isLoading, error, refetch } = useBankPayments(pid);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Payments"
        hint="payment-lifecycle"
        description="The payments this bank is a party to, as debtor or as creditor. Another bank's customers, counterparties and amounts are not here — and are not reachable from here by editing a URL, because this listener has no route that names another bank."
      />
      {error ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : (
        <PaymentsTable
          rows={data}
          isLoading={isLoading}
          empty="No payments yet. This bank has neither sent nor received one."
        />
      )}
    </div>
  );
}
```

Re-run `cd web && npm run test` — both green.

- [ ] **Step 4: Rewrite the bank's home**

Today's overview is a thin card of internal account ids, and the deposit-account
list is a second route showing the table the home should be. They fold together:
a back office opens on its customers, not on its own chart of accounts, which is
one click away under General ledger.

Replace `web/src/app/bank/[pid]/page.tsx`. `DepositAccountRow` is taken verbatim
from `deposit-accounts/page.tsx` with its href re-pointed; the totals and reserve
cards are today's overview:

```tsx
"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { ChevronRight } from "lucide-react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { IdText } from "@/components/id-text";
import { EnumBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { Money } from "@/components/money";
import { Skeleton } from "@/components/ui/skeleton";
import { OpenDepositAccountForm } from "@/components/forms/open-deposit-account-form";
import {
  useAssetLookup,
  useDepositAccounts,
  useDepositBalance,
  useReserve,
  useTotals,
} from "@/lib/api/hooks";
import type { DepositAccount } from "@/lib/types";

function DepositAccountRow({ pid, account }: { pid: string; account: DepositAccount }) {
  const { data } = useDepositBalance(pid, account.id);
  const { byCode } = useAssetLookup();
  const asset = byCode.get(account.asset);
  const iban = account.identifiers.find((i) => i.scheme === "IBAN");
  return (
    <Link
      href={`/bank/${pid}/deposit-accounts/${account.id}`}
      className="flex items-center justify-between gap-3 px-3 py-2.5 transition-colors hover:bg-muted/50"
    >
      <span className="flex min-w-0 items-center gap-2">
        <span className="truncate text-sm font-medium">{account.name}</span>
        <EnumBadge value={account.status} />
        {iban ? (
          <span className="font-mono text-xs text-muted-foreground">{iban.value}</span>
        ) : (
          <IdText id={account.id} />
        )}
      </span>
      <span className="flex items-center gap-3">
        <span className="text-right text-sm font-medium">
          {asset ? (
            <Money amount={data?.available ?? 0} asset={asset} />
          ) : (
            <Skeleton className="ml-auto h-4 w-16" />
          )}
          <span className="block text-xs font-normal text-muted-foreground">available</span>
        </span>
        <ChevronRight className="size-4 text-muted-foreground" />
      </span>
    </Link>
  );
}

// A back office opens on its customers. The internal-accounts card of raw ids
// that used to be here is gone: the chart of accounts is one click away under
// General ledger, and a bank's home is the people it holds money for.
export default function BankHome() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const accounts = useDepositAccounts(pid);
  const { data: totals } = useTotals(pid);
  const { data: reserve } = useReserve(pid);
  const { byCode } = useAssetLookup();

  return (
    <div className="space-y-6">
      <div className="grid gap-4 sm:grid-cols-2">
        <Card size="sm">
          <CardContent>
            <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
              Reserves at the central bank
              <Hint id="reserve-account" />
            </p>
            <div className="mt-1 space-y-0.5 text-xl font-semibold tabular-nums">
              {(reserve ?? []).map((r) => {
                const asset = byCode.get(r.asset);
                return asset ? (
                  <p key={r.asset}>
                    <Money amount={r.reserve} asset={asset} />
                  </p>
                ) : (
                  <Skeleton key={r.asset} className="h-6 w-24" />
                );
              })}
            </div>
          </CardContent>
        </Card>
        <Card size="sm">
          <CardContent>
            <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
              Customer deposits
              <Hint id="derived-balance" />
            </p>
            <div className="mt-1 space-y-0.5 text-xl font-semibold tabular-nums">
              {(totals ?? []).map((t) => {
                const asset = byCode.get(t.asset);
                return asset ? (
                  <p key={t.asset}>
                    <Money amount={t.deposits} asset={asset} />
                    <span className="ml-2 text-xs font-normal text-muted-foreground">
                      less <Money amount={t.overdrafts} asset={asset} /> drawn
                    </span>
                  </p>
                ) : (
                  <Skeleton key={t.asset} className="h-6 w-24" />
                );
              })}
            </div>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader className="flex flex-row items-center justify-between">
          <CardTitle className="flex items-center gap-1.5 text-base">
            Customer accounts
            <Hint id="balance-available" />
          </CardTitle>
          <OpenDepositAccountForm pid={pid} />
        </CardHeader>
        <CardContent>
          {accounts.error ? (
            <ErrorState error={accounts.error} onRetry={() => accounts.refetch()} />
          ) : accounts.isLoading ? (
            <Skeleton className="h-32 w-full" />
          ) : accounts.data && accounts.data.length > 0 ? (
            <div className="divide-y rounded-lg border">
              {accounts.data.map((a) => (
                <DepositAccountRow key={a.id} pid={pid} account={a} />
              ))}
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">
              No deposit accounts yet. Open one, then fund it to start the money loop.
            </p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
```

- [ ] **Step 5: Retire the list route**

```bash
cd /Users/raphaelgruber/Git/cbs-account-addressing
git rm web/src/app/bank/'[pid]'/deposit-accounts/page.tsx
```

The bank's home is that list now. Re-point the one back-link that pointed at it —
`web/src/app/bank/[pid]/deposit-accounts/[did]/page.tsx:280`:

```tsx
  const back = `/bank/${pid}`;
```

and change that page's back-link **label** from "Deposit accounts" to "Customer
accounts" (two occurrences, in the error branch and the normal branch).
`accounts/[aid]/page.tsx:82` links to a *detail* page and needs no change.

Nothing was in the nav pointing at the retired route (Task 2 dropped the entry),
so `nav-integrity.test.ts` has nothing to say here — but run it, because that is
what would catch a missed one.

- [ ] **Step 6: Run the gate**

Run: `cd web && npm run typecheck && npm run lint && npm run test && npm run build`
Expected: all clean.

- [ ] **Step 7: Load it, and prove the narrowing**

With `make dev`, open `/bank/<pid>`: reserves, customer deposits, and the customer
list with IBANs, statuses and available balances. Click through to an account and
back — the back-link should say "Customer accounts" and land on the home. Confirm
the sidebar no longer offers "Deposit accounts" and does offer "Payments".

Then the part that matters, which no test in `web/` can see:

```bash
curl -s localhost:3000/api/clearing-house/payments | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))'
curl -s localhost:3000/api/bank/bank_1/payments | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))'
```

Expected: the second is strictly smaller than the first on the seeded dataset.
Compare against `/bank/bank_1/payments` in the browser and against
`/clearing-house/payments`, and confirm every row on the bank's screen has
`bank_1` on one end. **A bank's list equal in length to the clearing house's
means the screen is reading the wrong listener** — which is the failure this step
exists to catch.

- [ ] **Step 8: Commit**

```bash
git add -A web/src
git commit -m "feat(web): open a back office on its customers, and show it only its own legs"
```

---

## Task 12: The customer's shell, overview and activity

A retail bank app is a content column, not a console — so this shell has **no
left panel**, a top tab strip, and a `max-w-2xl` column, which is what makes the
switch unmistakable. The concepts rail stays, in this shell as in every other: a
real retail app has no concepts rail, so this costs a little realism and buys the
thing the repository exists for. A customer screen that cannot explain
`balance-available` is a worse trade.

A customer is not a backend of their own. Every call on these screens goes to
their bank's listener, which is what a retail app does.

**Files:**
- Create: `web/src/components/shell/customer-shell.tsx`
- Create: `web/src/app/customer/[pid]/[did]/page.tsx`, `web/src/app/customer/[pid]/[did]/activity/page.tsx`
- Modify: `web/src/lib/identity.ts`, `web/src/lib/identity.test.ts` (the customer nav)
- Modify: `web/src/components/shell/persona-shell.tsx`
- Modify: `web/src/components/statement/statement-table.tsx` (a `retail` variant)

**Interfaces:**
- Consumes: `useDepositAccount`, `useDepositBalance`, `useStatement`,
  `useParticipant`, `useAssetLookup`; `ShellFrame`, `Topbar`, `accentFor`.
- Produces: `<CustomerShell pid did>`; `StatementTable`'s `retail?: boolean`.

- [ ] **Step 1: Add the customer's nav, test first**

Edit `web/src/lib/identity.test.ts`: replace the "has no customer nav" case with

```ts
  // Send arrives in Task 13 and is inserted between these two, in the order the
  // tab strip shows them.
  it("gives a customer the screens they have, all under their own account", () => {
    const nav = navFor({ persona: "customer", pid: "bank_1", did: "dep_9" });
    expect(nav.map((n) => n.href)).toEqual([
      "/customer/bank_1/dep_9",
      "/customer/bank_1/dep_9/activity",
    ]);
  });
```

The "exactly one exact match" case already loops over `ALL`, which includes the
customer, and its `if (nav.length === 0) continue;` guard now stops applying —
that is intended.

- [ ] **Step 2: Run it to verify it fails**

Run: `cd web && npx vitest run src/lib/identity.test.ts`
Expected: FAIL — `navFor` returns `[]` for a customer.

- [ ] **Step 3: Fill in the customer's nav**

Edit `web/src/lib/identity.ts`, replacing the `case "customer"` body:

```ts
    case "customer": {
      const base = `/customer/${identity.pid}/${identity.did}`;
      return [
        { href: base, label: "Account", icon: Wallet, exact: true },
        { href: `${base}/activity`, label: "Activity", icon: Receipt },
      ];
    }
```

Add `Receipt` and `Wallet` to the lucide import.

Run `cd web && npx vitest run src/lib/identity.test.ts` — expect PASS.
`nav-integrity.test.ts` will now fail until Step 5 creates the pages. That is the
test doing its job.

- [ ] **Step 4: Give StatementTable a retail framing**

`StatementTable` renders an `AccountRef` per contra leg and an expandable
"Underlying GL transaction" panel listing every GL entry — the bank's chart of
accounts, linking into `/bank/[pid]/accounts/[aid]`. Reused whole in the
customer's Activity screen it leaks the back office into the retail view and
navigates out of the persona. Edit
`web/src/components/statement/statement-table.tsx`:

- Add to the props, after `amountHintId`:

```tsx
  // Retail framing: no contra column, no expandable GL detail, no reconciliation
  // banner. A customer's statement is dates, descriptions, amounts and a running
  // balance; the double entry behind it is the bank's business, and linking to it
  // would navigate out of the persona.
  retail?: boolean;
```

  with `retail = false` in the destructuring.
- Wrap `<TableHead>Contra</TableHead>` and the `<TableCell><ContraCell …/></TableCell>`
  in `{!retail && (…)}`.
- Make the row non-interactive under `retail`:
  `className={cn(!retail && "cursor-pointer")}` and
  `onClick={retail ? undefined : () => setOpenTx((cur) => (cur === row.txId ? null : row.txId))}`.
- Guard the expansion row: `{!retail && openTx === row.txId && (…)}`.
- The expansion's `<TableCell colSpan={5}>` becomes `colSpan={retail ? 4 : 5}` —
  unreachable under `retail`, but wrong is wrong.
- Guard the whole `{book != null && (reconciles ? … : …)}` block with `!retail &&`.
- The empty state is back-office copy ("Fund the account or post one"). Under
  `retail` render:

```tsx
        No activity yet. Money arriving or leaving this account will appear here.
```

`cn` is already imported. Nothing else changes, and every existing caller keeps
today's behaviour because `retail` defaults to false.

- [ ] **Step 5: Write the customer shell and its two screens**

Create `web/src/components/shell/customer-shell.tsx`:

```tsx
"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { cn } from "@/lib/utils";
import { accentFor } from "@/lib/accent";
import { navFor, type Identity } from "@/lib/identity";
import { ShellFrame } from "./shell-frame";
import { Topbar } from "./topbar";

// A retail bank app is a content column, not a console: no left panel, a top tab
// strip, and a narrow column. That is what makes the switch unmistakable — and it
// is a shell rather than a variant of the console because the layouts genuinely
// differ, not because a flag was cheaper.
//
// The concepts rail stays. A real retail app has no concepts rail, so this costs
// a little realism and buys the thing the repository exists for; the max-w-2xl
// constraint is on the content column, not the viewport, so the panel keeps its
// own width and its collapse strip.
//
// The accent is the customer's *bank's*. You are a customer of Aurora, and the
// screen should say so without a label.
export function CustomerShell({
  pid,
  did,
  children,
}: {
  pid: string;
  did: string;
  children: React.ReactNode;
}) {
  const identity: Identity = { persona: "customer", pid, did };
  const pathname = usePathname();
  const items = navFor(identity);

  return (
    <ShellFrame accent={accentFor(identity)} topbar={<Topbar />}>
      <div className="mx-auto w-full max-w-2xl space-y-6">
        <nav className="flex gap-1 border-b">
          {items.map((t) => {
            const active = t.exact ? pathname === t.href : pathname.startsWith(t.href);
            return (
              <Link
                key={t.href}
                href={t.href}
                className={cn(
                  "-mb-px border-b-2 px-3 py-2 text-sm font-medium transition-colors",
                  active
                    ? "border-foreground text-foreground"
                    : "border-transparent text-muted-foreground hover:text-foreground",
                )}
              >
                {t.label}
              </Link>
            );
          })}
        </nav>
        {children}
      </div>
    </ShellFrame>
  );
}
```

Edit `web/src/components/shell/persona-shell.tsx`, adding a case before the
default:

```tsx
    case "customer":
      return (
        <CustomerShell pid={identity.pid} did={identity.did}>
          {children}
        </CustomerShell>
      );
```

with the import.

Create `web/src/app/customer/[pid]/[did]/page.tsx`:

```tsx
"use client";

import { useParams } from "next/navigation";
import { Snowflake } from "lucide-react";

import { Card, CardContent } from "@/components/ui/card";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Money } from "@/components/money";
import { EnumBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useAssetLookup,
  useDepositAccount,
  useDepositBalance,
  useParticipant,
} from "@/lib/api/hooks";

// What a customer sees of their own account. No GL account, no product id, no
// pricing source, no internal ids — those are the bank's business and live in
// the back office. Every call here goes to their bank's listener: a customer is
// a view onto a bank, not an institution with a backend of its own.
export default function CustomerOverview() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const did = typeof params.did === "string" ? params.did : "";

  const { data: account, isLoading, error, refetch } = useDepositAccount(pid, did);
  const { data: balance } = useDepositBalance(pid, did);
  const { data: bank } = useParticipant(pid);
  const { byCode } = useAssetLookup();
  const asset = account ? byCode.get(account.asset) : undefined;
  const iban = account?.identifiers.find((i) => i.scheme === "IBAN");

  if (error) return <ErrorState error={error} onRetry={() => refetch()} />;
  if (isLoading || !account || !asset) return <Skeleton className="h-64 w-full" />;

  // The headroom below zero, which a customer thinks of as part of what they can
  // spend and the bank does not: the available balance already includes it.
  const headroom = account.overdraftLimit;

  return (
    <div className="space-y-5">
      <div className="space-y-1">
        <h1 className="text-2xl font-semibold tracking-tight">{account.name}</h1>
        <p className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
          {bank?.name}
          {iban && <span className="font-mono">{iban.value}</span>}
          <EnumBadge value={account.status} />
        </p>
      </div>

      {account.status === "Frozen" && (
        <Alert>
          <Snowflake className="size-4" />
          <AlertTitle>This account is frozen</AlertTitle>
          <AlertDescription>
            No money can leave it until your bank unfreezes it. Money can still
            arrive — an incoming payment is a credit, and the block is on debits.
          </AlertDescription>
        </Alert>
      )}

      <Card>
        <CardContent className="space-y-4">
          <div>
            <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
              Available
              <Hint id="balance-available" />
            </p>
            <p className="text-3xl font-semibold tabular-nums">
              <Money amount={balance?.available ?? 0} asset={asset} />
            </p>
          </div>
          <div className="grid grid-cols-2 gap-4 border-t pt-4">
            <div>
              <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
                Balance
                <Hint id="balance-book" />
              </p>
              <p className="text-lg font-medium tabular-nums">
                <Money amount={balance?.book ?? 0} asset={asset} />
              </p>
            </div>
            <div>
              <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
                On hold
                <Hint id="balance-holds" />
              </p>
              <p className="text-lg font-medium tabular-nums">
                <Money amount={balance?.holds ?? 0} asset={asset} />
              </p>
            </div>
          </div>
          {headroom > 0 && (
            <p className="flex items-center gap-1.5 border-t pt-4 text-sm text-muted-foreground">
              Includes an arranged overdraft of <Money amount={headroom} asset={asset} />
              <Hint id="overdraft-interest" />
            </p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
```

Note the hint key is **`overdraft-interest`**. There is no `overdraft` key; `tsc`
would catch it, but knowing saves a round trip.

Create `web/src/app/customer/[pid]/[did]/activity/page.tsx`:

```tsx
"use client";

import { useParams } from "next/navigation";

import { ErrorState } from "@/components/error-state";
import { Skeleton } from "@/components/ui/skeleton";
import { StatementTable } from "@/components/statement/statement-table";
import { useAssetLookup, useDepositAccount, useStatement } from "@/lib/api/hooks";

// The same projection the back office reads, framed for the person whose money
// it is: no contra accounts, no double entry, no reconciliation check. The
// statement is theirs; the bookkeeping behind it is the bank's.
export default function CustomerActivity() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const did = typeof params.did === "string" ? params.did : "";

  const { data: account, error: accountError } = useDepositAccount(pid, did);
  const { byCode } = useAssetLookup();
  const asset = account ? byCode.get(account.asset) : undefined;
  const { rows, book, isLoading, error, refetch } = useStatement(
    pid,
    did,
    account?.glAccount ?? "",
  );

  if (accountError || error) {
    return <ErrorState error={accountError ?? error} onRetry={() => refetch()} />;
  }
  if (!account || !asset || isLoading) return <Skeleton className="h-64 w-full" />;

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold tracking-tight">Activity</h1>
      <StatementTable
        retail
        rows={rows}
        book={book}
        glAccount={account.glAccount}
        pid={pid}
        asset={asset}
      />
      <p className="text-xs text-muted-foreground">
        Money held for a card authorisation is not here — it reduces what you can
        spend without moving your balance, and only appears once it is taken.
      </p>
    </div>
  );
}
```

- [ ] **Step 6: Run the gate**

Run: `cd web && npm run typecheck && npm run lint && npm run test && npm run build`
Expected: all clean, `nav-integrity.test.ts` included now that both pages exist.

- [ ] **Step 7: Load it**

Pick a customer from the lobby or the picker. Confirm: **no left nav**, a narrow
column, a top tab strip, the concepts rail present and expandable, and the
account carrying its bank's accent — switch to that bank's console and back to
check the accent is the same colour.

Then walk the seeded cast, which is where the persona earns its keep:

- **Alice Andersson** (`SE89-AURORA-1001`) — active, with holds; the on-hold
  figure is non-zero and Available is below Balance by exactly that.
- **Bruno Bianchi** (`IT60-VERDE-2001`) — an arranged overdraft; the headroom
  line appears and says 500.00.
- **Bianca Belli** (`IT60-VERDE-2003`) — frozen; the alert appears.
- **Annie Ahlberg** (`SE89-AURORA-1003`) — dormant.
- **Closed Account** (`SE89-AURORA-1005`) — closed, with the retail empty state.

On Activity, confirm there is **no Contra column**, rows do not expand on click,
and there is no reconciliation banner — then open the same account's statement in
the back office (`/bank/<pid>/deposit-accounts/<did>/statement`) and confirm all
three are still there. That contrast is the variant working.

Finally click a `?` to confirm the concepts panel opens from inside the customer
shell, and follow one `[[wiki-link]]` in it.

- [ ] **Step 8: Commit**

```bash
git add -A web/src
git commit -m "feat(web): give a customer a point of view"
```

---

## Task 13: Sending money to an IBAN

The screen that required sub-project 5, reshaped by 6a. A payee is entered as an
IBAN and resolved live — through **their own bank's** listener, never the
clearing house's, because a retail client has no CSM connection in the real thing
either. Submission is `POST /payments` on that bank, answering **202 Accepted**
with a `{paymentId}` whose outcome the form reads back.

The asynchrony is not pretend. Today's handler is synchronous; 7b converts it,
because a real CSM answers with a `pacs.002` later and not by return value. A
form built around a synchronous "payment created" response would be rewritten
then. This one will not be, and the cost is one extra request.

**Files:**
- Modify: `web/src/lib/types.ts`, `web/src/lib/api/{endpoints,query-keys,hooks}.ts`
- Create: `web/src/app/customer/[pid]/[did]/send/page.tsx`
- Modify: `web/src/lib/identity.ts`, `web/src/lib/identity.test.ts`

**Interfaces:**
- Consumes: `bank()` (operator.ts); `useDepositAccount`, `useDepositBalance`,
  `useSchemes`, `useAssetLookup`, `useParticipants`, `useDebouncedValue`
  (Task 10), `MoneyInput`, `Money`, `describeError`.
- Produces:
  - `interface AcceptedPayment { paymentId: string }`
  - `function resolveIdentifierAtBank(pid, scheme, value): Promise<DirectoryEntry>`
  - `function bankPayment(pid: string, payid: string): Promise<Payment>`
  - `function submitPayment(pid: string, body: InitiatePaymentRequest): Promise<AcceptedPayment>`
  - `useBankDirectory(pid, scheme, value)`, `useBankPayment(pid, payid)`,
    `useSubmitPayment(pid)`

- [ ] **Step 1: Add the customer's half of the data layer**

`web/src/lib/types.ts` — beside `Payment`:

```ts
// What a bank answers a customer's instruction with: an identifier to ask about,
// not an outcome. See api/handlers_bank_payment.go's acceptedPaymentDTO — and the
// reason it is 202 and not 201, which is that 7b makes the wait real.
export interface AcceptedPayment {
  paymentId: string;
}
```

`web/src/lib/api/endpoints.ts` — in the `--- Directory ---` section, beside
`resolveIdentifierAtCsm`:

```ts
// The same question asked of a customer's own bank. A bank is a scheme
// participant with directory access, and validating a payee's address before
// accepting an instruction is what it uses that for. This is the one a customer's
// browser may call; the CSM's is an operator's.
export function resolveIdentifierAtBank(
  pid: string,
  scheme: string,
  value: string,
): Promise<DirectoryEntry> {
  return request("GET", bank(pid, `/directory${qs({ scheme, value })}`));
}
```

and in `--- Payment: payments ---`, beside `bankPayments`:

```ts
export function bankPayment(pid: string, payid: string): Promise<Payment> {
  return request("GET", bank(pid, `/payments/${payid}`));
}

// A customer's instruction, submitted to their own bank. The answer is 202 with
// an identifier rather than the payment: the outcome comes from asking again.
export function submitPayment(
  pid: string,
  body: InitiatePaymentRequest,
): Promise<AcceptedPayment> {
  return request("POST", bank(pid, "/payments"), body);
}
```

Add `AcceptedPayment` to the type import.

`web/src/lib/api/query-keys.ts`:

```ts
  bankPayment: (pid: string, payid: string) =>
    ["participants", pid, "payments", payid] as const,
  bankDirectory: (pid: string, scheme: string, value: string) =>
    ["participants", pid, "directory", scheme, value] as const,
```

`web/src/lib/api/hooks.ts`:

```ts
// A customer's payee lookup, on their own bank's listener. `retry: false` because
// a 404 here is an answer — nobody holds that IBAN — and retrying it three times
// only delays telling them so.
export function useBankDirectory(pid: string, scheme: string, value: string) {
  return useQuery({
    queryKey: qk.bankDirectory(pid, scheme, value),
    queryFn: () => api.resolveIdentifierAtBank(pid, scheme, value),
    enabled: pid !== "" && scheme !== "" && value !== "",
    retry: false,
  });
}

// The second half of a 202: ask about the identifier you were given. Today the
// answer is already final; 7b makes the wait real, and a client shaped this way
// will not need rewriting when it does.
export function useBankPayment(pid: string, payid: string) {
  return useQuery({
    queryKey: qk.bankPayment(pid, payid),
    queryFn: () => api.bankPayment(pid, payid),
    enabled: pid !== "" && payid !== "",
  });
}

export function useSubmitPayment(pid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: import("../types").InitiatePaymentRequest) =>
      api.submitPayment(pid, body),
    onSuccess: () => invalidateNetwork(qc),
  });
}
```

- [ ] **Step 2: Add Send to the customer's nav, test first**

Edit `web/src/lib/identity.test.ts`, extending the customer expectation to the
final three in the order the tab strip shows them:

```ts
    expect(nav.map((n) => n.href)).toEqual([
      "/customer/bank_1/dep_9",
      "/customer/bank_1/dep_9/send",
      "/customer/bank_1/dep_9/activity",
    ]);
```

Run `cd web && npx vitest run src/lib/identity.test.ts` — expect FAIL. Then edit
`web/src/lib/identity.ts`'s `case "customer"`, inserting between Account and
Activity:

```ts
        { href: `${base}/send`, label: "Send", icon: Send },
```

and adding `Send` to the lucide import. Re-run: `identity.test.ts` passes,
`nav-integrity.test.ts` fails until Step 3.

- [ ] **Step 3: Write the send screen**

Create `web/src/app/customer/[pid]/[did]/send/page.tsx`:

```tsx
"use client";

import { useState } from "react";
import { useParams } from "next/navigation";
import Link from "next/link";
import { toast } from "sonner";

import { Card, CardContent } from "@/components/ui/card";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { FieldLabel } from "@/components/field-label";
import { EnumBadge } from "@/components/enum-badge";
import { MoneyInput, Money } from "@/components/money";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import {
  useAssetLookup,
  useBankDirectory,
  useBankPayment,
  useDepositAccount,
  useDepositBalance,
  useParticipants,
  useSchemes,
  useSubmitPayment,
} from "@/lib/api/hooks";
import { useDebouncedValue } from "@/lib/use-debounced-value";
import { describeError } from "@/lib/api/errors";

// A retail "send money" is a SEPA credit transfer: a push scheme needing no
// mandate, addressed by IBAN. Naming it here rather than offering a scheme picker
// is the point — a customer picks a payee, not a clearing arrangement.
const SEND_SCHEME = "sepa.ct";

export default function CustomerSend() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const did = typeof params.did === "string" ? params.did : "";

  const { data: account, error: accountError } = useDepositAccount(pid, did);
  const { data: balance } = useDepositBalance(pid, did);
  const { data: schemes } = useSchemes();
  const { byCode } = useAssetLookup();
  const submit = useSubmitPayment(pid);

  const [iban, setIban] = useState("");
  const [amount, setAmount] = useState<number | null>(null);
  const [reference, setReference] = useState("");
  // The identifier the bank accepted. Holding it is what makes this form the
  // shape 7b needs: the answer to "did it work?" is a second request, not a
  // return value.
  const [acceptedId, setAcceptedId] = useState<string | null>(null);

  // Resolved live as you type, settled first so a keystroke is not a request.
  // Through the customer's own bank — a retail client has no CSM connection.
  const settledIban = useDebouncedValue(iban.trim(), 350);
  const payee = useBankDirectory(pid, settledIban ? "IBAN" : "", settledIban);

  const asset = account ? byCode.get(account.asset) : undefined;
  const scheme = schemes?.find((s) => s.id === SEND_SCHEME);
  const frozen = account?.status === "Frozen";
  const closed = account?.status === "Closed";
  const ownIban = account?.identifiers.find((i) => i.scheme === "IBAN");

  if (accountError) return <ErrorState error={accountError} />;
  if (!account || !asset) return <Skeleton className="h-64 w-full" />;

  // The scheme settles in one asset and this account holds one; a mismatch is not
  // a form error the customer can fix, so it is stated rather than hidden.
  const assetMismatch = scheme != null && scheme.asset !== account.asset;
  const payingSelf = payee.data?.account === did && payee.data?.participant === pid;

  const canSend =
    !frozen &&
    !closed &&
    !assetMismatch &&
    payee.data != null &&
    !payingSelf &&
    amount != null &&
    amount > 0;

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!canSend || !payee.data) return;
    try {
      const accepted = await submit.mutateAsync({
        scheme: SEND_SCHEME,
        // Routing is by id, which is why the IBAN had to be resolved. The
        // identifier is quoted so the payment records the address it was reached
        // by; initiation would back-fill it either way.
        debtor: { participant: pid, account: did },
        creditor: {
          participant: payee.data.participant,
          account: payee.data.account,
          identifier: payee.data.identifier,
        },
        amount: amount!,
        description: reference.trim() || undefined,
      });
      setAcceptedId(accepted.paymentId);
      setIban("");
      setAmount(null);
      setReference("");
    } catch (err) {
      toast.error(describeError(err));
    }
  }

  return (
    <div className="space-y-5">
      <h1 className="text-2xl font-semibold tracking-tight">Send money</h1>

      {frozen && (
        <Alert>
          <AlertTitle>This account is frozen</AlertTitle>
          <AlertDescription>
            Nothing can leave it until your bank unfreezes it. Money can still
            arrive.
          </AlertDescription>
        </Alert>
      )}
      {closed && (
        <Alert>
          <AlertTitle>This account is closed</AlertTitle>
          <AlertDescription>Closed is terminal — it cannot send or receive.</AlertDescription>
        </Alert>
      )}
      {assetMismatch && (
        <Alert>
          <AlertTitle>Nothing to send with</AlertTitle>
          <AlertDescription>
            This account holds {account.asset} and the transfer scheme settles in{" "}
            {scheme?.asset}. A payment settles in one asset, so there is no such
            transfer to make.
          </AlertDescription>
        </Alert>
      )}

      {acceptedId && (
        <Outcome pid={pid} did={did} payid={acceptedId} onDismiss={() => setAcceptedId(null)} />
      )}

      <Card>
        <CardContent>
          <form onSubmit={onSubmit} className="space-y-4">
            <div className="space-y-1.5">
              <FieldLabel htmlFor="send-iban" hint="account-addressing" required>
                Pay to (IBAN)
              </FieldLabel>
              <Input
                id="send-iban"
                value={iban}
                placeholder={ownIban ? ownIban.value.replace(/\d{4}$/, "0000") : "SE89-…"}
                className="font-mono"
                disabled={frozen || closed}
                onChange={(e) => setIban(e.target.value)}
              />
              <PayeeLine
                query={settledIban}
                isLoading={payee.isLoading}
                error={payee.error}
                name={payee.data?.name}
                bank={payee.data?.participant}
                payingSelf={payingSelf}
              />
            </div>

            <div className="space-y-1.5">
              <FieldLabel htmlFor="send-amount" required>
                Amount
              </FieldLabel>
              <MoneyInput
                id="send-amount"
                value={amount}
                onChange={setAmount}
                asset={asset}
                disabled={frozen || closed}
              />
              <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
                Available <Money amount={balance?.available ?? 0} asset={asset} />
                <Hint id="balance-available" />
              </p>
            </div>

            <div className="space-y-1.5">
              <FieldLabel htmlFor="send-ref">Reference (optional)</FieldLabel>
              <Input
                id="send-ref"
                value={reference}
                disabled={frozen || closed}
                onChange={(e) => setReference(e.target.value)}
              />
            </div>

            <Button type="submit" disabled={!canSend || submit.isPending}>
              {submit.isPending ? "Sending…" : "Send"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}

// What the directory said about the address typed so far. A miss is an answer and
// is stated plainly; an ambiguous address — two banks claiming it — is a 409 and
// describeError names it.
function PayeeLine({
  query,
  isLoading,
  error,
  name,
  bank,
  payingSelf,
}: {
  query: string;
  isLoading: boolean;
  error: unknown;
  name?: string;
  bank?: string;
  payingSelf: boolean;
}) {
  // The directory answers with a participant id; the customer needs its name.
  const { data: participants } = useParticipants();
  if (!query) return null;
  if (isLoading) return <Skeleton className="h-4 w-40" />;
  if (error) return <p className="text-xs text-destructive">{describeError(error)}</p>;
  if (payingSelf) {
    return <p className="text-xs text-destructive">That is this account&apos;s own IBAN.</p>;
  }
  if (!name) return null;
  const bankName = participants?.find((p) => p.id === bank)?.name ?? bank;
  return (
    <p className="text-xs text-muted-foreground">
      <span className="font-medium text-foreground">{name}</span> at {bankName}
    </p>
  );
}

// The second half of a 202. The bank answered with an identifier and no outcome,
// so the outcome is a second request — which is what this is.
//
// Today it resolves immediately. 7b makes the wait real: the CSM answers with a
// pacs.002 later, and this panel will show Initiated until it does, with nothing
// here needing to change.
function Outcome({
  pid,
  did,
  payid,
  onDismiss,
}: {
  pid: string;
  did: string;
  payid: string;
  onDismiss: () => void;
}) {
  const { data, isLoading, error } = useBankPayment(pid, payid);
  const { byCode } = useAssetLookup();
  const asset = data ? byCode.get(data.asset) : undefined;

  return (
    <Alert>
      <AlertTitle className="flex items-center gap-2">
        Instruction accepted
        {data && <EnumBadge value={data.status} />}
      </AlertTitle>
      <AlertDescription className="space-y-2">
        {error ? (
          <span>{describeError(error)}</span>
        ) : isLoading || !data || !asset ? (
          <span>Your bank took the instruction and gave it a reference. Asking what became of it…</span>
        ) : (
          <span>
            <Money amount={data.amount} asset={asset} /> to{" "}
            {data.creditor.identifier?.value ?? data.creditor.account}. Your bank
            answered with a reference rather than an outcome, and this is the
            answer to asking again.
          </span>
        )}
        <span className="flex gap-3">
          <Link
            href={`/customer/${pid}/${did}/activity`}
            className="text-xs underline underline-offset-2"
          >
            See it on your activity
          </Link>
          <button type="button" onClick={onDismiss} className="text-xs underline underline-offset-2">
            Dismiss
          </button>
        </span>
      </AlertDescription>
    </Alert>
  );
}
```

The form does **not** redirect to Activity on success. The whole point of the 202
shape is that acceptance and outcome are two things; navigating away at the
moment of acceptance would hide the distinction the screen exists to teach.

- [ ] **Step 4: Run the gate**

Run: `cd web && npm run typecheck && npm run lint && npm run test && npm run build`
Expected: all clean. (`MoneyInput` already accepts `disabled`, and `Alert`,
`AlertTitle` and `AlertDescription` are all exported from
`@/components/ui/alert` — verified, nothing to add.)

- [ ] **Step 5: Load it and send real money**

As **Alice Andersson** (Aurora), open Send:

- Type `IT60-VERDE-2002` → resolves to "Bella Bruno at Banca Verde".
- Type `NOBODY-0001` → the 404 surfaces as a plain miss and Send stays disabled.
- Type Alice's own IBAN → "That is this account's own IBAN", Send disabled.
- Send 25.00 with a reference. Expect the acceptance panel with a status badge,
  Activity showing the debit, and Available down by 25.00.
- Check `/clearing-house/payments` shows the new payment, addressed by IBAN on
  both legs. Check `/bank/bank_1/payments` shows it and `/bank/bank_5/payments`
  does not (unless Verde is a party, in which case check a third bank).

As **Bianca Belli** (frozen), open Send: the alert appears, every field is
disabled and Send is disabled. Then confirm money can still *arrive* — send her
10.00 from Alice, and see it on Bianca's Activity. That is the best single lesson
in the persona.

Finally, in the Network tab, confirm the submission is a `POST` to
`/api/bank/<pid>/payments` answering **202** with `{"paymentId":…}`, followed by a
`GET` to `/api/bank/<pid>/payments/<id>`. **A 201 with a whole payment means the
form is calling the clearing house's route**, which is the failure this step
exists to catch.

- [ ] **Step 6: Commit**

```bash
git add -A web/src
git commit -m "feat(web): let a customer pay an IBAN, and ask what became of it"
```

---

## Task 14: Close it out

**Files:**
- Modify: `web/CLAUDE.md` (the Routing paragraph)
- Modify: `docs/expansion-roadmap.md`

- [ ] **Step 1: Correct the routing documentation**

`web/CLAUDE.md`'s **Routing** paragraph still describes the old tree
("Network-wide pages at `src/app/{payments,mandates,cycles,settlements,central-bank,schemes}`…
Participant-scoped pages under `src/app/participants/[pid]/`; to add a section,
append to the `tabs` array in `[pid]/layout.tsx`"). Every clause of it is now
false. Replace with:

```markdown
**Routing is by persona.** Who you are is the top-level structure, and it lines
up with the operator split: `/central-bank/…` (reserves, its own audit, and
settling a closed cycle), `/clearing-house/…` (payments, mandates, cycles,
settlements, schemes, directory), `/bank/[pid]/…` (one bank's back office,
including its own payment legs), `/customer/[pid]/[did]/…` (one deposit account,
retail-framed). `/` is a lobby and never redirects; `/learn/*` sits outside the
persona system. `src/lib/identity.ts` derives the `Identity` from the pathname
and owns `homeFor`/`navFor`/`backendFor` — the last being the operator key the
proxy routes on, which is why a customer resolves to their *bank's* listener and
not one of their own. **To add a section, add its entry to `navFor` and its
`page.tsx` in the same commit; `src/lib/nav-integrity.test.ts` holds the two
together, and also holds the quiz's `EXPLORE_ROUTES` against the route tree.**
Old `/participants/…` links are forwarded by `app/participants/[...rest]/page.tsx`.
Each persona gets its own shell from `components/shell/`; the customer's has no
left panel, and `plain-shell` is the lobby's and Learn's.
```

Also correct the **Proxy** paragraph's neighbours if they still claim a single
`BACKEND_URL` — the port derivation now lives in `src/lib/api/backend-url.ts`,
shared by the proxy and `app/api/operators/route.ts`, which answers which
listeners actually exist. Add one sentence saying so.

No other layer needs a change. This sub-project moved no domain fact, so
`README.md`, `hint-content.ts`, the quiz's *content* and the schema comments all
still say the same true things. (The quiz's `explore.href` targets moved in Task
4 — that is a link, not a claim.)

- [ ] **Step 2: Log the sub-project**

Mark **6b** `done` in `docs/expansion-roadmap.md`'s section heading and point its
`Plan:` line at this file rather than the superseded one. Then append a row to
the log table at the foot, in the established style: dated, saying what was
settled and why rather than what was done. Cover at least:

- Identity derived from the URL and persisted nowhere, with `backendFor` on the
  same switch as `homeFor` so an identity cannot name a persona and a backend
  that do not go together.
- **Five** shell arrangements rather than one filtered by role — four personas
  plus the plain shell the lobby and Learn fall into — and the customer's losing
  the sidebar entirely.
- One flat picker of complete identities, because a persona without its context
  addresses nothing.
- The lobby always at the root, trading a repeat visitor's click for the
  newcomer's orientation.
- The concepts rail kept in the customer shell against realism.
- **The two spec/6a collisions and how they were settled** (see this plan's
  header): the central bank gained four read routes because an operator that
  settles must be able to read the instruction, and the awaiting-provisioning
  badge is served by a Next-side `/api/operators` probe because 6a's derived port
  table left nothing client-readable.
- **The three findings, with their real numbers**: the route move touched 27 link
  literals in components and pages *plus* 33 of 40 quiz `explore.href` values
  across 8 chapter files; `explore.href` was typed `string`, and narrowing it to
  `(typeof EXPLORE_ROUTES)[number]` plus the new `nav-integrity.test.ts` put the
  chapters under the compiler and the allowlist under the route tree; and
  `StatementTable` needed a `retail` variant, because reusing it whole would have
  leaked the bank's chart of accounts into the customer's statement and linked
  out of the persona.
- Whether the scripted rewrite's asserted counts all matched first time. **Record
  this either way** — it is the direct test of 6a's first lesson, and a plan that
  predicted its own counts correctly is worth as much evidence as one that did
  not.

- [ ] **Step 3: Final verification**

From `web/`:

```bash
npm run typecheck && npm run lint && npm run test && npm run build
```

From the repo root:

```bash
cd /Users/raphaelgruber/Git/cbs-account-addressing
go build ./... && go vet ./... && go test ./...
TEST_DATABASE_URL=postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable go test ./...
git diff --stat spec/operator-split-api -- ':!web' ':!docs'
```

Expected: all green, and the last command shows **only** `api/surface.go` and
`api/surface_test.go` — Task 1 and nothing else.

Then, with `make dev` running, load **one page in each of the five shell
arrangements**. This is the `CLAUDE.md` rule, not diligence theatre: a
`[[wiki-link]]` to a missing key takes every route down at runtime while
`next build` stays green.

- `/` — plain shell (lobby)
- `/central-bank` — central-bank shell
- `/clearing-house/directory` — clearing-house shell
- `/bank/<pid>/payments` — bank shell
- `/customer/<pid>/<did>/send` — customer shell
- `/learn/12-sepa` — plain shell again, with the quiz

On each, open the concepts panel and follow one `[[wiki-link]]`. Check the
browser console is free of errors on all six. Then run one quiz chapter with
explore links on it (chapter 9, 11 or 12 — they carry the most) and click an
explore link from a question, confirming it lands on a real clearing-house page.

- [ ] **Step 4: Commit**

```bash
git add docs/expansion-roadmap.md web/CLAUDE.md
git commit -m "docs: record what the persona split settled"
```

---

## Self-review notes

Checked against the design spec, section by section, with the *revised for 6a*
sections taken as replacing what it originally said.

- **Goal, four personas** — Tasks 2–13. The table of persona → backend → scope is
  `Identity` + `backendFor` (Task 2), tested including the customer asymmetry.
- **Out of scope** — honoured throughout: no authn/authz (the port is the claim,
  and nothing here verifies a caller); no card-processor persona; no customer
  mandate, credit, loan or overdraft-terms screen (the customer's nav has exactly
  three entries, pinned by `identity.test.ts`); no bank-scoped mandates view
  (mandates are the clearing house's only); no products screen; no party master
  (a customer identity is `(pid, did)`). The scheme-operator deferral is
  superseded and the persona ships.
- **Identity from the URL, persisted nowhere** — Task 2; `participant-switcher.tsx`
  and its `ledger.lastParticipant` key are deleted in Task 7, not adapted.
- **`backendFor` on the same switch** — Task 2, with the test the spec names:
  a customer resolves to their bank's backend and not one of their own.
- **Routes** — Task 4 moves every one the spec lists, plus the catch-all redirect.
  Three route decisions the spec did not make: today's `/central-bank` holds
  *both* reserves and the central-bank audit, so Task 9 splits it; the spec's
  bank list has no `/bank/[pid]/deposit-accounts`, so Task 11 folds it into the
  bank home and retires the route; and the quiz's two `href: "/"` links stay
  pointing at the root, which is the lobby now — "See the ledger" still lands on
  a real page, and re-pointing them at a console would pick a persona for the
  reader.
- **The proxy routes by operator** — already true after 6a; Task 3 extracts its
  port derivation into a tested module rather than rewriting it, and adds the
  probe the *awaiting provisioning* rule needs. `ResetButton` and
  `CreateParticipantDialog` address the central bank explicitly from whatever
  shell they are in (Tasks 6 and 9) — the two controls the spec says stop
  following their shell.
- **Four shells** — Task 6, plus the fifth (`plain-shell`) the spec names as
  falling out of `useIdentity()` returning null. `ConceptPanelProvider` moves to
  the root layout in Task 6 Step 4, and the panel surviving a persona switch is
  checked in Task 6 Step 7.
- **Concepts panel in every shell including the customer's** — it lives in
  `ShellFrame`, so no shell can omit it; verified in Task 12 Step 7 and Task 14
  Step 3.
- **Accent per identity, two distinct institutional colours, customer inherits
  its bank's** — Task 8, tested, including that the two institutions differ and
  that the four seeded banks are four colours.
- **One flat grouped picker** — Task 7, Institutions / Banks / Customers,
  including frozen and closed accounts, excluding un-provisioned banks.
- **Lobby always the root** — Task 5. Task 4 leaves an interim redirect there for
  exactly one commit, called out where it happens.
- **The two institutions are a re-home and a subtraction** — Task 4 re-homes,
  Task 9 does the settle action and the audit split, Task 10 the directory.
- **Back office is mostly a re-home** — Tasks 4, 6 (tab strip → sidebar) and 11
  (customers home, own payments).
- **Customer surface** — Tasks 12 and 13: overview with balance, available,
  headroom, IBAN and status; activity retail-framed; send by IBAN resolved at the
  bank and submitted as 202 + a status read; the frozen account's debit block and
  the lesson that money can still arrive.
- **Testing** — `identity.test.ts` (Task 2, extended in 10, 11, 12, 13),
  `backend-url.test.ts` (Task 3), `nav-integrity.test.ts` (Task 4, covering nav
  hrefs, persona homes and `EXPLORE_ROUTES`), `accent.test.ts` (Task 8);
  `concept-links.test.ts` and `quiz/diversity.test.ts` are in every task's gate;
  a page is loaded in each of the five arrangements in Task 14.
- **Failure modes** — the route move is covered by the integrity test, the
  narrowed `explore.href` type, the redirect and the click-through in Task 4 Step
  11; a customer identity pointing at a missing account renders the existing
  not-found treatment via `useDepositAccount`'s error branch; the picker and the
  lobby share `useIdentityDirectory` rather than each fetching, and it skips
  un-provisioned banks entirely; an un-provisioned bank is badged and unselectable
  in both; a 502 from one operator is named by the proxy and no longer claims
  ":8080" (Task 3 Step 8); the concepts panel keeps its own resizable panel,
  since the `max-w-2xl` constraint is on the content column, not the viewport.

Three things a reviewer should push on:

1. **Task 1 breaks "frontend-only".** It is deliberate and was agreed, but it
   means `go test ./...` on both stores is a gate on that task and a
   non-regression check on every later one.
2. **The nav/page pairing is load-bearing across tasks.** Three nav entries are
   deliberately absent from Task 2 and arrive in Tasks 10, 11 and 12–13, each
   test-first. If a task adds a nav entry without its page,
   `nav-integrity.test.ts` goes red — which is correct, and is the signal to move
   the entry, not to weaken the test.
3. **The scripted rewrite's counts were derived from the tree on the day this
   plan was written.** If any rule reports a mismatch, the tree moved; fix the
   rule and the expectation together and record it. Do not lower an expectation
   to make the script pass — that is precisely how 6a's `-entity` flag shipped as
   a no-op.
