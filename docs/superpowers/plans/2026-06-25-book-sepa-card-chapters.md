# SEPA + Card Transactions Book Chapters Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split the book's Chapter 11 ("Payment Schemes") so SEPA and card transactions each get their own chapter, with Chapter 11 retaining the shared machinery.

**Architecture:** Trim ch.11 to the conceptual foundation (netting, worked example, scheme axes, instant-payments forward-look). Relocate the SEPA + Returns prose into a new ch.12. Write a net-new domain chapter for cards as ch.13. Renumber the records chapter 12→14 and flip all build/index wiring in one atomic commit.

**Tech Stack:** Markdown chapters in `book/`, assembled into EPUB3 by `book/build.sh` (pandoc + perl).

## Global Constraints

- **Domain voice, no code.** No code, function names, or APIs in any chapter. Reference "our reference library" only as light abstract prose, ≤1 sentence, matching the style in chs. 2/4/5/7/8/10/11.
- **No changes to the service / library.** Book-only change; touch nothing outside `book/` and the `docs/superpowers/` plan/spec.
- **Card scope is exactly three topics:** four-party model; authorization→presentment (incl. single-message variant); authorized≠final amount. **No** interchange economics or chargebacks/disputes.
- **Card authorization links to the "hold" concept from Chapter 7; capture → the debtor leg.** No "this isn't implemented" disclaimer.
- **Chapter 11 keeps the title "Payment Schemes."** The detailed **Returns** section moves to the SEPA chapter; the "returns allowed?" *axis* stays in ch.11.
- **Numbers add up and postings balance**, consistent with the rest of the book.
- Final layout: Part IV = chs. 9, 10, 11, 12 (SEPA), 13 (Cards); Part V = ch. 14 (Records).

---

## Task 1: Create Chapter 12 — SEPA (relocated content)

Relocate the named-scheme prose out of ch.11 into a new, self-contained chapter. (Ch.11 still holds the originals at this point; the duplication is removed in Task 3. The new file is not yet in `build.sh`, so the EPUB is unaffected.)

**Files:**
- Create: `book/12-sepa.md`
- Source (read-only reference for relocation): `book/11-payment-schemes.md:120-167`

**Interfaces:**
- Produces: `book/12-sepa.md` with H1 `# Chapter 12 — SEPA: Credit Transfers and Direct Debits` and sections "SEPA Credit Transfer", "SEPA Direct Debit", "Returns".

- [ ] **Step 1: Draft the new chapter file**

Create `book/12-sepa.md` with this structure:

1. H1: `# Chapter 12 — SEPA: Credit Transfers and Direct Debits`
2. **Intro (new, ~1 short paragraph):** ch.11 gave us the shared machinery and the axes that distinguish schemes; now meet the two net-settled SEPA schemes by name. Cross-reference the worked credit-transfer posting example in ch.11 ("the worked example in Chapter 11 *was* a SEPA credit transfer").
3. **## SEPA Credit Transfer — a push:** relocate `book/11-payment-schemes.md:120-133` verbatim (the SCT description + the `pacs.008` / ISO 20022 note), adjusting only the sentence that says "The worked example above *was* a credit transfer" to instead point back to Chapter 11.
4. **## SEPA Direct Debit — a pull, with a mandate:** relocate `book/11-payment-schemes.md:135-155` verbatim (SDD, the mandate checks, the shared-machinery payoff, `pacs.003`).
5. **## Returns: unwinding a settled payment:** relocate `book/11-payment-schemes.md:157-167` verbatim.
6. **Closing transition (new, ~2 sentences):** SEPA moves money debtor→creditor by agreement between two parties; cards add a different *shape* — a four-party network and a two-step authorize-then-settle life. Lead into Chapter 13.

- [ ] **Step 2: Verify headings and the no-code rule**

Run: `grep -nE '^#' book/12-sepa.md && grep -nE '\.go|func |CreateHold|ledger\.|deposit\.' book/12-sepa.md || echo "no-code OK"`
Expected: H1 + three `##` headings (SCT, SDD, Returns) printed; then `no-code OK`.

- [ ] **Step 3: Commit**

```bash
git add book/12-sepa.md
git commit -m "Add SEPA chapter (relocated from payment-schemes)"
```

---

## Task 2: Create Chapter 13 — Card Transactions (net-new)

A net-new domain chapter in the book's established voice. Beat sheet below; draft full prose matching the cadence of chs. 7 and 11 (concrete examples, balanced postings where shown, light "reference library" nods only).

**Files:**
- Create: `book/13-card-transactions.md`
- Voice references (read-only): `book/07-balances-and-holds.md` (holds/auth-capture), `book/11-payment-schemes.md` (scheme axes, closing-transition style)

**Interfaces:**
- Produces: `book/13-card-transactions.md` with H1 `# Chapter 13 — Card Transactions` and sections for the four-party model, authorization→presentment, authorized≠final amount, and reusing the rails.

