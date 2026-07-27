// Package interest is the arithmetic of lending: day-count conventions, daily
// accrual, and the precision that accrual needs and a ledger cannot hold.
//
// It is deliberately pure. There is no store, no ledger.Book, and no notion of
// an account — only amounts, rates, dates and conventions. Both credit-bearing
// layers use it: deposit accrues on an overdrawn current account, lending
// accrues on a loan or a revolving line, and neither has to reimplement a
// convention or a rounding rule.
//
// # Why there is a type between a rate and a posting
//
// A day's interest on a small balance is mostly fraction. €50 overdrawn at 15%
// accrues 2.0548 cents a day; rounding that to 2 cents daily and discarding the
// remainder is a 2.4% annual error on the interest, which no bank would accept
// and no reader should be taught. Real core banking systems hold accrued
// interest at higher precision than the posting currency and round only when it
// is charged.
//
// So there are two representations of the same interest, and the split is the
// point:
//
//   - Accrued, in micro-minor-units, lives on the account or facility record and
//     is exact.
//   - Its Minor() rounding lives in the general ledger, and the daily posting is
//     the change in that rounded value.
//
// The residue between them is what the ledger structurally cannot represent,
// which is the same justification a hold already has for living outside it.
package interest
