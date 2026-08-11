package payment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
)

// ReturnWindowDays is how long a receiving bank has to send back money it cannot
// apply, in whole calendar days.
//
// The rulebook figure is THREE BANKING BUSINESS DAYS (EPC SCT, D+3), and this is
// calendar days because there is no business date in this system — no roll
// event, no calendar and no holiday handling. What the approximation costs is
// exactly one thing: a balance that arrives on a Thursday is called overdue on
// Sunday where the rulebook would call it overdue on Tuesday. The error is
// always EARLY and never late.
//
// That costs nothing operationally, because the window GATES NO ACT. A bank may
// return an unapplicable credit at any time; the rulebook window is a deadline
// on its obligation and not permission to begin. So the window decides which
// line of a report is printed in bold, which is the whole amount of correctness
// a calendar-day approximation can carry.
//
// It is SCT's, and it is applied to push schemes because SCT is the only one. A
// second push scheme states its own or inherits this deliberately; a PULL scheme
// gets no deadline at all, for the reason AgedLot.Blocked gives.
const ReturnWindowDays = 3

// AgeingReport is one of a bank's own in-transit accounts, decomposed into what
// its balance is made of and how long each part has been there.
//
// A balance is one number and says nothing about time. A clearing suspense
// holding a thousand is business as usual if it arrived this morning and a
// finding if it arrived in March, and only the decomposition tells them apart.
// Money in flight between two consistency domains has to sit visibly in a named
// account, and a named clearing account WITH AN AGEING REPORT is business as
// usual where an unexplained gap between two systems is a finding. The account
// has been here since the banks were founded; this is the other half.
type AgeingReport struct {
	Bank    iso20022.BIC
	Asset   ledger.AssetCode
	Account ledger.AccountID
	AsOf    time.Time
	Balance ledger.Amount
	Lots    []AgedLot
}

// Overdue is the lots a rulebook says should have moved by now. It is empty on
// an account no rulebook puts a clock on; see AgedLot.Deadline.
func (r AgeingReport) Overdue() []AgedLot {
	var out []AgedLot
	for _, l := range r.Lots {
		if l.Overdue() {
			out = append(out, l)
		}
	}
	return out
}

// AgedLot is one part of a balance, with what put it there and what may now be
// done about it.
type AgedLot struct {
	ledger.Lot

	// Payment is what the posting named, and it is EMPTY where the posting named
	// nothing — which is not a gap in this code but a fact about the postings.
	// See AgeClearingSuspense.
	Payment PaymentID
	Scheme  SchemeID

	// Deadline is the rulebook window in whole days, and ZERO where there is no
	// rulebook clock on this money at all. A clearing suspense is discharged by
	// a conversation rather than by a clock, so its lots carry none; an
	// unclaimed balance on a push scheme carries ReturnWindowDays.
	Deadline int

	// Blocked says why this bank cannot clear this lot itself, and is empty when
	// it can. It is prose because a reader of it is deciding what to do next,
	// and the three reasons are not variants of one thing.
	Blocked string
}

// Overdue reports whether a rulebook clock has run out on this lot. A lot with
// no deadline is never overdue, however old it is.
func (l AgedLot) Overdue() bool { return l.Deadline > 0 && l.Days >= l.Deadline }

// AgeClearingSuspense decomposes this bank's clearing suspense by age.
//
// # It ages and does not judge
//
// There is no rulebook deadline on a clearing suspense, so no lot here carries
// one. What discharges it is a conversation — the mirror leg from the central
// bank and the per-payment advices from the clearing house — and a conversation
// has no due date this bank could hold anybody to. What the report gives an
// operator is the age; what counts as too long is their judgement, and pretending
// otherwise would be inventing a rulebook.
//
// # Whose bank you are decides whether your residue has a name
//
// A payer's bank raises its suspense with the DEBTOR leg, which names the
// payment, and lowers it with the netted mirror leg, which names nothing. A
// payee's bank does the opposite: the netted leg raises it and the creditor legs
// lower it. So the surviving lots carry a payment id at one end of a payment and
// nothing at all at the other, out of the same walk — and the missing name is a
// fact about a netted settlement rather than something this report could recover.
//
// # And it is never a break
//
// The instrument reports this as a position however old it is, and that is not
// timidity. A bank told its payment SETTLED and not told its reserves moved has
// a suspense nothing in its own database explains, and it has done nothing
// wrong; only the settlement agent's register separates that from a defect, and
// reading it is what no institution may do. See payment/recon, which can.
func (s *Network) AgeClearingSuspense(ctx context.Context, asset ledger.AssetCode) (AgeingReport, error) {
	var out AgeingReport
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.AgeClearingSuspenseTx(ctx, tx, asset)
		return err
	})
	return out, err
}

// AgeClearingSuspenseTx is AgeClearingSuspense within a caller-supplied unit of
// work.
func (s *Network) AgeClearingSuspenseTx(ctx context.Context, tx Tx, asset ledger.AssetCode) (AgeingReport, error) {
	bank, accts, err := s.ownAccountsTx(ctx, tx, asset)
	if err != nil {
		return AgeingReport{}, err
	}
	rep, _, err := s.ageTx(ctx, tx, bank, accts.Suspense, asset)
	return rep, err
}

