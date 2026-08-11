# Concept side panel — design

**Date:** 2026-06-23
**Status:** Approved (pending implementation plan)
**Area:** `web/` (educational Next.js frontend)

## Problem

Teaching concepts are currently surfaced through `?` buttons that open a Radix
**Popover** with one short plain-text paragraph (`web/src/components/hint.tsx`,
registry in `web/src/components/hint-content.ts`, ~40 entries used in 25 places).
A popover is a glance: it can't render rich content, can't link concepts to each
other, and shows nothing until clicked.

We want a **persistent rich side panel** that renders markdown, cross-links
concepts, and is always present as part of the teaching surface.

## Decisions (locked during brainstorming)

| # | Decision | Choice |
|---|----------|--------|
| 1 | Panel layout | **Persistent right rail** that pushes content narrower on wide screens; collapsible |
| 2 | Default content | **One primary concept per page** (no `?concept=` set) |
| 3 | Content depth | **Expand all ~40 concepts** to rich markdown, distilled from `README.md` |
| 4 | Cross-link syntax | **Wiki-style** `[[key]]` / `[[key\|label]]`, validated against the registry |
| 5 | Open-concept state | **URL query param** `?concept=key` (deep-linkable, back/forward = concept history) |
| 6 | "Related" footer | **Auto-derived** from the `[[ ]]` links in the body (no extra field) |
| 7 | Integration approach | **Context provider + per-page default via `PageHeader`'s existing `hint` prop** |

## Architecture (Approach A)

The panel logic lives in one provider; the page's primary concept reuses the
`hint` prop already present on `PageHeader`. This avoids a separate route→concept
map and avoids Next.js parallel routes (risky on this Next 16 setup per
`web/AGENTS.md`).

### New files (`web/src/components/`)

- **`concept-panel-provider.tsx`** — React context exposing:
  - `currentConcept: HintKey | null` — derived from the `?concept=` query param
  - `defaultConcept: HintKey | null` — the page's primary concept
  - `openConcept(key)` — `router.push` to `?concept=key`
  - `closeConcept()` — clears the param (panel falls back to default)
  - `setDefaultConcept(key)` — called by `PageHeader`
  - `collapsed` / `setCollapsed` — persisted in `localStorage`
- **`concept-panel.tsx`** — the right-rail UI: header (back / title / collapse),
  scrollable markdown body, auto-derived "Related" chips. Renders `currentConcept`,
  falling back to `defaultConcept`. Wrapped in `<Suspense>` (reads `useSearchParams`).
- **`concept-markdown.tsx`** — wraps `react-markdown` + `remark-gfm`. Preprocesses
  `[[ ]]` links, supplies a custom `<a>` renderer.

### Changed files

- **`app-shell.tsx`** — wrap the app in `ConceptPanelProvider`; render
  `<ConceptPanel>` as the collapsible right column. Below `md`, the panel is a
  bottom `Sheet` instead of a rail, with a "Concepts" trigger in the topbar.
- **`hint.tsx`** — remove Radix Popover. The `?` button calls `openConcept(id)`.
  Keeps `className` and the `aria-label`. The ad-hoc `title`/`children` path is
  removed.
- **`field-label.tsx`** — drop `hintTitle`/`hintBody`; keep `hint` (registry key).
- **`page-header.tsx`** — its existing `hint` prop calls `setDefaultConcept(hint)`
  in an effect, registering the page default.
- **`hint-content.ts`** — `body` becomes a multi-line markdown string authored
  with `[[ ]]` cross-links. `HintEntry` shape (`{ title, body }`) is unchanged.

### Data flow

1. Page mounts → `PageHeader hint="payment-lifecycle"` → `setDefaultConcept`.
2. No `?concept=` → panel renders the default concept.
3. Click a `?` or a `[[link]]` → `openConcept(key)` → `router.push(?concept=key)`.
4. Panel reads `?concept=` and renders that concept. Back/forward walks concept
   history. Hard-loading `/payments?concept=netting` deep-links straight to it.

## Content model

- Each `body` is markdown. Cross-links use `[[key]]` (label = the concept's title)
  or `[[key|custom label]]`.
- **"Related"** is computed: parse distinct `[[ ]]` keys from the body, render as
  chips under the body. No new registry field.
- **Worked examples** use fenced code blocks (` ``` `) — they render as the
  monospace example boxes (e.g. the netting sum-to-zero illustration).
- Content is distilled from `README.md`, the authoritative source.

## Link rendering & validation

`concept-markdown.tsx`:

1. **Preprocess:** regex-rewrite `[[key|label]]` → `[label](concept:key)` and
   `[[key]]` → `[title](concept:key)`.
2. **Render** with `react-markdown` + `remark-gfm`, custom `components.a`:
   - `concept:` href → element that calls `openConcept(key)`
   - internal path (`/payments`) → `next/link`
   - external URL → `<a target="_blank" rel="noreferrer">`
   - Raw HTML is not enabled.
3. **Validation:** a dev-time check scans every concept body's `[[ ]]` keys
   against `HintKey` on first render and throws a descriptive error for any key
   that doesn't resolve. (String contents can't be checked by `tsc`; this is the
   runtime equivalent and fails loudly in dev.)

## Mobile & collapse

- **`< md`:** no rail. Panel renders as a bottom `Sheet` (reusing the existing
  component), opened by any `?`/link or the topbar "Concepts" button. `?concept=`
  still drives its content.
- **Collapse:** open/collapsed persists in `localStorage` and survives navigation.
  On wide screens the collapsed state is a thin strip on the right edge. The open
  *concept* always lives in the URL, never `localStorage`.

## Migration

- Promote any ad-hoc `FieldLabel` hints (`hintTitle`/`hintBody`) to real registry
  entries (grep during planning; remove the props once none remain).
- Expand all existing one-paragraph bodies into rich markdown with `[[ ]]`
  cross-links.

## Verification (no frontend test runner)

- Gates clean: `npm run typecheck`, `npm run lint`, `npm run build`.
- Playwright drive against a running backend:
  - Click a `?` → panel shows the concept; URL gains `?concept=`.
  - Click a `[[link]]` → panel swaps; URL updates.
  - Back button walks concept history.
  - Hard-load `/payments?concept=netting` deep-links correctly.
  - Below `md`, the Sheet opens instead of the rail.
  - Collapse state persists across navigation.

## Out of scope

- No search over concepts, no concept index page, no per-concept analytics.
- Gross-settlement and other not-yet-wired backend features stay as-is.

## Dependencies added

- `react-markdown`
- `remark-gfm`
