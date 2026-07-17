# CLAUDE.md

Guidance for Claude Code when working in this repository (an in-memory core banking
system plus a quiz and web UI).

The teaching book _How Money Moves_ that used to live in `book/` has moved to the
_Lead Engineer's Field Guide_ (the `second-brain` repo, Part IX) and is no longer
maintained here.

## Domain knowledge stays consistent across layers

The banking/accounting/payments content is duplicated, by design, across `README.md`
(the authoritative source), `web/src/components/hint-content.ts` (distilled from the
README), and `web/src/lib/quiz/chapters/*.ts`. When you correct a domain fact in one
layer, check and fix the same claim in the others.