- [ ] **Step 1: Draft the chapter**

Create `book/13-card-transactions.md`:

1. H1: `# Chapter 13 — Card Transactions`
2. **Intro (~1 paragraph):** A card payment is another scheme, but with a different *shape* than SEPA. Its authorization is exactly the **hold** of Chapter 7 — a reservation against available balance that posts nothing yet. Preview the two structural differences: more parties, and a deliberate gap between authorizing and actually moving the money.
3. **## The four-party model:** introduce cardholder, issuer (cardholder's bank), acquirer (merchant's bank), merchant, and the card network/scheme that connects the two banks. Contrast with SEPA's two-party debtor→creditor flow: the issuer authorizes and ultimately debits the cardholder; the acquirer credits the merchant; the network nets between the banks. Keep it conceptual — name the roles and who faces whom.
4. **## Authorization → presentment:** the **dual-message** flow — at the point of sale the issuer *authorizes* (real-time check + hold; no money moves, book balance unchanged), then later the acquirer submits the **presentment** (a.k.a. clearing record) and the issuer posts the actual debit. Then the **single-message** variant (PIN debit / ATM), where authorization and clearing are fused into one step with no separate presentment. Tie authorize→hold (ch.7), present/capture→the debtor leg.
5. **## Authorized ≠ final amount:** why the held amount and the captured amount can differ — the gas-pump pre-auth (e.g. a fixed hold, settled for whatever was pumped), restaurant tips added after auth, and partial/incremental captures. Connect to holds (ch.7: a hold reduces *available*, not *book*) and to account states (ch.8: a *frozen* account blocks a card authorization).
6. **## Reusing the rails:** once captured, the card amount becomes an ordinary debtor→creditor flow that nets and settles through the same machinery as Chapter 11 — card networks net much like SEPA, so clearing and settlement (chs. 9–11) need no new mechanism. One sentence of "reference library" nod is allowed here (the library already models authorization as holds and has capture; a full card scheme would extend from there).
7. **Closing transition to Part V:** relocate the spirit of the old ch.11 closing here — we have now followed money from a single entry all the way to settlement across every scheme; the last part of the book steps back from movement to *records*: how a bank snapshots, audits, and reports on what we've watched happen.

- [ ] **Step 2: Verify headings, scope, and no-code rule**

Run: `grep -nE '^#' book/13-card-transactions.md && grep -niE 'interchange|chargeback' book/13-card-transactions.md && echo "SCOPE LEAK" || echo "scope OK"`
Expected: H1 + four `##` headings; then `scope OK` (no interchange/chargeback content).

Run: `grep -nE '\.go|func |CreateHold|ledger\.|deposit\.' book/13-card-transactions.md || echo "no-code OK"`
Expected: `no-code OK`.

- [ ] **Step 3: Commit**

```bash
git add book/13-card-transactions.md
git commit -m "Add Card Transactions chapter"
```

---

## Task 3: Trim Chapter 11 to the shared machinery

Remove what now lives in chs. 12–13; keep the foundation; rewire the intro and closing.

**Files:**
- Modify: `book/11-payment-schemes.md`

- [ ] **Step 1: Remove the relocated sections**

Delete these three sections in full (now in ch.12): "## SEPA Credit Transfer — a push", "## SEPA Direct Debit — a pull, with a mandate", and "## Returns: unwinding a settled payment" (`book/11-payment-schemes.md:120-167`).

- [ ] **Step 2: Remove the card forward-look, keep instant payments**

In the "## What's deliberately left out, and what comes next" section, delete the **Card transactions** bullet (`:190-194`) and the following account-states paragraph's card example clause. Keep the **Instant payments** bullet (it is ch.11's "gross" settlement example). Replace the deleted card bullet with a single pointer sentence, e.g. "Card transactions get their own chapter next (Chapter 13)."

- [ ] **Step 3: Rewrite the intro and closing to point forward**

- Intro (`:3-8`): adjust the last sentence so the chapter promises the *machinery and the scheme model*, with the named schemes (SEPA, cards) introduced in the chapters that follow — rather than implying SCT/SDD/cards are covered here.
- Closing (`:203-206`): replace the "The last part of the book steps back from movement to records" transition (that line now belongs at the end of ch.13) with a 1–2 sentence lead-in to the named-scheme chapters: SEPA first (Chapter 12), then cards (Chapter 13).

- [ ] **Step 4: Verify the trim**

Run: `grep -nE '^## ' book/11-payment-schemes.md`
Expected: headings for Netting, the worked credit transfer, the scheme abstraction, and "what's deliberately left out" — and **no** "SEPA Credit Transfer", "SEPA Direct Debit", or "Returns" headings.

Run: `grep -ni 'last part of the book steps back' book/11-payment-schemes.md && echo "STALE CLOSING" || echo "closing OK"`
Expected: `closing OK`.

- [ ] **Step 5: Commit**

```bash
git add book/11-payment-schemes.md
git commit -m "Trim Chapter 11 to the shared payment-scheme machinery"
```

