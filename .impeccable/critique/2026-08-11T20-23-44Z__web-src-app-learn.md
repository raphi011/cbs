---
target: Learn / quiz surface (web/src/app/learn)
total_score: 20
max_score: 40
na_heuristics: 
p0_count: 1
p1_count: 3
timestamp: 2026-08-11T20-23-44Z
slug: web-src-app-learn
---
Method: dual-agent (A: design review, isolated · B: detector + measured browser evidence, isolated)

Target: `web/src/app/learn` — the Learn / quiz surface. Mode: **Read**.
Dev server started for inspection at `localhost:3000`, stopped before reporting.

## Design Health Score

| # | Heuristic | Score | Key Issue |
|---|-----------|-------|-----------|
| 1 | Visibility of System Status | 2 | `answeredCount` counts selections, not checks — the header flips to "Score 0/1" the instant you pick an option, before submitting. Opens at "Score 0/0". No live regions anywhere in `main`. |
| 2 | Match System / Real World | 3 | Banker vocabulary is excellent (presentment, ACCP, T+1, CdtrAgt). But the concept rail's empty state reads 'Select a "?" to see an explanation here' on a surface with no "?" control, and "Concept shown in the sidebar →" renders on mobile where no sidebar exists. |
| 3 | User Control and Freedom | 1 | `main` on a chapter page contains zero links — no way back to Learn. No end-session, no revisit, no resume. `/learn/mixed` is a 380-question commitment that saves nothing until the last question. |
| 4 | Consistency and Standards | 2 | Learn hardcodes indigo/violet/emerald/amber/teal; `globals.css` defines a strictly achromatic token set. Detector confirms `ai-color-palette` at `learn/page.tsx:34`. `Card size="sm"` on the index vs ad-hoc `p-6` on the quiz card, against the repo's own `--card-spacing` convention. |
| 5 | Error Prevention | 3 | `hasResponse` correctly gates Check on empty multi / NaN numeric. But multi-select never states how many to pick, and abandoning a 380-question session takes no confirmation. |
| 6 | Recognition Rather Than Recall | 1 | The result screen lists misses as prompt + correct answer only — the explanation you were just shown is destroyed. No "your answer" vs "correct answer" per option, so you must recall what you clicked. |
| 7 | Flexibility and Efficiency | 1 | Session length is 20 or 380, nothing between. `difficulty` is authored on all 380 questions, rendered as a label, and filters nothing. No resume, no search across 18 chapters, no keyboard shortcuts on a pure keyboard task. |
| 8 | Aesthetic and Minimalist Design | 2 | ~32% of a 1440px viewport is a permanently empty concept rail on `/learn` and through every answering phase. Topbar carries three controls irrelevant to Learn. `ChapterCard`'s indigo bar overflows its own rounded corners (no `overflow-hidden`), on all 18 cards, both themes. |
| 9 | Error Recovery | 2 | "✗ Not quite" plus a teaching explanation is well-judged, but right/wrong on the options is colour-only, and the feedback text itself fails AA: "✓ Correct" 3.47:1, "✗ Not quite" 4.36:1, explanation body on a wrong answer 4.34:1. |
| 10 | Help and Documentation | 3 | Concept panel + `[[wiki-link]]` graph + Related pills is a real help system, and opt-in "Show the concept" is a genuinely good decision. No onboarding, no legend for Intro/Core/Challenge, no warning that Mixed review is 380 questions. |
| **Total** | | **20/40** | **Acceptable (bottom of band)** |

No heuristic scored `n/a`.

**Cognitive load: 5 of 8 failed → HIGH.** Failures: single focus (the loudest element on the index is the worst first action), chunking (42 of 254 option lists carry 5–6 options), visual hierarchy (the pedagogically correct result-screen action is the outline button, the wrong one is filled), minimal choices (5-option multi-selects are 32-way decisions; 19 entry points on the index with no recommendation), working memory (explanations discarded between question and result). Passes: visual grouping, one-thing-at-a-time, progressive disclosure — the last is the surface's standout.

## Design Specificity Verdict

**LLM assessment.** The question bank is authored for banking education. The interface is not. Strip the 380 questions out and what remains — a gradient "Mixed review" banner, a grid of numbered cards with percentage pills, a type badge, four bordered option buttons, a green/red feedback box, a progress ring, a "N in a row 🔥" streak, and a score ring — is the default quiz-app composition any bootcamp ships. Nothing in the composition, interaction model, or motion knows the subject is double-entry bookkeeping.

