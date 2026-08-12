package payment

import (
	"context"

	"github.com/raphi011/cbs/iso20022"
)

// What the CLEARING HOUSE has taken in and not yet handed over.

// A HeldFile is one receiving bank's share of an uploaded instruction file,
// built when the file is taken in and released when the cut-off carrying it
// settles.
type HeldFile struct {
	// CycleID is the cut-off whose settlement releases this share.
	CycleID CycleID

	// Seq is which share of that cut-off this is, in build order, and it is what
	// names one for discharge.
	Seq int64

	// Destination is the receiving bank, which is also the subscriber whose
	// download queue the share is put in.
	Destination iso20022.BIC

	// File is the submitting bank's uploaded file, exactly as it arrived.
	File []byte

	// Transactions is which of that file's transactions are this destination's,
	// in the order they will be handed over.
	Transactions []HeldTransaction
}

// A HeldTransaction is one transaction of a held file: where it sits in the
// submitter's document, and which payment it is.
type HeldTransaction struct {
	Position  int
	PaymentID PaymentID
}

// A HeldReturn is one pacs.004 uploaded to the settlement agent and not yet
// answered: the second hop of a return, waiting for finality.
type HeldReturn struct {
	PaymentID PaymentID

	// ReturnedBy is the bank that uploaded the return, and it is what the second
	// hop is routed against: the other bank is whichever of the message's two
	// agents this one is not.
	ReturnedBy iso20022.BIC

	// File is the returning bank's uploaded file, exactly as it arrived. See
	// HeldFile.File — what differs is that the document travels unchanged, so
	// only the header is rebuilt at release.
	File []byte
}

// HoldFile records one receiving bank's share of a file this institution has
// just taken in.
func (s *ClearingHouseNetwork) HoldFile(ctx context.Context, f HeldFile) error {
	if err := s.clearingHouse(); err != nil {
		return err
	}
	return s.store.Update(ctx, func(ctx context.Context, tx CsmTx) error {
		return tx.AddHeldFile(ctx, f)
	})
}

// ListHeldFiles is every share this institution is holding for one cut-off, in
// the order they were built.
func (s *ClearingHouseNetwork) ListHeldFiles(ctx context.Context, id CycleID) ([]HeldFile, error) {
	if err := s.clearingHouse(); err != nil {
		return nil, err
	}
	var out []HeldFile
	err := s.store.View(ctx, func(ctx context.Context, tx CsmTx) error {
		var err error
		out, err = tx.ListHeldFiles(ctx, id)
		return err
	})
	return out, err
}

// DropHeldFile discards ONE share, once the bank it is addressed to has been
// handed it.
func (s *ClearingHouseNetwork) DropHeldFile(ctx context.Context, id CycleID, seq int64) error {
	if err := s.clearingHouse(); err != nil {
		return err
	}
	return s.store.Update(ctx, func(ctx context.Context, tx CsmTx) error {
		return tx.DeleteHeldFile(ctx, id, seq)
	})
}

// HoldReturn records a return uploaded to the settlement agent, for the bank
// that has not heard about it yet.
func (s *ClearingHouseNetwork) HoldReturn(ctx context.Context, r HeldReturn) error {
	if err := s.clearingHouse(); err != nil {
		return err
	}
	return s.store.Update(ctx, func(ctx context.Context, tx CsmTx) error {
		return tx.PutHeldReturn(ctx, r)
	})
}

// GetHeldReturn is the return this institution is holding for one payment, or
// ErrHeldReturnNotFound.
func (s *ClearingHouseNetwork) GetHeldReturn(ctx context.Context, id PaymentID) (HeldReturn, error) {
	if err := s.clearingHouse(); err != nil {
		return HeldReturn{}, err
	}
	var out HeldReturn
	err := s.store.View(ctx, func(ctx context.Context, tx CsmTx) error {
		var err error
		out, err = tx.GetHeldReturn(ctx, id)
		return err
	})
	return out, err
}

// DropHeldReturn discards a held return, whatever the settlement agent said
// about it.
func (s *ClearingHouseNetwork) DropHeldReturn(ctx context.Context, id PaymentID) error {
	if err := s.clearingHouse(); err != nil {
		return err
	}
	return s.store.Update(ctx, func(ctx context.Context, tx CsmTx) error {
		return tx.DeleteHeldReturn(ctx, id)
	})
}
