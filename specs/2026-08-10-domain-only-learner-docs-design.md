# Domain-only learner-facing docs

The concept hints and the quiz are read by someone learning banking. They
currently name Go symbols, error identifiers and HTTP endpoints, which are facts
about this repository rather than about money. This removes them, and writes the
rule down so the layers do not drift back.

## Scope

- `web/src/components/hint-content.ts` — 71 identifier mentions across 19 of 90
  hints.
- `web/src/lib/quiz/chapters/*.ts` — 32 across 8 of 18 chapters.

`README.md` (373) and `docs/expansion-roadmap.md` (116) are engineer-facing and
keep theirs. Distilling the README into a hint is what drops them.

## The line

**Stays**, because a banker would recognise it:

- wire vocabulary — `CdtrAgt`/`DbtrAgt`, `pacs.008`, `AC01`/`TM01`/`AM04`,
  IBAN, BIC, `ACT/365`
- the three schemas' table and column names — `entries` and its position
  column, `(created_at, seq)` — since the relational mapping is itself one of
  the layers this content spans
- institutions as the subject of every rule: the submitting bank, the debtor's
  bank, the clearing house
- the admissions that something is *not* built — gross settlement, snapshot
  checkpointing, the absent return window. The limitation is domain content;
  only its symbols go.

**Goes**: Go packages, types, methods, fields; `Err*` identifiers; HTTP method
and path; React components and source file names.

## Translation, not deletion

Every claim survives with its subject changed from a function to an institution.
`SubmitPaymentTx` refusing an unnamed counterparty becomes *the submitting bank
refuses an instruction that names no counterparty at all, before either leg
posts*. Rewording must not quietly soften or widen a claim — the README is
authoritative and the hint must still agree with it afterwards.

Two hints need paragraph-level rewriting: `counterparty-details` and
`account-addressing`. The other 17 hints and the 8 chapters are sentence-level.

## Three sentences are deleted rather than reworded

They carry no domain claim once the symbol is gone:

- `asset` — that the Go type is `AssetDef` because `Asset` was taken. Naming
  trivia. The neighbouring "defined in code, not stored" claim stays.
- `per-asset-balance` — the error-wrapping sentence. That a caller may ask
  either question is Go mechanics; the domain half is that a failure names
  *which* asset broke, not merely that the transaction did.
- `unit-of-work` — the `…Tx` method-pairing convention, replaced by: every
  operation either opens a unit of work or joins one already open.

## Verification

`npm run test` — `concept-links.test.ts` scans hint bodies *and* quiz
explanations, and `diversity.test.ts` holds the per-chapter floors. No
wiki-link, concept tag or difficulty tier is touched, so both suites should be
untouched-green rather than merely green. Load a page as well, since a broken
link only throws at runtime.

## The rule is written down

`CLAUDE.md` gains it under *Domain knowledge stays consistent across layers*, so
the next hint written does not reintroduce a symbol.
