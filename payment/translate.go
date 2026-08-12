package payment

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
)

// reasonMapping classifies one sentinel from errors.go.
type reasonMapping struct {
	Err  error
	Name string
	Code iso20022.StatusReason
}

// reasonTable maps this package's sentinels to wire codes, undriftably.
var reasonTable = []reasonMapping{
	// --- Rejections a counterparty actually receives ---

	{ErrAccountNotInParticipant, "ErrAccountNotInParticipant", iso20022.StatusReasonIncorrectAccountNumber},
	{ErrDuplicateEndToEndID, "ErrDuplicateEndToEndID", iso20022.StatusReasonDuplication},

	// MD01 means there is NO mandate, which is the right and more serious
	// claim for a revoked one: a revoked mandate is precisely the absence of a
	// valid one.
	{ErrMandateRequired, "ErrMandateRequired", iso20022.StatusReasonNoMandate},
	{ErrMandateNotFound, "ErrMandateNotFound", iso20022.StatusReasonNoMandate},
	{ErrMandateRevoked, "ErrMandateRevoked", iso20022.StatusReasonNoMandate},

	// Not MD01. A valid mandate exists and this collection falls outside it,
	// which the external set has no code for; MD01 would put a false statement
	// on the wire. MS03 plus AddtlInf says less, accurately.
	{ErrMandateMismatch, "ErrMandateMismatch", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrMandateExceeded, "ErrMandateExceeded", iso20022.StatusReasonNotSpecifiedAgentGenerated},

	{ErrParticipantNotFound, "ErrParticipantNotFound", iso20022.StatusReasonBankIdentifierIncorrect},

	// The same code for a narrower fact, and the code set's own gloss covers both:
	// RC01 is "the BIC does not identify a reachable participant", true of a bank
	// that does not exist and equally of one this scheme has not admitted.
	{ErrBankNotAdmitted, "ErrBankNotAdmitted", iso20022.StatusReasonBankIdentifierIncorrect},
	{ErrUnaddressableAccount, "ErrUnaddressableAccount", iso20022.StatusReasonMissingDebtorAccountOrIdentification},
	{ErrIdentifierMismatch, "ErrIdentifierMismatch", iso20022.StatusReasonMissingDebtorAccountOrIdentification},
	{ErrAmbiguousAddress, "ErrAmbiguousAddress", iso20022.StatusReasonMissingDebtorAccountOrIdentification},
	{ErrCycleNotOpen, "ErrCycleNotOpen", iso20022.StatusReasonInvalidCutOffTime},

	// A settlement instruction the agent cannot read as one batch, and unlike
	// every cycle error above it IS a judgement about the message. The clearing
	// house sent it and can fix it, so it is answered rather than swallowed.
	{ErrInvalidSettlement, "ErrInvalidSettlement", iso20022.StatusReasonNotSpecifiedAgentGenerated},

	// MS03 for a stronger reason than "no better code exists": in SEPA a currency
	// mismatch cannot happen, the scheme being euro-only, so the code set never
	// needed one.
	{ErrAssetMismatch, "ErrAssetMismatch", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrSchemeNotFound, "ErrSchemeNotFound", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrInvalidPaymentAmount, "ErrInvalidPaymentAmount", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrParticipantAssetNotFound, "ErrParticipantAssetNotFound", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrSchemeUnsupportedReturn, "ErrSchemeUnsupportedReturn", iso20022.StatusReasonNotSpecifiedAgentGenerated},

	// An instruction naming no counterparty is refused at submission, before any
	// leg posts and before any message could exist to carry a reason back —
	// classified here alongside the other malformed-instruction refusals because
	// nothing distinguishes it from them.
	{ErrCounterpartyNotNamed, "ErrCounterpartyNotNamed", iso20022.StatusReasonNotSpecifiedAgentGenerated},

	// An on-us instruction is the same category again — an instruction this scheme
	// cannot carry, refused at submission — and MS03 because the code set has
	// nothing for "both of these parties are yours".
	{ErrOnUsPayment, "ErrOnUsPayment", iso20022.StatusReasonNotSpecifiedAgentGenerated},

	// Its sibling, and RC01 rather than MS03 because there IS a code for this one:
	// "the BIC does not identify a reachable participant" is exactly what an
	// absent or malformed CdtrAgt/DbtrAgt means.
	{ErrCounterpartyAgentNotNamed, "ErrCounterpartyAgentNotNamed", iso20022.StatusReasonBankIdentifierIncorrect},

	// And the third of the address refusals, same RC01 for the same reason: an
	// address whose bank code resolves to nothing in this bank's copy of the
	// directory is a payee this scheme cannot be told to reach.
	{ErrBankCodeUnknown, "ErrBankCodeUnknown", iso20022.StatusReasonBankIdentifierIncorrect},

	// --- Classified as never reaching a counterparty ---

	// A lookup for an id this system generated and then could not find is a
	// bug here, not a defect in the message.
	{ErrPaymentNotFound, "ErrPaymentNotFound", ""},
	{ErrCycleNotFound, "ErrCycleNotFound", ""},
	{ErrSettlementNotFound, "ErrSettlementNotFound", ""},

	// A settlement advice is a bank's OWN row about its OWN cut-off, so a
	// missing one is a question this bank asked itself and got no answer to.
	// There is no counterparty in the conversation to tell.
	{ErrSettlementAdviceNotFound, "ErrSettlementAdviceNotFound", ""},

	// The two rows admission gives the other institutions, missing.
	{ErrSettlementMemberNotFound, "ErrSettlementMemberNotFound", ""},
	{ErrRosterEntryNotFound, "ErrRosterEntryNotFound", ""},

	// All three mean a message arrived at the wrong bank — a settled-payment
	// advice for somebody else's customer, a reserve statement about somebody
	// else's account, a return naming a payment this bank is neither side of.
	{ErrNotThisBanksPayment, "ErrNotThisBanksPayment", ""},
	{ErrStatementNotForThisBank, "ErrStatementNotForThisBank", ""},
	{ErrNotAPartyToThisReturn, "ErrNotAPartyToThisReturn", ""},

	// A mandate recorded at a bank that is not its creditor's. It reaches no message at
	// all: creating one is an operator's request on a bank's own console, and no file
	// any institution exchanges carries CreateMandateTx.
	{ErrNotThisBanksMandate, "ErrNotThisBanksMandate", ""},

	// One institution's act reached through another's Network, and the only entry
	// here that is not about a message at all.
	{ErrNotThisInstitutionsAct, "ErrNotThisInstitutionsAct", ""},

	// Cycle lifecycle errors reach only the operator who drove the cycle into
	// the wrong state; no counterparty ever sees one.
	{ErrCycleNotClosed, "ErrCycleNotClosed", ""},
	{ErrCycleAlreadyOpen, "ErrCycleAlreadyOpen", ""},

	// A cut-off the clearing house will not settle because it could not release
	// what it settled.
	{ErrCycleNotReleasable, "ErrCycleNotReleasable", ""},

	// The clearing house holding no return for a payment an answer names.
	{ErrHeldReturnNotFound, "ErrHeldReturnNotFound", ""},

	// A cut-off the settlement agent has already discharged: a redelivered
	// pacs.009 and nothing more.
	{ErrCycleAlreadySettled, "ErrCycleAlreadySettled", ""},

	// An illegal transition means this system tried to move a payment
	// somewhere its own state machine forbids. Telling the counterparty
	// "rejected, unspecified" would hide a defect behind a plausible message.
	{ErrInvalidStateTransition, "ErrInvalidStateTransition", ""},

	// A return the settlement agent has already settled: the redelivery case
	// ErrInvalidStateTransition covers on every other path, arriving from the one
	// actor that has no payment row to read a status off.
	{ErrReturnAlreadySettled, "ErrReturnAlreadySettled", ""},

	// --- The admission refusals, which are answered off this code set ---
	{ErrBICAlreadyAdmitted, "ErrBICAlreadyAdmitted", ""},
	{ErrBankAlreadyAdmitted, "ErrBankAlreadyAdmitted", ""},
	{ErrAdmissionNotIdentified, "ErrAdmissionNotIdentified", ""},
	{ErrAdmittedAccountUnusable, "ErrAdmittedAccountUnusable", ""},
	{ErrSettlementAccountReplaced, "ErrSettlementAccountReplaced", ""},
	{ErrNotThisBanksAdmission, "ErrNotThisBanksAdmission", ""},
	// The three about addressing, on this path for the reason the six above are:
	// every one is refused during an admission, and no admission is answered on a
	// wire.
	{ErrBankCodeNotAllocated, "ErrBankCodeNotAllocated", ""},
	{ErrBankCodeTaken, "ErrBankCodeTaken", ""},
	{ErrBankCodeReplaced, "ErrBankCodeReplaced", ""},
}

