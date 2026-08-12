# Architecture decision records

One file per decision that shapes more than the code it is written in: numbered,
dated, and naming the alternative it rejected and what that cost.

**These are not design records.** `docs/specs/` holds one design per sub-project — the
whole plan, its phasing, its testing strategy — and is where a change large
enough to need phasing is written before it starts. An ADR is narrower and
outlives the sub-project that produced it: it states a decision, in the present
tense, that a later reader has to know about before changing the shape of
anything near it.

**A comment in the code states the rule; the reasoning lives here.** That is why
`CLAUDE.md` forbids a change log in a comment. If you find yourself writing "this
used to work the other way" beside the code, the other way belongs in an ADR.

If your work contradicts one of these, say so explicitly rather than silently
overriding it — see `docs/agents/domain.md`.

| # | Decision | From |
|---|---|---|
| [0001](0001-the-deployment-owns-the-clock.md) | The deployment owns the clock | sub-project 21 |
| [0002](0002-settle-before-release.md) | The clearing house settles before it releases | sub-project 21, task 8 |
| [0003](0003-an-institutions-obligations-live-in-its-database.md) | An institution's unfinished obligations live in its own database | held-files durability, phase 2 |
| [0004](0004-a-queue-is-a-table-and-stays-opaque.md) | A download queue is a table, and the bytes in it stay opaque | held-files durability, phase 3 |
| [0005](0005-a-business-day-is-a-declared-sequence.md) | A business day is a declared sequence, and a subset is derived from it | architecture review |
| [0006](0006-one-type-per-institution.md) | One type per institution, and the identity guards what is left | architecture review |