// AgeUnclaimedBalances decomposes this bank's unclaimed balances by age and says
// what may be done about each part.
//
// Every credit into this account is ONE payment's diverted leg and carries that
// payment's id, so the answer here is per payment and exact where a clearing
// suspense's is FIFO lots. The difference is a fact about the postings rather
// than about the report.
//
// # Three states, and only the first can be cleared by this bank
//
//   - A settled payment whose payee could not be paid, on a PUSH scheme. This
//     bank is the returner, and a return sends it back: PostReturnLegTx
//     debits this account with no customer check, because the payee never
//     received the money and the bank is releasing an obligation rather than
//     taking money off anybody. Deadline is ReturnWindowDays.
//
//   - The same, on a PULL scheme. The bank holding the money is the CREDITOR's,
//     and the returner is the DEBTOR's — a pacs.004 on a pull is the payer's
//     bank's instrument and the payer has asked for nothing. What the creditor's
//     bank wants is a REVERSAL, pacs.007, which this system does not have. So
//     the lot is reported, blocked, and carries no deadline: a clock this bank
//     has no instrument to beat would be a report telling somebody off for a
//     message they cannot send.
//
//   - A payment already RETURNED, whose payer could not take the refund
//     (refundTx's diversion). There is nothing further to return it to: the money
//     has been sent back and the payer's own bank is holding it for a customer
//     who closed the account. It is terminal, and blocked for that reason rather
//     than for a missing message.
func (s *Network) AgeUnclaimedBalances(ctx context.Context, asset ledger.AssetCode) (AgeingReport, error) {
	var out AgeingReport
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.AgeUnclaimedBalancesTx(ctx, tx, asset)
		return err
	})
	return out, err
}

// AgeUnclaimedBalancesTx is AgeUnclaimedBalances within a caller-supplied unit
// of work.
func (s *Network) AgeUnclaimedBalancesTx(ctx context.Context, tx Tx, asset ledger.AssetCode) (AgeingReport, error) {
	bank, accts, err := s.ownAccountsTx(ctx, tx, asset)
	if err != nil {
		return AgeingReport{}, err
	}
	rep, self, err := s.ageTx(ctx, tx, bank, accts.Unclaimed, asset)
	if err != nil {
		return AgeingReport{}, err
	}
	for i := range rep.Lots {
		lot := &rep.Lots[i]
		if lot.Payment == "" {
			lot.Blocked = "no payment names this posting, so there is nobody to send it back to"
			continue
		}
		p, err := tx.GetPayment(ctx, lot.Payment)
		if err != nil {
			// A lot naming a payment this bank holds no copy of. It cannot
			// arise from anything in this system — the leg and the row are the
			// same institution's — so it is reported rather than swallowed, and
			// it is BLOCKED rather than a returnable lot, because acting on it
			// would be acting on a payment nobody can read.
			if errors.Is(err, ErrPaymentNotFound) {
				lot.Blocked = fmt.Sprintf("this bank holds no copy of payment %s", lot.Payment)
				continue
			}
			return AgeingReport{}, err
		}
		scheme, ok := s.scheme(p.Scheme)
		switch {
		case !ok:
			lot.Blocked = fmt.Sprintf("scheme %s is not registered here, so what may be done with this is unknown", p.Scheme)
		case p.Status == Returned:
			lot.Blocked = "this payment has already been returned; the money is a refund its payer could not take, and there is nothing further to send it back to"
		case !scheme.AllowsReturn():
			lot.Blocked = fmt.Sprintf("scheme %s allows no return", p.Scheme)
		case ReturnerOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent) != self:
			lot.Blocked = fmt.Sprintf(
				"on a %s the return is %s's to send and this bank holds the money; what a creditor's bank sends back is a reversal (pacs.007), which this system does not have",
				scheme.Direction(), ReturnerOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent))
		default:
			lot.Deadline = ReturnWindowDays
		}
	}
	return rep, nil
}

// ownAccountsTx is the acting institution's own bank row and its internal
// accounts in one asset. The clearing house has no ledger and the settlement
// agent holds no suspense of its own, so both are refused here rather than
// answered with an empty report.
func (s *Network) ownAccountsTx(ctx context.Context, tx Tx, asset ledger.AssetCode) (*Bank, BankAccounts, error) {
	bank, err := s.selfBankTx(ctx, tx)
	if err != nil {
		return nil, BankAccounts{}, err
	}
	accts, err := bank.AccountsFor(asset)
	if err != nil {
		return nil, BankAccounts{}, err
	}
	return bank, accts, nil
}

// ageTx is the walk both reports share: one account's history, decomposed FIFO,
// with whatever identity each posting carried lifted onto the lot.
//
// The identity comes off the transaction's METADATA, which every payment-layer
// posting carries (paymentMetadata) and the netted mirror leg does not. Reading
// it here rather than in each report is what keeps "which payment put this
// here" one rule instead of two.
func (s *Network) ageTx(ctx context.Context, tx Tx, bank *Bank, account ledger.AccountID,
	asset ledger.AssetCode,
) (AgeingReport, iso20022.BIC, error) {
	hist, err := bank.Ledger.AccountHistoryTx(ctx, tx, account.Total())
	if err != nil {
		return AgeingReport{}, "", err
	}
	now := s.now()
	rep := AgeingReport{
		Bank:    bank.BIC,
		Asset:   asset,
		Account: account,
		AsOf:    now,
		Balance: hist.Closing,
	}
	for _, lot := range hist.AgeAt(now).Lots {
		rep.Lots = append(rep.Lots, AgedLot{
			Lot:     lot,
			Payment: PaymentID(lot.Metadata["payment_id"]),
			Scheme:  SchemeID(lot.Metadata["scheme"]),
		})
	}
	return rep, bank.BIC, nil
}
