# Role-Scoped Web UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the web app's single unified dashboard with three role-scoped
personas — central bank operator, bank back office, bank customer — that you
switch between, each on persona-prefixed routes with its own shell.

**Architecture:** Identity is derived from the URL and persisted nowhere
(`web/src/lib/identity.ts`). Routes move under `/central-bank/…`, `/bank/[pid]/…`
and `/customer/[pid]/[did]/…`, with a catch-all redirect keeping the old
`/participants/…` links alive. The 430-line `app-shell.tsx` splits into
`components/shell/`: a persona-agnostic `ShellFrame` (the resizable-panel
machinery) plus one shell per persona, chosen by a `PersonaShell` dispatcher.
`/` is always a lobby. No backend change: the Go API stays open and untouched,
and the scoping is navigational only.

**Tech Stack:** Next.js 16 (App Router) · React 19 · Tailwind v4 · shadcn/ui on
Radix (unified `radix-ui` package) · TanStack Query v5 · lucide-react · vitest.

## Global Constraints

- **Frontend only.** No `.go` file is created, modified or deleted by this plan.
  `go test ./...` is untouched and must stay green; do not run it as part of a
  task's gate, but do not break it either.
- **Every commit must pass all four web gates**, run from `web/`:
  `npm run typecheck` (tsc --noEmit) · `npm run lint` · `npm run test` (vitest)
  · `npm run build`. All four must be clean. If `node_modules` is missing in a
  fresh worktree, run `npm install` first.
