# Spec — Customer account statement (GL-derived)

**Date:** 2026-06-23
**Status:** Approved design, ready for implementation plan
**Area:** `web/` frontend only — no backend change

## Goal & teaching point

Make a customer **deposit account** feel like a real bank account: open it and see
**every transaction that hit it** with a **running balance**, while making explicit
that there is **no separate ledger** behind the account. The per-account statement is
*derived* — it is the bank's **General Ledger filtered to the account's backing GL
account and projected onto that one leg**.

The backing account (`DepositAccount.glAccount`) is a **liability** under
`Participant.customerSubledger`. The whole feature is a **client-side projection** of
data already returned by `useTransactions(pid, account.glAccount)` — the API already
filters the GL by account.

## Scope

**In:**
- A `projectStatement(...)` pure function: GL transactions → ordered statement rows +
  running balances, designed as a tagged union so future line-item kinds slot in.
- A compact **StatementCard** on the deposit detail page (most recent 10 rows).
- A full **statement sub-route** `…/deposit-accounts/[did]/statement` (all rows).
- Inline-expandable rows revealing the full double-entry; reconciliation tie-out to
  the Balance card's `book`.

**Out (separate future specs — design leaves room, builds nothing):** interest
accrual, fees, formal statement periods/cycles, value-dating on deposits, multi-
currency / IBAN at account level.

## Decisions (resolved during brainstorming)

| # | Decision |
|---|---|
| Scope | Statement view only, **designed to extend** (tagged-union row model). |
| Placement | **Compact card on detail page (recent 10) + dedicated sub-route (all rows)**, sharing one projection + one table component. |
| Amount display | **Refined hybrid:** signed amount (`+€100 / −€40`) with color; contra account always visible in the row; **no** per-row Dr/Cr tag (redundant with sign). |
| Direction teaching | A one-time `?` `Hint` on the Amount header explains the liability convention; Dr/Cr badges appear in the **expanded** double-entry, not on every row. |
| Double-entry reveal | **Inline expand** (not the existing transaction dialog), so the projected leg and its source transaction sit together; this account's leg highlighted. |
| Reversals | **Both lines kept with full amounts** (they net to zero in the running balance); original badged `Reversed` (from `status`), reversal badged `reverses {reversalOf}`. |
| Holds | **Excluded** from the statement (they post nothing to the GL). A one-line footnote links to the Holds card. |
| Ordering | Accumulate oldest→newest by `valueDate`, tiebreak `createdAt`; display newest-first. |
| Reconciliation | Final running balance must equal `book`; surface a green tie-out when equal, a muted warning if not. |
| States | Reuse `DataTable`/`ErrorState` patterns for loading/empty/error; **closed accounts still show full history**; no pagination (backend returns the full list; card slices to recent 10). |

## Architecture & data flow

```
useTransactions(pid, account.glAccount)  ──┐
useDepositBalance(pid, did) → book         ├─▶ projectStatement(txs, glAccount, knownAccounts)
useParticipant(pid) → well-known accounts ─┘        │
                                                    ▼
                                  { rows: StatementRow[] (newest-first), finalBalance }
                                                    │
                         ┌──────────────────────────┴───────────────────────────┐
                         ▼                                                        ▼
              StatementCard (detail page)                          StatementView (sub-route)
              rows.slice(0, 10) + "View full statement →"          all rows
                         └──────────── share ──── StatementTable ──── share ──────┘
                                    (rows + book + expand state + tie-out)
```

### The projection (`web/src/lib/statement.ts`)

Pure, unit-testable, no React. The teaching core lives here.

```ts
export type ContraRef =
  | { kind: "single"; accountId: string; label?: string } // label if a known account
  | { kind: "split"; count: number };                       // 3+ legs → show "Split"

export type StatementRow = {
  kind: "gl-leg";                 // tagged union — future: "interest" | "fee"
  txId: string;
  date: string;                   // valueDate (display + sort)
  description?: string;
  direction: Direction;           // the backing-account leg's direction
  delta: number;                  // signed cents: Credit +amount, Debit −amount
  runningBalance: number;         // cents after this row (liability convention)
  contra: ContraRef;              // the other leg(s) — "where it came from / went to"
  status: TransactionStatus;
  isReversed: boolean;            // status === "Reversed" (original later undone)
  reversalOf?: string;            // set when this row IS a reversal
  transaction: Transaction;       // full legs, for the inline expand
};

export function projectStatement(
  txs: Transaction[],
  glAccount: string,
  knownAccounts?: Record<string, string>, // accountId → friendly label
): { rows: StatementRow[]; finalBalance: number };
```

