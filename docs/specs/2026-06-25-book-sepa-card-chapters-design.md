# Design — Split Payment Schemes into SEPA and Card Transactions chapters

**Date:** 2026-06-25
**Status:** Approved (design)
**Topic:** Book restructure — give SEPA and card transactions their own chapters

## Goal

The book's Chapter 11 ("Payment Schemes") currently bundles four distinct things:
the netting/clearing rationale, a worked credit-transfer posting example, the
scheme-abstraction model (the axes that distinguish schemes), the two SEPA schemes
(SCT + SDD) with returns, and a forward-look at instant and card flows.

Split this so SEPA and card transactions each get a dedicated chapter, while
Chapter 11 retains the shared foundation that both build on. Add genuinely new
prose for cards, since the book currently only mentions them as a forward-look.

## Non-goals

- **No changes to the service / library.** This is a book-only change. No Go or
  frontend code is touched. The card flows described are domain knowledge, not an
  implemented feature. (The library already models authorization as holds and has
  capture; it does not implement a full card scheme, and this work does not add
  one.)
- **No interchange economics or chargebacks/disputes** in the card chapter
  (explicitly scoped out to keep the chapter focused).
- No restructuring of chapters 1–10 or the records chapter's content (it only gets
  renumbered).

## Constraints (voice & honesty)

- The book is a **domain book**, not a code walkthrough. It never shows code,
  function names, or APIs; it references "our reference library" only as abstract
  prose, in roughly 8 chapters. The new chapters match this voice exactly.
- The card chapter links **authorization → the "hold" concept from Chapter 7** (a
  domain term the book already teaches) and **capture → the debtor leg**. Any
  reference to the library is kept to a sentence, in the same light style used
  elsewhere — no code, no heavy "this isn't implemented" disclaimer (none is needed,
  because the book never promised an implementation).
- Numbers add up and postings balance, consistent with the rest of the book.

## New structure (Part IV — Moving Money Between Banks)

| #  | Chapter                                   | Change                                   |
|----|-------------------------------------------|------------------------------------------|
| 9  | Clearing and Settlement                   | unchanged                                |
| 10 | The Interbank Network                     | unchanged                                |
| 11 | **Payment Schemes** (title kept)          | trimmed to the shared machinery          |
| 12 | **SEPA: Credit Transfers and Direct Debits** | **new** — mostly relocated from old ch.11 |
| 13 | **Card Transactions**                     | **new** — net-new prose                  |
| 14 | Snapshots, Audit Trails, and Statements   | renumbered from 12 (Part V), content unchanged |

**Resolved decisions:** (a) Chapter 11 keeps the title "Payment Schemes" (it is the
chapter that defines what a scheme *is*); (b) the detailed Returns section moves
into the SEPA chapter (returns are introduced by direct debits), while the
"returns allowed?" *axis* remains in Chapter 11's abstraction.

## Chapter content

### Chapter 11 — Payment Schemes (trimmed)

Becomes the conceptual foundation both later chapters lean on.

- **Keeps:** Netting ("why net"), the worked credit-transfer posting example, the
  scheme-abstraction axes (push/pull, net/gross, mandate required, returns allowed,
  settlement delay), the "deliberate simplifications" note, and the
  **instant-payments** forward-look (the chapter's "gross" settlement example).
- **Removes:** the "SEPA Credit Transfer", "SEPA Direct Debit", and "Returns"
  sections (→ ch.12); the **card** forward-look bullet (→ ch.13), replaced by a
  one-line pointer.
- **New closing:** instead of transitioning to Part V, it points forward to "the
  schemes by name — SEPA and cards."

### Chapter 12 — SEPA: Credit Transfers and Direct Debits (new; relocated)

- Short new intro: now meet the net-settled schemes by name; cross-reference the
  worked posting example in ch.11.
- **SEPA Credit Transfer (SCT):** push, no mandate, T+1, ISO 20022 `pacs.008`.
- **SEPA Direct Debit (SDD):** pull, mandate + the mandate checks (exists, active,
  matches parties, within limit), T+2, `pacs.003`.
- **Returns / R-transactions:** relocated here; unwinding a settled payment as the
  cross-bank cousin of the Chapter 5 reversal.
- Close: transition into cards — "SEPA moves debtor→creditor by agreement; cards add
  a different *shape*."

### Chapter 13 — Card Transactions (new; net-new prose)

Domain chapter in the established voice. Scope = the three confirmed topics:

- Intro: a card as a payment scheme with a different shape; authorization is the
  **hold** of Chapter 7.
- **The four-party model:** cardholder, issuer, acquirer, merchant, and the
  network/scheme — contrasted with SEPA's two-party debtor→creditor flow.
- **Authorization → presentment:** the dual-message flow (authorize now,
  present/clear later) and the **single-message** variant (PIN debit / ATM, where
  auth and clearing are fused).
- **Authorized ≠ final amount:** gas-pump holds, tips, partial/incremental capture;
  ties to holds (ch.7) and to *a frozen account blocking authorization* (ch.8).
- **Reusing the rails:** captured card amounts net and settle through the same
  ch.9–11 machinery (card networks net much like SEPA).
- Close: the book's existing "step back from movement to records" transition into
  Part V moves to the end of this chapter.

### Chapter 14 — Snapshots, Audit Trails, and Statements (renumbered)

Content unchanged. Only the H1 ("Chapter 12" → "Chapter 14") and its position
change.

## File & build ripple (concrete)

- Rename `book/12-snapshots-audit-and-statements.md` → `book/14-snapshots-audit-and-statements.md`;
  update its H1 to "Chapter 14".
- Add `book/12-sepa.md` (H1 "Chapter 12 — SEPA: Credit Transfers and Direct Debits").
- Add `book/13-card-transactions.md` (H1 "Chapter 13 — Card Transactions").
- Edit `book/11-payment-schemes.md` per the trim above (H1 title unchanged).
- `book/build.sh`: in the chapter list, replace the single `12-snapshots-…md` entry
  with `12-sepa.md`, `13-card-transactions.md`, `14-snapshots-audit-and-statements.md`
  (order matters — it drives EPUB order).
- `book/README.md`: update the Part IV list (9–13) and Part V (14) in the Table of
  Contents.
- `book/PLAN-epub.md`: update the chapter-mapping table (rows for 11–14) and the
  example file list on line ~57.
- `book/metadata.yaml`: no change (no chapter list).

## Cross-reference sweep

Verified surface (grep for `Chapter 1[12]`, `payment-schemes.md`, `12-snapshots`):
no in-prose numbered cross-references to chapters 11 or 12 exist in any other
chapter. The only renumbering touch-points are the records chapter's own H1 and the
three build/index files above. New chapters reference only *earlier*, stable
chapter numbers (2, 4, 5, 7, 8, 9, 10, 11). After the edits, re-run the grep to
confirm nothing dangles.

## Verification

- `book/build.sh` runs clean (requires pandoc + perl) and produces a validated
  `how-money-moves.epub` with a working TOC and one navigable section per chapter,
  now 14 chapters + preface.
- Manual read-through of the three touched/added chapters for voice consistency and
  that postings/numbers balance.
- Re-run the cross-reference grep; confirm the TOC in README, build.sh order, and
  PLAN-epub table all agree on 14 chapters.
