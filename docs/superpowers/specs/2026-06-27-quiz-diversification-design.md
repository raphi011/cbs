# Quiz Diversification — Design

**Date:** 2026-06-27
**Status:** Implemented 2026-06-27 (committed on main, 4cd8f9e..d6abc45)

## Goal

The quiz bank (`web/src/lib/quiz/chapters/01..14`) has grown lopsided: of its 139
questions, a cluster all re-test the single idea *"a customer deposit is a
liability"* (≈5 of Chapter 1's 10 questions; echoed in ch2 and ch3). Concept
coverage is similarly skewed — `double-entry` is tagged 11×, `account-type-liability`
6×, while `hold-release`, `creditor-leg`, and `allows-return` appear once each.

Rewrite the bank so it is **roughly double the size (139 → ~280, ~20 per chapter,
evenly), more diverse, and free of near-duplicate questions** — each question
earns its place by testing a *distinct* concept or a *distinct cognitive angle* on
a concept. Diversity is enforced as an automated test so it cannot silently
regress.

## Decisions (from brainstorming)

1. **Full rewrite per chapter** — rebuild each chapter's set to a curated ~20,
   reusing the best existing questions rather than layering new ones on top.
2. **Even distribution** — ~20 questions per chapter (acceptable band 18–22).
3. **Diversity enforced as a test** — a new `diversity.test.ts` makes the rubric
   below a hard invariant.
4. **Numeric questions: dollars, exact** — `unit: "dollars"`, `tolerance: 0`,
   prompts phrased "(Enter a number of dollars.)".

## Non-goals

- No change to the quiz *engine* (`session.ts`, `storage.ts`, runner UI, types).
- No change to `book/*.md`, `README.md`, or `hint-content.ts` — these are the
  authoritative domain sources the questions are *grounded in*; the quiz follows
  them, not vice-versa. (So **no `make book`** is required — the quiz is web-only.)
- New `concept` tags are out of scope: questions tag only the existing 41
  `HintKey`s. If a chapter genuinely needs a concept with no hint, leave `concept`
  unset rather than invent a key.

## Approach: blueprint-first, then parallel authoring

The risk in a 14-file rewrite is that each chapter, written in isolation,
re-introduces the same foundational questions (the liability problem, 14×). The
fix is to decide global coverage **once, up front**, then let each chapter execute
its assigned slice.

1. **Global concept blueprint (authored centrally, in this spec).** Every one of
   the 41 concepts gets a *home* chapter where it is taught (2–3 questions) and may
   be *called back* elsewhere (≤1 question, for integration). This is the single
   control that prevents cross-chapter duplication and guarantees breadth.
2. **Per-chapter authoring via subagents.** One subagent per chapter, each given:
   the chapter's `book/NN-*.md` text, the relevant `README.md` section, the 41
   valid `HintKey`s, its assigned concept budget + cognitive-angle targets, the
   authoring rules, and its current chapter file (to harvest the best existing
   questions). Each rewrites only its own `NN-*.ts`.
3. **Diversity test + verification + consistency pass.**

*Alternatives considered:* (A) author all 280 solo — most consistent but slow and
repetition-prone across 14 files in one context; (B) parallel agents with no
blueprint — fast but each agent is blind to the others, so the liability idea
returns 14×. The hybrid keeps A's global view and B's throughput.

## The diversity rubric (per chapter, ~20 questions)

| Dimension | Target |
|-----------|--------|
| Question kinds | ~10 `mc` · ~4 `truefalse` · ~3 `multi` · ~3 `numeric` |
| Difficulty curve | ~5 `intro` · ~10 `core` · ~5 `challenge` |
| Distinct `concept` tags | aim ≥ 10 (enforced floor 8) |
| Max uses of any one `concept` tag (per chapter) | ≤ 3 |
| "Deposit = liability" idea (whole quiz) | ≤ 3 questions total |

**Six cognitive angles** — each chapter mixes these rather than asking 20
definitions:

1. **Recognition** — "what *is* X / which of these is an X"
2. **Mechanism** — the postings / the flow that makes X happen
3. **Calculation** — numeric: compute a balance, a net position, available funds
4. **Discrimination** — X vs Y (clearing vs settlement, booking vs value date,
   book vs available balance, push vs pull)
5. **Edge case / failure mode** — overdraft breach, failed settlement, reversal,
   returned direct debit, hold expiry
6. **Scenario / cross-chapter callback** — apply an earlier concept inside a later
   chapter's context

## Global concept blueprint (home chapters)

