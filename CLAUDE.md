# CLAUDE.md

Guidance for Claude Code when working in this repository (an in-memory core banking
system plus a teaching book, quiz, and web UI).

## Book (`book/`)

**Always regenerate both book editions after editing any book source.** The
downloadable `book/how-money-moves.epub` and `book/how-money-moves.pdf` are committed
artifacts derived from the `book/*.md` chapters and the build inputs (`metadata.yaml`,
`epub.css`, `pdf-header.tex`, `table-fit.lua`). After changing any of these, run:

```bash
make book   # builds EPUB + PDF via book/build.sh (default PDF engine: tectonic)
```

Never hand-edit the EPUB/PDF, and never leave them stale relative to the markdown
sources. Use `make epub` / `make pdf` to build a single edition.

## Domain knowledge stays consistent across layers

The banking/accounting/payments content is duplicated, by design, across `README.md`
(the authoritative source), the `book/*.md` chapters, `web/src/components/hint-content.ts`
(distilled from the README), and `web/src/lib/quiz/chapters/*.ts`. When you correct a
domain fact in one layer, check and fix the same claim in the others.
