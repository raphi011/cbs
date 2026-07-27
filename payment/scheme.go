package payment

import (
	"context"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Scheme is the generic abstraction over a payment scheme. Concrete schemes
// (SEPA Credit Transfer, SEPA Direct Debit, and later instant or card
// schemes) implement this interface, so the Network orchestrator can drive
// any of them without knowing their specifics.
//
// Adding a new scheme means writing one type that implements this interface
// and registering it with the Network — no changes to the orchestrator.
type Scheme interface {
	// ID is the scheme's unique identifier, e.g. "sepa.ct".
	ID() SchemeID

	// Asset is the unit the scheme settles in. Both legs of a payment must be
	// denominated in it.
	//
	// This is a property of the scheme, not a limitation of the ledger: SEPA
	// is a euro scheme. A cross-currency payment is not one payment at all —
	// it is a payment plus an FX trade.
	Asset() ledger.AssetCode

	// Direction reports whether the debtor pushes funds or the creditor
	// pulls them.
	Direction() SchemeDirection

	// SettlementModel reports whether payments are netted and settled in
	// batches (Net) or settled individually and immediately (Gross).
	SettlementModel() SettlementModel

	// RequiresMandate reports whether a mandate must accompany the payment.
	RequiresMandate() bool

	// AllowsReturn reports whether a settled payment may be returned (a SEPA
	// R-transaction).
	AllowsReturn() bool

	// SettlementDelay is how far after booking the funds take economic
	// effect; it determines the value date of the postings.
	SettlementDelay() time.Duration

	// Validate checks scheme-specific preconditions (funds, mandate, ...)
	// before a payment is accepted.
	Validate(ctx context.Context, p *Payment, sc SchemeContext) error
}

// SchemeContext gives a scheme read access to the rest of the network during
// validation.
//
// It carries the Tx of the unit of work the payment is being accepted in, so a
// scheme reads the same snapshot the postings will be made against — a funds
// check that ran in its own transaction could be stale by the time the debit
// posts. For the same reason schemes must drive Tx and the …Tx methods only,
// never a plain Network or Register method: those open a second unit of work
// inside this one.
type SchemeContext struct {
	Network *Network
	Tx      Tx
	Now     time.Time
}

// validateFunds is shared by the SEPA schemes: it confirms both legs are
// denominated in the scheme's asset, that the debtor's deposit account exists
// at its bank, and that it is permitted to withdraw the payment amount. The
// deposit layer is the authority for the funds/status check; a shortfall
// surfaces as deposit.ErrInsufficientAvailable.
func validateFunds(ctx context.Context, p *Payment, sc SchemeContext) error {
	scheme, ok := sc.Network.scheme(p.Scheme)
	if !ok {
		return ErrSchemeNotFound
	}

	part, err := sc.Network.participantTx(ctx, sc.Tx, p.Debtor.Participant)
	if err != nil {
		return ErrParticipantNotFound
	}
	debtorAccount, err := sc.Tx.GetDepositAccount(ctx, part.BookID, p.Debtor.Account)
	if err != nil {
		return ErrAccountNotInParticipant
	}

	// The creditor's account lives in a different participant's book, so it is
	// resolved the same way the creditor leg is at settlement (see
	// Network.SettleCycleTx) rather than assumed to share the debtor's book.
	creditorPart, err := sc.Network.participantTx(ctx, sc.Tx, p.Creditor.Participant)
	if err != nil {
		return ErrParticipantNotFound
	}
	creditorAccount, err := sc.Tx.GetDepositAccount(ctx, creditorPart.BookID, p.Creditor.Account)
	if err != nil {
		return ErrAccountNotInParticipant
	}

	if debtorAccount.Asset != scheme.Asset() || creditorAccount.Asset != scheme.Asset() {
		return ErrAssetMismatch
	}

	return part.Deposit.CheckWithdrawalTx(ctx, sc.Tx, p.Debtor.Account, p.Amount)
}

// ---------------------------------------------------------------------------
// SEPA Credit Transfer (SCT)
// ---------------------------------------------------------------------------

// SchemeSEPACT is the identifier of the SEPA Credit Transfer scheme.
const SchemeSEPACT SchemeID = "sepa.ct"

// SCT implements SEPA Credit Transfer: a push payment, net-settled, with no
// mandate. It maps to the ISO 20022 pacs.008 interbank message.
type SCT struct{}

func (SCT) ID() SchemeID                     { return SchemeSEPACT }
func (SCT) Asset() ledger.AssetCode          { return "EUR" }
func (SCT) Direction() SchemeDirection       { return Push }
func (SCT) SettlementModel() SettlementModel { return Net }
func (SCT) RequiresMandate() bool            { return false }
func (SCT) AllowsReturn() bool               { return true }
func (SCT) SettlementDelay() time.Duration   { return 24 * time.Hour } // T+1

func (SCT) Validate(ctx context.Context, p *Payment, sc SchemeContext) error {
	return validateFunds(ctx, p, sc)
}

// ---------------------------------------------------------------------------
// SEPA Direct Debit (SDD)
// ---------------------------------------------------------------------------

// SchemeSEPADD is the identifier of the SEPA Direct Debit scheme.
const SchemeSEPADD SchemeID = "sepa.dd"

// SDD implements SEPA Direct Debit: a pull payment, net-settled, requiring a
// mandate, and allowing returns. It maps to the ISO 20022 pacs.003 message.
type SDD struct{}

func (SDD) ID() SchemeID                     { return SchemeSEPADD }
func (SDD) Asset() ledger.AssetCode          { return "EUR" }
func (SDD) Direction() SchemeDirection       { return Pull }
func (SDD) SettlementModel() SettlementModel { return Net }
func (SDD) RequiresMandate() bool            { return true }
func (SDD) AllowsReturn() bool               { return true }
func (SDD) SettlementDelay() time.Duration   { return 48 * time.Hour } // T+2

func (SDD) Validate(ctx context.Context, p *Payment, sc SchemeContext) error {
	if p.MandateID == "" {
		return ErrMandateRequired
	}
	m, err := sc.Tx.GetMandate(ctx, p.MandateID)
	if err != nil {
		return err
	}
	if m.Status == MandateRevoked {
		return ErrMandateRevoked
	}
	if m.Debtor != p.Debtor || m.Creditor != p.Creditor {
		return ErrMandateMismatch
	}
	if m.MaxAmount > 0 && p.Amount > m.MaxAmount {
		return ErrMandateExceeded
	}
	return validateFunds(ctx, p, sc)
}