Two ideas here are genuinely unrepeatable elsewhere. `explore` deep-links a question into the *running* clearing house so you can go look at the actual payment — no generic quiz app can ship that, because it needs a live banking backend to point at. And the concept rail is a cross-linked hint graph spanning 18 chapters. Both are buried: `explore` is a small text link under the explanation on a minority of questions, and the graph's only door is a quiz.

The visual language fights the product it ships inside, and the inversion is precise: decorative chrome got the saturated palette, while the *selected answer* — the one moment that needed a distinctive accent — got `ring-1 ring-primary`, a black outline indistinguishable from a focus ring.

**Deterministic scan.** 1 finding, exit code 2: `ai-color-palette` (warning) at `web/src/app/learn/page.tsx:34` — the `from-indigo-600 to-violet-600` gradient, measured in-browser as `#4f39f6 → #7f22fe`, identical in both themes with no adaptation. True positive as a factual match. It corroborates the palette argument above from a completely independent direction, which is the strongest signal in this run: the one thing a mechanical scanner could see is the same thing the design review called the whole story.

The detector found nothing else across six scanned targets. Everything else below came from measurement or judgment, not from the scan — which is the expected division of labour, not a gap.

**Visual overlays.** Script injection was verified working on this target (title mutation, `<script>` execution, and DOM overlay all succeeded). No overlay is currently displayed: the probe was removed and the dev server has been stopped. There is no live overlay to look at.

## Overall Impression

The content is excellent and the container is generic, and the gap between them is the entire finding. 380 banker-accurate questions with explanations that teach the mechanism rather than restate the answer, held by real tests — wrapped in chrome that could belong to any subject. The single biggest opportunity: the explanation is the product and the score is the receipt, and right now the app persists the receipt and destroys the product.

## What's Working

**The concept is withheld while you answer and revealed on submit.** Giving it up front would leak the answer; giving it never would waste the teachable moment. Opt-in "Show the concept" turns the hint into a cost the learner chooses to pay. Very few quiz products reason this carefully about *when* knowledge should arrive.

**The question bank is a real asset.** Banker vocabulary, explanations written as prose a learner would want, a `[[wiki-link]]` registry with a computed Related graph, and `diversity.test.ts` / `concept-links.test.ts` holding the shape.

**The semantic floor is better than it looks.** Answer options are real `<button type="button">`, not divs. Heading order is clean on all three routes with no skipped levels. Every focus indicator is actually visible under real Tab traversal, and the console is completely clean — zero errors or React warnings across all three routes.

## Priority Issues

**[P0] `/learn/mixed` is a 380-question session with no exit and no saved progress.**
`mixedQuestions()` is literally `allQuestions()`. The counter reads "1/380". `recordResult` fires only on the last question, so leaving at question 200 saves nothing and there is no resume. It is also the most visually prominent CTA on the index — the thing a first-timer taps.
*Why it matters:* the task is unfinishable in any realistic sitting, and every abandonment is total loss.
*Fix:* `mixedQuestions(limit = 20)` sampling by seeded shuffle, weighted toward concepts the learner keeps missing. Add "End session" that scores what was answered. Persist `{seed, index, responses}` so a session resumes. Relabel the CTA "Mixed review · 20 questions".
*Command:* `/impeccable harden`

**[P1] The result screen praises failure and throws away the teaching.**
25% earns "Nicely done — new best for this chapter!" because `isNewBest` is `finalPct > prevBest` and `prevBest` was 0. The miss list carries only `prompt` + `correctText(q)`.
*Why it matters:* this is the end of the peak-end curve, and it is simultaneously dishonest and useless. The learner leaves with 15 answers to memorise and no reasons — and once the praise is noticed as hollow, it stops meaning anything anywhere.
*Fix:* gate the congratulation on a real threshold (`isNewBest && pct >= 70`) with honest alternates below it. Render each miss as a collapsible carrying `q.explanation` through `ConceptMarkdown`, plus a chip opening `q.concept` in the rail. Make "Practice N missed" the filled primary and "Retry chapter" the outline. Carry the streak onto this screen.
*Command:* `/impeccable clarify`

