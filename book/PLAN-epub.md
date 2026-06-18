# Plan: Package the Book as an EPUB3

The book in this directory is authored as Markdown chapters so it reads well on
GitHub and is easy to edit. This document is the plan for turning those chapters
into a single **EPUB3** file that can be side-loaded onto e-readers (Kobo, Kindle
via conversion, Apple Books, Calibre, etc.). It is a plan, not yet an
implementation.

## Goal

`book/*.md` → a valid, well-formed `book/how-money-moves.epub` containing all
twelve chapters plus front matter, a working table of contents, and readable
diagrams — produced by a single command, with no manual steps.

## Source inventory

| Order | File | Becomes |
|-------|------|---------|
| — | `README.md` | Cover + title page + preface |
| 1 | `01-what-a-bank-is.md` | Chapter 1 |
| 2 | `02-double-entry-bookkeeping.md` | Chapter 2 |
| 3 | `03-the-chart-of-accounts.md` | Chapter 3 |
| 4 | `04-ledgers-subledgers-and-money.md` | Chapter 4 |
| 5 | `05-transactions-and-postings.md` | Chapter 5 |
| 6 | `06-booking-date-vs-value-date.md` | Chapter 6 |
| 7 | `07-balances-and-holds.md` | Chapter 7 |
| 8 | `08-account-lifecycle-and-overdraft.md` | Chapter 8 |
| 9 | `09-clearing-and-settlement.md` | Chapter 9 |
| 10 | `10-the-interbank-network.md` | Chapter 10 |
| 11 | `11-payment-schemes.md` | Chapter 11 |
| 12 | `12-snapshots-audit-and-statements.md` | Chapter 12 |

The chapter order is explicit (this table), not inferred from filenames, so
reordering or inserting chapters is a one-line change.

## Recommended approach

There are two realistic routes. Pick based on whether the build machine has
network/toolchain access.

### Route A (preferred when tooling is available): Pandoc

[Pandoc](https://pandoc.org/) converts Markdown directly to EPUB3 and handles the
TOC, manifest, and packaging for us.

```bash
pandoc \
  --from gfm \
  --to epub3 \
  --toc --toc-depth=1 \
  --metadata title="How Money Moves" \
  --metadata author="<author>" \
  --metadata lang=en \
  --css book/epub.css \
  --epub-cover-image book/img/cover.png \
  -o book/how-money-moves.epub \
  01-what-a-bank-is.md 02-double-entry-bookkeeping.md ... 12-snapshots-audit-and-statements.md
```

- **Pros:** minimal code; robust; battle-tested EPUB output.
- **Cons:** requires Pandoc installed. Mermaid/ASCII diagrams (see below) need
  pre-processing.
- A tiny `book/build.sh` (or `make epub`) should wrap the command so the chapter
  list lives in exactly one place.

### Route B (zero external dependencies): a small stdlib Go builder

If the environment forbids installing Pandoc (e.g. a locked-down CI sandbox), add
`cmd/book/main.go` using only the Go standard library:

- Render each chapter's Markdown to XHTML (a small, dependency-free Markdown
  subset is enough — these chapters use headings, paragraphs, lists, tables,
  blockquotes, code fences, and emphasis).
- Assemble the EPUB3 ZIP by hand with `archive/zip`:
  - `mimetype` as the **first** entry, **stored uncompressed** (the one EPUB
    gotcha).
  - `META-INF/container.xml` → `OEBPS/content.opf`.
  - `OEBPS/content.opf` — package metadata, manifest, and spine generated from the
    chapter table above.
  - `OEBPS/nav.xhtml` — the EPUB3 navigation document (TOC + landmarks).
  - The rendered chapter XHTML, the CSS, and any images.
- `go run ./cmd/book` writes `book/how-money-moves.epub`. Keeps the module
  dependency-free, consistent with the rest of the repo.

**Recommendation:** use Route A if Pandoc is available; fall back to Route B to
keep the build self-contained and dependency-free. Implement one, not both.

## Diagrams

The chapters currently contain two kinds of diagram, both as fenced code blocks:

1. **ASCII diagrams** (the account state machine, the hierarchy tree, the payment
   posting blocks). These render fine as-is inside `<pre>` on any e-reader —
   **no work needed**. Keep them in a monospace block in the EPUB CSS.
2. There are currently **no Mermaid diagrams** in the book (the balance-sheet
   relationship is rendered as an ASCII box diagram in Chapter 3). If a future
   chapter adds Mermaid, it must be pre-rendered to SVG/PNG at build time, because
   e-readers can't run Mermaid. Options: `mermaid-cli` (needs Node + Chromium) or
   replacing it with hand-authored inline SVG. Until then, this is not a blocker.

For the cover, add `book/img/cover.png` (a simple title image). Optional but makes
the result look finished in a library view.

## Styling

Add `book/epub.css` with: readable serif body text, sans-serif headings, sensible
table borders, and a monospace block style that preserves the ASCII diagrams and
posting examples. Keep it minimal — e-readers override much of it, and users set
their own fonts.

## Verification

1. **Build runs clean:** the chosen command/`go run` produces
   `book/how-money-moves.epub` with no errors.
2. **Structure check:** `unzip -l book/how-money-moves.epub` shows `mimetype`
   first; `unzip -v` shows it `Stored` (0% compression). `META-INF/container.xml`,
   the OPF, the nav doc, and all twelve chapters are present.
3. **Validation:** run [`epubcheck`](https://github.com/w3c/epubcheck) against the
   output (`epubcheck book/how-money-moves.epub`) — zero errors. (Route A: Pandoc
   output passes epubcheck out of the box. Route B: validate during development.)
4. **Render check:** open in Calibre / Apple Books and confirm the TOC lists all
   chapters, chapter-to-chapter navigation works, tables render, and the ASCII
   diagrams stay aligned in their monospace block.

## Out of scope

- No PDF output (EPUB only).
- No automated chapter-from-source generation; chapters are hand-written prose.
- The build is a developer convenience; the Markdown remains the canonical source.
