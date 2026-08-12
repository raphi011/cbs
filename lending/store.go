package lending

import (
	"context"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Store owns the lending layer's persistent state.
type Store interface {
	Update(ctx context.Context, fn func(context.Context, Tx) error) error
	View(ctx context.Context, fn func(context.Context, Tx) error) error
	Reset(ctx context.Context) error
	Close() error
}

// Tx embeds ledger.Tx so one concrete transaction spans both layers. That is
// what makes a disbursement — a facility write plus a GL posting — a single
// unit of work rather than two that can half-fail.
type Tx interface {
	ledger.Tx

	// The slot mapping, for the reason deposit.Tx carries it: a facility's
	// principal, receivable and income lines are resolved through it on every
	// draw and every accrual.
	ledger.SlotTx

	PutFacility(ctx context.Context, book ledger.BookID, f Facility) error
	GetFacility(ctx context.Context, book ledger.BookID, id FacilityID) (Facility, error)
	ListFacilities(ctx context.Context, book ledger.BookID) ([]Facility, error)

	PutInstallment(ctx context.Context, book ledger.BookID, i Installment) error
	ListInstallments(ctx context.Context, book ledger.BookID, id FacilityID) ([]Installment, error)

	// The facility's effective-dated terms timeline.
	PutFacilityTerms(ctx context.Context, book ledger.BookID, t FacilityTerms) error
	// ListFacilityTerms is named after ListInstallments, the facility-scoped
	// listing precedent in this interface, rather than after deposit's
	// ListOverdraftTermsForAccount, which is named after ListHoldsForAccount.
	ListFacilityTerms(ctx context.Context, book ledger.BookID, id FacilityID) ([]FacilityTerms, error)
	GetFacilityTermsAsOf(ctx context.Context, book ledger.BookID, id FacilityID, day time.Time) (FacilityTerms, error)
}