// borrowedReasons classifies the errors an actor's half produces that this
// package did not declare.
var borrowedReasons = []reasonMapping{
	{deposit.ErrInsufficientAvailable, "deposit.ErrInsufficientAvailable", iso20022.StatusReasonInsufficientFunds},
	{ledger.ErrInsufficientBalance, "ledger.ErrInsufficientBalance", iso20022.StatusReasonInsufficientFunds},
	{deposit.ErrAccountClosed, "deposit.ErrAccountClosed", iso20022.StatusReasonClosedAccountNumber},
}

// ReasonFor maps an error to the code a pacs.002 should carry.
func ReasonFor(err error) iso20022.StatusReason {
	for _, table := range [][]reasonMapping{reasonTable, borrowedReasons} {
		for _, m := range table {
			if m.Code != "" && errors.Is(err, m.Err) {
				return m.Code
			}
		}
	}
	return iso20022.StatusReasonNotSpecifiedAgentGenerated
}

// Answerable reports whether an error is a judgement about the INSTRUCTION —
// something a counterparty can be told and can act on — as against a failure of
// this system's own bookkeeping, which nobody outside it could do anything
// with.
func Answerable(err error) bool {
	for _, table := range [][]reasonMapping{reasonTable, borrowedReasons} {
		for _, m := range table {
			if m.Code != "" && errors.Is(err, m.Err) {
				return true
			}
		}
	}
	return false
}

// ReturnReasonFor maps an error to the code a pacs.004 should carry.
func ReturnReasonFor(err error) iso20022.ReturnReason {
	switch ReasonFor(err) {
	case iso20022.StatusReasonIncorrectAccountNumber:
		return iso20022.ReturnReasonIncorrectAccountNumber
	case iso20022.StatusReasonClosedAccountNumber:
		return iso20022.ReturnReasonClosedAccountNumber
	case iso20022.StatusReasonInsufficientFunds:
		return iso20022.ReturnReasonInsufficientFunds
	case iso20022.StatusReasonDuplication:
		return iso20022.ReturnReasonDuplication
	case iso20022.StatusReasonNoMandate:
		return iso20022.ReturnReasonNoMandate
	case iso20022.StatusReasonBankIdentifierIncorrect:
		return iso20022.ReturnReasonBankIdentifierIncorrect
	case iso20022.StatusReasonMissingDebtorAccountOrIdentification:
		return iso20022.ReturnReasonMissingDebtorAccountOrIdentification
	default:
		return iso20022.ReturnReasonNotSpecifiedAgentGenerated
	}
}

// ---------------------------------------------------------------------------
// Outbound: payment types to messages
// ---------------------------------------------------------------------------

// MessageContext is everything a message needs that the payment itself does not
// carry: who is sending it, who to, what to call it, and when.
type MessageContext struct {
	From  iso20022.BIC
	To    iso20022.BIC
	MsgID string
	Now   time.Time

	// DecidedBy is who MADE the decision this message reports, when that is not
	// the sender. Empty means the sender decided it, which is every message but
	// one.
	DecidedBy iso20022.BIC
}

func (mc MessageContext) header(msgDefIdr string) iso20022.AppHdr {
	return iso20022.AppHdr{
		Fr:        iso20022.NewAgent(mc.From),
		To:        iso20022.NewAgent(mc.To),
		BizMsgIdr: mc.MsgID,
		MsgDefIdr: msgDefIdr,
		CreDt:     iso20022.ISODateTime{Time: mc.Now},
	}
}

// orgtr is the party that DECIDED, as opposed to the party that sent.
func (mc MessageContext) orgtr() *iso20022.PartyIdentification {
	decider := mc.DecidedBy
	if decider == "" {
		decider = mc.From
	}
	return &iso20022.PartyIdentification{
		Id: &iso20022.PartyChoice{
			OrgId: &iso20022.OrganisationIdentification{AnyBIC: decider},
		},
	}
}

// messageParty is one side of a payment with everything a message says about
// it: the bank that holds the account, the name on the account, and the address
// the payment quoted to reach it.
type messageParty struct {
	BIC        iso20022.BIC
	Name       string
	Identifier deposit.Identifier
}

// agentOf is the element that says WHICH BANK. In SEPA that is a BIC and
// nothing else.
func agentOf(b iso20022.BIC) iso20022.BranchAndFinancialInstitution {
	return iso20022.BranchAndFinancialInstitution{
		FinInstnId: iso20022.FinancialInstitutionIdentification{BICFI: b},
	}
}

// agentRef is agentOf as a pointer, for the elements — OrgnlTxRef's DbtrAgt and
// CdtrAgt — where the schema makes the agent optional.
func agentRef(b iso20022.BIC) *iso20022.BranchAndFinancialInstitution {
	a := agentOf(b)
	return &a
}

// amountOf converts a ledger amount to the standard's decimal representation.
func amountOf(amt ledger.Amount, asset ledger.AssetCode) (iso20022.ActiveCurrencyAndAmount, error) {
	def, err := ledger.LookupAsset(asset)
	if err != nil {
		return iso20022.ActiveCurrencyAndAmount{}, err
	}
	out, err := iso20022.NewAmount(int64(amt), def.Scale, string(asset))
	if err != nil {
		return iso20022.ActiveCurrencyAndAmount{}, err
	}
	if err := out.Validate(); err != nil {
		return iso20022.ActiveCurrencyAndAmount{}, fmt.Errorf("%w: %d minor units of %s", err, amt, asset)
	}
	return out, nil
}

// ibanOf turns a stored address into the message's account identification.
func ibanOf(field string, id deposit.Identifier) (iso20022.IBAN, error) {
	if id.Scheme != deposit.IdentifierIBAN || id.Value == "" {
		return "", fmt.Errorf("%w: %s", ErrUnaddressableAccount, field)
	}
	iban := iso20022.IBAN(id.Value).Compact()
	if err := iban.Validate(); err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrUnaddressableAccount, field, err)
	}
	return iban, nil
}

// cashAccount wraps a validated IBAN as the account element of a message.
func cashAccount(iban iso20022.IBAN) iso20022.CashAccount {
	return iso20022.CashAccount{Id: iso20022.AccountIdentification4Choice{IBAN: &iban}}
}

// namedPartyOf is the customer element of a pacs.008 or a pacs.003.
func namedPartyOf(element, name string) (iso20022.PartyIdentification, error) {
	if name == "" {
		return iso20022.PartyIdentification{}, fmt.Errorf("%w: %s/Nm", iso20022.ErrMissingElement, element)
	}
	return iso20022.PartyIdentification{Nm: name}, nil
}

// endToEndOf is the customer's own reference, or the value the guidelines
// reserve for its absence.
func endToEndOf(p Payment) string {
	if p.EndToEndID == "" {
		return "NOTPROVIDED"
	}
	return p.EndToEndID
}

func paymentIdentificationOf(p Payment) iso20022.PaymentIdentification {
	return iso20022.PaymentIdentification{
		EndToEndId: endToEndOf(p),
		TxId:       string(p.ID),
	}
}

// remittanceOf is what the payment is for, as far as the two customers are
// concerned. Nothing at all rather than an empty element: RmtInf with an empty
// Ustrd is a claim that the sender said something and it was blank.
func remittanceOf(text string) *iso20022.RemittanceInformation {
	if text == "" {
		return nil
	}
	return &iso20022.RemittanceInformation{Ustrd: text}
}

// settlementDateOf is the date the message asserts interbank settlement for.
func settlementDateOf(p Payment, mc MessageContext) iso20022.ISODate {
	if p.ValueDate.IsZero() {
		return iso20022.ISODate{Time: mc.Now}
	}
	return iso20022.ISODate{Time: p.ValueDate}
}

// clearingSettlement is how every message this system sends settles: through a
// clearing system, rather than across accounts the two agents hold with each
// other.
func clearingSettlement() iso20022.SettlementInstruction {
	return iso20022.SettlementInstruction{SttlmMtd: iso20022.SettlementMethodClearing}
}

// assetOf is the unit a payment settles in, which is a property of its scheme
// and not of the payment.
func (s *Network) assetOf(p Payment) (ledger.AssetCode, error) {
	sc, ok := s.scheme(p.Scheme)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrSchemeNotFound, p.Scheme)
	}
	return sc.Asset(), nil
}

// partiesOf resolves a payment's two sides to what a message says about them:
// each bank's BIC and each account holder's name.
func partiesOf(p Payment) (debtor, creditor messageParty) {
	return messageParty{
			BIC:        p.DebtorDetails.Agent,
			Name:       p.DebtorDetails.Name,
			Identifier: p.Debtor.Identifier,
		}, messageParty{
			BIC:        p.CreditorDetails.Agent,
			Name:       p.CreditorDetails.Name,
			Identifier: p.Creditor.Identifier,
		}
}

// outbound is one payment and everything a message builder needs that the
// payment does not carry: both sides as a message names them, the asset its
// scheme settles in, and, on a pull, the mandate that authorises it.
type outbound struct {
	payment  Payment
	mandate  Mandate
	debtor   messageParty
	creditor messageParty
	asset    ledger.AssetCode
}

