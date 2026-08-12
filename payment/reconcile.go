package payment

import (
	"context"
	"fmt"
	"time"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
)

// Reconciliation is one member bank checking its own books, out of its own
// database and the statements it was sent.
type Reconciliation struct {
	Bank  iso20022.BIC
	Asset ledger.AssetCode
	AsOf  time.Time

	// Reserve is what this bank's own book says its claim on the central bank
	// stands at.
	Reserve ledger.Amount
	// Advised is the closing balance on the NEWEST statement this bank has booked,
	// and Reference is what that statement quoted — a cycle id at a cut-off, a
	// payment id at a return, and this bank cannot tell which and has no reason
	// to.
	Advised   ledger.Amount
	Reference string
	// LodgedSince is what this bank has lodged since that statement, which is the
	// whole of the difference between the two figures above.
	LodgedSince ledger.Amount

	// Breaks is what cannot be true. Every one of these is a defect.
	Breaks []Finding
	// Positions is money legitimately in flight, with how long it has been.
	// None of these is a defect, and a run that reported one as such would be a
	// run nobody could make against a network with a payment in it.
	Positions []AgeingReport
}

// Reconciled reports whether this bank's own books hold together. Positions do
// not make it false.
func (r Reconciliation) Reconciled() bool { return len(r.Breaks) == 0 }

// Finding is one thing this bank's own books say cannot be true.
type Finding struct {
	// Account is what its reader goes and looks at.
	Account ledger.AccountID
	// What is the disagreement as one sentence, with both figures in it. A
	// finding naming only the difference would send its reader back to the book
	// to find out which side was wrong.
	What string
}

func (f Finding) String() string { return string(f.Account) + ": " + f.What }

// MetadataLodgementRef labels the reserve leg of a lodgement, which is what
// makes it distinguishable from an advice's mirror leg without reading an
// idempotency key as if it were a label. See LodgeReservesTx.
const MetadataLodgementRef = "lodgement_ref"

// Reconcile runs one bank's own reconciliation for one asset and records that
// it ran.
func (s *BankNetwork) Reconcile(ctx context.Context, asset ledger.AssetCode) (Reconciliation, error) {
	var out Reconciliation
	err := s.store.Update(ctx, func(ctx context.Context, tx BankTx) error {
		var err error
		out, err = s.ReconcileTx(ctx, tx, asset)
		return err
	})
	return out, err
}

// ReconcileTx is Reconcile within a caller-supplied unit of work.
func (s *BankNetwork) ReconcileTx(ctx context.Context, tx BankTx, asset ledger.AssetCode) (Reconciliation, error) {
	bank, err := s.selfBankTx(ctx, tx)
	if err != nil {
		return Reconciliation{}, err
	}
	accts, err := bank.AccountsFor(asset)
	if err != nil {
		return Reconciliation{}, err
	}

	rec := Reconciliation{Bank: bank.BIC, Asset: asset, AsOf: s.now()}
	advices, err := tx.ListSettlementAdvices(ctx, bank.BookID)
	if err != nil {
		return Reconciliation{}, err
	}
	byMirror := map[ledger.TransactionID]SettlementAdvice{}
	for _, a := range advices {
		if a.Asset != asset {
			continue
		}
		if a.MirrorTx == "" {
			// The zero-movement arm of PostSettlementAdviceTx commits a row and posts no
			// leg, because there is nothing to post.
			if a.Movement != 0 {
				rec.breakf(accts.Reserve,
					"this bank holds an advice of %d against reference %s and no posting behind it",
					a.Movement, a.Reference)
			}
			continue
		}
		byMirror[a.MirrorTx] = a
	}

	if err := s.reserveHoldsTogether(ctx, tx, bank, accts, byMirror, &rec); err != nil {
		return Reconciliation{}, err
	}
	if err := s.suspenseIsAged(ctx, tx, bank, accts, &rec); err != nil {
		return Reconciliation{}, err
	}

	if err := s.appendAuditTx(ctx, tx, ledger.EventReconciliationRun, string(asset), rec); err != nil {
		return Reconciliation{}, err
	}
	for _, b := range rec.Breaks {
		if err := s.appendAuditTx(ctx, tx, ledger.EventReconciliationBreak, string(b.Account), b); err != nil {
			return Reconciliation{}, err
		}
	}
	return rec, nil
}

