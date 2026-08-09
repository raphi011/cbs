// Package product is the catalogue: named products, each with an
// effective-dated timeline of immutable published versions, that instances are
// opened from and priced by.
//
// # What floats and what does not
//
// A product carries the parameters the BANK sets: the arranged rate, the
// unarranged surcharge, the day-count convention. It cannot carry an overdraft
// limit, because a limit is an underwriting decision about one customer — see
// OverdraftPricing, where the absence is enforced by the type rather than by a
// rule.
//
// An instance therefore has three sources for its terms on a day, and
// deposit.Resolve merges them in this order: the account's own row supplies the
// limit, always; the account's row supplies the pricing if it carries a
// negotiated overlay; otherwise the product version in force on that day does.
// Nothing is cached. What an account cost on a day is re-derived from rows,
// which is the property the whole design exists for.
//
// # Why this package imports nothing of the domain layers
//
// It imports interest and ledger and nothing else, so deposit may import it —
// and so lending may too, later, without the import cycle that a catalogue
// living inside deposit would create the moment the credit side needed one.
//
// # What is deliberately absent
//
// No content-addressed identity beyond the tamper hash, no pinned-vs-floating
// binding beyond the two groups above, no parameter overlays other than the
// per-account pricing one, no resolution log, and no maker-checker. Publication
// is forward-only, which is this package's answer to the control problem a
// retroactive reprice creates. docs/superpowers/specs/2026-07-30-product-catalogue-design.md
// argues each of those; docs/expansion-roadmap.md records which stay open and
// why.
package product
