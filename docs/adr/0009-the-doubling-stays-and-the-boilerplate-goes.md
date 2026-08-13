# ADR-0009: The `X` / `XTx` doubling stays, and the boilerplate goes

**Status:** accepted
**Date:** 2026-08-13
**From:** architecture review — the `X` / `XTx` doubling, with a direction

## Context

Every domain act exists twice: `X` opens a unit of work, `XTx` runs inside one a
caller supplies. There are **86 such pairs** — `payment` 35, `deposit` 25,
`lending` 13, `ledger` 10, `product` 3 — and the roadmap carried a standing item
to collapse them, prescribing **`Tx`-first**: every method takes a `Tx`, and one
helper above the domain opens and commits.

That prescription rested on three claims. Measured against the tree, none of
them held.

- **"The failure mode is a deadlock."** It is not. `store/sqlite` stamps the
  open unit of work into the context and `checkNotNested` refuses a second one
  on the same store, from both `inUpdate` and `inView`, returning
  `ErrNestedTransaction` — whose message names the remedy. Seven nesting shapes
  and the legal cross-store case are held by `TestNestedUnitOfWorkIsRefused`.
- **"Visible cost today: `handleListFacilities` opens two store transactions per
  facility."** That handler does not exist. `lending.Portfolio.ListFacilitiesWithTerms`
  resolves the whole listing in one unit of work and cites the nesting refusal as
  its reason.
- **"Nearly every one is a six-line closure."** Backwards: the closure is the
  wrapper, and the `XTx` method is the implementation. The two halves are not
  interchangeable and the entry costed the wrong one.

Which half is load-bearing was then measured. Deleting the **wrappers** moves
209 call sites in `payment` alone (78 production, 131 test). Deleting the
**`XTx` methods** moves 5: three compositions in production
(`addressedPartyTx`, `RejectAtBankTx`, `TakeInstruction`) and two race tests in
`store/storetest`. The wrappers carry the callers; the `XTx` methods carry the
rules.

## Decision

**Both halves stay.** `Tx`-first is refused: it would spend 209 call-site edits
to convert a failure that is already caught at runtime, with a precise error,
into one caught at compile time.

**What goes is the boilerplate.** `internal/unit` holds `Run` and `Run2`, which
turn a store's `Update` or `View` into a call that returns a value — the thing a
`func(context.Context, Tx) error` closure cannot do on its own. A wrapper is now
its signature and three lines.

- **The seam is named at the call site** — `unit.Run(ctx, s.store.View, …)` —
  because read-only and writing are a domain distinction and must not be hidden
  behind a helper that picks one.
- **A unit that does not commit yields the zero value.** Nothing it built was
  kept, so handing back a half-built value beside an error is wrong. Eight
  wrappers already did this and the rest returned whatever was assigned;
  `unit_test.go` is where the rule is now stated once.
- **20 error-only wrappers are left alone.** They have no value to carry, so
  they are already three lines and a helper would only add a name to read
  through.
- **`payment.RecordRelayed` is left alone.** It is the one wrapper that is not
  a wrapper: it loops `RecordRelayedTx` over a batch inside a single unit of
  work, which is the composition the `XTx` half exists for.

## Consequences

**65 wrappers lost their bodies**, and roughly 290 lines went with them across
five packages. No call site moved and no signature changed.

**The doubling is now visibly mechanical.** A reader who wonders whether a
wrapper does anything beyond opening a unit of work can see that it does not, in
one line, rather than reading eight and comparing them to the seven above it.

**A new wrapper that cannot be written as `unit.Run` is a signal**, not a style
violation: it means the act composes something, like `RecordRelayed`, and the
composition is worth reading.

**`internal/unit` is the module's first `internal` package.** It names no domain
type and depends on nothing but `context`, so `ledger` may import it without
inverting anything.

**The runtime guard is what makes this safe, and it is the thing to preserve.**
If `checkNotNested` were ever removed, the argument above collapses and
`Tx`-first becomes worth its price again.