Algorithm:
1. Keep transactions with an entry where `accountId === glAccount` (defensive; API
   already filters).
2. Sort **ascending** by `valueDate`, tiebreak `createdAt`.
3. `running = 0`; per transaction: sum legs on `glAccount` into `delta`
   (Credit `+amount`, Debit `−amount` — usually one leg; sum if more). `running += delta`.
   Contra = the other legs: exactly one → `single` (labeled via `knownAccounts`);
   2+ → `split`.
4. `finalBalance = running`.
5. Return rows **reversed** (newest-first); each row's `runningBalance` is the
   accumulated value *at that point* — computed over the **full** history, never over a
   slice.

`knownAccounts` is built from `Participant` (`suspenseAccount` → "suspense",
`reserveAccount` → "reserve", `settlementAccount` → "settlement"); unknown contra IDs
render as raw `IdText`.

### Components (`web/src/components/statement/`)

- **`StatementTable`** — built on the shadcn `Table` primitives (like the transactions
  dialog uses bespoke markup). Columns: Date · Description (+ reversal/Reversed badges)
  · Contra · Amount (signed, `AmountCell`, `?` hint on header) · Balance. Clicking a row
  toggles an inline expanded sub-row showing all legs with `DirectionBadge` (Dr/Cr), the
  `glAccount` leg highlighted + tagged "this account". Renders the tie-out footer.
  *Rationale:* `DataTable` has no expandable-row or footer support; a dedicated table is
  cleaner than overloading the shared one. Row rendering switches on `row.kind` to stay
  open to future kinds.
- **`StatementCard`** — wraps `StatementTable` with recent 10 rows for the detail page;
  header + "View full statement →" link; holds footnote.
- **`StatementView`** — the sub-route page body: full history via the same table.

### Page wiring

- `…/[did]/page.tsx` — add `<StatementCard pid did account />` after `SnapshotsCard`.
- `…/[did]/statement/page.tsx` — new client page (`useParams()`), back-link to the
  account, renders `StatementView`.

### Hints (`web/src/components/hint-content.ts`)

Add entries (new keys are automatic via `HintKey`):
- `statement` — what a derived statement is; links `[[double-entry]]`, `[[normal-balance]]`.
- `statement-amount` — the liability sign convention (credit = increase); links
  `[[normal-balance]]`.

## UI behavior & states

- **Loading:** skeleton rows (match `DataTable`).
- **Empty:** "No transactions yet. Fund the account or post one to see it here."
- **Error:** `ErrorState` + retry.
- **Closed account:** statement still fully rendered (read-only history).
- **Reconciliation:** compare `finalBalance` to `useDepositBalance().book`; equal →
  green "Reconciles to book €X"; unequal → muted warning (signals a projection/data gap).
- **Holds footnote:** "Holds reduce *available* balance but don't appear here until
  captured — see Holds above."

## Reuse map (don't rebuild)

`Money` / `AmountCell` (signed + color) · `IdText` · `DirectionBadge` / `EnumBadge` ·
`Hint` (+ `hint-content.ts`) · `Card` · `formatDate` (`lib/dates.ts`) · `ErrorState` ·
shadcn `Table` primitives. Hooks: `useTransactions`, `useDepositBalance`,
`useDepositAccount`, `useParticipant`.

## Verification

```bash
go run ./cmd/server                 # repo root :8080 (in-memory, resets)
cd web && npm run dev               # :3000
npm run typecheck && npm run lint && npm run build   # all clean
```

- **Unit:** `projectStatement` — running balance, Credit/Debit signing, multi-leg
  contra (single vs split), reversal pairs net to zero, ordering & tiebreak, final
  balance equals sum of deltas. (Pure function ⇒ testable even without a FE runner;
  if none exists, note it and verify in-browser.)
- **Browser:** create participant → open deposit account → fund → post a few
  transactions → reverse one. Confirm: newest-first order, signed amounts + color,
  contra labels, expand shows balanced legs with this account highlighted, reversal
  badges present, and the **top running balance equals the Balance card's book**.

Backend gotchas (`web/CLAUDE.md`): `DisallowUnknownFields()`; `*time.Time` wants
RFC3339 (snapshot `date` is the exception, `YYYY-MM-DD`); Next 16 async params via
`useParams()` in client pages.

## Open to extend (built later, not now)

`StatementRow` is a tagged union and the table switches on `kind`; `projectStatement`
returns a normalized stream. Adding `{ kind: "interest" }` / `{ kind: "fee" }` later is
a new variant + a new render branch — no change to the GL-leg path, the card, or the
sub-route.
