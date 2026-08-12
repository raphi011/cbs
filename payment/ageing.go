package payment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/raphi011/cbs/calendar"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
)

// ReturnWindowDays is how long a receiving bank has to send back money it
// cannot apply, in whole BANKING BUSINESS days.
const ReturnWindowDays = 3

// AgeingReport is one of a bank's own in-transit accounts, decomposed into what
// its balance is made of and how long each part has been there.
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
		if l.Overdue(r.AsOf) {
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

	// Deadline is the rulebook window in whole banking business days, and ZERO
	// where there is no rulebook clock on this money at all.
	Deadline int

	// Due is the day the window runs out, Deadline settlement days after the lot's
	// own day, and ZERO wherever Deadline is.
	Due time.Time

	// Blocked says why this bank cannot clear this lot itself, and is empty when
	// it can. It is prose because a reader of it is deciding what to do next,
	// and the three reasons are not variants of one thing.
	Blocked string
}

// Overdue reports whether a rulebook clock has run out on this lot by asOf. A
// lot with no deadline is never overdue, however old it is.
func (l AgedLot) Overdue(asOf time.Time) bool {
	return !l.Due.IsZero() && !ledger.DayStart(asOf).Before(l.Due)
}

// AgeClearingSuspense decomposes this bank's clearing suspense by age.
func (s *BankNetwork) AgeClearingSuspense(ctx context.Context, asset ledger.AssetCode) (AgeingReport, error) {
	var out AgeingReport
	err := s.store.View(ctx, func(ctx context.Context, tx BankTx) error {
		var err error
		out, err = s.AgeClearingSuspenseTx(ctx, tx, asset)
		return err
	})
	return out, err
}

// AgeClearingSuspenseTx is AgeClearingSuspense within a caller-supplied unit of
// work.
func (s *BankNetwork) AgeClearingSuspenseTx(ctx context.Context, tx BankTx, asset ledger.AssetCode) (AgeingReport, error) {
	bank, accts, err := s.ownAccountsTx(ctx, tx, asset)
	if err != nil {
		return AgeingReport{}, err
	}
	rep, _, err := s.ageTx(ctx, tx, bank, accts.Suspense, asset)
	return rep, err
}

// AgeUnclaimedBalances decomposes this bank's unclaimed balances by age and
// says what may be done about each part.
func (s *BankNetwork) AgeUnclaimedBalances(ctx context.Context, asset ledger.AssetCode) (AgeingReport, error) {
	var out AgeingReport
	err := s.store.View(ctx, func(ctx context.Context, tx BankTx) error {
		var err error
		out, err = s.AgeUnclaimedBalancesTx(ctx, tx, asset)
		return err
	})
	return out, err
}

// AgeUnclaimedBalancesTx is AgeUnclaimedBalances within a caller-supplied unit
// of work.
func (s *BankNetwork) AgeUnclaimedBalancesTx(ctx context.Context, tx BankTx, asset ledger.AssetCode) (AgeingReport, error) {
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
			// A lot naming a payment this bank holds no copy of.
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
			lot.Due = calendar.AddSettlementDays(lot.Since, ReturnWindowDays)
		}
	}
	return rep, nil
}

// ownAccountsTx is the acting institution's own bank row and its internal
// accounts in one asset.
func (s *BankNetwork) ownAccountsTx(ctx context.Context, tx BankTx, asset ledger.AssetCode) (*Bank, BankAccounts, error) {
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
func (s *BankNetwork) ageTx(ctx context.Context, tx BankTx, bank *Bank, account ledger.AccountID,
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