---

## Task 4: Renumber records chapter and flip all build/index wiring

Atomic commit: rename the records chapter to 14 and update every index that references chapter order, so `build.sh` always points at files that exist.

**Files:**
- Rename: `book/12-snapshots-audit-and-statements.md` → `book/14-snapshots-audit-and-statements.md`
- Modify: `book/14-snapshots-audit-and-statements.md` (H1), `book/build.sh`, `book/README.md`, `book/PLAN-epub.md`

- [ ] **Step 1: Rename the records chapter (preserve history)**

Run: `git mv book/12-snapshots-audit-and-statements.md book/14-snapshots-audit-and-statements.md`

- [ ] **Step 2: Update its H1**

In `book/14-snapshots-audit-and-statements.md`, change the H1 from `# Chapter 12 — Snapshots, Audit Trails, and Statements` to `# Chapter 14 — Snapshots, Audit Trails, and Statements`.

- [ ] **Step 3: Update `book/build.sh` chapter list**

In the chapter array (`book/build.sh:32-33`), replace the single line `  12-snapshots-audit-and-statements.md` so the tail of the list reads:

```
  11-payment-schemes.md
  12-sepa.md
  13-card-transactions.md
  14-snapshots-audit-and-statements.md
```

- [ ] **Step 4: Update `book/README.md` Table of Contents**

In the TOC, set Part IV and Part V to:

```
**Part IV — Moving Money Between Banks**
9. [Clearing and Settlement](09-clearing-and-settlement.md)
10. [The Interbank Network](10-the-interbank-network.md)
11. [Payment Schemes](11-payment-schemes.md)
12. [SEPA: Credit Transfers and Direct Debits](12-sepa.md)
13. [Card Transactions](13-card-transactions.md)

**Part V — Records and Reporting**
14. [Snapshots, Audit Trails, and Statements](14-snapshots-audit-and-statements.md)
```

- [ ] **Step 5: Update `book/PLAN-epub.md`**

Update the chapter-mapping table (`:30-31`) to list rows 11→`11-payment-schemes.md`, 12→`12-sepa.md`, 13→`13-card-transactions.md`, 14→`14-snapshots-audit-and-statements.md`, and update the trailing example file list (`:57`) so it ends `… 14-snapshots-audit-and-statements.md`.

- [ ] **Step 6: Verify wiring agreement**

Run: `grep -nE '1[1-4]-|sepa|card' book/build.sh`
Expected: 11-payment-schemes, 12-sepa, 13-card-transactions, 14-snapshots in that order.

Run: `grep -c '\.md' book/README.md` and confirm the TOC lists 14 numbered chapters plus the preface reference.

- [ ] **Step 7: Commit**

```bash
git add book/11-payment-schemes.md book/12-sepa.md book/13-card-transactions.md book/14-snapshots-audit-and-statements.md book/build.sh book/README.md book/PLAN-epub.md
git commit -m "Renumber records chapter to 14 and wire up SEPA/card chapters"
```

---

## Task 5: Build + cross-reference verification

**Files:** none modified (verification only). The built `book/how-money-moves.epub` is gitignored, so nothing is committed here.

- [ ] **Step 1: Cross-reference sweep for dangling numbers/links**

Run: `grep -rnE 'Chapter 12 —|12-snapshots|\(12-snapshots' book/*.md`
Expected: no matches (the records chapter is now 14; old links are gone).

Run: `grep -rni 'payment-schemes.md' book/*.md`
Expected: only the README TOC link to `11-payment-schemes.md`.

- [ ] **Step 2: Confirm chapter count and order**

Run: `ls book/[0-1][0-9]-*.md`
Expected: contiguous `01` … `14` with `12-sepa.md` and `13-card-transactions.md` present and no `12-snapshots…`.

- [ ] **Step 3: Build the EPUB (if pandoc + perl available)**

Run: `cd book && ./build.sh`
Expected: `Wrote …/how-money-moves.epub` with no pandoc errors; the EPUB has a working TOC and one navigable section per chapter (14 + preface). If pandoc is unavailable, note it and rely on Steps 1–2 for structural verification.

- [ ] **Step 4: No commit needed** — verification only (EPUB is gitignored).

---

## Self-Review notes

- **Spec coverage:** ch.11 trim (Task 3) ✓; SEPA chapter (Task 1) ✓; card chapter with the three scoped topics (Task 2) ✓; renumber + file/build/TOC/PLAN ripple (Task 4) ✓; cross-reference sweep + build verify (Task 5) ✓; "no service changes" honored (only `book/` touched) ✓.
- **Voice/scope guards** are encoded as grep checks in Tasks 1–3 (`no-code OK`, `scope OK`).
- **Type/name consistency:** new filenames `12-sepa.md`, `13-card-transactions.md`, `14-snapshots-audit-and-statements.md` are used identically across Tasks 1, 2, 4, 5.