**[P1] The surface is effectively unusable with a screen reader.**
Measured: zero live regions in `main` (the only one in the document is sonner's empty toaster mount, outside `main`, never populated). Options carry no `role`, no `aria-checked`, no `aria-pressed` — selection is CSS-only. On Check, the verdict and explanation are injected above while focus stays on the node whose label flips to "Next question →", so the entire educational payload is silent. `numeric` questions render an `<Input type="number">` with no `<label>`, no `for`, no `aria-label`. Right/wrong on the options is colour-only. The progress-ring `<svg>` is the one SVG on the page without `aria-hidden`, and it has no title — an unlabeled exposed graphic. Chapter card titles are `div`s, so 18 cards contribute zero headings.
*Why it matters:* the questions are answerable and the teaching is not reachable. That is worse than an inaccessible quiz — it is an accessible quiz with an inaccessible lesson.
*Fix:* `role="radiogroup"` (`aria-labelledby` the prompt) with `role="radio"` + `aria-checked`, `aria-multiselectable` for `multi`, roving tabindex. Feedback block in `role="status" aria-live="polite"`, with focus moved to its heading on Check. Visually-hidden "Your answer" / "Correct answer" text. Label the numeric input. `aria-hidden` or title the ring. Promote card titles to `h3`.
*Command:* `/impeccable audit`

**[P1] Mobile: the page scrolls sideways and the primary action is always below the fold.**
Measured at a true 390px viewport: `scrollWidth 464` vs `clientWidth 390` — 74px of horizontal overflow on all three routes. The cause is the shared header's non-wrapping `ml-auto` cluster: a 240px identity combobox pushes the theme toggle to a right edge of 464.2, entirely off-screen. No page-content element overflows; Learn inherits this from `components/shell/`. Separately, "Next question →" sits 283px below the fold after every Check, in the bottom-right corner, twenty times a session. "Show the concept" opens a Sheet covering 293px of 390 — it hides the question it exists to help with — and the monospace teaching diagrams inside are clipped mid-word with no scroll affordance.
*Fix:* let the topbar cluster wrap, or hide `BusinessDay` + `IdentityPicker` below `md` on Learn, where they do nothing. Put Check/Next in a `sticky bottom-0` bar inside the card on mobile, or `scrollIntoView` the feedback on Check. Give the diagram boxes a visible scroll affordance.
*Command:* `/impeccable adapt`

**[P2] The authored difficulty ramp is discarded, and the success/failure text fails AA.**
The chapter files are authored intro → core → challenge (ch1-q1..q5 are all `intro`), and `buildSession` shuffles uniformly — so Chapter 1 opened on a question tagged "Challenge" on both observed runs. `difficulty` is rendered as a bare label with no legend and filters nothing. Separately, measured contrast failures: "✓ Correct" `#009966` on `#ecfdf5` = **3.47:1**; "✗ Not quite" `#e7000b` on `#fef2f3` = **4.36:1**; explanation body on a wrong answer = **4.34:1**; "Not started" badge = **4.35:1** (light only — dark passes at 5.86). The idle answer-option border is `#e5e5e5` on white = **1.26:1**, failing WCAG 1.4.11 for the only thing that renders an option as a control.
*Why it matters:* the ramp is a one-line fix that turns a quiz into a curriculum, and the contrast failures land precisely on the feedback text — the words that carry the teaching.
*Fix:* shuffle within tier and concatenate tiers in `buildSession`. `text-emerald-600` → `text-emerald-700` (measured 5.09:1 in the existing progress pill, so the value is already in this codebase). Darken the destructive text and explanation body on tinted backgrounds. Raise the idle option border to ≥3:1.
*Command:* `/impeccable polish`

## Persona Red Flags

**Jordan (confused first-timer)** — Taps the biggest brightest thing ("Mixed review · Start →") and lands on question 1 of **380**, drawn from Chapter 18 amortization, before reading Chapter 1. No exit. Opens Chapter 1 instead and gets a question tagged "Challenge". The header reads "Score 0/0" before he starts, then "Score 0/1" the instant he *selects* — he thinks he already failed. A third of his screen says 'Select a "?" to see an explanation here'; there is no "?" anywhere on Learn. He scores 5/20, is told "Nicely done — new best for this chapter!", then shown 15 failures with no explanations. He wants Chapter 2: `main` has zero links, and the only topbar option drops him into the persona lobby — a different product.

**Sam (screen reader / keyboard-only / low vision)** — Options announce as unlabeled buttons with no selected state. Check produces silence: no live region, focus parked on a button whose label just changed under him. The `numeric` input is unlabeled. Right/wrong is border colour only. The `multi` checkbox is a 16px decorative `<span>` at `border-muted-foreground/40` — sub-target, near-invisible, and the only cue that the question accepts multiple answers. `ChapterCard`'s `<Link>` wraps the entire card, so the accessible name is "CHAPTER 1 What a Bank Is 20 questions 100%" and "Best 89%" is 89% of nothing named. A 1×970px `div[role="separator"]` sits in the tab order between main and the rail — a keyboard stop with no visible target. No `<nav>`, no `<aside>`, no skip link.

**Casey (distracted, one-handed, mobile)** — 74px of horizontal scroll on every page, theme toggle entirely off-screen. "Next question →" 283px below the fold after every Check, bottom-right, the far corner for a left thumb. "Show the concept" covers the question. The T-account and payment-lifecycle diagrams — the best content in the product — are clipped mid-word (`Alice (debtor) ⟶ Bank A ⟶ clea`) with no scroll affordance. Seven interactive elements fall under 44×44 at 390px: "Open concepts" and "Change theme" at 32×32, "Check answer →" at 118×32, "Show the concept" at 133×28, the identity combobox at 240×28, "Day" at 63×28, the wordmark at 49×24. The answer options themselves pass at 310×46 — the failures are all chrome. 18 chapter cards stack to ~2,600px of scroll with no search or jump-to-Part.

## Minor Observations

- Dark mode inverts the light-mode figure/ground: option buttons use `bg-background` (0.145) inside a `Card` at `bg-card` (0.205), so unselected options are *darker* than their container. Designed light-first, inherited dark.
- `QuizResult` states the same fact three times — "5/20", "25% correct", "Your best so far: 25%" — under a heading already claiming a new best.
- `TypeBadge` spends the card's most prominent slot saying "Multiple choice", which the four options below already communicate.
- The rail keeps showing the *last question's* concept on the result screen: `QuizRunner`'s effect never clears `defaultConcept` when `finished` flips.
- `readProgress` silently swallows corrupt localStorage and returns `null`, so the card reads "Not started" — a learner's history vanishes with no notice.
- `prefers-reduced-motion` is not honoured by any app-authored CSS. The only two reduced-motion blocks in 218 accessible rules come from sonner and a browser extension. The 500ms `stroke-dasharray` transition on the progress ring and the 150ms `transition-all` on every button run unconditionally.
- `PageHeader` and `QuizRunner` both write `setDefaultConcept` on the same page; sibling effect order is the only thing making `QuizRunner` win.
- The `"Coming soon"` empty-chapter state is unreachable — every chapter has ≥20 questions.
- Result-screen actions sit ~1,700px down on a 15-miss result, with no sticky footer or back-to-top.
- Light `truefalse` badge is the tightest passing contrast at 4.85:1; all four type badges pass in both themes.

## Questions to Consider

1. The chapter files are authored intro → core → challenge, and `buildSession` throws that ordering away with a uniform shuffle. Why does a *teaching* product randomise the one thing a curriculum is?
2. Every question carries a `concept` key into a cross-linked hint graph spanning 18 chapters, and the only door into that graph is a quiz. What would a concept-map entry point look like, where the quiz is one way to *test* a node rather than the only way to reach it?
3. The explanation is the product; the score is the receipt. Today the explanation is shown once and destroyed, and the score is what persists. What if that were inverted — the app kept the explanations you've earned and tracked which *concepts* you keep missing, instead of a best-percentage per chapter?
4. `explore` sends you from a question into the live clearing house to look at the actual payment, and it appears as a small text link on a minority of questions. What if that inversion were the entire design — you learn by being sent into the running system, and the question is just the prompt to go look?

## Coverage Notes

- `/learn/1` does not exist; the chapter route takes slugs (`/learn/01-what-a-bank-is`). It returns HTTP 200 with a "Chapter not found" body, so a status probe reads as healthy. All chapter measurements were taken on `/learn/01-what-a-bank-is`.
- Chrome clamps window width to ~500–570px on this machine, so all 390px measurements were taken in a verified same-origin 390×844 iframe, where `matchMedia` resolves the mobile shell correctly.
- Dark-theme "✓ Correct" contrast was not captured — question order is randomised and both dark runs served a wrong-answer state. Dark "✗ Not quite" passes at 5.84:1.
- The `ChapterCard` "Coming soon" state and `QuizRunner`'s `seed === null` skeleton were not observable. No error state exists on this surface; it is client-only with no network calls, which is correct by design.
- Both assessment agents drove the same Chrome profile and intermittently navigated each other's tabs. Every finding was re-established after each interruption, with `location.href` echoed in the same call that produced the measurement.
