package payment

import (
	"context"
	"time"

	"github.com/raphi011/cbs/deposit"
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

	// AddressedBy is the kind of external address this scheme routes on. Both
	// legs of a payment must carry an identifier in it.
	//
	// It is a property of the scheme, exactly as Asset is: SEPA routes on
	// IBANs, a card scheme routes on a PAN. Putting it here rather than on the
	// account is what keeps a euro-area retail standard out of the deposit
	// layer — an account has addresses, and the scheme decides which kind it
	// reads.
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

	// Validate checks the preconditions the DEBTOR's bank owns — the funds,
	// and anything else that can only be read in the payer's own book.
	//
	// It is one half of a pair, and which actor runs it depends on the
	// direction: the submitting bank for a push, the receiving bank for a
	// pull. The other half is ValidateMandate.
	Validate(ctx context.Context, p *Payment, sc SchemeContext) error

	// ValidateMandate checks the preconditions the CREDITOR's bank owns. In
	// SEPA that is the mandate, because the mandate is a document the creditor
	// holds; a scheme with no mandate has nothing to check here and says so by
	// returning nil.
	//
	// It is on the interface rather than reached by a type assertion because
	// the two halves are one decision — who may check what — and a scheme that
	// implemented only one of them would be silently half-validated.
	ValidateMandate(ctx context.Context, p *Payment, sc SchemeContext) error
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

// validateFunds is shared by the SEPA schemes: it confirms the debtor's
// deposit account exists at its bank and is permitted to withdraw the payment
// amount. The deposit layer is the authority for the funds/status check; a
// shortfall surfaces as deposit.ErrInsufficientAvailable.
//
// The asset check (each leg must be denominated in the scheme's asset) does
// not live here: it runs unconditionally in Network.debtorSideTx and
// Network.creditorSideTx, before any scheme's Validate is reached, so it
// applies to every scheme rather than only the ones whose Validate happens to
// call this helper.
func validateFunds(ctx context.Context, p *Payment, sc SchemeContext) error {
	part, err := sc.Network.participantTx(ctx, sc.Tx, p.Debtor.Participant)
	if err != nil {
		return ErrParticipantNotFound
	}
	if _, err := sc.Tx.GetDepositAccount(ctx, part.BookID, p.Debtor.Account); err != nil {
		return ErrAccountNotInParticipant
	}
	return part.Deposit.CheckWithdrawalTx(ctx, sc.Tx, p.Debtor.Account, p.Amount)
}

// ReturnerOf is the party whose bank sends a settled payment back.
//
// A return is sent by the bank that RECEIVED the instruction — the payee's bank
// on a push, the payer's bank on a pull — which is the SEPA rule book's own
// division. The beneficiary bank returns a credit transfer it cannot apply; the
// debtor bank returns a collection its customer disputes.
//
// Written as its own rule rather than as "not the submitter", because the two
// are answers to different questions and the reason each is what it is has
// nothing to do with the other: a submitter is chosen by who is instructing, and
// a returner by who is holding a payment they cannot keep. That they come out
// opposite in both directions is a fact about these two flows, not a derivation.
// And a party who is both — a payment from a bank to itself — would make a
// negation ambiguous, while these two rules stay total.
//
// It takes the two refs rather than a Payment, because mesh.Mesh.Submit's
// counterpart rule has only a request to work from and the two are written the
// same way.
//
// # Why it lives here and not in mesh
//
// It used to be mesh.returnerOf and nothing else, because picking which actor's
// goroutine sends the pacs.004 was the only use for it. PostReturnLegTx is a
// second use, in the domain: which leg a bank posts follows from which side of
// the payment it is on, and whether that bank may REFUSE the leg follows from
// whether it is the returner. Two copies of a rule that both the mesh's
// actor-selection and the domain's refusal test consult would be free to drift,
// and a mesh that sent a return from one bank while the domain let the other
// refuse is a payment nobody can finish. mesh.returnerOf is a one-line
// delegation now.
func ReturnerOf(scheme Scheme, debtor, creditor PartyRef) PartyRef {
	if scheme.Direction() == Pull {
		return debtor
	}
	return creditor
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
// mandate quoted on one is simply ignored rather than refused, exactly as it
// was before this half existed.
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
// creditor holds the mandate. It is synchronous, at submission, and its
// failures are 422s rather than pacs.002 rejections — your own bank refusing
// your collection is not a message from a counterparty.
//
// A real debtor's bank keeps mandate records of its own and can reject a
// collection with MD01 on the wire. This system's mandates live once, in the
// network's own store, so that rejection has nowhere to come from; the limit
// is worth naming rather than discovering.
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
	// SameParty and not ==: a mandate authorises debits from an ACCOUNT, and
	// the address quoted to reach that account is a record on each payment, not
	// part of the authorisation's identity. Comparing whole structs would mean
	// that withdrawing the debtor's IBAN and issuing a new one — which the
	// register allows precisely because it is not supposed to disturb anything
	// — killed every mandate on the account, permanently. See PartyRef.SameParty.
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
//
// It is identical to SCT.Validate, which is correct and not a smell: both say
// "the PAYER's bank checks the payer's funds", and only the moment differs —
// at submission for a push, on receipt of the collection for a pull. It runs in
// debtorSideTx either way (system.go), which is the one function both paths
// reach and the reason the two bodies are the same line.
func (SDD) Validate(ctx context.Context, p *Payment, sc SchemeContext) error {
	return validateFunds(ctx, p, sc)
}
