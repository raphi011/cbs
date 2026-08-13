package payment

import (
	"context"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
)

// Scheme is the generic abstraction over a payment scheme.
type Scheme interface {
	// ID is the scheme's unique identifier, e.g. "sepa.ct".
	ID() SchemeID

	// Asset is the unit the scheme settles in. Both legs of a payment must be
	// denominated in it.
	Asset() ledger.AssetCode

	// AddressedBy is the kind of external address this scheme routes on. Both legs
	// of a payment must carry an identifier in it.
	AddressedBy() deposit.IdentifierScheme

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

	// Validate checks the preconditions the DEBTOR's bank owns — the funds, and
	// anything else that can only be read in the payer's own book.
	Validate(ctx context.Context, p *Payment, sc SchemeContext) error

	// ValidateMandate checks the preconditions the CREDITOR's bank owns.
	ValidateMandate(ctx context.Context, p *Payment, sc SchemeContext) error
}

// SchemeContext gives a scheme read access to the rest of the network during
// validation.
type SchemeContext struct {
	// Network is a MEMBER BANK's, because both halves that validate a payment run
	// at one — a push at the submitting bank, a pull at the receiving bank, and
	// both of those are the payer's.
	Network *BankNetwork
	Tx      BankTx
	Now     time.Time
}

// validateFunds is shared by the SEPA schemes: it confirms the debtor's deposit
// account exists at its bank and is permitted to withdraw the payment amount.
func validateFunds(ctx context.Context, p *Payment, sc SchemeContext) error {
	part, err := sc.Network.selfBankTx(ctx, sc.Tx)
	if err != nil {
		return err
	}
	if _, err := sc.Tx.GetDepositAccount(ctx, part.BookID, p.Debtor.Account); err != nil {
		return ErrAccountNotInParticipant
	}
	return part.Deposit.CheckWithdrawalTx(ctx, sc.Tx, p.Debtor.Account, p.Amount)
}

// Which of a payment's two agents plays which part. One rule — the submitting
// side is the payer's bank on a push and the payee's on a pull — and the acts
// that name the other side are its complement.

// SubmitterOf is the party whose bank hands a payment to the clearing house.
func SubmitterOf(scheme Scheme, debtorAgent, creditorAgent iso20022.BIC) iso20022.BIC {
	if scheme.Direction() == Pull {
		return creditorAgent
	}
	return debtorAgent
}

// ReceiverOf is the bank that ANSWERS a payment and the address a released
// output file carries: the other one.
func ReceiverOf(scheme Scheme, debtorAgent, creditorAgent iso20022.BIC) iso20022.BIC {
	return CounterpartyOf(SubmitterOf(scheme, debtorAgent, creditorAgent), debtorAgent, creditorAgent)
}

// ReturnerOf is the party whose bank sends a settled payment back, which is the
// receiving side under the name of a different act.
func ReturnerOf(scheme Scheme, debtorAgent, creditorAgent iso20022.BIC) iso20022.BIC {
	return ReceiverOf(scheme, debtorAgent, creditorAgent)
}

// CounterpartyOf is a payment's other agent, and self again when one bank is
// both sides of it.
func CounterpartyOf(self, debtorAgent, creditorAgent iso20022.BIC) iso20022.BIC {
	if self == debtorAgent {
		return creditorAgent
	}
	return debtorAgent
}

// ---------------------------------------------------------------------------
// SEPA Credit Transfer (SCT)
// ---------------------------------------------------------------------------

// SchemeSEPACT is the identifier of the SEPA Credit Transfer scheme.
const SchemeSEPACT SchemeID = "sepa.ct"

// SCT implements SEPA Credit Transfer: a push payment, net-settled, with no
// mandate. It maps to the ISO 20022 pacs.008 interbank message.
type SCT struct{}

func (SCT) ID() SchemeID                          { return SchemeSEPACT }
func (SCT) Asset() ledger.AssetCode               { return "EUR" }
func (SCT) AddressedBy() deposit.IdentifierScheme { return deposit.IdentifierIBAN }
func (SCT) Direction() SchemeDirection            { return Push }
func (SCT) SettlementModel() SettlementModel      { return Net }
func (SCT) RequiresMandate() bool                 { return false }
func (SCT) AllowsReturn() bool                    { return true }
func (SCT) SettlementDelay() time.Duration        { return 24 * time.Hour } // T+1

func (SCT) Validate(ctx context.Context, p *Payment, sc SchemeContext) error {
	return validateFunds(ctx, p, sc)
}

// ValidateMandate has nothing to check: a credit transfer is the payer
// instructing their own bank, and the instruction IS the authorisation. A
// mandate quoted on one is simply ignored rather than refused.
func (SCT) ValidateMandate(context.Context, *Payment, SchemeContext) error { return nil }

// ---------------------------------------------------------------------------
// SEPA Direct Debit (SDD)
// ---------------------------------------------------------------------------

// SchemeSEPADD is the identifier of the SEPA Direct Debit scheme.
const SchemeSEPADD SchemeID = "sepa.dd"

// SDD implements SEPA Direct Debit: a pull payment, net-settled, requiring a
// mandate, and allowing returns. It maps to the ISO 20022 pacs.003 message.
type SDD struct{}

func (SDD) ID() SchemeID                          { return SchemeSEPADD }
func (SDD) Asset() ledger.AssetCode               { return "EUR" }
func (SDD) AddressedBy() deposit.IdentifierScheme { return deposit.IdentifierIBAN }
func (SDD) Direction() SchemeDirection            { return Pull }
func (SDD) SettlementModel() SettlementModel      { return Net }
func (SDD) RequiresMandate() bool                 { return true }
func (SDD) AllowsReturn() bool                    { return true }
func (SDD) SettlementDelay() time.Duration        { return 48 * time.Hour } // T+2

// ValidateMandate is the half the CREDITOR's bank runs, because in SEPA the
// creditor holds the mandate.
func (SDD) ValidateMandate(ctx context.Context, p *Payment, sc SchemeContext) error {
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
	// SameParty and not ==: a mandate authorises debits from an ACCOUNT, and the
	// address quoted to reach that account is a record on each payment, not part
	// of the authorisation's identity.
	if !m.Debtor.SameParty(p.Debtor) || !m.Creditor.SameParty(p.Creditor) {
		return ErrMandateMismatch
	}
	if m.MaxAmount > 0 && p.Amount > m.MaxAmount {
		return ErrMandateExceeded
	}
	return nil
}

// Validate is now only the funds check, which the DEBTOR's bank runs on
// receipt. It is the one fact about a direct debit that only the debtor's own
// bank holds.
func (SDD) Validate(ctx context.Context, p *Payment, sc SchemeContext) error {
	return validateFunds(ctx, p, sc)
}
