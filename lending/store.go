package lending

import (
	"context"

	"github.com/raphi011/cbs/ledger"
)

// Store owns the lending layer's persistent state. Like ledger.Store and
// deposit.Store it is declared here, by the consumer, and implemented by
// store/mem and store/pg — so the store packages import the domain packages and
// never the reverse.
type Store interface {
	Update(ctx context.Context, fn func(context.Context, Tx) error) error
	View(ctx context.Context, fn func(context.Context, Tx) error) error
	Reset(ctx context.Context) error
	Close() error
}

// Tx embeds ledger.Tx so one concrete transaction spans both layers. That is
// what makes a disbursement — a facility write plus a GL posting — a single
// unit of work rather than two that can half-fail.
//
// The names need no Lending prefix: ledger.Tx claims PutAccount, GetAccount and
// ListAccounts, and nothing here collides with them.
type Tx interface {
	ledger.Tx

	PutFacility(ctx context.Context, book ledger.BookID, f Facility) error
	GetFacility(ctx context.Context, book ledger.BookID, id FacilityID) (Facility, error)
	ListFacilities(ctx context.Context, book ledger.BookID) ([]Facility, error)

	PutInstallment(ctx context.Context, book ledger.BookID, i Installment) error
	ListInstallments(ctx context.Context, book ledger.BookID, id FacilityID) ([]Installment, error)
}

// Contract notes for implementers — storetest.RunLending pins all of these:
//
//   - GetFacility returns ErrFacilityNotFound when the row is missing. Wrapping
//     is fine; errors.Is is what the suite checks.
//   - Every method is scoped by book: the same FacilityID in two books is two
//     different facilities, exactly as in ledger.Tx.
//   - PutFacility is an upsert keyed by (book, id). PutInstallment is an upsert
//     keyed by (book, facility, seq) — a repayment re-puts an instalment with
//     its paid amounts filled in, and must not create a second row.
//   - ListFacilities: OpenedAt ascending, ties broken by the row's monotonic
//     insertion sequence — the same ORDER BY created_at, seq rule every other
//     listing follows. Never break ties on the ID.
//   - ListInstallments is ordered by Seq ascending, and that is the ONE listing
//     in this system not ordered by a timestamp. Seq is the instalment's
//     position in the contract, so it is already a total order within a
//     facility, and a due date is not: two instalments can fall due on the same
//     date after a reschedule. It is a number rather than a counter-derived
//     string, so it sorts correctly.
//   - ListInstallments on an unknown facility returns an empty slice, not an
//     error: it is a listing, and a facility with no schedule is an ordinary
//     state (a term loan before disbursement, a line before its first cycle).
//   - Writes roll back with the surrounding Update, lending rows and ledger rows
//     together — that is the whole point of Tx embedding ledger.Tx.