- **A `[[wiki-link]]` to a key absent from `web/src/components/hint-content.ts`
  throws at runtime under `RootLayout` and takes every route in the dev app
  down, while `next build` stays green.** `npm run test` catches it. Every hint
  key referenced in this plan already exists in the registry — do not invent new
  ones. Note the correct key for the available balance is `balance-available`,
  **not** `available-balance` (the design spec's prose has it wrong).
- **Vitest is node-environment, pure logic only**: `include: ["src/**/*.test.ts"]`
  — `.ts` only, no `.tsx`, no DOM, no component tests. `lucide-react` and
  `next/navigation` both import cleanly under it (verified).
- **All amounts are integers in the minor units of their asset.** Every
  formatter takes the asset it renders; there is no default scale. An asset code
  that has not resolved yet means do not render a number — render a `<Skeleton>`
  or `<UnresolvedAmount>`.
- **Backend `DisallowUnknownFields()`**: send only the exact keys a request DTO
  defines. A stray key is a 400.
- **Next 16 async params**: dynamic *pages* are client components using
  `useParams()`; a server component awaits its `params` promise.
- **Match the existing shadcn/Tailwind design system.** This is a refined,
  minimal UI — do not impose a new aesthetic.
- **Do not add a dependency.** Everything here is buildable from what
  `web/package.json` already has.

## Two things the design spec undercounts

Both were found by reading the code and both are real work, so they are folded
into Task 2 rather than left to be discovered:

1. **The route move touches the quiz, not just ~18 link sites.**
   `web/src/lib/quiz/index.ts` exports an `EXPLORE_ROUTES` allowlist, and
   `web/src/lib/quiz/index.test.ts:73` asserts every question's `explore.href`
   is in it. Roughly 35 `explore.href` values across seven chapter files point
   at `/payments`, `/mandates`, `/cycles`, `/settlements`, `/central-bank` and
   `/schemes` — every one of which moves. This is the single largest link
   surface in the move and the existing test will fail loudly if the allowlist
   and the chapters disagree, but *nothing* today checks that an allowlisted
   route corresponds to a real page. Task 2's nav-integrity test closes that.
2. **"Retail-framed" needs a mechanism.** `StatementTable` renders an
   `AccountRef` per contra leg and an expandable "Underlying GL transaction"
   panel listing every GL entry — i.e. the bank's chart of accounts, linking
   into `/bank/[pid]/accounts/[aid]`. Reused as-is in the customer's Activity
   screen that leaks the back office into the retail view and links out of the
   persona. Task 8 adds a `retail` prop that drops the contra column, the GL
   expansion and the reconciliation banner.

## File Structure

**Created**

| File | Responsibility |
|---|---|
| `web/src/lib/identity.ts` | `Identity`, `identityFromPathname`, `useIdentity`, `homeFor`, `navFor` |
| `web/src/lib/identity.test.ts` | pathname → identity, `homeFor`, `navFor` shape |
| `web/src/lib/nav-integrity.test.ts` | every nav href and `EXPLORE_ROUTES` entry resolves to a real `page.tsx` |
| `web/src/lib/accent.ts` + `.test.ts` | per-identity accent colour; a customer inherits its bank's |
| `web/src/lib/use-debounced-value.ts` | debounce for the send form's live IBAN lookup |
| `web/src/app/participants/[...rest]/page.tsx` | server redirect `/participants/…` → `/bank/…` |
| `web/src/app/central-bank/reserves/page.tsx` | reserves table (split out of today's `/central-bank`) |
| `web/src/app/central-bank/audit/page.tsx` | central-bank audit trail (the other half of the split) |
| `web/src/components/shell/shell-frame.tsx` | `ResizablePanelGroup`, collapse bridging, `ResizeObserver`; sidebar optional |
| `web/src/components/shell/sidebar-nav.tsx` | renders a `NavItem[]` |
| `web/src/components/shell/topbar.tsx` | brand, identity picker, theme toggle, mobile triggers |
| `web/src/components/shell/central-bank-shell.tsx` | frame + central-bank nav |
| `web/src/components/shell/bank-shell.tsx` | frame + bank nav |
| `web/src/components/shell/customer-shell.tsx` | **no left panel**; top tab strip + `max-w-2xl` column |
| `web/src/components/shell/plain-shell.tsx` | no persona: the lobby and `/learn/*` |
| `web/src/components/shell/persona-shell.tsx` | picks the shell from `useIdentity()` |
| `web/src/components/shell/identity-picker.tsx` | one flat grouped searchable list of identities |
| `web/src/app/customer/[pid]/[did]/page.tsx` | customer overview |
| `web/src/app/customer/[pid]/[did]/activity/page.tsx` | customer activity |
| `web/src/app/customer/[pid]/[did]/send/page.tsx` | customer send |

**Moved** (`git mv`, so history follows) — see Task 2 for the exact sequence.

**Modified**: `web/src/app/layout.tsx`, `web/src/app/page.tsx`,
`web/src/lib/quiz/index.ts`, seven quiz chapter files,
`web/src/lib/api/{endpoints,query-keys,hooks}.ts`, `web/src/lib/types.ts`,
`web/src/components/{account-ref,create-participant-dialog}.tsx`,
`web/src/components/statement/{statement-card,statement-table}.tsx`.

**Deleted**: `web/src/components/app-shell.tsx`,
`web/src/components/participant-switcher.tsx`,
`web/src/app/bank/[pid]/deposit-accounts/page.tsx` (Task 7 folds it into the
bank home).

---

## Task 1: The identity module

**Files:**
- Create: `web/src/lib/identity.ts`
- Test: `web/src/lib/identity.test.ts`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Identity = { persona: "central-bank" } | { persona: "bank"; pid: string } | { persona: "customer"; pid: string; did: string }`
  - `interface NavItem { href: string; label: string; icon: LucideIcon; exact?: boolean }`
  - `function identityFromPathname(pathname: string): Identity | null`
  - `function useIdentity(): Identity | null`
  - `function homeFor(identity: Identity): string`
  - `function navFor(identity: Identity): NavItem[]`

`navFor` covers the central bank and the bank in this task and returns `[]` for
a customer: the customer's screens do not exist yet, and a nav entry pointing at
a route with no file is exactly what Task 2's integrity test exists to reject.
Task 8 fills it in.

- [ ] **Step 1: Write the failing test**

Create `web/src/lib/identity.test.ts`:

```ts
import { describe, expect, it } from "vitest";

import { homeFor, identityFromPathname, navFor, type Identity } from "./identity";

describe("identityFromPathname", () => {
  it("reads the central bank off its prefix", () => {
    expect(identityFromPathname("/central-bank")).toEqual({ persona: "central-bank" });
    expect(identityFromPathname("/central-bank/settlements/set_1")).toEqual({
      persona: "central-bank",
    });
  });

  it("reads a bank and its pid", () => {
    expect(identityFromPathname("/bank/part_1")).toEqual({ persona: "bank", pid: "part_1" });
    expect(identityFromPathname("/bank/part_1/ledger")).toEqual({
      persona: "bank",
      pid: "part_1",
    });
  });

  it("reads a customer as a (bank, account) pair", () => {
    expect(identityFromPathname("/customer/part_1/dep_9")).toEqual({
      persona: "customer",
      pid: "part_1",
      did: "dep_9",
    });
    expect(identityFromPathname("/customer/part_1/dep_9/send")).toEqual({
      persona: "customer",
      pid: "part_1",
      did: "dep_9",
    });
  });

  // The two null cases the design names: the lobby and Learn sit outside the
  // persona system entirely.
  it("has no identity at the lobby or under Learn", () => {
    expect(identityFromPathname("/")).toBeNull();
    expect(identityFromPathname("/learn")).toBeNull();
    expect(identityFromPathname("/learn/sepa")).toBeNull();
    expect(identityFromPathname("/learn/mixed")).toBeNull();
  });

  // A prefix without its context addresses nothing, so it is not an identity.
  // Landing on /bank with no pid must not produce { pid: undefined }.
  it("refuses a persona prefix with its context missing", () => {
    expect(identityFromPathname("/bank")).toBeNull();
    expect(identityFromPathname("/customer/part_1")).toBeNull();
  });

  it("ignores a trailing slash", () => {
    expect(identityFromPathname("/bank/part_1/")).toEqual({ persona: "bank", pid: "part_1" });
  });
});

describe("homeFor", () => {
  it("sends each identity to its own root", () => {
    expect(homeFor({ persona: "central-bank" })).toBe("/central-bank");
    expect(homeFor({ persona: "bank", pid: "part_1" })).toBe("/bank/part_1");
    expect(homeFor({ persona: "customer", pid: "part_1", did: "dep_9" })).toBe(
      "/customer/part_1/dep_9",
    );
  });

  // homeFor is what the identity picker navigates to, so it must round-trip:
  // the identity you pick is the identity the destination reads back.
  it("round-trips through identityFromPathname", () => {
    const identities: Identity[] = [
      { persona: "central-bank" },
      { persona: "bank", pid: "part_1" },
      { persona: "customer", pid: "part_1", did: "dep_9" },
    ];
    for (const it of identities) {
      expect(identityFromPathname(homeFor(it))).toEqual(it);
    }
  });
});

describe("navFor", () => {
  it("gives the central bank the network screens", () => {
    const hrefs = navFor({ persona: "central-bank" }).map((n) => n.href);
    expect(hrefs).toEqual([
      "/central-bank",
      "/central-bank/reserves",
      "/central-bank/payments",
      "/central-bank/mandates",
      "/central-bank/cycles",
      "/central-bank/settlements",
      "/central-bank/schemes",
      "/central-bank/audit",
    ]);
  });

  it("scopes every bank entry to the bank's own pid", () => {
    const nav = navFor({ persona: "bank", pid: "part_1" });
    expect(nav.length).toBeGreaterThan(0);
    for (const item of nav) {
      expect(item.href.startsWith("/bank/part_1")).toBe(true);
    }
  });

  // Only the home entry may match exactly; every other entry is a section
  // prefix, so a detail page below it keeps its parent highlighted.
  it("marks exactly one entry per persona as an exact match", () => {
    for (const identity of [
      { persona: "central-bank" } as const,
      { persona: "bank", pid: "part_1" } as const,
    ]) {
      const nav = navFor(identity);
      expect(nav.filter((n) => n.exact)).toHaveLength(1);
      expect(nav[0].exact).toBe(true);
      expect(nav[0].href).toBe(homeFor(identity));
    }
  });

  it("has no customer nav until the customer screens exist", () => {
    expect(navFor({ persona: "customer", pid: "part_1", did: "dep_9" })).toEqual([]);
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
  Banknote,
  BookOpen,
  Building2,
  FileSignature,
  Landmark,
  LayoutDashboard,
  Network,
  RefreshCw,
  ScrollText,
  Users,
  Wallet,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

// Who you are in the app. There is no observer who sees all of it: a back
// office sees one bank, a customer sees one account, the central bank sees the
// network. The identity is derived from the URL and persisted nowhere — a view
// that is not addressable is not a view, so "the customer's version of this
// account" has to be something you can link to, refresh into and go back out
// of.
//
// A customer identity IS one deposit account: there is no party master, so
// "Alice Andersson" the identity is the pair (Aurora, that account), and a
// second account would be a second identity.
export type Identity =
  | { persona: "central-bank" }
  | { persona: "bank"; pid: string }
  | { persona: "customer"; pid: string; did: string };

export interface NavItem {
  href: string;
  label: string;
  icon: LucideIcon;
  // True only for a persona's home. Every other entry names a section, so a
  // detail page below it keeps its parent highlighted.
  exact?: boolean;
}

// A persona prefix without its context addresses nothing, so "/bank" with no
// pid is no identity at all rather than a bank with an undefined id.
export function identityFromPathname(pathname: string): Identity | null {
  const [prefix, pid, did] = pathname.split("/").filter(Boolean);
  if (prefix === "central-bank") return { persona: "central-bank" };
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
    case "bank":
      return `/bank/${identity.pid}`;
    case "customer":
      return `/customer/${identity.pid}/${identity.did}`;
  }
}

// Clearing cycles and payments live under the central bank rather than a
// scheme-operator persona of their own, which does mean this central bank sees
// every individual payment and a real one does not. That is a recorded
// compromise, not an oversight — see the design's "Out of scope".
const CENTRAL_BANK_NAV: NavItem[] = [
  { href: "/central-bank", label: "Network", icon: LayoutDashboard, exact: true },
  { href: "/central-bank/reserves", label: "Reserves", icon: Landmark },
  { href: "/central-bank/payments", label: "Payments", icon: ArrowLeftRight },
  { href: "/central-bank/mandates", label: "Mandates", icon: FileSignature },
  { href: "/central-bank/cycles", label: "Clearing cycles", icon: RefreshCw },
  { href: "/central-bank/settlements", label: "Settlements", icon: Building2 },
  { href: "/central-bank/schemes", label: "Schemes", icon: Network },
  { href: "/central-bank/audit", label: "Audit", icon: ScrollText },
];

export function navFor(identity: Identity): NavItem[] {
  switch (identity.persona) {
    case "central-bank":
      return CENTRAL_BANK_NAV;
    case "bank": {
      const base = `/bank/${identity.pid}`;
      return [
        { href: base, label: "Overview", icon: LayoutDashboard, exact: true },
        { href: `${base}/deposit-accounts`, label: "Deposit accounts", icon: Wallet },
        { href: `${base}/ledger`, label: "General ledger", icon: BookOpen },
        { href: `${base}/transactions`, label: "Transactions", icon: ArrowLeftRight },
        { href: `${base}/facilities`, label: "Facilities", icon: Banknote },
        { href: `${base}/audit`, label: "Ledger audit", icon: ScrollText },
        { href: `${base}/deposit-audit`, label: "Deposit audit", icon: Users },
      ];
    }
    case "customer":
      // The customer's screens arrive with Task 8; a nav entry pointing at a
      // route with no file is what nav-integrity.test.ts exists to reject.
      return [];
  }
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd web && npx vitest run src/lib/identity.test.ts`
Expected: PASS — all cases green.

- [ ] **Step 5: Run the full gate**

Run: `cd web && npm run typecheck && npm run lint && npm run test`
Expected: all clean. (`npm run build` is not needed yet — no route or component
references this module.)

- [ ] **Step 6: Commit**

```bash
git add web/src/lib/identity.ts web/src/lib/identity.test.ts
git commit -m "feat(web): derive who you are from the URL"
```

---

## Task 2: Move the routes under their personas

The riskiest mechanical part of the work. Everything moves in one commit,
because a half-moved tree is a broken app.

`/` is left as a one-line redirect to `/central-bank` at the end of this task
and becomes the real lobby in Task 3. That is working software, not a
placeholder: the design's "`/` never redirects" is a property of the finished
lobby, and shipping a 404 at the root between two tasks would be worse.

**Files:**
- Move: `web/src/app/{payments,mandates,cycles,settlements,schemes}` → `web/src/app/central-bank/…`
- Move: `web/src/app/participants/[pid]` → `web/src/app/bank/[pid]`
- Move: `web/src/app/central-bank/page.tsx` → `web/src/app/central-bank/reserves/page.tsx` (then split)
- Move: `web/src/app/page.tsx` → `web/src/app/central-bank/page.tsx`
- Create: `web/src/app/central-bank/audit/page.tsx`, `web/src/app/participants/[...rest]/page.tsx`, `web/src/app/page.tsx`
- Modify: `web/src/components/app-shell.tsx:50-59`, `web/src/components/account-ref.tsx:28`, `web/src/components/create-participant-dialog.tsx:60`, `web/src/components/participant-switcher.tsx:37`, `web/src/components/statement/statement-card.tsx:27`, `web/src/app/bank/[pid]/layout.tsx:26`, and every route literal listed in Step 5
- Modify: `web/src/lib/quiz/index.ts:22-31` and seven chapter files
- Test: `web/src/lib/nav-integrity.test.ts`

**Interfaces:**
- Consumes: `navFor`, `homeFor`, `type Identity` from Task 1.
- Produces: the route tree every later task builds on, and
  `EXPLORE_ROUTES` re-pointed at `/central-bank/…`.

- [ ] **Step 1: Write the failing test**

Create `web/src/lib/nav-integrity.test.ts`. It is the thing that catches a dead
link after the move — for nav entries *and* for the quiz's deep-links, which
are the larger surface.

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

  // The quiz deep-links into the explorer from ~35 questions across seven
  // chapters. index.test.ts already pins that every question's explore.href is
  // in this allowlist; nothing pinned that an allowlisted route exists. Every
  // one of them moves under /central-bank in this task.
  it("every allowlisted quiz explore route resolves to a real page", () => {
    for (const href of EXPLORE_ROUTES) {
      expect(existsSync(routeFileFor(href)), `EXPLORE_ROUTES entry ${href}`).toBe(true);
    }
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && npx vitest run src/lib/nav-integrity.test.ts`
Expected: FAIL — the central-bank nav points at `/central-bank/reserves`,
`/central-bank/payments` etc., none of which exist yet, and the bank nav points
at `/bank/[pid]/…`.

- [ ] **Step 3: Move the tree**

Run from the repo root, in this order — `git mv` so history follows each file:

```bash
cd web/src/app

# The central bank's own screens. Today's /central-bank is one page holding
# both the reserves table and the central-bank audit trail; the design gives
# them a route each, so the file moves to /reserves and the audit half is
# lifted out in Step 4.
mkdir -p central-bank/reserves
git mv central-bank/page.tsx central-bank/reserves/page.tsx

# Today's dashboard becomes the network overview. This must come after the
# move above, which vacates central-bank/page.tsx.
git mv page.tsx central-bank/page.tsx

# The network-wide screens are the central bank's section now.
git mv payments central-bank/payments
git mv mandates central-bank/mandates
git mv cycles central-bank/cycles
git mv settlements central-bank/settlements
git mv schemes central-bank/schemes

# The back office. `participants` keeps existing, holding only the redirect.
mkdir -p bank
git mv participants/'[pid]' bank/'[pid]'
mkdir -p participants/'[...rest]'
```

Verify the tree:

```bash
cd /Users/raphaelgruber/Git/cbs-account-addressing/web && find src/app -name page.tsx | sort
```

Expected: pages under `central-bank/`, `bank/[pid]/`, `learn/`, and nothing left
under `participants/[pid]`.

- [ ] **Step 4: Split the central bank's reserves and audit**

Edit `web/src/app/central-bank/reserves/page.tsx`: drop the audit half. Remove
`AuditTable`/`useAuditPager` from the imports, remove `useCentralBankAudit` from
the `@/lib/api/hooks` import, delete the `pager`/`audit` locals and the whole
`<section>` holding the audit trail, and retitle the header. The component
becomes:

```tsx
export default function CentralBankReservesPage() {
  const reserves = useReserves();

  return (
    <div className="space-y-8">
      <PageHeader
        title="Reserves"
        hint="central-bank-reserves"
        description="Banks meet only here. The central bank holds one reserve account per participant and asset, and settlement is reserves moving between them."
      />

      {reserves.error ? (
        <ErrorState error={reserves.error} onRetry={() => reserves.refetch()} />
      ) : (
        <DataTable
          columns={reserveColumns}
          rows={reserves.data}
          rowKey={(r) => `${r.participant}:${r.asset}`}
          isLoading={reserves.isLoading}
          empty="No participants yet. Create one to see its reserve account."
        />
      )}
    </div>
  );
}
```

Keep `ReserveAmountCell` and `reserveColumns` exactly as they are.

Create `web/src/app/central-bank/audit/page.tsx`:

```tsx
"use client";

import { PageHeader } from "@/components/page-header";
import { AuditTable, useAuditPager } from "@/components/audit-table";
import { useCentralBankAudit } from "@/lib/api/hooks";

// The central bank's own log: reserve movements and settlements, the events
// produced by the layer the banks meet at. A bank's ledger and deposit logs are
// its own and live in its back office.
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

- [ ] **Step 5: Repoint every route literal**

Each of these is an exact edit. Work through them in order.

*Old `/participants/…` → `/bank/…`:*

| File:line | Old | New |
|---|---|---|
| `src/app/bank/[pid]/layout.tsx:26` | `` `/participants/${pid}` `` | `` `/bank/${pid}` `` |
| `src/app/bank/[pid]/ledger/page.tsx:211` | `` `/participants/${pid}/accounts/${account.id}` `` | `` `/bank/${pid}/accounts/${account.id}` `` |
| `src/app/bank/[pid]/deposit-accounts/page.tsx:32` | `` `/participants/${pid}/deposit-accounts/${account.id}` `` | `` `/bank/${pid}/deposit-accounts/${account.id}` `` |
| `src/app/bank/[pid]/deposit-accounts/[did]/page.tsx:280` | `` `/participants/${pid}/deposit-accounts` `` | `` `/bank/${pid}/deposit-accounts` `` |
| `src/app/bank/[pid]/deposit-accounts/[did]/statement/page.tsx:39` | `` `/participants/${pid}/deposit-accounts/${did}` `` | `` `/bank/${pid}/deposit-accounts/${did}` `` |
| `src/app/bank/[pid]/accounts/[aid]/page.tsx:35` | `` `/participants/${pid}/ledger` `` | `` `/bank/${pid}/ledger` `` |
| `src/app/bank/[pid]/accounts/[aid]/page.tsx:82` | `` `/participants/${pid}/deposit-accounts/${backingDeposit.id}` `` | `` `/bank/${pid}/deposit-accounts/${backingDeposit.id}` `` |
| `src/app/bank/[pid]/facilities/page.tsx:29` | `` `/participants/${pid}/facilities/${facility.id}` `` | `` `/bank/${pid}/facilities/${facility.id}` `` |
| `src/app/bank/[pid]/facilities/[fid]/page.tsx:177` | `` `/participants/${pid}/facilities` `` | `` `/bank/${pid}/facilities` `` |
| `src/components/account-ref.tsx:28` | `` `/participants/${pid}/accounts/${id}` `` | `` `/bank/${pid}/accounts/${id}` `` |
| `src/components/create-participant-dialog.tsx:60` | `` `/participants/${p.id}` `` | `` `/bank/${p.id}` `` |
| `src/components/participant-switcher.tsx:37` | `` `/participants/${pid}` `` | `` `/bank/${pid}` `` |
| `src/components/statement/statement-card.tsx:27` | `` `/participants/${pid}/deposit-accounts/${did}/statement` `` | `` `/bank/${pid}/deposit-accounts/${did}/statement` `` |
| `src/app/central-bank/page.tsx:165` | `` `/participants/${p.id}` `` | `` `/bank/${p.id}` `` |

*Network screens → `/central-bank/…`:*

| File:line | Old | New |
|---|---|---|
| `src/app/central-bank/cycles/page.tsx:52` | `` `/cycles/${c.id}` `` | `` `/central-bank/cycles/${c.id}` `` |
| `src/app/central-bank/cycles/[cid]/page.tsx:36` | `"/cycles"` | `"/central-bank/cycles"` |
| `src/app/central-bank/cycles/[cid]/page.tsx:86` | `` `/settlements/${s.id}` `` | `` `/central-bank/settlements/${s.id}` `` |
| `src/app/central-bank/cycles/[cid]/page.tsx:111` | `` `/settlements/${c.settlementId}` `` | `` `/central-bank/settlements/${c.settlementId}` `` |
| `src/app/central-bank/cycles/[cid]/page.tsx:152` | `` `/payments/${id}` `` | `` `/central-bank/payments/${id}` `` |
| `src/app/central-bank/payments/page.tsx:75` | `` `/payments/${p.id}` `` | `` `/central-bank/payments/${p.id}` `` |
| `src/app/central-bank/payments/[payid]/page.tsx:70` | `"/payments"` | `"/central-bank/payments"` |
| `src/app/central-bank/payments/[payid]/page.tsx:195` | `` `/cycles/${p.cycleId}` `` | `` `/central-bank/cycles/${p.cycleId}` `` |
| `src/app/central-bank/payments/layout.tsx:13-14` | `"/payments"`, `"/payments/audit"` | `"/central-bank/payments"`, `"/central-bank/payments/audit"` |
| `src/app/central-bank/settlements/page.tsx:48` | `` `/settlements/${s.id}` `` | `` `/central-bank/settlements/${s.id}` `` |
| `src/app/central-bank/settlements/[sid]/page.tsx:24` | `"/settlements"` | `"/central-bank/settlements"` |
| `src/app/central-bank/settlements/[sid]/page.tsx:49` | `` `/cycles/${s.cycleId}` `` | `` `/central-bank/cycles/${s.cycleId}` `` |

Then confirm nothing was missed:

```bash
cd /Users/raphaelgruber/Git/cbs-account-addressing/web
grep -rn '"/participants\|`/participants/\|"/payments\|`/payments/\|"/cycles\|`/cycles/\|"/settlements\|`/settlements/\|"/schemes\|"/mandates' src --include="*.tsx" | grep -v 'src/lib/api/'
```

Expected: no output. (Prose comments mentioning the backend routes
`GET /participants/{pid}/…` are fine and are not matched by these patterns —
`src/lib/types.ts` and `src/components/pickers/product-picker.tsx` keep theirs.
`src/components/concept-links.test.ts:22` asserts `conceptUrlTransform` passes
`/participants/p_1` through untouched; it is testing the URL sanitizer, not a
link in the app, so leave it alone.)

- [ ] **Step 6: Repoint the quiz's deep-links**

Edit `web/src/lib/quiz/index.ts:22-31`:

```ts
/** Network-level explorer routes a question may deep-link to. */
export const EXPLORE_ROUTES = [
  "/",
  "/central-bank",
  "/central-bank/reserves",
  "/central-bank/payments",
  "/central-bank/mandates",
  "/central-bank/cycles",
  "/central-bank/settlements",
  "/central-bank/schemes",
] as const;
```

Then rewrite every `explore.href` in the chapter files. `/central-bank` in the
quiz always means the reserves table (the questions say "central-bank
reserves"), so it maps to `/central-bank/reserves`, not the network overview:

```bash
cd /Users/raphaelgruber/Git/cbs-account-addressing/web/src/lib/quiz/chapters
sed -i '' \
  -e 's|href: "/central-bank"|href: "/central-bank/reserves"|g' \
  -e 's|href: "/payments"|href: "/central-bank/payments"|g' \
  -e 's|href: "/mandates"|href: "/central-bank/mandates"|g' \
  -e 's|href: "/cycles"|href: "/central-bank/cycles"|g' \
  -e 's|href: "/settlements"|href: "/central-bank/settlements"|g' \
  -e 's|href: "/schemes"|href: "/central-bank/schemes"|g' \
  *.ts
```

Verify no old form survives and nothing was double-rewritten:

```bash
cd /Users/raphaelgruber/Git/cbs-account-addressing/web
grep -rn 'href: "/' src/lib/quiz/chapters | grep -v '"/central-bank' || echo "all repointed"
grep -rn 'central-bank/central-bank' src/lib/quiz && echo "DOUBLE REWRITE — fix" || echo "clean"
```

- [ ] **Step 7: Add the redirect and a root**

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
  redirect("/central-bank");
}
```

- [ ] **Step 8: Point the existing nav at `navFor`**

`app-shell.tsx` still holds a hardcoded `NETWORK_NAV` pointing at the old
routes. It is deleted wholesale in Task 4, but leaving it broken now would mean
a commit with an unusable app. Edit `web/src/components/app-shell.tsx`:

- Delete the `NETWORK_NAV` array (`:50-59`) and the now-unused icon imports
  (`LayoutDashboard`, `ArrowLeftRight`, `FileSignature`, `RefreshCw`,
  `Landmark`, `Building2`, `Network`; keep `GraduationCap`, `Menu`, `BookOpen`,
  `PanelRightOpen`, `PanelLeftOpen`, `PanelLeftClose`).
- Add `import { GraduationCap } from "lucide-react";` if it is not already
  there, and `import { navFor, useIdentity, type NavItem } from "@/lib/identity";`
- In `NavLinks`, replace the `NETWORK_NAV.map(…)` source with:

```tsx
const identity = useIdentity();
const items: NavItem[] = [
  ...(identity ? navFor(identity) : []),
  { href: "/learn", label: "Learn", icon: GraduationCap },
];
```

and change the active test from the `href === "/"` special case to
`const active = item.exact ? pathname === item.href : pathname.startsWith(item.href);`.

- [ ] **Step 9: Run the tests**

Run: `cd web && npm run test`
Expected: PASS — `nav-integrity.test.ts` green (every nav href and every
`EXPLORE_ROUTES` entry resolves to a file), `quiz/index.test.ts` green (every
`explore.href` is in the allowlist), `concept-links.test.ts` and
`quiz/diversity.test.ts` unchanged and green.

- [ ] **Step 10: Run the full gate and load the app**

Run: `cd web && npm run typecheck && npm run lint && npm run build`
Expected: all clean.

Then, with the Go backend running (`go run ./cmd/server` from the repo root) and
`npm run dev`, load and click through: `/` (forwards to `/central-bank`), a
member bank card → `/bank/<pid>`, its tabs, a deposit account, its statement, a
GL account, `/central-bank/payments` → a payment → its cycle → its settlement,
`/central-bank/schemes`, `/central-bank/reserves`, `/central-bank/audit`,
`/learn` and one chapter. Confirm `/participants/<pid>/ledger` forwards to
`/bank/<pid>/ledger`. In-page links are the residual risk the integrity test
cannot see, and this is what catches them.

- [ ] **Step 11: Commit**

```bash
git add -A web/src
git commit -m "refactor(web): give every screen a persona to belong to"
```

---

## Task 3: The lobby

**Files:**
- Modify: `web/src/app/page.tsx` (replace the interim redirect)
- Modify: `web/src/lib/api/hooks.ts` (append `useIdentityDirectory`)

**Interfaces:**
- Consumes: `homeFor`, `type Identity` (Task 1); `useParticipants`,
  `useReserves`, `qk.depositAccounts`, `api.listDepositAccounts`.
- Produces:
  - `interface BankEntry { participant: Participant; accounts: DepositAccount[] }`
  - `function useIdentityDirectory(): { banks: BankEntry[]; isLoading: boolean; error: unknown }`
    — the picker (Task 5) and the lobby share this rather than each fetching.

- [ ] **Step 1: Add the shared directory hook**

Append to `web/src/lib/api/hooks.ts` (after the deposit-account hooks, and add
`useQueries` to the existing `@tanstack/react-query` import):

```ts
// --- Identities -----------------------------------------------------------

// Every identity in the system, in one place: each member bank with its
// customer accounts. The lobby and the identity picker both need exactly this,
// so they share one set of queries — four parallel per-bank fetches, cached by
// react-query under the same keys the back office already uses, rather than
// each surface fetching its own.
export interface BankEntry {
  participant: import("../types").Participant;
  accounts: import("../types").DepositAccount[];
}

export function useIdentityDirectory(): {
  banks: BankEntry[];
  isLoading: boolean;
  error: unknown;
} {
  const participants = useParticipants();
  const list = participants.data ?? [];

  const results = useQueries({
    queries: list.map((p) => ({
      queryKey: qk.depositAccounts(p.id),
      queryFn: () => api.listDepositAccounts(p.id),
    })),
  });

  const banks = list.map((participant, i) => ({
    participant,
    accounts: results[i]?.data ?? [],
  }));

  return {
    banks,
    isLoading: participants.isLoading || results.some((r) => r.isLoading),
    error: participants.error ?? results.find((r) => r.error)?.error ?? null,
  };
}
```

- [ ] **Step 2: Write the lobby**

Replace `web/src/app/page.tsx` entirely:

```tsx
"use client";

import Link from "next/link";
import { Building2, GraduationCap, Landmark } from "lucide-react";

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
          bank&apos;s customers, a customer sees one account, and the central
          bank sees reserves and settlement. Pick a seat.
        </p>
      </div>

      <section className="space-y-3">
        <h2 className="text-sm font-medium text-muted-foreground">The network</h2>
        <Link href={homeFor({ persona: "central-bank" })}>
          <Card className="transition-colors hover:border-foreground/30">
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <Landmark className="size-4" />
                Central bank
                <Hint id="central-bank-reserves" />
              </CardTitle>
            </CardHeader>
            <CardContent>
              <p className="text-sm text-muted-foreground">
                Reserves, net positions, clearing cycles and settlement.
              </p>
            </CardContent>
          </Card>
        </Link>
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
            {banks.map(({ participant }) => (
              <BankCard
                key={participant.id}
                participant={participant}
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
        {banks.map(({ participant, accounts }) => (
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

function BankCard({
  participant,
  reserves,
}: {
  participant: Participant;
  reserves: Reserve[];
}) {
  const { byCode } = useAssetLookup();
  return (
    <Link href={homeFor({ persona: "bank", pid: participant.id })}>
      <Card className="h-full transition-colors hover:border-foreground/30">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Building2 className="size-4" />
            {participant.name}
          </CardTitle>
        </CardHeader>
        <CardContent>
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
        </CardContent>
      </Card>
    </Link>
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
`EXPLORE_ROUTES` and `src/app/page.tsx` exists.

- [ ] **Step 4: Load it**

With the backend and `npm run dev` running, open `/`. Expect the central bank,
four member banks with reserves, twelve customer accounts grouped by bank with
their IBANs and a status badge (Bianca Belli frozen, Annie Ahlberg dormant,
Closed Account closed), and the Learn card. Click a bank card and a customer row
and confirm both land on the right URL.

- [ ] **Step 5: Commit**

```bash
git add web/src/app/page.tsx web/src/lib/api/hooks.ts
git commit -m "feat(web): show the cast at the root and let you pick a seat"
```

---

## Task 4: Split the shell

`app-shell.tsx` is 430 lines doing the resizable-panel machinery, the nav
config, the brand, the mobile sheets and the concept-panel bridging at once.
This splits it by responsibility and gives each persona its own shell.

**Files:**
- Create: `web/src/components/shell/{shell-frame,sidebar-nav,topbar,central-bank-shell,bank-shell,plain-shell,persona-shell}.tsx`
- Modify: `web/src/app/layout.tsx`, `web/src/app/bank/[pid]/layout.tsx`
- Delete: `web/src/components/app-shell.tsx`

**Interfaces:**
- Consumes: `navFor`, `useIdentity`, `type NavItem` (Task 1).
- Produces:
  - `<ShellFrame sidebar?: React.ReactNode>{children}</ShellFrame>`
  - `<SidebarNav items={NavItem[]} collapsed onNavigate />`
  - `<PersonaShell>{children}</PersonaShell>` — the root layout's only shell import.

`ShellFrame` takes its sidebar as a prop and renders **two** panels when there
is none, which is what both the customer shell (Task 8) and the lobby need. The
two arrangements persist their layouts under different ids (`app-shell-nav` and
`app-shell-plain`) so a two-panel layout can never be restored into a
three-panel group.

- [ ] **Step 1: Extract the frame**

Create `web/src/components/shell/shell-frame.tsx`. Lift `DesktopShell`,
`MobileShell`, `ConceptStrip`, `ConceptSheet`, `MobileNavSheet` and the
`useIsDesktop` gate out of `app-shell.tsx` **unchanged in behaviour** — the
collapse bridging and the `ResizeObserver` reverse-direction logic are subtle
and were got right once. The only changes:

- `sidebar` and `mobileSidebar` become optional props.
- When `sidebar` is absent, render a two-panel group (`main` | `concepts`), use
  `useDefaultLayout({ id: "app-shell-plain", panelIds: ["main", "concepts"] })`,
  and skip the nav panel, the nav ref, the nav collapse flag and its half of the
  `ResizeObserver`. When present, everything is as today with
  `id: "app-shell-nav"`.
- The `ResizeObserver` effect observes `conceptEl` always and `navEl` only when
  there is one; keep the `initial` skip exactly as written, and keep the comment
  explaining why a `ResizeObserver` is used rather than rrp's `onResize`.
- The topbar is a prop (`topbar`), so a persona shell can supply its own.

```tsx
export function ShellFrame({
  children,
  sidebar,
  mobileSidebar,
  topbar,
}: {
  children: React.ReactNode;
  // Rendered inside the collapsible left panel, given the panel's collapsed
  // state. Absent means no left panel at all — the customer shell and the
  // lobby, which are content columns rather than consoles.
  sidebar?: (collapsed: boolean, toggle: () => void) => React.ReactNode;
  mobileSidebar?: React.ReactNode;
  topbar: React.ReactNode;
}) { /* … */ }
```

Keep the `NAV_COLLAPSED_KEY = "nav-collapsed"` localStorage flag and the
`h-screen overflow-hidden` wrapper (rrp's group hard-codes `height:100%`, which
only resolves against a definite-height ancestor) — both comments move with the
code.

- [ ] **Step 2: Extract the nav and topbar**

Create `web/src/components/shell/sidebar-nav.tsx` — `Brand`, `NavLinks` and
`NavSidebar` from `app-shell.tsx`, with the nav items passed in:

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

Active-state test: `item.exact ? pathname === item.href : pathname.startsWith(item.href)`.
Keep the collapsed icon-only rendering with `title`/`aria-label`, and keep
`<ResetButton collapsed={collapsed} />` in the footer.

Create `web/src/components/shell/topbar.tsx` — `Topbar`, `ConceptTrigger` and
the brand wordmark from `app-shell.tsx`. Where it rendered `<ParticipantSwitcher />`
it renders nothing for now; Task 5 puts the identity picker there.

- [ ] **Step 3: Write the three shells and the dispatcher**

Create `web/src/components/shell/central-bank-shell.tsx`:

```tsx
"use client";

import { ShellFrame } from "./shell-frame";
import { SidebarNav } from "./sidebar-nav";
import { Topbar } from "./topbar";
import { homeFor, navFor } from "@/lib/identity";

const IDENTITY = { persona: "central-bank" } as const;

export function CentralBankShell({ children }: { children: React.ReactNode }) {
  const items = navFor(IDENTITY);
  return (
    <ShellFrame
      topbar={<Topbar />}
      sidebar={(collapsed, toggle) => (
        <SidebarNav
          items={items}
          home={homeFor(IDENTITY)}
          collapsed={collapsed}
          onToggle={toggle}
        />
      )}
      mobileSidebar={<SidebarNav items={items} home={homeFor(IDENTITY)} collapsed={false} onToggle={() => {}} />}
    >
      {children}
    </ShellFrame>
  );
}
```

Create `web/src/components/shell/bank-shell.tsx` — identical, but taking
`{ pid }` and building `const identity = { persona: "bank", pid } as const;`.

Create `web/src/components/shell/plain-shell.tsx` — `ShellFrame` with no
`sidebar`, so the lobby and Learn get a content column and the concepts rail and
nothing else.

Create `web/src/components/shell/persona-shell.tsx`:

```tsx
"use client";

import { useIdentity } from "@/lib/identity";
import { CentralBankShell } from "./central-bank-shell";
import { BankShell } from "./bank-shell";
import { PlainShell } from "./plain-shell";

// Who you are decides which software you get. The customer's shell arrives with
// the customer's screens; until then a customer URL has no page to render and
// falls through to the plain shell like the lobby does.
export function PersonaShell({ children }: { children: React.ReactNode }) {
  const identity = useIdentity();
  if (identity?.persona === "central-bank") return <CentralBankShell>{children}</CentralBankShell>;
  if (identity?.persona === "bank") return <BankShell pid={identity.pid}>{children}</BankShell>;
  return <PlainShell>{children}</PlainShell>;
}
```

- [ ] **Step 4: Move the provider up and delete the old shell**

Edit `web/src/app/layout.tsx`: `ConceptPanelProvider` moves to the root layout so
the concepts panel and its state survive a persona switch, and `AppShell` becomes
`PersonaShell`.

```tsx
import { Providers } from "@/components/providers";
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

`ShellFrame` no longer wraps children in `ConceptPanelProvider` — it consumes
the context instead.

Then delete the old shell:

```bash
git rm web/src/components/app-shell.tsx
```

- [ ] **Step 5: Promote the bank's tabs into its sidebar**

The bank's sub-nav is currently a tab strip *inside* a page — which is what "a
section of the network app" looks like. It is the shell's sidebar now. Edit
`web/src/app/bank/[pid]/layout.tsx`: delete the `tabs` array, the `<nav>` block
and the now-unused `usePathname`/`cn`/`Link` imports. Keep the `useParticipant`
call (a bad pid must still surface the friendly not-found) and the bank header:

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

- [ ] **Step 7: Load all three surfaces**

With the backend and `npm run dev`: open `/` (plain shell — no left nav,
concepts rail present), `/central-bank` (network nav in the sidebar),
`/bank/<pid>` (the bank's own sections in the sidebar, no tab strip in the
page), and `/learn/sepa`. In each, collapse and expand both the nav and the
concepts panel, drag both handles past their minimum to confirm the reverse
direction still flips the content mode, and reload to confirm the collapse
states persist. Then click a `[[wiki-link]]` in the concepts panel and switch
persona to confirm the panel survives the switch.

- [ ] **Step 8: Commit**

```bash
git add -A web/src
git commit -m "refactor(web): one shell per persona, not one app with filters"
```

---

## Task 5: The identity picker

One control, not a persona toggle plus a context picker: a persona without its
context is not an identity — "customer" alone addresses nothing, and a
two-control design has a state where the persona has changed and the context has
not.

**Files:**
- Create: `web/src/components/shell/identity-picker.tsx`
- Modify: `web/src/components/shell/topbar.tsx`
- Delete: `web/src/components/participant-switcher.tsx`

**Interfaces:**
- Consumes: `useIdentityDirectory` (Task 3); `homeFor`, `useIdentity` (Task 1);
  `Popover`, `Command*` from `@/components/ui/*`.
- Produces: `<IdentityPicker />`.

Built directly on Popover + cmdk rather than the generic `Combobox`, because
`Combobox` renders one flat option list and this needs three groups.

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
// Central bank / Banks / Customers, with customers under their bank. Selecting
// one navigates to its home. Frozen and Closed accounts are listed and
// selectable — seeing the customer view of a frozen account is a lesson.
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

            <CommandGroup heading="Central bank">
              <CommandItem
                value="central bank network reserves settlement"
                onSelect={() => go({ persona: "central-bank" })}
              >
                Central bank
              </CommandItem>
            </CommandGroup>

            <CommandGroup heading="Banks">
              {banks.map(({ participant }) => (
                <CommandItem
                  key={participant.id}
                  value={`bank ${participant.name} ${participant.id}`}
                  onSelect={() => go({ persona: "bank", pid: participant.id })}
                >
                  {participant.name}
                </CommandItem>
              ))}
            </CommandGroup>

            {banks.map(({ participant, accounts }) => (
              <CommandGroup key={participant.id} heading={`Customers · ${participant.name}`}>
                {accounts.map((account) => (
                  <CommandItem
                    key={account.id}
                    value={`customer ${account.name} ${participant.name} ${
                      account.identifiers.map((i) => i.value).join(" ")
                    }`}
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
`<ParticipantSwitcher />` used to be, in every shell.

Then:

```bash
git rm web/src/components/participant-switcher.tsx
```

Nothing else imports it (`app-shell.tsx` is already gone). Its
`ledger.lastParticipant` localStorage key goes with it — `/` is always the
lobby, so there is no last identity to remember.

`CreateParticipantDialog` was the switcher's `+` button as well as the
dashboard's action. It stays on `/central-bank`, where creating a member bank
belongs; check `web/src/app/central-bank/page.tsx` still renders it and drop
nothing.

- [ ] **Step 3: Run the gate**

Run: `cd web && npm run typecheck && npm run lint && npm run test && npm run build`
Expected: all clean.

- [ ] **Step 4: Load it**

Open the picker from `/central-bank`, `/bank/<pid>` and `/`. Confirm: the three
groups appear with customers nested under their bank; typing "aurora", "alice"
and an IBAN fragment (`1001`) each filter to the right rows; the trigger shows
the current identity; selecting a customer navigates to
`/customer/<pid>/<did>` (which 404s until Task 8 — expected here); selecting a
bank and the central bank land correctly.

- [ ] **Step 5: Commit**

```bash
git add -A web/src
git commit -m "feat(web): switch identities, not filters"
```

---

## Task 6: An accent per identity

**Files:**
- Create: `web/src/lib/accent.ts`, `web/src/lib/accent.test.ts`
- Modify: `web/src/components/shell/shell-frame.tsx`,
  `web/src/components/shell/{central-bank-shell,bank-shell}.tsx`

**Interfaces:**
- Consumes: `type Identity` (Task 1).
- Produces: `function accentFor(identity: Identity | null): string | undefined`
  — an `oklch()` string for `--identity-accent`.

- [ ] **Step 1: Write the failing test**

Create `web/src/lib/accent.test.ts`:

```ts
import { describe, expect, it } from "vitest";

import { accentFor } from "./accent";

describe("accentFor", () => {
  it("gives the central bank an institutional accent", () => {
    expect(accentFor({ persona: "central-bank" })).toMatch(/^oklch\(/);
  });

  it("is stable for a given bank", () => {
    expect(accentFor({ persona: "bank", pid: "part_1" })).toBe(
      accentFor({ persona: "bank", pid: "part_1" }),
    );
  });

  it("distinguishes different banks", () => {
    const a = accentFor({ persona: "bank", pid: "part_1" });
    const b = accentFor({ persona: "bank", pid: "part_2" });
    const c = accentFor({ persona: "bank", pid: "part_3" });
    expect(new Set([a, b, c]).size).toBeGreaterThan(1);
  });

  // You are a customer *of* Aurora, and the screen should say so without a
  // label — so a customer carries their bank's accent, not one of their own.
  it("gives a customer their bank's accent", () => {
    expect(accentFor({ persona: "customer", pid: "part_1", did: "dep_9" })).toBe(
      accentFor({ persona: "bank", pid: "part_1" }),
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

// Each identity carries an accent, set as --identity-accent on the shell root.
// A customer inherits their bank's: you are a customer *of* Aurora, and the
// screen should say so without a label.
//
// Hues are picked from a fixed palette by hashing the pid, so a bank's colour
// is stable across reloads and needs nothing persisted. Chroma and lightness
// are held constant so no bank is louder than another, and so the same values
// read in both themes.
const CENTRAL_BANK_ACCENT = "oklch(0.55 0.02 260)";
const BANK_HUES = [25, 145, 265, 330, 60, 200];

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
  return `oklch(0.58 0.14 ${hueFor(identity.pid)})`;
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `cd web && npx vitest run src/lib/accent.test.ts`
Expected: PASS.

- [ ] **Step 5: Set the variable and use it**

Add an `accent?: string` prop to `ShellFrame` and put it on the outermost
element of both the desktop and the mobile arrangement:

```tsx
<div
  className="h-screen overflow-hidden"
  style={accent ? ({ "--identity-accent": accent } as React.CSSProperties) : undefined}
>
```

Then use it in two places, both inside `ShellFrame`, so every persona gets it
for free:

- The topbar grows a 2px top rule:
  `<header className="… border-t-2 border-t-[color:var(--identity-accent,transparent)]">`
- The sidebar's brand block takes the accent as its text colour:
  `<span className="text-base font-semibold tracking-tight text-[color:var(--identity-accent,inherit)]">`

Pass `accent={accentFor(identity)}` from `central-bank-shell.tsx` and
`bank-shell.tsx`. `plain-shell.tsx` passes nothing, and the `transparent` /
`inherit` fallbacks in the arbitrary values are what make that render correctly.

- [ ] **Step 6: Run the gate**

Run: `cd web && npm run typecheck && npm run lint && npm run test && npm run build`
Expected: all clean.

- [ ] **Step 7: Load it**

Switch between the four member banks and the central bank and confirm the top
rule changes colour and stays the same colour for a given bank across reloads.
Check both light and dark theme.

- [ ] **Step 8: Commit**

```bash
git add -A web/src
git commit -m "feat(web): let each identity carry a colour, and a customer their bank's"
```

---

## Task 7: The bank's home is its customers

Today's bank overview is a thin card of internal account ids. A back office
opens on its customers.

**Files:**
- Modify: `web/src/app/bank/[pid]/page.tsx` (rewrite)
- Modify: `web/src/lib/identity.ts` (drop the Deposit accounts nav entry)
- Modify: `web/src/app/bank/[pid]/deposit-accounts/[did]/page.tsx:280` and
  `web/src/app/bank/[pid]/accounts/[aid]/page.tsx:82` (back-links)
- Delete: `web/src/app/bank/[pid]/deposit-accounts/page.tsx`

**Interfaces:**
- Consumes: `useDepositAccounts`, `useDepositBalance`, `useTotals`,
  `useAssetLookup`, `useReserve`, `OpenDepositAccountForm`.
- Produces: nothing new.

The list page and the bank home would otherwise be two routes showing the same
table, so the list folds into the home and its route goes. `/bank/[pid]/deposit-accounts/[did]`
and `…/statement` stay exactly where they are.

- [ ] **Step 1: Rewrite the bank home**

Replace `web/src/app/bank/[pid]/page.tsx`. Take `DepositAccountRow` verbatim
from `deposit-accounts/page.tsx` (repointing its href to
`` `/bank/${pid}/deposit-accounts/${account.id}` ``), keep the totals and
reserves cards from today's overview, and drop the "Internal accounts" card of
raw ids — a back office opens on customers, not on its own chart of accounts,
which is one click away under General ledger.

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

- [ ] **Step 2: Retire the list route**

```bash
git rm web/src/app/bank/'[pid]'/deposit-accounts/page.tsx
```

Edit `web/src/lib/identity.ts`: delete the
`{ href: `${base}/deposit-accounts`, label: "Deposit accounts", icon: Wallet }`
entry from the bank nav and drop `Wallet` from the icon import if nothing else
uses it. The bank's home is that list now.

Repoint the two back-links that pointed at the retired page:

| File:line | Old | New |
|---|---|---|
| `src/app/bank/[pid]/deposit-accounts/[did]/page.tsx:280` (`back`) | `` `/bank/${pid}/deposit-accounts` `` | `` `/bank/${pid}` `` |

Also update that page's back-link label from "Deposit accounts" to "Customer
accounts" (two occurrences, in the error branch and the normal branch).
`accounts/[aid]/page.tsx:82` links to a *detail* page and needs no change.

- [ ] **Step 3: Run the gate**

Run: `cd web && npm run typecheck && npm run lint && npm run test && npm run build`
Expected: all clean — `nav-integrity.test.ts` in particular, which is what
would catch the nav entry having been left pointing at the deleted page.

- [ ] **Step 4: Load it**

Open `/bank/<pid>`: reserves, customer deposits, and the customer list with
IBANs, statuses and available balances. Click through to an account, and back.
Confirm the sidebar no longer offers "Deposit accounts".

- [ ] **Step 5: Commit**

```bash
git add -A web/src
git commit -m "feat(web): open a back office on its customers"
```

---

## Task 8: The customer's shell, overview and activity

The only genuinely new surface. A retail bank app is a content column, not a
console — so this shell has **no left panel**, a top tab strip, and a
`max-w-2xl` column. The concepts rail stays, in all three shells including this
one: a retail app has no concepts rail, so this costs a little realism and buys
the thing the repository exists for. A customer screen that cannot explain
`balance-available` is a worse trade.

**Files:**
- Create: `web/src/components/shell/customer-shell.tsx`
- Create: `web/src/app/customer/[pid]/[did]/page.tsx`, `web/src/app/customer/[pid]/[did]/activity/page.tsx`
- Modify: `web/src/lib/identity.ts` (customer nav), `web/src/lib/identity.test.ts`
- Modify: `web/src/components/shell/persona-shell.tsx`
- Modify: `web/src/components/statement/statement-table.tsx` (a `retail` variant)

**Interfaces:**
- Consumes: `useDepositAccount`, `useDepositBalance`, `useStatement`,
  `useParticipant`, `useAssetLookup`; `ShellFrame`, `Topbar`.
- Produces: `<CustomerShell pid did>`; `StatementTable`'s `retail?: boolean`.

- [ ] **Step 1: Update the identity test for the customer's nav**

Edit `web/src/lib/identity.test.ts`: replace the "has no customer nav" case with

```ts
  it("gives a customer the screens they have, all under their own account", () => {
    const nav = navFor({ persona: "customer", pid: "part_1", did: "dep_9" });
    expect(nav.map((n) => n.href)).toEqual([
      "/customer/part_1/dep_9",
      "/customer/part_1/dep_9/activity",
    ]);
  });
```

and extend the "exactly one exact match" case's identity list with
`{ persona: "customer", pid: "part_1", did: "dep_9" } as const`.

(Send arrives in Task 9 and is appended to both this expectation and `navFor`
there, so the nav-integrity test never sees an href without a page.)

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

Add `Receipt` to the lucide import, and re-add `Wallet` if Task 7 removed it.

Run: `cd web && npx vitest run src/lib/identity.test.ts` — expect PASS on
`identity.test.ts`; `nav-integrity.test.ts` will now fail until Step 5 creates
the pages. That is the test doing its job.

- [ ] **Step 4: Give StatementTable a retail framing**

`StatementTable` renders an `AccountRef` per contra leg and an expandable
"Underlying GL transaction" panel listing every GL entry — the bank's chart of
accounts, linking into `/bank/[pid]/accounts/[aid]`. A customer sees none of
that. Edit `web/src/components/statement/statement-table.tsx`:

- Add to the props: `retail?: boolean`, documented as

```tsx
  // Retail framing: no contra column, no expandable GL detail, no
  // reconciliation banner. A customer's statement is dates, descriptions,
  // amounts and a running balance; the double entry behind it is the bank's
  // business, and linking to it would navigate out of the persona.
  retail?: boolean;
```

- Wrap the `<TableHead>Contra</TableHead>` and the `<TableCell><ContraCell …/></TableCell>`
  in `{!retail && (…)}`.
- Make the row non-interactive under `retail`: `className={cn(!retail && "cursor-pointer")}`
  and `onClick={retail ? undefined : () => setOpenTx(…)}`.
- Guard the expansion row with `{!retail && openTx === row.txId && (…)}`.
- Guard the whole `book != null && (reconciles ? … : …)` block with `!retail`.
- The expansion `<TableCell colSpan={5}>` becomes `colSpan={retail ? 4 : 5}` —
  unreachable under `retail`, but wrong is wrong.
- The empty-state copy is back-office ("Fund the account or post one"). Under
  `retail` render "No activity yet." instead.

- [ ] **Step 5: Write the customer shell and its two screens**

Create `web/src/components/shell/customer-shell.tsx`:

```tsx
"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { cn } from "@/lib/utils";
import { ShellFrame } from "./shell-frame";
import { Topbar } from "./topbar";
import { accentFor } from "@/lib/accent";
import { navFor, type Identity } from "@/lib/identity";

// A retail bank app is a content column, not a console: no left panel, a top
// tab strip, and a narrow column. That is what makes the switch unmistakable.
// The concepts rail stays — a little realism traded for the thing this
// repository exists for.
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

Edit `web/src/components/shell/persona-shell.tsx` to add, before the fallback:

```tsx
  if (identity?.persona === "customer")
    return (
      <CustomerShell pid={identity.pid} did={identity.did}>
        {children}
      </CustomerShell>
    );
```

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
// the back office.
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

  // The headroom below zero, which a customer thinks of as part of what they
  // can spend and the bank does not: the available balance already includes it.
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
            arrive — an incoming payment is a credit, and the block is on
            debits.
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
              <Hint id="overdraft" />
            </p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
```

Create `web/src/app/customer/[pid]/[did]/activity/page.tsx`:

```tsx
"use client";

import { useParams } from "next/navigation";

import { ErrorState } from "@/components/error-state";
import { Skeleton } from "@/components/ui/skeleton";
import { StatementTable } from "@/components/statement/statement-table";
import { useAssetLookup, useDepositAccount, useStatement } from "@/lib/api/hooks";

// The same projection the back office reads, framed for the person whose money
// it is: no contra accounts, no double entry, no reconciliation check.
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

Pick a customer from the picker or the lobby. Confirm: no left nav, a narrow
column, a top tab strip, the concepts rail present and expandable, and the
account's own accent matching its bank's. Check Alice Andersson (active, holds —
the on-hold figure should be non-zero), Bianca Belli (frozen — the alert
appears), Bruno Bianchi (an arranged overdraft — the headroom line appears),
Annie Ahlberg (dormant) and Closed Account (closed, empty activity). On
Activity, confirm there is no Contra column, rows do not expand, and no
reconciliation banner. Click a `?` to confirm the concepts panel still opens
from within the customer shell.

- [ ] **Step 8: Commit**

```bash
git add -A web/src
git commit -m "feat(web): give a customer a point of view"
```

---

## Task 9: Sending money to an IBAN

The screen that required sub-project 5. A payee is entered as an IBAN and
resolved live through `GET /directory` to a name and a bank before the customer
confirms — because routing is by id, and an IBAN is not one.

**Files:**
- Modify: `web/src/lib/types.ts` (`DirectoryEntry`)
- Modify: `web/src/lib/api/endpoints.ts`, `query-keys.ts`, `hooks.ts`
- Create: `web/src/lib/use-debounced-value.ts`
- Create: `web/src/app/customer/[pid]/[did]/send/page.tsx`
- Modify: `web/src/lib/identity.ts`, `web/src/lib/identity.test.ts`

**Interfaces:**
- Consumes: `useDepositAccount`, `useDepositBalance`, `useSchemes`,
  `useInitiatePayment`, `useAssetLookup`, `MoneyInput`, `describeError`.
- Produces:
  - `interface DirectoryEntry { participant: string; account: string; name: string; asset: string; identifier: AccountIdentifier }`
  - `function resolveIdentifier(scheme: string, value: string): Promise<DirectoryEntry>`
  - `function useResolveIdentifier(scheme: string, value: string)`
  - `function useDebouncedValue<T>(value: T, delayMs: number): T`

- [ ] **Step 1: Add the directory to the data layer**

`web/src/lib/types.ts` — add beside `AccountIdentifier`:

```ts
// What GET /directory answers: enough to tell a caller who an address belongs
// to before they pay it. `identifier` is echoed back so a client that fired
// several lookups at once can tell the answers apart. See
// api/handlers_directory.go's directoryEntryDTO.
export interface DirectoryEntry {
  participant: string;
  account: string;
  name: string;
  asset: string;
  identifier: AccountIdentifier;
}
```

`web/src/lib/api/endpoints.ts` — add `DirectoryEntry` to the type import and a
new section after `// --- Schemes ---`:

```ts
// --- Directory ------------------------------------------------------------

// Network-scoped rather than participant-scoped: resolving an address is
// exactly the question "which bank?", so a route that already named the bank
// would answer nothing. 404 when nobody holds it, 409 when two banks claim it.
export function resolveIdentifier(scheme: string, value: string): Promise<DirectoryEntry> {
  return request("GET", `/directory${qs({ scheme, value })}`);
}
```

`web/src/lib/api/query-keys.ts` — add above the payment-network block:

```ts
  // Network-wide: an address belongs to whoever holds it, not to a bank you
  // name up front.
  directory: (scheme: string, value: string) => ["directory", scheme, value] as const,
```

`web/src/lib/api/hooks.ts` — add beside the scheme hooks:

```ts
// Resolves an external address to the account that holds it. `retry: false`
// because a 404 here is an answer — nobody holds that IBAN — and retrying it
// three times only delays telling the customer so.
export function useResolveIdentifier(scheme: string, value: string) {
  return useQuery({
    queryKey: qk.directory(scheme, value),
    queryFn: () => api.resolveIdentifier(scheme, value),
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

// Settles a value that changes per keystroke. The send form resolves a typed
// IBAN through the directory as you type; without this it would fire a request
// per character, and each miss along the way would be cached under its own key.
export function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setSettled(value), delayMs);
    return () => clearTimeout(t);
  }, [value, delayMs]);
  return settled;
}
```

- [ ] **Step 3: Add Send to the customer's nav**

Edit `web/src/lib/identity.test.ts`, extending the customer nav expectation to
the final three, in the order the tab strip shows them:

```ts
    expect(nav.map((n) => n.href)).toEqual([
      "/customer/part_1/dep_9",
      "/customer/part_1/dep_9/send",
      "/customer/part_1/dep_9/activity",
    ]);
```

Run `cd web && npx vitest run src/lib/identity.test.ts` — expect FAIL. Then edit
`web/src/lib/identity.ts`'s `case "customer"` to insert
`{ href: `${base}/send`, label: "Send", icon: Send }` between Account and
Activity, adding `Send` to the lucide import. Re-run — expect PASS on
`identity.test.ts` and FAIL on `nav-integrity.test.ts` until Step 4.

- [ ] **Step 4: Write the send screen**

Create `web/src/app/customer/[pid]/[did]/send/page.tsx`:

```tsx
"use client";

import { useState } from "react";
import { useParams, useRouter } from "next/navigation";
import { toast } from "sonner";

import { Card, CardContent } from "@/components/ui/card";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { FieldLabel } from "@/components/field-label";
import { MoneyInput, Money } from "@/components/money";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import {
  useAssetLookup,
  useDepositAccount,
  useDepositBalance,
  useInitiatePayment,
  useResolveIdentifier,
  useSchemes,
} from "@/lib/api/hooks";
import { useDebouncedValue } from "@/lib/use-debounced-value";
import { describeError } from "@/lib/api/errors";

// A retail "send money" is a SEPA credit transfer: a push scheme needing no
// mandate, addressed by IBAN. Naming it here rather than offering a scheme
// picker is the point — a customer picks a payee, not a clearing arrangement.
const SEND_SCHEME = "sepa.ct";

export default function CustomerSend() {
  const params = useParams();
  const router = useRouter();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const did = typeof params.did === "string" ? params.did : "";

  const { data: account, error: accountError } = useDepositAccount(pid, did);
  const { data: balance } = useDepositBalance(pid, did);
  const { data: schemes } = useSchemes();
  const { byCode } = useAssetLookup();
  const initiate = useInitiatePayment();

  const [iban, setIban] = useState("");
  const [amount, setAmount] = useState<number | null>(null);
  const [reference, setReference] = useState("");

  // Resolved live as you type, settled first so a keystroke is not a request.
  const settledIban = useDebouncedValue(iban.trim(), 350);
  const payee = useResolveIdentifier(settledIban ? "IBAN" : "", settledIban);

  const asset = account ? byCode.get(account.asset) : undefined;
  const scheme = schemes?.find((s) => s.id === SEND_SCHEME);
  const frozen = account?.status === "Frozen";
  const closed = account?.status === "Closed";
  const ownIban = account?.identifiers.find((i) => i.scheme === "IBAN");

  if (accountError) return <ErrorState error={accountError} />;
  if (!account || !asset) return <Skeleton className="h-64 w-full" />;

  // The scheme settles in one asset and this account holds one; a mismatch is
  // not a form error the customer can fix, so it is stated rather than hidden.
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

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!canSend || !payee.data) return;
    try {
      const p = await initiate.mutateAsync({
        scheme: SEND_SCHEME,
        // Routing is by id, which is why the IBAN had to be resolved. The
        // identifier is quoted so the payment records the address it was
        // reached by; initiation would back-fill it either way.
        debtor: { participant: pid, account: did },
        creditor: {
          participant: payee.data.participant,
          account: payee.data.account,
          identifier: payee.data.identifier,
        },
        amount: amount!,
        description: reference.trim() || undefined,
      });
      toast.success(`Sent (${p.id})`);
      setIban("");
      setAmount(null);
      setReference("");
      router.push(`/customer/${pid}/${did}/activity`);
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

      <Card>
        <CardContent>
          <form onSubmit={submit} className="space-y-4">
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

            <Button type="submit" disabled={!canSend || initiate.isPending}>
              {initiate.isPending ? "Sending…" : "Send"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}

// What the directory said about the address typed so far. A miss is an answer
// and is stated plainly; an ambiguous address (two banks claiming it) is a 409
// and describeError names it.
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
  if (error) {
    return <p className="text-xs text-destructive">{describeError(error)}</p>;
  }
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
```

Add `useParticipants` to the existing `@/lib/api/hooks` import at the top of the
file — one import statement, not a second one; lint flags a duplicate.

- [ ] **Step 5: Run the gate**

Run: `cd web && npm run typecheck && npm run lint && npm run test && npm run build`
Expected: all clean. (`MoneyInput` already accepts `disabled`, and `Alert`,
`AlertTitle` and `AlertDescription` are all exported from
`@/components/ui/alert` — verified, nothing to add.)

- [ ] **Step 6: Load it and send real money**

As Alice Andersson (Aurora), open Send:

- Type `IT60-VERDE-2002` → resolves to "Bella Bruno at Banca Verde".
- Type `NOBODY-0001` → the 404 surfaces as a plain miss, and Send stays disabled.
- Type Alice's own IBAN → "That is this account's own IBAN", Send disabled.
- Send 25.00 with a reference; expect a success toast and a redirect to
  Activity showing the debit, with the available balance down by 25.00.
- Check `/central-bank/payments` shows the new payment, addressed by IBAN on
  both legs.
- As Bianca Belli (frozen), open Send: the alert appears, every field is
  disabled, and Send is disabled. Then confirm money can still *arrive* — send
  her 10.00 from Alice and see it on Bianca's Activity.

- [ ] **Step 7: Commit**

```bash
git add -A web/src
git commit -m "feat(web): let a customer pay an IBAN"
```

---

## Task 10: Close it out

**Files:**
- Modify: `docs/expansion-roadmap.md`
- Modify: `web/CLAUDE.md` (the Routing paragraph)

- [ ] **Step 1: Correct the routing documentation**

`web/CLAUDE.md`'s **Routing** paragraph still describes the old tree
("Network-wide pages at `src/app/{payments,mandates,…}`… Participant-scoped
pages under `src/app/participants/[pid]/`; to add a section, append to the
`tabs` array in `[pid]/layout.tsx`"). Every clause of it is now false. Replace
with:

```markdown
**Routing is by persona.** Who you are is the top-level structure: `/central-bank/…`
(network: reserves, payments, mandates, cycles, settlements, schemes, audit),
`/bank/[pid]/…` (one bank's back office), `/customer/[pid]/[did]/…` (one deposit
account, retail-framed). `/` is a lobby and never redirects; `/learn/*` sits
outside the persona system. `src/lib/identity.ts` derives the `Identity` from the
pathname and owns `homeFor`/`navFor` — **to add a section, add its entry to
`navFor` and its `page.tsx`, and `src/lib/nav-integrity.test.ts` will hold the
two together.** Old `/participants/…` links are forwarded by
`app/participants/[...rest]/page.tsx`. Each persona gets its own shell from
`components/shell/`; the customer's has no left panel.
```

No other layer needs a change: this sub-project moved no domain fact, so
`README.md`, `hint-content.ts`, the quiz's *content* and the schema comments all
still say the same true things. (The quiz's `explore.href` targets moved in Task
2 — that is a link, not a claim.)

- [ ] **Step 2: Log the sub-project**

Append a row to the log table at the foot of `docs/expansion-roadmap.md`, in the
established style: dated, saying what was settled and why, not what was done.
Cover at least: identity derived from the URL and persisted nowhere; three
genuinely different shells rather than one filtered by role; one flat picker of
complete identities because a persona without its context addresses nothing; the
lobby always at the root, trading a repeat visitor's click for the newcomer's
orientation; the concepts rail kept in the customer shell against realism; and
the two things the spec undercounted — the quiz's ~35 `explore.href` deep-links,
which the new `nav-integrity.test.ts` now holds against the route tree, and
`StatementTable` needing a `retail` variant because reusing it whole would have
leaked the bank's chart of accounts into the customer's statement and linked out
of the persona.

Mark sub-project 6 complete wherever the roadmap tracks status.

- [ ] **Step 3: Final verification**

From `web/`:

```bash
npm run typecheck && npm run lint && npm run test && npm run build
```

From the repo root, confirming the backend is untouched:

```bash
go build ./... && go test ./... 2>&1 | tail -20
git diff --stat main -- ':!web' ':!docs'
```

Expected: all green, and the last command prints nothing — no Go file changed.

Then, with the backend and `npm run dev` running, load **one page in each of the
three shells plus the lobby and Learn** — this is the `CLAUDE.md` rule, because
a `[[wiki-link]]` to a missing key takes every route down at runtime while
`next build` stays green:

- `/` — lobby
- `/central-bank/settlements` — central-bank shell
- `/bank/<pid>/ledger` — bank shell
- `/customer/<pid>/<did>/send` — customer shell
- `/learn/sepa` — plain shell

On each, open the concepts panel and follow one `[[wiki-link]]`. Check the
browser console is free of errors.

- [ ] **Step 4: Commit**

```bash
git add docs/expansion-roadmap.md web/CLAUDE.md
git commit -m "docs: record what the persona split settled"
```

---

## Self-review notes

Checked against the design spec, section by section:

- **Goal, three personas** — Tasks 1–9.
- **Out of scope** — honoured throughout: no `.go` file is touched (Global
  Constraints, verified in Task 10 Step 3); no scheme-operator persona (cycles
  and payments sit in `CENTRAL_BANK_NAV`, with the compromise noted in the code
  comment); no card-processor persona; no customer mandate, credit, loan or
  overdraft-terms screen (the customer's nav has exactly three entries); no
  bank-scoped mandates view (mandates are central-bank only); no products
  screen; no party master (a customer identity is `(pid, did)`).
- **Identity from the URL, persisted nowhere** — Task 1; `participant-switcher.tsx`
  and its `ledger.lastParticipant` key are deleted in Task 5, not adapted.
- **Routes** — Task 2 moves every one listed, and adds the catch-all redirect.
  Two spec routes needed a decision the spec did not make: today's
  `/central-bank` page holds *both* reserves and the central-bank audit, and the
  spec lists them as separate routes, so Task 2 Step 4 splits it; and the spec's
  bank list has no `/bank/[pid]/deposit-accounts`, so Task 7 folds it into the
  bank home and retires the route.
- **Three shells** — Task 4, plus a fourth (`plain-shell`) the spec did not
  name but implies by making `useIdentity()` return null on `/` and `/learn/*`;
  it is `ShellFrame` with no sidebar, the same arrangement the customer shell
  uses. `ConceptPanelProvider` moves to the root layout in Task 4 Step 4.
- **Concepts panel in all three shells** — Task 4 (it lives in `ShellFrame`, so
  no shell can omit it), verified in Task 8 Step 7 and Task 10 Step 3.
- **Accent per identity, customer inherits its bank's** — Task 6, tested.
- **One flat grouped picker** — Task 5, including frozen and closed accounts.
- **Lobby always the root** — Task 3. Task 2 leaves an interim redirect there
  for exactly one commit, called out where it happens.
- **Bank is mostly a re-home** — Tasks 2 and 7; the tab strip becomes the
  sidebar in Task 4 Step 5.
- **Customer surface** — Tasks 8 and 9, including the frozen account's debit
  block and the lesson that money can still arrive.
- **Testing** — `identity.test.ts` (Task 1), the nav-integrity test (Task 2,
  extended to `EXPLORE_ROUTES`), `accent.test.ts` (Task 6);
  `concept-links.test.ts` and `quiz/diversity.test.ts` are in every task's gate;
  a page is loaded in each shell in Task 10.
- **Failure modes** — the route move is covered by the integrity test plus the
  redirect plus a click-through in Task 2 Step 10; a customer identity pointing
  at a missing account renders the existing not-found treatment via
  `useDepositAccount`'s error branch; the picker and the lobby share
  `useIdentityDirectory` rather than each fetching; the concepts panel keeps its
  own resizable panel, since the `max-w-2xl` constraint is on the content
  column, not the viewport.
