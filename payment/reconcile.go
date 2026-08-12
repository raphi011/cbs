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
//
// # Why it is a Network method and payment/recon is not
//
// A Network is one institution's handle and every method on it is an act that
// institution may perform. This is such an act: a bank holding its reserve
// account against the camt.053 statements it received reads nothing that is not
// its own. payment/recon is deliberately NOT a Network method for the mirror
// reason — it opens all N+2 databases, which is not an act, which is why its
// reader is a harness.
//
// The two are one word apart and answer different questions from different
// numbers of databases. What this finds, the harness finds too; what the harness
// finds, this mostly cannot. See Breaks below for where the line falls.
//
// # The reserve identity closes, and the suspense one does not
//
// Exactly two things post to a bank's Reserve at Central Bank: an advice's
// mirror leg (PostSettlementAdviceTx, recorded by a settlement_advices row
// naming the transaction) and a lodgement (LodgeReservesTx, this bank's own act,
// which posts BEFORE the agent does because a camt.025 carries no amount). There
// is no third. So every entry on that account is classifiable from inside, and
// an entry in neither class is a break this bank can stand behind.
//
// A clearing suspense has no such identity and cannot be given one, because the
// mirror leg is NETTED: one cut-off produces one figure per member covering
// every payment in the batch, naming none of them, deliberately, since a member
// holds no cycle. So the suspense is reported as a POSITION with an age and
// never as a break — from inside the bank nothing proves it wrong.
//
// That is the same distinction payment/recon draws, with the balance of the two
// changed by how much less this instrument can see.
type Reconciliation struct {
	Bank  iso20022.BIC
	Asset ledger.AssetCode
	AsOf  time.Time

	// Reserve is what this bank's own book says its claim on the central bank
	// stands at.
	Reserve ledger.Amount
	// Advised is the closing balance on the NEWEST statement this bank has
	// booked, and Reference is what that statement quoted — a cycle id at a
	// cut-off, a payment id at a return, and this bank cannot tell which and has
	// no reason to. Both are zero when the bank has booked nothing.
	Advised   ledger.Amount
	Reference string
	// LodgedSince is what this bank has lodged since that statement, which is
	// the whole of the difference between the two figures above.
	//
	// It is never negative and the sign is not a convention: a movement the
	// agent made and this bank has not booked leaves NO statement, so it moves
	// neither figure. A lodgement moves this bank's side first, so it moves one.
	// Nothing else can separate them.
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

// Reconcile runs one bank's own reconciliation for one asset and records that it
// ran.
//
// It is an Update and not a View, and that is the decision rather than an
// oversight. The read and the audit appends are ONE unit of work, so a finding
// is a claim about a consistent snapshot rather than about several reads taken
// while a cut-off committed underneath them. It is therefore a read-then-write —
// the one shape the ephemeral store hides, which is why the concurrency test for
// it opens a file.
func (s *BankNetwork) Reconcile(ctx context.Context, asset ledger.AssetCode) (Reconciliation, error) {
	var out Reconciliation
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.ReconcileTx(ctx, tx, asset)
		return err
	})
	return out, err
}

// ReconcileTx is Reconcile within a caller-supplied unit of work.
//
// It refuses an institution that is no bank: the clearing house has no ledger at
// all and the settlement agent holds no reserve of its own, so the question has
// no answer at either of them rather than an empty one.
func (s *BankNetwork) ReconcileTx(ctx context.Context, tx Tx, asset ledger.AssetCode) (Reconciliation, error) {
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
			// The zero-movement arm of PostSettlementAdviceTx commits a row and
			// posts no leg, because there is nothing to post. It is a guard on a
			// caller and the settlement path never reaches it — the agent sends
			// no statement for a position of zero. A row claiming a MOVEMENT
			// with no leg behind it is a different thing and is a break.
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
//
// The walk is what makes the check complete rather than a comparison of two
// totals. Three defects have the same total and different histories:
//
//   - a mirror leg posted in the WRONG DIRECTION OR AMOUNT — the advice says one
//     figure and the leg moved another;
//   - a reserve moved by something with NO STATEMENT behind it, which is exactly
//     the damage cmd/server/recon_test.go's diverged-mirror fixture injects and which
//     needed five databases to catch until this existed;
//   - a statement MISSED and then superseded by one that did arrive: the bank
//     books the later movement onto a reserve that never took the earlier one,
//     so the later advice's closing balance and the running balance part
//     company, and stay parted.
//
// The running balance is what catches all three, and it is only available in
// order, which is why this reads a history rather than a balance.
//
// # What it cannot catch, and the row is worth as much as the others
//
// The LAST statement never arriving. A closing balance only ever arrives on a
// statement the bank holds, and both absences — told and could not book, never
// told at all — leave the newest statement it holds agreeing with its own
// reserve. Nothing inside this bank distinguishes them and nothing here pretends
// to.
//
// It is REACHABLE and it is INVISIBLE, and that combination is the point. A
// statement is not pushed at a member; it waits in the member's download queue
// until the member collects, so a bank that stops collecting stops being told
// and this walk goes on agreeing with itself. What closes it is not a check at
// this bank at all — it is the settlement agent's own liability to the member,
// which recon compares across the two databases, or a periodic statement. There
// is no periodic statement.
func (s *Network) reserveHoldsTogether(ctx context.Context, tx Tx, bank *Bank, accts BankAccounts,
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
//
// There is no closed-form identity for it from inside one bank and there cannot
// be one, for the reason Reconciliation's doc gives: the mirror leg is netted and
// names no payment, so the balance cannot be decomposed into the payments that
// put it there. What CAN be said is how long each part of it has sat, by walking
// the account's own entries FIFO — which is what an operations team does, and
// which attributes a netted leg by the only fact available, order.
//
// So a suspense that has not returned to zero is money in flight, and how long
// it has been is the whole of what this can say about it. No rulebook puts a
// clock on a clearing suspense — what discharges it is a conversation — so there
// is no deadline to apply here or anywhere. See AgeClearingSuspense, whose
// report this is.
func (s *Network) suspenseIsAged(ctx context.Context, tx Tx, bank *Bank, accts BankAccounts,
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
// first: what each one said its reserve moved by, what the central bank said the
// account was left at, and the transaction this bank booked from it.
//
// Reconcile is what CHECKS these rows and this is what shows them, which is why
// the two sit together. A break naming a reference sends its reader here, and
// there is nowhere else to go: the statement is a message, and this row is the
// whole of what survives it.
//
// It refuses an institution that is no bank, for ReconcileTx's reason. An advice
// belongs to the MEMBER that was advised — Book is part of its identity — and
// neither of the other two institutions holds one.
func (s *BankNetwork) ListSettlementAdvices(ctx context.Context) ([]SettlementAdvice, error) {
	var out []SettlementAdvice
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.ListSettlementAdvicesTx(ctx, tx)
		return err
	})
	return out, err
}

// ListSettlementAdvicesTx is ListSettlementAdvices within a caller-supplied unit
// of work.
func (s *BankNetwork) ListSettlementAdvicesTx(ctx context.Context, tx Tx) ([]SettlementAdvice, error) {
	bank, err := s.selfBankTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	return tx.ListSettlementAdvices(ctx, bank.BookID)
}

func (r *Reconciliation) breakf(account ledger.AccountID, format string, args ...any) {
	r.Breaks = append(r.Breaks, Finding{Account: account, What: fmt.Sprintf(format, args...)})
}