// outboundOf is the one lookup a builder needs — the payment's scheme, for the
// asset it settles in. partiesOf reads nothing.
func (s *Network) outboundOf(p Payment) (outbound, error) {
	asset, err := s.assetOf(p)
	if err != nil {
		return outbound{}, err
	}
	debtor, creditor := partiesOf(p)
	return outbound{payment: p, debtor: debtor, creditor: creditor, asset: asset}, nil
}

// CreditTransferMessage renders a file of payments as the pacs.008 that carries
// them between banks.
func (s *Network) CreditTransferMessage(ps []Payment, mc MessageContext) (iso20022.Envelope, error) {
	out := make([]outbound, 0, len(ps))
	for _, p := range ps {
		o, err := s.outboundOf(p)
		if err != nil {
			return iso20022.Envelope{}, err
		}
		out = append(out, o)
	}
	return creditTransfer(out, mc)
}

// creditTransfer builds the message from data its caller already holds. Everything it
// does is a pure function of its arguments, which is what FuzzTranslate needs: there
// is no I/O anywhere on this path for a fuzz input to depend on.
func creditTransfer(out []outbound, mc MessageContext) (iso20022.Envelope, error) {
	settled, err := groupSettlementDate(out, mc)
	if err != nil {
		return iso20022.Envelope{}, err
	}

	txs := make([]iso20022.CreditTransferTransaction, 0, len(out))
	for _, o := range out {
		tx, err := creditTransferTx(o)
		if err != nil {
			return iso20022.Envelope{}, err
		}
		txs = append(txs, tx)
	}

	doc := &iso20022.Pacs008{FIToFICstmrCdtTrf: iso20022.FIToFICustomerCreditTransfer{
		GrpHdr: iso20022.CreditTransferGroupHeader{
			MsgId:   mc.MsgID,
			CreDtTm: iso20022.ISODateTime{Time: mc.Now},
			// NbOfTxs is what the SENDER asserts, not a derivation the receiver would
			// recompute. A receiver that recomputed it instead of checking it would never
			// notice a truncated file, which is the whole reason the element exists.
			NbOfTxs:       strconv.Itoa(len(txs)),
			IntrBkSttlmDt: settled,
			SttlmInf:      clearingSettlement(),
		},
		CdtTrfTxInf: txs,
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// creditTransferTx is one transaction of a pacs.008.
func creditTransferTx(o outbound) (iso20022.CreditTransferTransaction, error) {
	var zero iso20022.CreditTransferTransaction

	amt, err := amountOf(o.payment.Amount, o.asset)
	if err != nil {
		return zero, err
	}
	dbtr, err := namedPartyOf("Dbtr", o.debtor.Name)
	if err != nil {
		return zero, err
	}
	dbtrIBAN, err := ibanOf("DbtrAcct", o.debtor.Identifier)
	if err != nil {
		return zero, err
	}
	cdtr, err := namedPartyOf("Cdtr", o.creditor.Name)
	if err != nil {
		return zero, err
	}
	cdtrIBAN, err := ibanOf("CdtrAcct", o.creditor.Identifier)
	if err != nil {
		return zero, err
	}

	return iso20022.CreditTransferTransaction{
		PmtId: paymentIdentificationOf(o.payment),
		PmtTpInf: &iso20022.PaymentTypeInformation{
			SvcLvl: &iso20022.ServiceLevelChoice{Cd: iso20022.ServiceLevelSEPA},
		},
		IntrBkSttlmAmt: amt,
		ChrgBr:         iso20022.ChargeBearerFollowingServiceLevel,
		Dbtr:           dbtr,
		DbtrAcct:       cashAccount(dbtrIBAN),
		DbtrAgt:        agentOf(o.debtor.BIC),
		CdtrAgt:        agentOf(o.creditor.BIC),
		Cdtr:           cdtr,
		CdtrAcct:       cashAccount(cdtrIBAN),
		RmtInf:         remittanceOf(o.payment.Description),
	}, nil
}

// groupSettlementDate is the one interbank settlement date a file asserts.
func groupSettlementDate(out []outbound, mc MessageContext) (iso20022.ISODate, error) {
	if len(out) == 0 {
		return iso20022.ISODate{}, fmt.Errorf("payment: a file with no transactions is not a message")
	}
	settled := settlementDateOf(out[0].payment, mc)
	for _, o := range out[1:] {
		if d := settlementDateOf(o.payment, mc); !d.Time.Equal(settled.Time) {
			return iso20022.ISODate{}, fmt.Errorf("payment: one file cannot assert two settlement dates, %s and %s",
				settled.Time.Format(time.DateOnly), d.Time.Format(time.DateOnly))
		}
	}
	return settled, nil
}

// InstructionMessage renders one cut-off file: the pacs.008 or pacs.003 a bank
// hands the clearing house when it reaches its cut-off.
func (s *BankNetwork) InstructionMessage(ctx context.Context, ps []Payment, mc MessageContext) (iso20022.Envelope, error) {
	if len(ps) == 0 {
		return iso20022.Envelope{}, fmt.Errorf("payment: a file with no transactions is not a message")
	}
	scheme, ok := s.scheme(ps[0].Scheme)
	if !ok {
		return iso20022.Envelope{}, fmt.Errorf("%w: %s", ErrSchemeNotFound, ps[0].Scheme)
	}
	for _, p := range ps[1:] {
		if p.Scheme != ps[0].Scheme {
			return iso20022.Envelope{}, fmt.Errorf("payment: one file cannot carry both %s and %s", ps[0].Scheme, p.Scheme)
		}
	}
	if scheme.Direction() != Pull {
		return s.CreditTransferMessage(ps, mc)
	}
	var cs []Collection
	err := s.store.View(ctx, func(ctx context.Context, tx BankTx) error {
		cs = make([]Collection, 0, len(ps))
		for _, p := range ps {
			m, err := tx.GetMandate(ctx, p.MandateID)
			if err != nil {
				return err
			}
			cs = append(cs, Collection{Payment: p, Mandate: m})
		}
		return nil
	})
	if err != nil {
		return iso20022.Envelope{}, err
	}
	return s.DirectDebitMessage(cs, mc)
}

// instructableTx proves that a payment can be put in a file, by building the
// transaction it would travel as and keeping nothing.
func (s *BankNetwork) instructableTx(ctx context.Context, tx BankTx, p Payment) error {
	scheme, ok := s.scheme(p.Scheme)
	if !ok {
		return fmt.Errorf("%w: %s", ErrSchemeNotFound, p.Scheme)
	}
	o, err := s.outboundOf(p)
	if err != nil {
		return err
	}
	if scheme.Direction() != Pull {
		_, err = creditTransferTx(o)
		return err
	}
	if o.mandate, err = tx.GetMandate(ctx, p.MandateID); err != nil {
		return err
	}
	_, err = directDebitTx(o)
	return err
}

// Collection is a direct debit and the mandate that authorises it.
type Collection struct {
	Payment Payment
	Mandate Mandate
}

// DirectDebitMessage renders a file of collections as the pacs.003 that carries
// them.
func (s *Network) DirectDebitMessage(cs []Collection, mc MessageContext) (iso20022.Envelope, error) {
	out := make([]outbound, 0, len(cs))
	for _, c := range cs {
		o, err := s.outboundOf(c.Payment)
		if err != nil {
			return iso20022.Envelope{}, err
		}
		o.mandate = c.Mandate
		out = append(out, o)
	}
	return directDebit(out, mc)
}

func directDebit(out []outbound, mc MessageContext) (iso20022.Envelope, error) {
	settled, err := groupSettlementDate(out, mc)
	if err != nil {
		return iso20022.Envelope{}, err
	}

	txs := make([]iso20022.DirectDebitTransactionInformation, 0, len(out))
	for _, o := range out {
		tx, err := directDebitTx(o)
		if err != nil {
			return iso20022.Envelope{}, err
		}
		txs = append(txs, tx)
	}

	doc := &iso20022.Pacs003{FIToFICstmrDrctDbt: iso20022.FIToFICustomerDirectDebit{
		GrpHdr: iso20022.DirectDebitGroupHeader{
			MsgId:         mc.MsgID,
			CreDtTm:       iso20022.ISODateTime{Time: mc.Now},
			NbOfTxs:       strconv.Itoa(len(txs)),
			IntrBkSttlmDt: settled,
			SttlmInf:      clearingSettlement(),
		},
		DrctDbtTxInf: txs,
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// directDebitTx is one collection of a pacs.003.
func directDebitTx(o outbound) (iso20022.DirectDebitTransactionInformation, error) {
	var zero iso20022.DirectDebitTransactionInformation

	p, m := o.payment, o.mandate
	amt, err := amountOf(p.Amount, o.asset)
	if err != nil {
		return zero, err
	}
	cdtr, err := namedPartyOf("Cdtr", o.creditor.Name)
	if err != nil {
		return zero, err
	}
	cdtrIBAN, err := ibanOf("CdtrAcct", o.creditor.Identifier)
	if err != nil {
		return zero, err
	}
	dbtr, err := namedPartyOf("Dbtr", o.debtor.Name)
	if err != nil {
		return zero, err
	}
	dbtrIBAN, err := ibanOf("DbtrAcct", o.debtor.Identifier)
	if err != nil {
		return zero, err
	}
	if m.ID == "" {
		return zero, ErrMandateRequired
	}

	local := iso20022.LocalInstrumentCore
	seq := iso20022.SequenceTypeRecurring
	return iso20022.DirectDebitTransactionInformation{
		PmtId: paymentIdentificationOf(p),
		PmtTpInf: &iso20022.PaymentTypeInformation{
			SvcLvl:    &iso20022.ServiceLevelChoice{Cd: iso20022.ServiceLevelSEPA},
			LclInstrm: &iso20022.LocalInstrumentChoice{Cd: &local},
			// SeqTp is RCUR for every collection, including the first.
			SeqTp: &seq,
		},
		IntrBkSttlmAmt: amt,
		ChrgBr:         iso20022.ChargeBearerFollowingServiceLevel,
		DrctDbtTx: iso20022.DirectDebitTransaction{
			MndtRltdInf: iso20022.MandateRelatedInformation{
				MndtId: string(m.ID),
				// DtOfSgntr maps Mandate.CreatedAt, and the elision is deliberate.
				DtOfSgntr: iso20022.ISODate{Time: m.CreatedAt},
			},
			CdtrSchmeId: creditorSchemeIdentification(cdtrIBAN),
		},
		Cdtr:     cdtr,
		CdtrAcct: cashAccount(cdtrIBAN),
		CdtrAgt:  agentOf(o.creditor.BIC),
		Dbtr:     dbtr,
		DbtrAcct: cashAccount(dbtrIBAN),
		DbtrAgt:  agentOf(o.debtor.BIC),
		RmtInf:   remittanceOf(p.Description),
	}, nil
}

// creditorSchemeIdentification is AT-02, the Creditor Identifier — and it is
// the creditor's own IBAN, which is the second elision this message makes and
// the larger.
func creditorSchemeIdentification(iban iso20022.IBAN) iso20022.CreditorSchemeIdentification {
	return iso20022.CreditorSchemeIdentification{
		Id: iso20022.PartyChoice{
			PrvtId: &iso20022.PersonIdentification{
				Othr: iso20022.GenericPersonIdentification{
					Id:      string(iban),
					SchmeNm: iso20022.PersonIdentificationScheme{Prtry: "SEPA"},
				},
			},
		},
	}
}

// OriginalMessage identifies the message a status report is about.
type OriginalMessage struct {
	MsgID     string
	MsgDefIdr string
	CreDtTm   time.Time
}

// TransactionStatusReport is the outcome of one transaction in that message.
type TransactionStatusReport struct {
	EndToEndID string
	TxID       string
	Status     iso20022.TransactionStatus
	Code       iso20022.StatusReason
	Text       string
}

// StatusMessage renders the fate of an earlier message as the pacs.002 that reports
// it. Not a Network method because nothing in it comes from the store: a status is
// about a message, and the message is what the caller has.
func StatusMessage(orig OriginalMessage, sts []TransactionStatusReport, mc MessageContext) (iso20022.Envelope, error) {
	txs := make([]iso20022.PaymentTransactionStatus, 0, len(sts))
	for i, s := range sts {
		txs = append(txs, iso20022.PaymentTransactionStatus{
			// StsId names THIS status, not the payment it is about. It is derived from the
			// message's own identifier and the position within it, so that two statuses in
			// one report are distinguishable and a later query can name one of them.
			StsId:           fmt.Sprintf("%s-%d", mc.MsgID, i+1),
			OrgnlEndToEndId: s.EndToEndID,
			OrgnlTxId:       s.TxID,
			TxSts:           s.Status,
			StsRsnInf:       statusReasonOf(s, mc),
		})
	}

	doc := &iso20022.Pacs002{FIToFIPmtStsRpt: iso20022.FIToFIPaymentStatusReport{
		GrpHdr: iso20022.StatusGroupHeader{
			MsgId:   mc.MsgID,
			CreDtTm: iso20022.ISODateTime{Time: mc.Now},
		},
		OrgnlGrpInfAndSts: iso20022.OriginalGroupHeader{
			OrgnlMsgId:   orig.MsgID,
			OrgnlMsgNmId: orig.MsgDefIdr,
			OrgnlCreDtTm: originalCreationOf(orig),
			GrpSts:       groupStatusOf(sts),
		},
		TxInfAndSts: txs,
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// originalCreationOf echoes when the reported-on message was created, and omits
// the element entirely when the caller does not know.
func originalCreationOf(orig OriginalMessage) *iso20022.ISODateTime {
	if orig.CreDtTm.IsZero() {
		return nil
	}
	return &iso20022.ISODateTime{Time: orig.CreDtTm}
}

// statusReasonOf is why, and it is present only for a rejection.
func statusReasonOf(s TransactionStatusReport, mc MessageContext) *iso20022.StatusReasonInformation {
	if s.Status != iso20022.TransactionStatusRejected {
		return nil
	}
	code := s.Code
	if code == "" {
		code = iso20022.StatusReasonNotSpecifiedAgentGenerated
	}
	return &iso20022.StatusReasonInformation{
		Orgtr:    mc.orgtr(),
		Rsn:      iso20022.StatusReasonChoice{Cd: &code},
		AddtlInf: s.Text,
	}
}

// groupStatusOf describes the whole bulk.
func groupStatusOf(sts []TransactionStatusReport) iso20022.GroupStatus {
	if len(sts) == 0 {
		return ""
	}
	var rejected int
	for _, s := range sts {
		if s.Status == iso20022.TransactionStatusRejected {
			rejected++
		}
	}
	switch rejected {
	case 0:
		return iso20022.GroupStatusAccepted
	case len(sts):
		return iso20022.GroupStatusRejected
	default:
		return iso20022.GroupStatusPartiallyAccepted
	}
}

// ReturnMessage renders a settled payment coming back as the pacs.004 that
// carries it.
func (s *Network) ReturnMessage(p Payment, reason iso20022.ReturnReason, text string, mc MessageContext) (iso20022.Envelope, error) {
	asset, err := s.assetOf(p)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	amt, err := amountOf(p.Amount, asset)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	if reason == "" {
		return iso20022.Envelope{}, fmt.Errorf("%w: RtrRsnInf/Rsn", iso20022.ErrMissingElement)
	}

	txs := []iso20022.ReturnTransaction{{
		RtrId:           string(p.ID) + ":rtr",
		OrgnlEndToEndId: p.EndToEndID,
		OrgnlTxId:       string(p.ID),
		// The two amounts are equal because this system's returns are whole —
		// nothing in the domain takes a partial amount. They are two elements because the
		// standard is shaped for partial returns; see iso20022.ReturnTransaction.
		OrgnlIntrBkSttlmAmt: amt,
		RtrdIntrBkSttlmAmt:  amt,
		ChrgBr:              iso20022.ChargeBearerFollowingServiceLevel,
		RtrRsnInf: &iso20022.ReturnReasonInformation{
			Orgtr:    mc.orgtr(),
			Rsn:      iso20022.ReturnReasonChoice{Cd: &reason},
			AddtlInf: text,
		},
		// The settlement agent that must reverse this return's two reserve legs
		// holds no payment row — see iso20022.OriginalTransactionReference. Both
		// agents are already on the payment, resolved once at submission.
		OrgnlTxRef: &iso20022.OriginalTransactionReference{
			DbtrAgt: agentRef(p.DebtorDetails.Agent),
			CdtrAgt: agentRef(p.CreditorDetails.Agent),
		},
	}}

	doc := &iso20022.Pacs004{PmtRtr: iso20022.PaymentReturn{
		GrpHdr: iso20022.ReturnGroupHeader{
			MsgId:         mc.MsgID,
			CreDtTm:       iso20022.ISODateTime{Time: mc.Now},
			NbOfTxs:       strconv.Itoa(len(txs)),
			IntrBkSttlmDt: iso20022.ISODate{Time: mc.Now},
			SttlmInf:      clearingSettlement(),
		},
		TxInf: txs,
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// ---------------------------------------------------------------------------
// Inbound: messages to payment requests
// ---------------------------------------------------------------------------
// Everything above renders something this system already decided; everything
// below reads an instruction another bank decided, and the difference is that
// nothing here can be trusted to be self-consistent.

// CreditTransferRequest turns a received pacs.008 into the requests this system
// can act on: one per transaction in the file, in the file's own order.
func (s *BankNetwork) CreditTransferRequest(ctx context.Context, doc *iso20022.Pacs008) ([]InboundTransaction, error) {
	// Before the message is read at all, and the placement is what makes the
	// paragraph above exact.
	if _, err := s.self(); err != nil {
		return nil, err
	}
	txs, err := s.creditTransferIn(doc)
	if err != nil {
		return nil, err
	}
	// The creditor is this bank's own customer on a push; the debtor is the sending
	// bank's and is recorded, not resolved. creditTransferIn left the creditor as the
	// ADDRESS the message quoted.
	for i := range txs {
		if err := s.localSideIn(ctx, &txs[i], &txs[i].Request.Creditor); err != nil {
			return nil, err
		}
	}
	return txs, nil
}

// InboundTransaction is one transaction read out of a file: what it instructs,
// the id the SUBMITTING bank minted for it, and — where this bank has already
// decided it cannot act on this one — why.
type InboundTransaction struct {
	ID      PaymentID
	Request InitiatePaymentRequest
	Refusal error
}

// localSideIn resolves one transaction's own side, and decides whether a
// failure belongs to that transaction or to the whole file.
func (s *BankNetwork) localSideIn(ctx context.Context, tx *InboundTransaction, side *PartyRef) error {
	ref, err := s.localPartyIn(ctx, side.Identifier)
	switch {
	case err == nil:
		*side = ref
		return nil
	case errors.Is(err, ErrAccountNotInParticipant), errors.Is(err, deposit.ErrIdentifierAmbiguous):
		tx.Refusal = err
		return nil
	default:
		return err
	}
}

// creditTransferIn is everything a pacs.008 SAYS, resolving nobody.
func (s *Network) creditTransferIn(doc *iso20022.Pacs008) ([]InboundTransaction, error) {
	body := doc.FIToFICstmrCdtTrf
	if err := checkNbOfTxs("CdtTrfTxInf", body.CdtTrfTxInf, body.GrpHdr.NbOfTxs); err != nil {
		return nil, err
	}

	out := make([]InboundTransaction, 0, len(body.CdtTrfTxInf))
	for _, tx := range body.CdtTrfTxInf {
		scheme, amount, err := s.schemeSettling(Push, tx.IntrBkSttlmAmt)
		if err != nil {
			return nil, err
		}
		dbtrID, err := identifierIn("DbtrAcct", tx.DbtrAcct)
		if err != nil {
			return nil, err
		}
		cdtrID, err := identifierIn("CdtrAcct", tx.CdtrAcct)
		if err != nil {
			return nil, err
		}
		out = append(out, InboundTransaction{
			ID: PaymentID(tx.PmtId.TxId),
			Request: InitiatePaymentRequest{
				Scheme:          scheme,
				Debtor:          PartyRef{Identifier: dbtrID},
				Creditor:        PartyRef{Identifier: cdtrID},
				Amount:          amount,
				EndToEndID:      endToEndIn(tx.PmtId.EndToEndId),
				Description:     remittanceIn(tx.RmtInf),
				DebtorDetails:   PartyDetails{Agent: agentIn(tx.DbtrAgt), Name: nameIn(tx.Dbtr)},
				CreditorDetails: PartyDetails{Agent: agentIn(tx.CdtrAgt), Name: nameIn(tx.Cdtr)},
			},
		})
	}
	return out, nil
}

// DirectDebitRequest turns a received pacs.003 into the requests this system
// can act on: one per collection in the file, in the file's own order.
func (s *BankNetwork) DirectDebitRequest(ctx context.Context, doc *iso20022.Pacs003) ([]InboundTransaction, error) {
	// First, for CreditTransferRequest's reason.
	if _, err := s.self(); err != nil {
		return nil, err
	}
	txs, err := s.directDebitIn(doc)
	if err != nil {
		return nil, err
	}
	// The debtor is this bank's own customer on a pull; the creditor is the
	// sending bank's and is recorded, not resolved. See creditTransferIn's
	// mirror note on what directDebitIn left behind for this line to consume.
	for i := range txs {
		if err := s.localSideIn(ctx, &txs[i], &txs[i].Request.Debtor); err != nil {
			return nil, err
		}
	}
	return txs, nil
}

// directDebitIn is everything a pacs.003 SAYS, resolving nobody.
func (s *Network) directDebitIn(doc *iso20022.Pacs003) ([]InboundTransaction, error) {
	body := doc.FIToFICstmrDrctDbt
	if err := checkNbOfTxs("DrctDbtTxInf", body.DrctDbtTxInf, body.GrpHdr.NbOfTxs); err != nil {
		return nil, err
	}

	out := make([]InboundTransaction, 0, len(body.DrctDbtTxInf))
	for _, tx := range body.DrctDbtTxInf {
		scheme, amount, err := s.schemeSettling(Pull, tx.IntrBkSttlmAmt)
		if err != nil {
			return nil, err
		}
		mandate := tx.DrctDbtTx.MndtRltdInf.MndtId
		if mandate == "" {
			return nil, fmt.Errorf("%w: DrctDbtTx/MndtRltdInf/MndtId", ErrMandateRequired)
		}
		dbtrID, err := identifierIn("DbtrAcct", tx.DbtrAcct)
		if err != nil {
			return nil, err
		}
		cdtrID, err := identifierIn("CdtrAcct", tx.CdtrAcct)
		if err != nil {
			return nil, err
		}
		out = append(out, InboundTransaction{
			ID: PaymentID(tx.PmtId.TxId),
			Request: InitiatePaymentRequest{
				Scheme:          scheme,
				Debtor:          PartyRef{Identifier: dbtrID},
				Creditor:        PartyRef{Identifier: cdtrID},
				Amount:          amount,
				MandateID:       MandateID(mandate),
				EndToEndID:      endToEndIn(tx.PmtId.EndToEndId),
				Description:     remittanceIn(tx.RmtInf),
				DebtorDetails:   PartyDetails{Agent: agentIn(tx.DbtrAgt), Name: nameIn(tx.Dbtr)},
				CreditorDetails: PartyDetails{Agent: agentIn(tx.CdtrAgt), Name: nameIn(tx.Cdtr)},
			},
		})
	}
	return out, nil
}

// schemeSettling is which of this network's schemes an inbound message
// travelled under, together with its amount in minor units.
func (s *Network) schemeSettling(dir SchemeDirection, amt iso20022.ActiveCurrencyAndAmount) (SchemeID, ledger.Amount, error) {
	minor, asset, err := amountIn(amt)
	if err != nil {
		return "", 0, err
	}
	var found []SchemeID
	for _, sc := range s.ListSchemes() {
		if sc.Direction() == dir && sc.Asset() == asset {
			found = append(found, sc.ID())
		}
	}
	switch len(found) {
	case 1:
		return found[0], minor, nil
	case 0:
		return "", 0, fmt.Errorf("%w: message is in %s and no %s scheme in this network settles in it",
			ErrAssetMismatch, asset, dir)
	default:
		return "", 0, fmt.Errorf("%w: message is in %s and %s schemes %v all settle in it, so nothing in it says which",
			ErrAssetMismatch, asset, dir, found)
	}
}

// amountIn is the inverse of amountOf: a decimal on the wire becomes minor
// units of the asset the message names, at that asset's own scale.
func amountIn(amt iso20022.ActiveCurrencyAndAmount) (ledger.Amount, ledger.AssetCode, error) {
	asset := ledger.AssetCode(amt.Ccy)
	def, err := ledger.LookupAsset(asset)
	if err != nil {
		return 0, "", err
	}
	minor, err := amt.Minor(def.Scale)
	if err != nil {
		return 0, "", fmt.Errorf("%w: %s %s", err, amt.Ccy, amt.Value)
	}
	return ledger.Amount(minor), asset, nil
}

// localPartyIn resolves the ONE side of a received message that belongs to this
// bank, by the address the message quotes for it.
func (s *BankNetwork) localPartyIn(ctx context.Context, ident deposit.Identifier) (PartyRef, error) {
	var ref PartyRef
	err := s.store.View(ctx, func(ctx context.Context, tx BankTx) error {
		var err error
		ref, err = s.addressedPartyTx(ctx, tx, ident)
		return err
	})
	return ref, err
}

// addressedPartyTx turns one quoted address into the party it names.
func (s *BankNetwork) addressedPartyTx(ctx context.Context, tx BankTx, ident deposit.Identifier) (PartyRef, error) {
	ref, err := s.ResolveIdentifierTx(ctx, tx, ident)
	if errors.Is(err, deposit.ErrIdentifierNotFound) {
		return PartyRef{}, fmt.Errorf("%w: %s", ErrAccountNotInParticipant, ident.Value)
	}
	if err != nil {
		return PartyRef{}, err
	}
	return ref, nil
}

// identifierIn reads the address a received message quotes for one party. It is
// the inverse of ibanOf and cashAccount, and where the nil arm they never
// produce actually arrives.
func identifierIn(element string, acct iso20022.CashAccount) (deposit.Identifier, error) {
	if acct.Id.IBAN == nil {
		return deposit.Identifier{}, fmt.Errorf("%w: %s/Id/IBAN", ErrUnaddressableAccount, element)
	}
	iban := acct.Id.IBAN.Compact()
	if err := iban.Validate(); err != nil {
		return deposit.Identifier{}, fmt.Errorf("%w: %s: %w", ErrUnaddressableAccount, element, err)
	}
	return deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: string(iban)}, nil
}

// agentIn reads which bank a message names for one party.
func agentIn(fi iso20022.BranchAndFinancialInstitution) iso20022.BIC {
	return fi.FinInstnId.BICFI
}

// nameIn reads the name a message gives for one party.
func nameIn(p iso20022.PartyIdentification) string {
	return p.Nm
}

// endToEndIn is the inverse of endToEndOf: the value the guidelines reserve for
// "the sender had no reference" comes back as no reference.
func endToEndIn(id string) string {
	if id == "NOTPROVIDED" {
		return ""
	}
	return id
}

// remittanceIn is what the payment is for, or nothing at all. The element is
// optional and the pointer is genuinely nil for most messages; see
// remittanceOf, which omits it rather than sending an empty one.
func remittanceIn(r *iso20022.RemittanceInformation) string {
	if r == nil {
		return ""
	}
	return r.Ustrd
}

// ReadStatus reads a received pacs.002 as what it says about the original
// message and what it says about each transaction in it.
func ReadStatus(doc *iso20022.Pacs002) (OriginalMessage, []TransactionStatusReport) {
	rpt := doc.FIToFIPmtStsRpt
	orig := OriginalMessage{
		MsgID:     rpt.OrgnlGrpInfAndSts.OrgnlMsgId,
		MsgDefIdr: rpt.OrgnlGrpInfAndSts.OrgnlMsgNmId,
	}
	if t := rpt.OrgnlGrpInfAndSts.OrgnlCreDtTm; t != nil {
		orig.CreDtTm = t.Time
	}
	out := make([]TransactionStatusReport, 0, len(rpt.TxInfAndSts))
	for _, s := range rpt.TxInfAndSts {
		r := TransactionStatusReport{
			EndToEndID: s.OrgnlEndToEndId,
			TxID:       s.OrgnlTxId,
			Status:     s.TxSts,
		}
		// The code and the text are both kept, because they say different things: the
		// code is what makes a rejection machine-actionable and the text is what says
		// the part no code can. Dropping either is a silent loss.
		if s.StsRsnInf != nil {
			r.Text = s.StsRsnInf.AddtlInf
			if s.StsRsnInf.Rsn.Cd != nil {
				r.Code = *s.StsRsnInf.Rsn.Cd
			}
		}
		out = append(out, r)
	}
	return orig, out
}

// ReadSettlement reads a received pacs.009 as the legs it instructs.
func ReadSettlement(doc *iso20022.Pacs009) ([]SettlementLeg, error) {
	body := doc.FICdtTrf
	if err := checkNbOfTxs("CdtTrfTxInf", body.CdtTrfTxInf, body.GrpHdr.NbOfTxs); err != nil {
		return nil, err
	}
	legs := make([]SettlementLeg, 0, len(body.CdtTrfTxInf))
	for i, tx := range body.CdtTrfTxInf {
		amount, asset, err := amountIn(tx.IntrBkSttlmAmt)
		if err != nil {
			return nil, fmt.Errorf("CdtTrfTxInf[%d]: %w", i, err)
		}
		legs = append(legs, SettlementLeg{
			From:      tx.Dbtr.FinInstnId.BICFI,
			To:        tx.Cdtr.FinInstnId.BICFI,
			Amount:    amount,
			Asset:     asset,
			Reference: tx.PmtId.EndToEndId,
		})
	}
	return legs, nil
}

// ReturnInstruction is one payment being returned, as a settlement agent needs
// it: which two agents move reserves, how much, and why.
type ReturnInstruction struct {
	PaymentID     PaymentID
	EndToEndID    string
	DebtorAgent   iso20022.BIC
	CreditorAgent iso20022.BIC
	Amount        ledger.Amount
	Asset         ledger.AssetCode
	Reason        string
}

// CodeAndText is a reason code and the free text beside it, joined for a ledger
// description.
func CodeAndText(code, text, none string) string {
	switch {
	case code == "" && text == "":
		return none
	case text == "":
		return code
	case code == "":
		return text
	default:
		return code + ": " + text
	}
}

// ReturnReason is what a return is described as where a CUSTOMER's money moves:
// the reason the returning bank gave, code and text.
func ReturnReason(info *iso20022.ReturnReasonInformation) string {
	if info == nil {
		return "returned"
	}
	var code string
	switch {
	case info.Rsn.Cd != nil:
		code = string(*info.Rsn.Cd)
	case info.Rsn.Prtry != nil:
		code = *info.Rsn.Prtry
	}
	return CodeAndText(code, info.AddtlInf, "returned")
}

// ReadReturn reads a received pacs.004 as the instructions it carries.
func ReadReturn(doc *iso20022.Pacs004) ([]ReturnInstruction, error) {
	body := doc.PmtRtr
	if err := checkNbOfTxs("TxInf", body.TxInf, body.GrpHdr.NbOfTxs); err != nil {
		return nil, err
	}
	ins := make([]ReturnInstruction, 0, len(body.TxInf))
	for i, tx := range body.TxInf {
		if tx.OrgnlTxId == "" {
			return nil, fmt.Errorf(
				"payment: TxInf[%d]: OrgnlTxId is absent; this return names no payment and its reserve reversal would be keyed by nothing",
				i)
		}
		ref := tx.OrgnlTxRef
		if ref == nil || ref.DbtrAgt == nil || ref.CdtrAgt == nil ||
			ref.DbtrAgt.FinInstnId.BICFI == "" || ref.CdtrAgt.FinInstnId.BICFI == "" {
			return nil, fmt.Errorf(
				"payment: TxInf[%d]: OrgnlTxRef names no agents; a settlement agent with no payment row cannot resolve this return",
				i)
		}
		amount, asset, err := amountIn(tx.RtrdIntrBkSttlmAmt)
		if err != nil {
			return nil, fmt.Errorf("TxInf[%d]: %w", i, err)
		}
		ins = append(ins, ReturnInstruction{
			PaymentID:     PaymentID(tx.OrgnlTxId),
			EndToEndID:    tx.OrgnlEndToEndId,
			DebtorAgent:   ref.DbtrAgt.FinInstnId.BICFI,
			CreditorAgent: ref.CdtrAgt.FinInstnId.BICFI,
			Amount:        amount,
			Asset:         asset,
			// RtrdIntrBkSttlmAmt, not OrgnlIntrBkSttlmAmt: the two are equal in this
			// system's own returns, which are always whole, but a settlement agent moves
			// the amount actually coming back, and only RtrdIntrBkSttlmAmt says that
			// under the standard's own partial-return shape.
			Reason: ReturnReason(tx.RtrRsnInf),
		})
	}
	return ins, nil
}

// checkNbOfTxs holds a sender to its own count, and every reader of a file
// begins with it.
func checkNbOfTxs[T any](element string, txs []T, nbOfTxs string) error {
	declared, err := strconv.Atoi(nbOfTxs)
	if err != nil {
		return fmt.Errorf("payment: GrpHdr/NbOfTxs is %q, which is not a count", nbOfTxs)
	}
	if declared != len(txs) {
		return fmt.Errorf("payment: GrpHdr/NbOfTxs declares %d transactions and %s carries %d",
			declared, element, len(txs))
	}
	return nil
}

// SettlementLeg is one bank's movement in a settlement instruction: who pays,
// who is paid, how much, and which closed cycle it discharges.
type SettlementLeg struct {
	From      iso20022.BIC
	To        iso20022.BIC
	Amount    ledger.Amount
	Asset     ledger.AssetCode
	Reference string
}

// SettlementLegsOf turns a closed cycle's net positions into the legs a
// settlement instruction carries.
func SettlementLegsOf(c ClearingCycle, asset ledger.AssetCode, centralBank iso20022.BIC) []SettlementLeg {
	legs := make([]SettlementLeg, 0, len(c.NetPositions))
	// A cycle's positions are keyed by BIC and a leg is addressed by BIC, so there
	// is nothing between the two. See ClearingCycle.NetPositions.
	for _, bic := range slices.Sorted(maps.Keys(c.NetPositions)) {
		net := c.NetPositions[bic]
		if net == 0 {
			continue
		}
		leg := SettlementLeg{From: bic, To: centralBank, Amount: -net, Asset: asset, Reference: string(c.ID)}
		if net > 0 {
			leg.From, leg.To, leg.Amount = centralBank, bic, net
		}
		legs = append(legs, leg)
	}
	return legs
}

// NetsToNothing reports whether a cycle leaves nobody with anything to
// discharge: every member's position is zero, or it took nothing in at all.
func NetsToNothing(c ClearingCycle) bool {
	for _, net := range c.NetPositions {
		if net != 0 {
			return false
		}
	}
	return true
}

// SettlementMessage renders a closed cycle's net positions as the pacs.009 that
// instructs the central bank to settle them.
func SettlementMessage(legs []SettlementLeg, mc MessageContext) (iso20022.Envelope, error) {
	if len(legs) == 0 {
		return iso20022.Envelope{}, fmt.Errorf("%w: CdtTrfTxInf", iso20022.ErrMissingElement)
	}
	txs := make([]iso20022.FinancialInstitutionCreditTransferTransaction, 0, len(legs))
	for _, leg := range legs {
		amt, err := amountOf(leg.Amount, leg.Asset)
		if err != nil {
			return iso20022.Envelope{}, err
		}
		txs = append(txs, iso20022.FinancialInstitutionCreditTransferTransaction{
			PmtId:          iso20022.PaymentIdentification{EndToEndId: leg.Reference},
			IntrBkSttlmAmt: amt,
			IntrBkSttlmDt:  iso20022.ISODate{Time: mc.Now},
			Dbtr:           agentOf(leg.From),
			Cdtr:           agentOf(leg.To),
		})
	}

	doc := &iso20022.Pacs009{FICdtTrf: iso20022.FIToFIFinancialInstitutionCreditTransfer{
		GrpHdr: iso20022.CreditTransferGroupHeader{
			MsgId:   mc.MsgID,
			CreDtTm: iso20022.ISODateTime{Time: mc.Now},
			// NbOfTxs is what the SENDER asserts, not a derivation the receiver would
			// recompute — and this is a settlement instruction, where a silently dropped leg
			// is a bank that does not get paid.
			NbOfTxs: strconv.Itoa(len(txs)),
			// TtlIntrBkSttlmAmt is deliberately absent. The legs of one message may be
			// denominated in different assets, a sum across assets is not a number, and the
			// standard's single total has nowhere to say which asset it is in.
			IntrBkSttlmDt: iso20022.ISODate{Time: mc.Now},
			SttlmInf:      clearingSettlement(),
		},
		CdtTrfTxInf: txs,
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// ---------------------------------------------------------------------------
// The statement
// ---------------------------------------------------------------------------

// StatementMessage renders one member's share of a settlement as the camt.053
// that tells it.
func StatementMessage(st SettlementStatement, mc MessageContext) (iso20022.Envelope, error) {
	if st.Account == "" {
		return iso20022.Envelope{}, fmt.Errorf("%w: Stmt/Acct/Id/Othr/Id", iso20022.ErrMissingElement)
	}
	if st.Reference == "" {
		return iso20022.Envelope{}, fmt.Errorf("%w: Ntry/AcctSvcrRef", iso20022.ErrMissingElement)
	}
	entryAmt, entryInd, err := signedAmountOf(st.Movement, st.Asset)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	balAmt, balInd, err := signedAmountOf(st.ClosingBalance, st.Asset)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	day := iso20022.ISODate{Time: ledger.DayStart(st.ValueDate)}

	doc := &iso20022.Camt053{BkToCstmrStmt: iso20022.BankToCustomerStatement{
		GrpHdr: iso20022.StatementGroupHeader{
			MsgId:   mc.MsgID,
			CreDtTm: iso20022.ISODateTime{Time: mc.Now},
		},
		Stmt: []iso20022.AccountStatement{{
			Id:      st.StatementRef,
			CreDtTm: iso20022.ISODateTime{Time: mc.Now},
			Acct: iso20022.CashAccount{Id: iso20022.AccountIdentification4Choice{
				Othr: &iso20022.GenericAccountIdentification{Id: string(st.Account)},
			}},
			Bal: []iso20022.CashBalance{{
				Tp:        iso20022.BalanceTypeChoice{CdOrPrtry: iso20022.BalanceTypeCode{Cd: iso20022.BalanceTypeClosingBooked}},
				Amt:       balAmt,
				CdtDbtInd: balInd,
				Dt:        iso20022.DateAndDateTime{Dt: &day},
			}},
			Ntry: []iso20022.StatementEntry{{
				Amt:         entryAmt,
				CdtDbtInd:   entryInd,
				Sts:         iso20022.EntryStatusChoice{Cd: iso20022.EntryStatusBooked},
				BookgDt:     iso20022.DateAndDateTime{Dt: &day},
				ValDt:       iso20022.DateAndDateTime{Dt: &day},
				AcctSvcrRef: st.Reference,
				// What kind of movement this is, and the schema's one mandatory child of an
				// entry.
				BkTxCd: iso20022.BankTransactionCode{
					Prtry: iso20022.ProprietaryBankTransactionCode{
						Cd:   iso20022.BankTransactionCodeSettlement,
						Issr: iso20022.BankTransactionCodeIssuer,
					},
				},
				// Named after the reference and nothing more: a cycle id and a payment id
				// are equally opaque to the member reading this, and saying which kind it
				// sent would be telling that bank something it has no row to resolve.
				AddtlNtryInf: "Settlement of " + st.Reference,
			}},
		}},
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// signedAmountOf splits a signed ledger amount into the magnitude the standard
// carries and the word that says which way it runs.
func signedAmountOf(amt ledger.Amount, asset ledger.AssetCode) (iso20022.ActiveCurrencyAndAmount, iso20022.CreditDebitCode, error) {
	ind := iso20022.CreditDebitCredit
	magnitude := amt
	if amt < 0 {
		ind, magnitude = iso20022.CreditDebitDebit, -amt
	}
	out, err := amountOf(magnitude, asset)
	if err != nil {
		return iso20022.ActiveCurrencyAndAmount{}, "", err
	}
	return out, ind, nil
}

// AdvisedMovement is what a member bank can see in a statement about its own
// reserve account: the movement, the balance it was left at, and the reference
// that names what caused it.
type AdvisedMovement struct {
	Account        ledger.AccountID
	Asset          ledger.AssetCode
	Movement       ledger.Amount
	ClosingBalance ledger.Amount
	Reference      string

	// ValueDate is CARRIED AND UNREAD, and that is recorded rather than left to be
	// discovered.
	ValueDate time.Time
}

// ReadStatement reads a received camt.053 as the movements it advises.
func ReadStatement(doc *iso20022.Camt053) ([]AdvisedMovement, error) {
	stmts := doc.BkToCstmrStmt.Stmt
	out := make([]AdvisedMovement, 0, len(stmts))
	for i, s := range stmts {
		if len(s.Ntry) != 1 {
			return nil, fmt.Errorf("payment: Stmt[%d] carries %d entries; a settlement statement advises one movement", i, len(s.Ntry))
		}
		entry := s.Ntry[0]
		movement, asset, err := signedAmountIn(entry.Amt, entry.CdtDbtInd)
		if err != nil {
			return nil, fmt.Errorf("Stmt[%d]/Ntry/Amt: %w", i, err)
		}
		closing, ok, err := closingBalanceIn(s.Bal)
		if err != nil {
			return nil, fmt.Errorf("Stmt[%d]/Bal: %w", i, err)
		}
		if !ok {
			return nil, fmt.Errorf("payment: Stmt[%d] carries no CLBD balance; a statement with nothing to check against is a notification", i)
		}
		acct := s.Acct.Id
		if acct.Othr == nil {
			return nil, fmt.Errorf("payment: Stmt[%d]/Acct is not identified by Othr; a reserve account has no IBAN", i)
		}
		day := time.Time{}
		if entry.ValDt.Dt != nil {
			day = entry.ValDt.Dt.Time
		}
		out = append(out, AdvisedMovement{
			Account:        ledger.AccountID(acct.Othr.Id),
			Asset:          asset,
			Movement:       movement,
			ClosingBalance: closing,
			Reference:      entry.AcctSvcrRef,
			ValueDate:      day,
		})
	}
	return out, nil
}

// signedAmountIn puts the sign back on: the magnitude the standard carried and
// the word beside it become one signed ledger amount.
func signedAmountIn(amt iso20022.ActiveCurrencyAndAmount, ind iso20022.CreditDebitCode) (ledger.Amount, ledger.AssetCode, error) {
	value, asset, err := amountIn(amt)
	if err != nil {
		return 0, "", err
	}
	switch ind {
	case iso20022.CreditDebitCredit:
		return value, asset, nil
	case iso20022.CreditDebitDebit:
		return -value, asset, nil
	default:
		return 0, "", fmt.Errorf("payment: CdtDbtInd is %q, which says neither credit nor debit", ind)
	}
}

// closingBalanceIn finds the CLBD balance among however many a statement
// carries, and reports whether there was one.
func closingBalanceIn(bals []iso20022.CashBalance) (ledger.Amount, bool, error) {
	for _, b := range bals {
		if b.Tp.CdOrPrtry.Cd != iso20022.BalanceTypeClosingBooked {
			continue
		}
		value, _, err := signedAmountIn(b.Amt, b.CdtDbtInd)
		if err != nil {
			return 0, false, err
		}
		return value, true, nil
	}
	return 0, false, nil
}

// ---------------------------------------------------------------------------
// The lodgement: camt.050 out, camt.025 back
// ---------------------------------------------------------------------------

// LodgementMessage renders a member's request for a reserve credit as the
// camt.050 that carries it.
func LodgementMessage(in LodgementInstruction, mc MessageContext) (iso20022.Envelope, error) {
	if err := in.BIC.Validate(); err != nil {
		return iso20022.Envelope{}, fmt.Errorf("payment: the lodging member's address: %w", err)
	}
	if err := mc.To.Validate(); err != nil {
		return iso20022.Envelope{}, fmt.Errorf("payment: Cdtr: %w", err)
	}
	if in.Account == "" {
		return iso20022.Envelope{}, fmt.Errorf(
			"%w: CdtrAcct/Id; a lodgement naming no reserve account is one no servicer can post",
			iso20022.ErrMissingElement)
	}
	if in.Ref == "" {
		return iso20022.Envelope{}, fmt.Errorf(
			"%w: LqdtyTrfId/EndToEndId; nothing else correlates the receipt", iso20022.ErrMissingElement)
	}
	if in.Ref != mc.MsgID {
		return iso20022.Envelope{}, fmt.Errorf(
			"payment: this lodgement is referenced %q and the message it would travel as is %q; "+
				"the receipt quotes the message id, so the two cannot differ", in.Ref, mc.MsgID)
	}
	amount, err := amountOf(in.Amount, in.Asset)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	doc := &iso20022.Camt050{LqdtyCdtTrf: iso20022.LiquidityCreditTransferV5{
		MsgHdr: iso20022.MessageHeader{MsgId: mc.MsgID, CreDtTm: iso20022.ISODateTime{Time: mc.Now}},
		LqdtyCdtTrf: iso20022.LiquidityTransfer{
			LqdtyTrfId: iso20022.LiquidityTransferIdentification{EndToEndId: in.Ref},
			Cdtr:       agentOf(mc.To),
			CdtrAcct: iso20022.CashAccount{Id: iso20022.AccountIdentification4Choice{
				// The generic arm, not the IBAN one: a reserve account at a central bank has
				// no IBAN, because it is not a payment address and no customer ever quotes
				// it. The same choice camt.053's Acct makes.
				Othr: &iso20022.GenericAccountIdentification{Id: string(in.Account)},
			}},
			TrfdAmt: iso20022.TransferredAmount{AmtWthCcy: amount},
			Dbtr:    agentOf(in.BIC),
		},
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// ReadLodgement reads a received camt.050 as the instruction it carries: which
// member, which account, how much, and in what.
func ReadLodgement(hdr iso20022.AppHdr, doc *iso20022.Camt050) (LodgementInstruction, error) {
	body := doc.LqdtyCdtTrf
	trf := body.LqdtyCdtTrf

	ref := body.MsgHdr.MsgId
	if ref == "" {
		return LodgementInstruction{}, fmt.Errorf(
			"%w: MsgHdr/MsgId; this request cannot be answered because nothing would correlate the receipt",
			iso20022.ErrMissingElement)
	}
	member := trf.Dbtr.FinInstnId.BICFI
	if member == "" {
		return LodgementInstruction{}, fmt.Errorf(
			"%w: Dbtr/FinInstnId/BICFI; this request names no member and its reserve would be keyed by nothing",
			iso20022.ErrMissingElement)
	}
	if err := member.Validate(); err != nil {
		return LodgementInstruction{}, fmt.Errorf("payment: Dbtr/FinInstnId/BICFI: %w", err)
	}
	if from := hdr.Fr.FIId.FinInstnId.BICFI; from != member {
		return LodgementInstruction{}, fmt.Errorf(
			"payment: this lodgement was sent by %s and asks to debit %s; a member lodges its own cash",
			from, member)
	}
	agent := trf.Cdtr.FinInstnId.BICFI
	if agent == "" {
		return LodgementInstruction{}, fmt.Errorf(
			"%w: Cdtr/FinInstnId/BICFI; this request names no account servicer", iso20022.ErrMissingElement)
	}
	if to := hdr.To.FIId.FinInstnId.BICFI; to != agent {
		return LodgementInstruction{}, fmt.Errorf(
			"payment: this lodgement was addressed to %s and names %s as the account servicer; "+
				"one servicer cannot post in another's book", to, agent)
	}
	// The generic arm. An IBAN here would be a reserve account addressed as a payment
	// address, which is what GenericAccountIdentification exists to avoid.
	if trf.CdtrAcct.Id.Othr == nil || trf.CdtrAcct.Id.Othr.Id == "" {
		return LodgementInstruction{}, fmt.Errorf(
			"%w: CdtrAcct/Id/Othr/Id; this request says which member and not which account",
			iso20022.ErrMissingElement)
	}
	amount, asset, err := amountIn(trf.TrfdAmt.AmtWthCcy)
	if err != nil {
		return LodgementInstruction{}, err
	}
	if amount <= 0 {
		return LodgementInstruction{}, fmt.Errorf("%w: TrfdAmt is %s", ErrInvalidPaymentAmount, trf.TrfdAmt.AmtWthCcy.Value)
	}
	return LodgementInstruction{
		BIC:     member,
		Agent:   agent,
		Account: ledger.AccountID(trf.CdtrAcct.Id.Othr.Id),
		Asset:   asset,
		Amount:  amount,
		Ref:     ref,
	}, nil
}

// LodgementReceiptMessage renders an account servicer's answer as the camt.025
// that carries it.
func LodgementReceiptMessage(r LodgementReceipt, mc MessageContext) (iso20022.Envelope, error) {
	if r.Ref == "" {
		return iso20022.Envelope{}, fmt.Errorf(
			"%w: RctDtls/OrgnlMsgId/MsgId; a receipt naming no request answers nothing",
			iso20022.ErrMissingElement)
	}
	if r.Status == "" {
		return iso20022.Envelope{}, fmt.Errorf("%w: RctDtls/ReqHdlg/StsCd", iso20022.ErrMissingElement)
	}
	handling := iso20022.RequestHandling{StsCd: string(r.Status)}
	if !r.Accepted() {
		// A refusal that says nothing is one nobody can act on, and the schema does not
		// require a reason here — so the requirement is this system's.
		if r.Reason == "" {
			return iso20022.Envelope{}, fmt.Errorf(
				"%w: RctDtls/ReqHdlg/Desc on a refusal", iso20022.ErrMissingElement)
		}
		handling.Desc = truncateTo(r.Reason, 140)
	}
	doc := &iso20022.Camt025{Rct: iso20022.ReceiptV5{
		MsgHdr: iso20022.ReceiptMessageHeader{MsgId: mc.MsgID, CreDtTm: iso20022.ISODateTime{Time: mc.Now}},
		RctDtls: []iso20022.ReceiptDetails{{
			OrgnlMsgId: iso20022.OriginalMessageAndIssuer{
				MsgId: r.Ref,
				// The request's message definition, so that a member holding more than one
				// kind of outstanding request can dispatch this answer without a table only
				// it has.
				MsgNmId: iso20022.Camt050{}.MessageDefinitionIdentifier(),
			},
			ReqHdlg: []iso20022.RequestHandling{handling},
		}},
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// truncateTo shortens a reason to fit an element's maximum length, marking that
// it was shortened.
func truncateTo(s string, limit int) string {
	if len([]rune(s)) <= limit {
		return s
	}
	const ellipsis = "…"
	runes := []rune(s)
	return string(runes[:limit-len([]rune(ellipsis))]) + ellipsis
}

// ReadLodgementReceipt reads a received camt.025 as the answer it carries.
func ReadLodgementReceipt(doc *iso20022.Camt025) (LodgementReceipt, error) {
	details := doc.Rct.RctDtls
	if n := len(details); n != 1 {
		return LodgementReceipt{}, fmt.Errorf(
			"payment: this system asks about one lodgement at a time and RctDtls carries %d", n)
	}
	d := details[0]
	if d.OrgnlMsgId.MsgId == "" {
		return LodgementReceipt{}, fmt.Errorf(
			"%w: RctDtls/OrgnlMsgId/MsgId; nothing on this receipt says which request it answers",
			iso20022.ErrMissingElement)
	}
	if n := len(d.ReqHdlg); n != 1 {
		return LodgementReceipt{}, fmt.Errorf(
			"payment: one request was made and ReqHdlg carries %d outcomes for it", n)
	}
	h := d.ReqHdlg[0]
	if h.StsCd == "" {
		return LodgementReceipt{}, fmt.Errorf(
			"%w: RctDtls/ReqHdlg/StsCd; this receipt says a request arrived and not what became of it",
			iso20022.ErrMissingElement)
	}
	return LodgementReceipt{
		Ref:    d.OrgnlMsgId.MsgId,
		Status: iso20022.TransactionStatus(h.StsCd),
		Reason: h.Desc,
	}, nil
}