// reserveHoldsTogether walks this bank's reserve account and holds every entry
// on it against the statement that ought to be behind it.
func (s *BankNetwork) reserveHoldsTogether(ctx context.Context, tx BankTx, bank *Bank, accts BankAccounts,
	byMirror map[ledger.TransactionID]SettlementAdvice, rec *Reconciliation,
) error {
	hist, err := bank.Ledger.AccountHistoryTx(ctx, tx, accts.Reserve.Total())
	if err != nil {
		return err
	}
	rec.Reserve = hist.Closing

	seen := map[ledger.TransactionID]bool{}
	for _, row := range hist.Rows {
		advice, isMirror := byMirror[row.Transaction]
		switch {
		case isMirror:
			seen[row.Transaction] = true
			if row.Movement != advice.Movement {
				rec.breakf(accts.Reserve,
					"the statement for %s advised a movement of %d and the leg booked from it moved %d",
					advice.Reference, advice.Movement, row.Movement)
			}
			if row.Running != advice.ClosingBalance {
				rec.breakf(accts.Reserve,
					"the statement for %s said this account would stand at %d and it stood at %d once the leg had posted; %d of movement is unaccounted for",
					advice.Reference, advice.ClosingBalance, row.Running, row.Running-advice.ClosingBalance)
			}
			// The newest booked statement, and everything lodged after it. Both
			// are reporting figures and neither is a second check: they follow
			// from the two above.
			rec.Advised, rec.Reference, rec.LodgedSince = advice.ClosingBalance, advice.Reference, 0
		case row.Metadata[MetadataLodgementRef] != "":
			rec.LodgedSince += row.Movement
		default:
			rec.breakf(accts.Reserve,
				"%d moved on this bank's claim on the central bank under transaction %s, and there is no statement and no lodgement behind it",
				row.Movement, row.Transaction)
		}
	}

	for mirror, advice := range byMirror {
		if !seen[mirror] {
			rec.breakf(accts.Reserve,
				"this bank's record of booking %s names posting %s, which is not on this account",
				advice.Reference, mirror)
		}
	}
	return nil
}

// suspenseIsAged reports this bank's clearing suspense as a position and never
// as a break.
func (s *BankNetwork) suspenseIsAged(ctx context.Context, tx BankTx, bank *Bank, accts BankAccounts,
	rec *Reconciliation,
) error {
	rep, _, err := s.ageTx(ctx, tx, bank, accts.Suspense, rec.Asset)
	if err != nil {
		return err
	}
	if rep.Balance == 0 {
		return nil
	}
	rec.Positions = append(rec.Positions, rep)
	return nil
}

// ListSettlementAdvices is every statement this bank has been sent, oldest
// first: what each one said its reserve moved by, what the central bank said
// the account was left at, and the transaction this bank booked from it.
func (s *BankNetwork) ListSettlementAdvices(ctx context.Context) ([]SettlementAdvice, error) {
	var out []SettlementAdvice
	err := s.store.View(ctx, func(ctx context.Context, tx BankTx) error {
		var err error
		out, err = s.ListSettlementAdvicesTx(ctx, tx)
		return err
	})
	return out, err
}

// ListSettlementAdvicesTx is ListSettlementAdvices within a caller-supplied unit
// of work.
func (s *BankNetwork) ListSettlementAdvicesTx(ctx context.Context, tx BankTx) ([]SettlementAdvice, error) {
	bank, err := s.selfBankTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	return tx.ListSettlementAdvices(ctx, bank.BookID)
}

func (r *Reconciliation) breakf(account ledger.AccountID, format string, args ...any) {
	r.Breaks = append(r.Breaks, Finding{Account: account, What: fmt.Sprintf(format, args...)})
}
