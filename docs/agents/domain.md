# Domain Docs

How the engineering skills should consume this repo's domain documentation when exploring the codebase.

## Before exploring, read these

- **`CONTEXT.md`** at the repo root, or
- **`CONTEXT-MAP.md`** at the repo root if it exists — it points at one `CONTEXT.md` per context. Read each one relevant to the topic.
- **`docs/specs/`** — read the design records that touch the area you're about to work in. Each is named `YYYY-MM-DD-<slug>-design.md` and is linked from `docs/expansion-roadmap.md`, which is the index.

If any of these files don't exist, **proceed silently**. Don't flag their absence; don't suggest creating them upfront. The `/domain-modeling` skill (reached via `/grill-with-docs` and `/improve-codebase-architecture`) creates them lazily when terms or decisions actually get resolved.

**There is no `docs/adr/` in this repo, and its absence is deliberate.** A decision, the alternative it rejected and what that cost live in the design record for the sub-project that made it. Don't create a separate ruling record.

## File structure

```
/
├── CONTEXT.md
├── docs/
│   ├── expansion-roadmap.md        ← the index; what is left to build
│   └── specs/                      ← one design record per sub-project
│       ├── 2026-07-27-multi-asset-ledger-design.md
│       └── 2026-08-13-the-message-log-design.md
└── src/
```

## Use the glossary's vocabulary

When your output names a domain concept (in an issue title, a refactor proposal, a hypothesis, a test name), use the term as defined in `CONTEXT.md`. Don't drift to synonyms the glossary explicitly avoids.

If the concept you need isn't in the glossary yet, that's a signal — either you're inventing language the project doesn't use (reconsider) or there's a real gap (note it for `/domain-modeling`).

## Flag conflicts with a design record

If your output contradicts a decision an existing design record states, surface it explicitly rather than silently overriding:

> _Contradicts the store-per-institution design (one store type per institution) — but worth reopening because…_
