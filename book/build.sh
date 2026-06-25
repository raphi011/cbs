#!/usr/bin/env bash
#
# Build the EPUB3 and/or PDF edition of "How Money Moves" from the Markdown
# chapters.
#
# Usage:
#   ./build.sh          # build both EPUB and PDF (default)
#   ./build.sh epub     # EPUB only
#   ./build.sh pdf      # PDF only
#
# Requires pandoc (https://pandoc.org/installing.html). The PDF build also needs
# a LaTeX PDF engine; tectonic (https://tectonic-typesetting.github.io/) is the
# default — override with PDF_ENGINE=<engine>.
#
# Both editions have a cover/title, a working table of contents, and one
# navigable chapter per Markdown file — no manual steps.
#
# The CHAPTERS list below is the single source of truth for reading order:
# reorder or insert a chapter by editing this list and nothing else.
#
set -euo pipefail

# Run from the book directory so all paths below are relative to it,
# regardless of where the script is invoked from.
cd "$(dirname "$0")"

EPUB_OUTPUT="how-money-moves.epub"
PDF_OUTPUT="how-money-moves.pdf"
PDF_ENGINE="${PDF_ENGINE:-tectonic}"
# Monospace font for the PDF — must cover the box-drawing/geometric glyphs the
# ASCII diagrams use (Latin Modern Mono does not). Menlo ships with macOS;
# override with PDF_MONOFONT (e.g. "DejaVu Sans Mono") on other platforms.
PDF_MONOFONT="${PDF_MONOFONT:-Menlo}"

CHAPTERS=(
  00-preface.md                       # Front matter (derived from README.md)
  01-what-a-bank-is.md
  02-double-entry-bookkeeping.md
  03-the-chart-of-accounts.md
  04-ledgers-subledgers-and-money.md
  05-transactions-and-postings.md
  06-booking-date-vs-value-date.md
  07-balances-and-holds.md
  08-account-lifecycle-and-overdraft.md
  09-clearing-and-settlement.md
  10-the-interbank-network.md
  11-payment-schemes.md
  12-sepa.md
  13-card-transactions.md
  14-snapshots-audit-and-statements.md
)

# gfm-tex_math_dollars: read GitHub-flavored Markdown but disable $...$ math, so
# dollar amounts in the prose (e.g. $10,000) stay literal instead of being parsed
# as TeX math. Critical for the PDF especially — otherwise a bare '$' would open
# LaTeX math mode and the build would fail.
FROM="gfm-tex_math_dollars"

build_epub() {
  pandoc \
    --from="$FROM" \
    --to=epub3 \
    --toc --toc-depth=1 \
    --split-level=1 \
    --metadata-file=metadata.yaml \
    --css=epub.css \
    --epub-cover-image=img/cover.png \
    --output="$EPUB_OUTPUT" \
    "${CHAPTERS[@]}"
  echo "Wrote $(pwd)/$EPUB_OUTPUT"
}

build_pdf() {
  if ! command -v "$PDF_ENGINE" >/dev/null 2>&1; then
    echo "error: PDF engine '$PDF_ENGINE' not found." >&2
    echo "       Install it (e.g. 'brew install tectonic') or set PDF_ENGINE=<engine>." >&2
    exit 1
  fi
  # documentclass=book + top-level-division=chapter turns each '#' heading into a
  # real chapter; oneside drops the blank verso pages that make sense only in
  # print; colorlinks makes the TOC and cross-references clickable in readers.
  pandoc \
    --from="$FROM" \
    --pdf-engine="$PDF_ENGINE" \
    --metadata-file=metadata.yaml \
    --toc --toc-depth=1 \
    --top-level-division=chapter \
    --lua-filter=table-fit.lua \
    --include-in-header=pdf-header.tex \
    -V documentclass=book \
    -V classoption=oneside \
    -V geometry:margin=1in \
    -V colorlinks=true \
    -V monofont="$PDF_MONOFONT" \
    --output="$PDF_OUTPUT" \
    "${CHAPTERS[@]}"
  echo "Wrote $(pwd)/$PDF_OUTPUT"
}

if ! command -v pandoc >/dev/null 2>&1; then
  echo "error: pandoc is not installed. See https://pandoc.org/installing.html" >&2
  exit 1
fi

case "${1:-all}" in
  epub) build_epub ;;
  pdf)  build_pdf ;;
  all)  build_epub; build_pdf ;;
  *)    echo "usage: $0 [epub|pdf|all]" >&2; exit 2 ;;
esac