Each concept's **home** is where it is primarily taught; other chapters may make a
single callback question. Foundational account-type concepts are deliberately
concentrated in Part I and only referenced afterward — this is what dissolves the
liability over-test.

| Chapter | Home concepts (taught here) |
|---------|------------------------------|
| 01 What a Bank Is | `account`, `account-type-asset`, `account-type-liability`, `account-type-equity` |
| 02 Double-Entry | `double-entry`, `normal-balance`, `reversal` |
| 03 Chart of Accounts | `account-type-revenue`, `account-type-expense`, `normal-balance` (applied) |
| 04 Ledgers & Subledgers | `ledger-vs-subledger`, `amount-cents` |
| 05 Transactions & Postings | `idempotency-key`, `reversal` (applied), posting mechanics |
| 06 Booking vs Value Date | `booking-date`, `value-date` |
| 07 Balances & Holds | `balance-book`, `balance-holds`, `balance-available`, `holds`, `hold-capture`, `hold-release` |
| 08 Lifecycle & Overdraft | `account-status`, `overdraft` |
| 09 Clearing & Settlement | `clearing-vs-settlement`, `settlement-delay`, `clearing-suspense`, `settlement-model-net`, `settlement-model-gross`, `netting`, `net-positions` |
| 10 Interbank Network | `reserve-account`, `central-bank-reserves`, `net-positions` (applied) |
| 11 Payment Schemes | `scheme-direction-push`, `scheme-direction-pull`, `requires-mandate`, `allows-return`, `payment-lifecycle`, `debtor-leg`, `creditor-leg` |
| 12 SEPA | `mandate`, `requires-mandate` (applied), `allows-return` (applied), `payment-lifecycle` (applied) |
| 13 Card Transactions | `holds`/`hold-capture`/`hold-release` (applied), `scheme-direction-pull` (applied) |
| 14 Snapshots, Audit, Statements | `snapshot`, `statement`, `statement-amount` |

Callbacks (≤1 question each) are encouraged for the angle-6 integration questions,
e.g. Chapter 13 may reference `reversal` (a card refund), Chapter 14 may reference
`booking-date`. The per-chapter `concept` cap (≤3) still applies to callbacks.

## Per-question authoring rules

- **Grounded:** every fact comes from that chapter's `book/NN-*.md` or `README.md`.
  No invented mechanics, fees, or figures.
- **Valid tags:** `concept` ∈ the 41 `HintKey`s (or omitted); `explore.href` ∈
  `EXPLORE_ROUTES` (`/`, `/payments`, `/mandates`, `/cycles`, `/settlements`,
  `/central-bank`, `/schemes`).
- **Explanations** teach the *why*, not just restate the answer, and link concepts
  with `[[wiki-link]]`s. ~1–3 sentences.
- **IDs:** `chN-qM`, sequential within the chapter, globally unique.
- **Money:** integer cents internally; numeric prompts ask in whole dollars
  (`unit: "dollars"`, `tolerance: 0`).
- **Distractors** are plausible (common misconceptions), not absurd; correct
  `answer`/`answers` indices in range; `multi` has ≥2 correct where natural.
- **Reuse:** keep the strongest existing questions verbatim or lightly edited; the
  rewrite is curation + expansion, not throwaway.

## Verification (makes diversity checkable)

New `web/src/lib/quiz/diversity.test.ts` (vitest), asserting per chapter:

- question count in **[18, 22]**;
- **no `concept` tag used > 3×**;
- **≥ 8 distinct `concept` tags** (narrow chapters lean on integration callbacks);
- **all three difficulty tiers present** (`intro`, `core`, `challenge`);
- and globally: `numeric` ≥ 35, `multi` ≥ 40, `truefalse` ≥ 45 (loose floors so
  the kind mix can't collapse back to mostly-`mc`).

Existing `index.test.ts` invariants (unique IDs, valid concepts/explore, answer
ranges, well-formed) remain and must stay green. Final gates:
`npm run typecheck && npm run lint && npx vitest run && npm run build`, then a
read-through consistency pass over the 14 files for tone and cross-chapter overlap.

## Risks & mitigations

- **Subagent quality variance / cross-chapter dupes** → the central blueprint +
  shared rubric constrain scope; the diversity test + a final human-style
  read-through catch drift.
- **Domain inaccuracy** → grounding in `book/*.md` + `README.md`; explanations
  must be traceable to those sources.
- **Test floors too tight** → bands (18–22, ≥8 distinct concepts, loose global
  kind floors) leave slack; tune once if a chapter legitimately can't comply.
