package payment

import (
	"context"

	"github.com/raphi011/cbs/iso20022"
)

// What the CLEARING HOUSE has taken in and not yet handed over.
//
// Every other row this package writes records something that has HAPPENED: a
// payment carried, a cut-off netted, a member admitted. These two record
// something that is OWED — a file one bank uploaded and another bank has not
// been given, a return uploaded to the settlement agent and not yet answered —
// and they exist because an obligation an institution keeps only in its process
// memory stops existing when the process does, while the money that moved
// against it stays moved.
//
// Neither is on any other institution's interface, and neither could be. A
// member bank holds no share of anybody's file; the settlement agent has no
// payments at all. Sorting one submitted file into M addressed ones is the act
// no member could perform for itself, and holding the M in between is what
// settle-before-release costs.

// A HeldFile is one receiving bank's share of an uploaded instruction file,
// built when the file is taken in and released when the cut-off carrying it
// settles.
//
// # It carries the SUBMITTER's file, not the one that will travel
//
// Which transactions travel is not settled until the cut-off is: a payment an
// operator rejects out of an open cycle must not reach the bank that would have
// been paid. So what is kept has to be cuttable, and a rendered output file is
// not — the share is the submitter's own file plus the POSITIONS in it this
// destination is owed.
//
// File is the whole uploaded envelope rather than a bare document, because a
// bare document is not something iso20022 renders: Marshal takes an envelope.
// The header on it is the submitting bank's and is dropped when the share is
// released, which puts this institution's own header on what it hands over.
type HeldFile struct {
	// CycleID is the cut-off whose settlement releases this share.
	CycleID CycleID

	// Seq is which share of that cut-off this is, in build order, and it is what
	// names one for discharge. The store allocates it; a caller building a share
	// leaves it zero, and only what ListHeldFiles reports carries a meaningful
	// one. See DropHeldFile for why a share has to be nameable at all.
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
//
// Both, because the release asks two different questions. The payment id is what
// it holds against the batch the settlement agent actually discharged; the
// position is how the document is cut once that answer is in. Neither is
// derivable from the other without re-reading the message this institution has
// already read.
type HeldTransaction struct {
	Position  int
	PaymentID PaymentID
}

// A HeldReturn is one pacs.004 uploaded to the settlement agent and not yet
// answered: the second hop of a return, waiting for finality.
//
// It cannot be queued before the agent says ACSC. The returning bank may still
// be refused, and a bank that had posted its customer leg against a refused
// return would have moved a customer's money for nothing, with no file in the
// flow that would ever tell it.
//
// It is keyed by the payment the return names, which is the only thing the
// message and the answer to it have in common.
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
//
// Nothing is audited here, and that is the distinction the log draws: the
// clearing house's decision about each transaction — accepted into the cycle, or
// refused — is written by AcceptAtCSMTx and RejectAtCSMTx beside this. A share is
// the mechanical consequence of those decisions and is not a second one.
func (s *Network) HoldFile(ctx context.Context, f HeldFile) error {
	if err := s.clearingHouse(); err != nil {
		return err
	}
	return s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return tx.AddHeldFile(ctx, f)
	})
}

// ListHeldFiles is every share this institution is holding for one cut-off, in
// the order they were built.
//
// Two callers, asking the same question for opposite reasons. The release walks
// them to hand each bank its instructions; the guard in front of the settlement
// walks them to refuse a cut-off whose payments have no share behind them — see
// ErrCycleNotReleasable, which is what makes the release certain to be possible
// before the reserves move.
func (s *Network) ListHeldFiles(ctx context.Context, id CycleID) ([]HeldFile, error) {
	if err := s.clearingHouse(); err != nil {
		return nil, err
	}
	var out []HeldFile
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListHeldFiles(ctx, id)
		return err
	})
	return out, err
}

// DropHeldFile discards ONE share, once the bank it is addressed to has been
// handed it.
//
// # One at a time, and after the hand-over
//
// What a share is handed INTO is a row as well — a download queue is the
// transport's table in this same institution's database — so an obligation
// changing hands is two writes, and no statement may make both: the domain
// cannot name a queue. What stands between them is therefore the order they run
// in. Reading, releasing and only then discharging leaves a process that ended
// in between holding the obligation rather than having consumed it.
//
// The same order is what makes a FAILED hand-over safe, and it is why this
// takes one share rather than a cut-off's. A share that could not be queued is
// still owed to the bank it names, against reserves that have already moved —
// discharging it with its neighbours would destroy exactly what these rows
// exist to keep. It stays, and the day's report carries the fault.
//
// Discharging is not optional either, for the failure on the other side: a
// share still standing after its bank has been handed it is released a second
// time by a redelivered answer, and a bank handed the same instructions twice
// credits the same customer twice.
func (s *Network) DropHeldFile(ctx context.Context, id CycleID, seq int64) error {
	if err := s.clearingHouse(); err != nil {
		return err
	}
	return s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return tx.DeleteHeldFile(ctx, id, seq)
	})
}

// HoldReturn records a return uploaded to the settlement agent, for the bank
// that has not heard about it yet.
func (s *Network) HoldReturn(ctx context.Context, r HeldReturn) error {
	if err := s.clearingHouse(); err != nil {
		return err
	}
	return s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return tx.PutHeldReturn(ctx, r)
	})
}

// GetHeldReturn is the return this institution is holding for one payment, or
// ErrHeldReturnNotFound.
//
// The miss is ordinary rather than exceptional: a pacs.004 naming no payment is
// uploaded and never held, and an answer about a payment nothing was held for is
// still owed to the bank that asked. The caller forwards the answer either way.
func (s *Network) GetHeldReturn(ctx context.Context, id PaymentID) (HeldReturn, error) {
	if err := s.clearingHouse(); err != nil {
		return HeldReturn{}, err
	}
	var out HeldReturn
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetHeldReturn(ctx, id)
		return err
	})
	return out, err
}

// DropHeldReturn discards a held return, whatever the settlement agent said
// about it.
//
// Whatever it said, because both answers end the wait: an ACSC releases the
// message to the other bank and an RJCT drops it unsent, which is the whole
// point of holding it. A row kept past either would be retried by nothing and
// swept by nothing.
func (s *Network) DropHeldReturn(ctx context.Context, id PaymentID) error {
	if err := s.clearingHouse(); err != nil {
		return err
	}
	return s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return tx.DeleteHeldReturn(ctx, id)
	})
}
