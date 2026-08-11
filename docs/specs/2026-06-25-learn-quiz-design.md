# Learn / Quiz Section — Design

**Date:** 2026-06-25
**Status:** Approved (design); pending implementation plan

## Goal

An interactive quiz section inside the existing `web/` app that lets the reader
practice the concepts from each chapter of *How Money Moves*. Many questions per
chapter, four question formats, immediate feedback, and a path to dig deeper via
the app's existing concept sidebar and explorer pages. Adding a new question must
be a one-object edit that the compiler can validate.

## Content policy (reconciling two constraints)

Earlier in brainstorming we said the quiz, like the book, should not depend on
this app to be answerable. We then decided that, *because* the quiz lives inside
the explorer, it should also link out to it. Both hold, with a clear division:

- **Question content is domain-first.** Every question tests understanding of a
  banking/accounting concept and is answerable from that understanding alone —
  it never instructs the reader to operate the app ("click X"). This keeps the
  questions aligned with the book.
- **The scaffolding around each question is app-enriched.** A question may carry
  a `concept` tag (drives the right sidebar), an `explore` link (to a relevant
  explorer page), and its explanation may use `[[concept]]` wiki-links. These are
  *aids to learn more*, not part of what's being tested.

## Placement & routing

A new **"Learn"** item in the left nav (`NETWORK_NAV` in `app-shell.tsx`,
`GraduationCap` icon).

- `/learn` — index. A "Mixed review" hero + chapter cards grouped by the book's
  Parts. Each card shows the chapter number, title, question count, and the
  reader's status from localStorage (`Not started` / `Best NN%` / `100%`).
- `/learn/[chapter]` — the quiz runner for one chapter. `[chapter]` is the book
  slug, e.g. `02-double-entry-bookkeeping`.
- `/learn/mixed` — the runner over a random sample drawn across all chapters.

`/learn/mixed` and `/learn/[chapter]` render the **same** `QuizRunner`, differing
only in the *question source* they pass it.

## Data model — typed discriminated union

`src/lib/quiz/types.ts`:

```ts
import type { HintKey } from "@/components/hint-content";

type Difficulty = "intro" | "core" | "challenge";

interface BaseQuestion {
  id: string;                 // stable & unique, e.g. "ch2-q3"
  prompt: string;             // inline markdown allowed
  explanation: string;        // shown after answering; supports [[concept]] links
  concept?: HintKey;          // sets the right-sidebar default while answering
  explore?: { href: string; label: string }; // link to a relevant explorer page
  difficulty?: Difficulty;
}

export type Question =
  | (BaseQuestion & { kind: "mc";        options: string[]; answer: number })
  | (BaseQuestion & { kind: "truefalse"; answer: boolean })
  | (BaseQuestion & { kind: "multi";     options: string[]; answers: number[] })
  | (BaseQuestion & { kind: "numeric";   answer: number; unit?: "cents" | "dollars"; tolerance?: number });

export interface Chapter {
  slug: string;     // matches the book filename stem, e.g. "02-double-entry-bookkeeping"
  number: number;   // 1..14
  part: string;     // book Part heading, for grouping on the index
  title: string;
  questions: Question[];
}
```

Notes:
- **`concept: HintKey`** reuses the existing concept registry. The compiler
  rejects a question that points at a concept key that does not exist.
- **Journal-entry questions** are composed from the primitives: amounts use
  `numeric`; "which side / which account" uses `mc` or `multi`. No bespoke
  journal-entry component in v1.
- **`explore.href`** must target a stable, non-parameterized route (the
  network-level pages: `/payments`, `/cycles`, `/settlements`, `/schemes`,
  `/central-bank`, or `/`). Participant-scoped routes need a `pid` and are out of
  scope for explore links.

## Data files & assembly

- One file per chapter: `src/lib/quiz/chapters/NN-slug.ts`, each
  `export const chapter: Chapter = { … }`.
- `src/lib/quiz/index.ts` assembles them into an ordered `chapters` array and
  exposes `getChapter(slug)`, `allQuestions()`, and the Part grouping used by the
  index.
- **Initial content:** each chapter ships with an initial set of at least 8
  questions spanning at least two formats. The structure is the point; the bank
  grows by appending objects.

## Pure logic (unit-tested)

`src/lib/quiz/session.ts` — no React, no DOM:
- `buildSession(questions, seed, limit?)` → ordered question list with a
  **seeded** shuffle of both question order and per-question option order
  (deterministic, so a session is reproducible and tests are stable).
- `isCorrect(question, response)` per kind: `mc` exact index; `multi` set-equality
  (order-independent); `truefalse` boolean; `numeric` `|response − answer| ≤
  tolerance` (tolerance defaults to 0).
- `score(session, responses)` → `{ correct, total, missed: Question[] }`.

`src/lib/quiz/storage.ts` — localStorage progress:
- Key per chapter, e.g. `quiz:02-double-entry-bookkeeping`, storing
  `{ bestPct, lastPct, lastAttempt }`.
- `readProgress(slug)`, `recordResult(slug, pct)` (keeps the max for `bestPct`),
  resilient to absent/corrupt storage (returns a safe default, never throws).

## Components

New, under `src/components/quiz/`:
- `QuizRunner` — owns session state (current index, responses, score, phase),
  sets the current question's `concept` as the sidebar default via
  `setDefaultConcept` (mirroring `PageHeader`), renders the progress ring, score,
  and streak.
- `QuestionCard` — switches on `kind` to render inputs and, after a check, the
  answered state + explanation. Explanation prose renders through the existing
  `ConceptMarkdown` so `[[concept]]` links open the sidebar for free; the
  `explore` link renders as a real Next `Link` (client routing), separate from the
  markdown.
- `QuizResult` — score ring, best-score line, missed-questions review, and
  Retry / Practice-missed / Back-to-chapters actions.
- `ChapterCard`, `ProgressRing`, `TypeBadge` — presentational.

Reused as-is: `PageHeader`, `Card`, `Button`, `Badge`, `ConceptMarkdown`,
`setDefaultConcept` / `openConcept`, the right-sidebar concept panel.

## Visual direction

Refined-but-lively, scoped to `/learn` only (the rest of the explorer keeps its
minimal aesthetic). Settled via the visual companion:
- Gradient "Mixed review" hero on the index.
- Per-type colored badges: MC (indigo), True/False (amber), Numeric (teal),
  Multi (violet).
- Progress rings (index status + in-session progress), a "N in a row" streak pill.
- Results: a large score ring, a "new best" line, and an inline missed-review list.

## Testing

`vitest` (already in the stack):
- **Bank validation** (`quiz.test.ts`): unique ids; non-empty prompt/explanation;
  `mc`/`multi` have ≥2 options and in-range answer indices; `multi` answers
  non-empty & unique; `numeric` answer finite; every `concept` is a real
  `HintKey`; every `explore.href` is one of the allowed routes; every chapter
  `slug` matches a book chapter.
- **`session.ts`**: seeded-shuffle determinism; `isCorrect` per kind (esp. `multi`
  order-independence and `numeric` tolerance); `score` counts and `missed`.
- **`storage.ts`**: best-score is monotonic (keeps max); safe on missing/corrupt
  storage.

No frontend e2e runner exists; final verification is driving the app in a browser
against a running backend (the explore links resolve to live pages).

## Out of scope (v1)

- Spaced repetition / scheduling.
- Test mode (graded-at-end) — practice mode only for now.
- Server-side persistence or accounts — localStorage only.
- Authoring UI — questions are added by editing typed `.ts` data files.
- Bespoke journal-entry builder — composed from numeric + mc/multi.
