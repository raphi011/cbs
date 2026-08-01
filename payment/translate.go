package payment

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
)

// reasonMapping classifies one sentinel from errors.go.
//
// This file is payment's translation layer: the conversion between this
// system's domain types and the ISO 20022 messages that carry them between
// banks. It lives here rather than in iso20022 because iso20022 imports
// nothing from this repository, deliberately — see that package's doc. A
// translator inside it would be the same import pointing the other way, and
// the claim "these are the standard's types" would stop being checkable.
//
// Name duplicates the identifier as a string so the test can compare the table
// against errors.go's own declarations without reflection, which cannot see
// package-level var names. The pair is kept honest by a second test asserting
// that the Err values are pairwise distinct and as numerous as the
// declarations — a mislabelled entry then either collides or leaves a name
// uncovered.
type reasonMapping struct {
	Err  error
	Name string
	Code iso20022.StatusReason
}

// reasonTable is the mapping sub-project 7a decided, made undriftable here.
//
// EVERY sentinel in errors.go must appear. An error that cannot reach a
// counterparty is mapped to the empty code with a comment saying why, rather
// than omitted — omission is indistinguishable from an oversight, which is the
// exact failure this table exists to prevent. TestReasonTableCoversEverySentinel
// parses errors.go and fails on any gap.
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
	{ErrUnaddressableAccount, "ErrUnaddressableAccount", iso20022.StatusReasonMissingDebtorAccountOrIdentification},
	{ErrIdentifierMismatch, "ErrIdentifierMismatch", iso20022.StatusReasonMissingDebtorAccountOrIdentification},
	{ErrAmbiguousAddress, "ErrAmbiguousAddress", iso20022.StatusReasonMissingDebtorAccountOrIdentification},
	{ErrCycleNotOpen, "ErrCycleNotOpen", iso20022.StatusReasonInvalidCutOffTime},

	// MS03 for a stronger reason than "no better code exists": in SEPA a
	// currency mismatch cannot happen, because the scheme is euro-only, so the
	// code set never needed one. That this repository can produce the error at
	// all is a consequence of its multi-asset ledger, and the honest wire
	// representation of a condition the scheme does not contemplate is
	// "unspecified".
	{ErrAssetMismatch, "ErrAssetMismatch", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrSchemeNotFound, "ErrSchemeNotFound", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrInvalidPaymentAmount, "ErrInvalidPaymentAmount", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrParticipantAssetNotFound, "ErrParticipantAssetNotFound", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrSchemeUnsupportedReturn, "ErrSchemeUnsupportedReturn", iso20022.StatusReasonNotSpecifiedAgentGenerated},

	// --- Classified as never reaching a counterparty ---
	//
	// Each is a failure of THIS system's own bookkeeping rather than a
	// judgement about the instruction, so there is nothing truthful to tell
	// the sender. They are classified here as never reaching a counterparty;
	// the mesh (Task 6) is where that classification becomes observable, as
	// a dead letter rather than a wire message. Until the mesh exists,
	// reasonFor cannot yet tell one of these apart from an error the table
	// has never heard of at all — both return MS03 through the same
	// fallback path. See TestReasonForEmptyCodeEntriesFallToMS03.

	// A lookup for an id this system generated and then could not find is a
	// bug here, not a defect in the message.
	{ErrPaymentNotFound, "ErrPaymentNotFound", ""},
	{ErrCycleNotFound, "ErrCycleNotFound", ""},
	{ErrSettlementNotFound, "ErrSettlementNotFound", ""},

	// Cycle lifecycle errors reach only the operator who drove the cycle into
	// the wrong state; no counterparty ever sees one.
	{ErrCycleNotClosed, "ErrCycleNotClosed", ""},
	{ErrCycleAlreadyOpen, "ErrCycleAlreadyOpen", ""},

	// An illegal transition means this system tried to move a payment
	// somewhere its own state machine forbids. Telling the counterparty
	// "rejected, unspecified" would hide a defect behind a plausible message.
	{ErrInvalidStateTransition, "ErrInvalidStateTransition", ""},
}

// reasonFor maps an error to the code a pacs.002 should carry.
//
// It unwraps, because the payment layer wraps freely and a table keyed on
// identity alone would degrade to MS03 for most real failures — silently,
// which is the failure mode this whole arrangement exists to prevent.
//
// An error the table does not know is MS03 rather than a panic. An actor that
// crashed instead of answering would be a worse outcome than an imprecise
// code, and the exhaustiveness test is what stops that path being reachable
// for a sentinel.
func reasonFor(err error) iso20022.StatusReason {
	for _, m := range reasonTable {
		if m.Code != "" && errors.Is(err, m.Err) {
			return m.Code
		}
	}
	return iso20022.StatusReasonNotSpecifiedAgentGenerated
}

// ---------------------------------------------------------------------------
// Outbound: payment types to messages
// ---------------------------------------------------------------------------

// MessageContext is everything a message needs that the payment itself does
// not carry: who is sending it, who to, what to call it, and when.
//
// It is a parameter rather than state on Network because the same payment
// produces different messages on different hops — the debtor's bank sends the
// pacs.008 to the CSM, the CSM sends the same instruction on to the creditor's
// bank — and the difference is entirely in here.
type MessageContext struct {
	From  iso20022.BIC
	To    iso20022.BIC
	MsgID string
	Now   time.Time
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
//
// The two differ the moment a message passes through an intermediary: a status
// report travelling back from the creditor's bank through the clearing house is
// sent by the clearing house and originated by the bank. Every message this
// system emits is one it decided itself, so the originator is mc.From — but it
// is written out rather than left to the header, because a receiver reading the
// header's Fr as the decider blames the wrong institution for every rejection
// that was relayed. See iso20022.StatusReasonInformation.
func (mc MessageContext) orgtr() *iso20022.PartyIdentification {
	return &iso20022.PartyIdentification{
		Id: &iso20022.PartyChoice{
			OrgId: &iso20022.OrganisationIdentification{AnyBIC: mc.From},
		},
	}
}

// messageParty is one side of a payment with everything a message says about
// it: the bank that holds the account, the name on the account, and the address
// the payment quoted to reach it.
//
// It exists so that the conversion itself is a pure function of resolved data,
// with the store reads on one side of the line and the mapping on the other.
// That is what lets FuzzTranslate drive the mapping over names and addresses no
// fixture builder would have thought of — and the store would never hold.
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

// amountOf converts a ledger amount to the standard's decimal representation.
//
// The scale comes from the asset definition rather than from a constant,
// because this repository's ledger is multi-asset and a two-decimal assumption
// would be wrong the first time a scheme in another asset arrives — which is
// exactly the shape of mistake sub-project 1 recorded.
//
// The rendered amount is then checked, and the check ASKS THE CODEC rather than
// deciding for itself. That framing is the whole of it: this function does not
// know what ISO 20022 permits and does not try to, it asks iso20022 whether
// iso20022 can carry the value, and refuses early if not. So the bound below is
// a description of that package's Validate at the time of writing, and if
// Validate is ever tightened or loosened this function needs no change.
//
// Two ways an amount this ledger holds is refused, one large and one small:
//
//   - FRACTION DIGITS. The standard caps ActiveCurrencyAndAmount at five for any
//     currency, so an asset scaled finer than that has no representation on the
//     wire at all. Bitcoin, at eight, is one — and it is in this repository's
//     asset table today, so this is a live limit rather than a hypothetical.
//     ErrAmountScale. See TestSettlementMessageTakesItsScaleFromTheAsset.
//
//   - MAGNITUDE, and here the honest number is not the one you would guess. The
//     refusal is NOT the standard's eighteen-total-digit ceiling: iso20022's
//     Validate implements its shape check by calling Minor(5), which zero-pads
//     the fraction to five places and parses the result as an int64, so the real
//     bound is MaxInt64 / 10^(5-scale) — for a two-decimal asset,
//     9,223,372,036,854,775 minor units, at SIXTEEN rendered digits. That is an
//     int64 overflow inside the validator's own padding, an artifact of how the
//     check is written, not a property of ISO 20022. It bites in one direction
//     only, refusing legal seventeen- and eighteen-digit values, so nothing
//     invalid escapes; it is recorded rather than worked around because a
//     workaround here would be this package second-guessing the codec, which is
//     exactly what "ask the codec" avoids. Enforcing the standard's actual
//     ceiling belongs in iso20022.ActiveCurrencyAndAmount.Validate, which is
//     where the bound would then also be testable. ErrAmountFormat, which is the
//     wrong sentinel for a well-formed number and is part of the same artifact.
//     Both edges are pinned by TestSettlementMessageAmountBound.
//
// Neither was found by FuzzTranslate, and that is worth recording rather than
// hiding. Two and a half million executions did not reach either, because the
// target fuzzes the AMOUNT and holds the asset at EUR, and a large amount only
// produces a message at all once the fuzzer has also assembled two valid IBANs
// and two non-empty names. A hand-written probe over ledger.Assets and the
// int64 boundaries found both in one run. A fuzz target explores the inputs it
// was given and no others, which is the honest limit of the technique.
//
// Refusing here rather than at Marshal is the same choice ibanOf makes. Both
// errors are correct; only the one raised here names the payment rather than an
// element inside a document, and only that one can be turned into a pacs.002 a
// customer can read.
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
//
// It refuses an empty or non-IBAN address rather than emitting an empty
// element. A pacs.008 whose DbtrAcct has no IBAN is invalid, and producing one
// would move the failure from here to a counterparty's parser.
//
// It also COMPACTS and then checks the value. Compaction because an IBAN is
// stored here with display separators for readability and transmitted without
// them (see iso20022.IBAN). The check because a stored value that is not an
// IBAN at all would otherwise reach Marshal, and the error a caller then sees
// names an element inside a document rather than the account this system could
// not address — both errors are correct, and only this one can be turned into a
// pacs.002 for the customer. Delete the check and FuzzTranslate fails in under
// a second on the address "0"; that is what it is for.
//
// It returns the IBAN rather than the CashAccount that wraps it, because a
// pacs.003 needs the same value in two places — CdtrAcct and, standing in for
// the Creditor Identifier, CdtrSchmeId — and the alternative was reaching back
// into the built element for it through a pointer that only happens never to be
// nil. See cashAccount.
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
//
// It takes the IBAN by value and takes its address here, which is the whole
// reason it exists: AccountIdentification4Choice's arms are pointers because
// encoding/xml cannot express an xsd:choice, and a pointer arm is an invitation
// to a nil dereference somewhere downstream. Building the choice in exactly one
// place, from a value that cannot be nil, means no other function in this file
// ever has to dereference it.
func cashAccount(iban iso20022.IBAN) iso20022.CashAccount {
	return iso20022.CashAccount{Id: iso20022.AccountIdentification4Choice{IBAN: &iban}}
}

// namedPartyOf is the customer element of a pacs.008 or a pacs.003.
//
// A nameless one is refused here rather than at Marshal, for the same reason
// ibanOf refuses an unaddressable account: EPC AT-P001 and AT-E001 make Nm
// mandatory on both sides of both messages, so a party with no name is a
// document a counterparty rejects. Delete the check and FuzzTranslate finds an
// empty creditor name in about half a second. The error is not one of
// payment's sentinels because there is no condition in this
// system's own vocabulary for "the account holder has no name" — it wraps
// iso20022.ErrMissingElement, which says exactly what went wrong, and falls to
// MS03 through reasonFor's default like any other error the table has never
// heard of.
func namedPartyOf(element, name string) (iso20022.PartyIdentification, error) {
	if name == "" {
		return iso20022.PartyIdentification{}, fmt.Errorf("%w: %s/Nm", iso20022.ErrMissingElement, element)
	}
	return iso20022.PartyIdentification{Nm: name}, nil
}

// endToEndOf is the customer's own reference, or the value the guidelines
// reserve for its absence.
//
// EndToEndId is 1..1 in the schema and payment.Payment's EndToEndID is
// optional, so the gap has to be filled with something. NOTPROVIDED is the EPC
// convention, and it is filled in HERE rather than defaulted at initiation
// because it is a fact about the message and not about the payment: a receiver
// reading NOTPROVIDED knows the sender had no reference, whereas a payment
// carrying the literal string in its own record would have one that means
// nothing.
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
//
// It is the payment's VALUE date and not the message's creation date: SEPA CT
// settles at T+1 and SDD at T+2, so the two are days apart by design and a
// message that used Now would instruct settlement on the wrong day.
//
// A payment with no value date has not been through InitiatePayment, and gets
// the message's own creation date rather than the zero time. The zero time
// marshals as 0001-01-01 — a schema-valid date asserting settlement in the
// first century, which is the same silent fabrication AppHdr.CreDt's validation
// exists to stop, arriving by a different door.
func settlementDateOf(p Payment, mc MessageContext) iso20022.ISODate {
	if p.ValueDate.IsZero() {
		return iso20022.ISODate{Time: mc.Now}
	}
	return iso20022.ISODate{Time: p.ValueDate}
}

// clearingSettlement is how every message this system sends settles: through a
// clearing system, rather than across accounts the two agents hold with each
// other. See iso20022.SettlementMethodClearing for why that is a property of
// THIS clearing house and not of the scheme.
func clearingSettlement() iso20022.SettlementInstruction {
	return iso20022.SettlementInstruction{SttlmMtd: iso20022.SettlementMethodClearing}
}

// assetOf is the unit a payment settles in, which is a property of its scheme
// and not of the payment. An unregistered scheme is ErrSchemeNotFound here for
// the same reason it is at initiation: this system cannot say what a payment
// under a scheme it does not implement is denominated in, and guessing euro
// would be the multi-asset mistake amountOf exists to avoid.
func (s *Network) assetOf(p Payment) (ledger.AssetCode, error) {
	sc, ok := s.scheme(p.Scheme)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrSchemeNotFound, p.Scheme)
	}
	return sc.Asset(), nil
}

// partiesOf resolves a payment's two sides to what a message says about them:
// each bank's BIC and each account holder's name.
//
// It is the ONLY part of building an outbound message that touches the store,
// and the reason the two Network methods above take a context while the four
// pure builders below do not. That division is deliberate: a caller draining a
// queue of messages can abandon the read, and everything after it is arithmetic
// on values it already holds.
func (s *Network) partiesOf(ctx context.Context, p Payment) (debtor, creditor messageParty, err error) {
	err = s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		if debtor, err = s.partyTx(ctx, tx, p.Debtor); err != nil {
			return err
		}
		creditor, err = s.partyTx(ctx, tx, p.Creditor)
		return err
	})
	return debtor, creditor, err
}

// partyTx resolves one side. The identifier is taken from the PAYMENT and not
// from the account, because a payment records the address actually quoted to
// reach a party on that occasion — see PartyRef — and an account may hold
// several or have had one withdrawn since.
//
// # Only a NOT-FOUND becomes a domain error
//
// The obvious shape here — `if err != nil { return ErrAccountNotInParticipant }`
// — is the one checkPartyTx uses, and it is wrong in this function in a way it
// is not wrong there, because of what happens downstream. These errors are
// destined for reasonFor and then for a counterparty's pacs.002:
// ErrParticipantNotFound becomes RC01 "bank identifier incorrect" and
// ErrAccountNotInParticipant becomes AC01 "incorrect account number". A dropped
// database connection, or a caller that cancelled the context this function now
// takes, would be reported to another bank as a defect in ITS message. The
// counterparty would then investigate an address that was never wrong.
//
// So a store failure is returned unchanged and falls to MS03 through reasonFor's
// default, which says "this agent could not carry it out" — true, unhelpful, and
// vastly better than a confident false statement about someone else's data. See
// TestCreditTransferMessageDoesNotBlameTheCounterpartyForAStoreFailure.
func (s *Network) partyTx(ctx context.Context, tx Tx, ref PartyRef) (messageParty, error) {
	part, err := s.participantTx(ctx, tx, ref.Participant)
	if err != nil {
		return messageParty{}, err
	}
	acct, err := tx.GetDepositAccount(ctx, part.BookID, ref.Account)
	if err != nil {
		if errors.Is(err, deposit.ErrAccountNotFound) {
			return messageParty{}, fmt.Errorf("%w: %s", ErrAccountNotInParticipant, ref.Account)
		}
		return messageParty{}, err
	}
	return messageParty{BIC: part.BIC, Name: acct.Name, Identifier: ref.Identifier}, nil
}

// CreditTransferMessage renders a payment as the pacs.008 that carries it
// between banks.
//
// The two AGENTS are the two banks, and the header's To is whoever the message
// is being handed to next — the clearing house, on the first hop. Those are
// different questions with different answers, and conflating them would model a
// topology this system does not have: banks here meet at a CSM, never directly.
//
// It takes a context because it reads the store: a payment records which
// participant and which account each side is, and a message needs the BIC and
// the account holder's NAME, neither of which a Payment carries. That is the
// whole of the I/O, and it is why this is a method rather than a function.
func (s *Network) CreditTransferMessage(ctx context.Context, p Payment, mc MessageContext) (iso20022.Envelope, error) {
	asset, err := s.assetOf(p)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	debtor, creditor, err := s.partiesOf(ctx, p)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	return creditTransfer(p, debtor, creditor, asset, mc)
}

// creditTransfer builds the message from data already resolved out of the
// store. Everything it does is a pure function of its arguments, which is what
// FuzzTranslate needs and what makes the store reads above a separate concern.
func creditTransfer(p Payment, debtor, creditor messageParty, asset ledger.AssetCode, mc MessageContext) (iso20022.Envelope, error) {
	amt, err := amountOf(p.Amount, asset)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	dbtr, err := namedPartyOf("Dbtr", debtor.Name)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	dbtrIBAN, err := ibanOf("DbtrAcct", debtor.Identifier)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	cdtr, err := namedPartyOf("Cdtr", creditor.Name)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	cdtrIBAN, err := ibanOf("CdtrAcct", creditor.Identifier)
	if err != nil {
		return iso20022.Envelope{}, err
	}

	// The field order is the schema's, and it is the payment's own path: party,
	// its bank, the other bank, the other party. LclInstrm is absent because
	// SEPA credit transfer does not populate it, and SeqTp is absent because
	// pacs.008 has no such element at all — see iso20022.PaymentTypeInformation.
	txs := []iso20022.CreditTransferTransaction{{
		PmtId: paymentIdentificationOf(p),
		PmtTpInf: &iso20022.PaymentTypeInformation{
			SvcLvl: &iso20022.ServiceLevelChoice{Cd: iso20022.ServiceLevelSEPA},
		},
		IntrBkSttlmAmt: amt,
		ChrgBr:         iso20022.ChargeBearerFollowingServiceLevel,
		Dbtr:           dbtr,
		DbtrAcct:       cashAccount(dbtrIBAN),
		DbtrAgt:        agentOf(debtor.BIC),
		CdtrAgt:        agentOf(creditor.BIC),
		Cdtr:           cdtr,
		CdtrAcct:       cashAccount(cdtrIBAN),
		RmtInf:         remittanceOf(p.Description),
	}}

	doc := &iso20022.Pacs008{FIToFICstmrCdtTrf: iso20022.FIToFICustomerCreditTransfer{
		GrpHdr: iso20022.CreditTransferGroupHeader{
			MsgId:   mc.MsgID,
			CreDtTm: iso20022.ISODateTime{Time: mc.Now},
			// NbOfTxs is what the SENDER asserts, not a derivation the receiver
			// would recompute. A receiver that recomputed it instead of checking
			// it would never notice a truncated file, which is the whole reason
			// the element exists. See
			// TestSettlementMessageNbOfTxsSurvivesATruncatedFile.
			NbOfTxs:       strconv.Itoa(len(txs)),
			IntrBkSttlmDt: settlementDateOf(p, mc),
			SttlmInf:      clearingSettlement(),
		},
		CdtTrfTxInf: txs,
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// DirectDebitMessage renders a collection as the pacs.003 that carries it.
//
// It is the mirror of CreditTransferMessage in the one way that matters: the
// SENDER is the party being paid. A push scheme's message travels with the
// money; a pull scheme's travels against it, which is why the creditor and its
// agent come first in the transaction and why the mandate has to travel too.
func (s *Network) DirectDebitMessage(ctx context.Context, p Payment, m Mandate, mc MessageContext) (iso20022.Envelope, error) {
	asset, err := s.assetOf(p)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	debtor, creditor, err := s.partiesOf(ctx, p)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	return directDebit(p, m, debtor, creditor, asset, mc)
}

func directDebit(p Payment, m Mandate, debtor, creditor messageParty, asset ledger.AssetCode, mc MessageContext) (iso20022.Envelope, error) {
	amt, err := amountOf(p.Amount, asset)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	cdtr, err := namedPartyOf("Cdtr", creditor.Name)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	cdtrIBAN, err := ibanOf("CdtrAcct", creditor.Identifier)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	dbtr, err := namedPartyOf("Dbtr", debtor.Name)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	dbtrIBAN, err := ibanOf("DbtrAcct", debtor.Identifier)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	if m.ID == "" {
		return iso20022.Envelope{}, ErrMandateRequired
	}

	local := iso20022.LocalInstrumentCore
	seq := iso20022.SequenceTypeRecurring
	txs := []iso20022.DirectDebitTransactionInformation{{
		PmtId: paymentIdentificationOf(p),
		PmtTpInf: &iso20022.PaymentTypeInformation{
			SvcLvl:    &iso20022.ServiceLevelChoice{Cd: iso20022.ServiceLevelSEPA},
			LclInstrm: &iso20022.LocalInstrumentChoice{Cd: &local},
			// SeqTp is RCUR for every collection, including the first.
			//
			// This system does not record whether a mandate has been exercised
			// before, so FRST is a claim it cannot make; the 2016 SEPA Core
			// rulebook removed the requirement to send FRST first and permits
			// RCUR throughout, which is what makes the weaker statement the
			// correct one rather than merely the safe one.
			SeqTp: &seq,
		},
		IntrBkSttlmAmt: amt,
		ChrgBr:         iso20022.ChargeBearerFollowingServiceLevel,
		DrctDbtTx: iso20022.DirectDebitTransaction{
			MndtRltdInf: iso20022.MandateRelatedInformation{
				MndtId: string(m.ID),
				// DtOfSgntr maps Mandate.CreatedAt, and this elision is
				// deliberate.
				//
				// The EPC makes the date of signature mandatory. payment.Mandate
				// has no signature date, and adding one was considered and
				// rejected in the spec: in this system a mandate is created at
				// the moment it is authorised, so the two are the same fact and a
				// second column would be a field with no independent source. A
				// reader meeting a real mandate — signed on paper, keyed in a
				// week later — finds the difference stated here rather than
				// assumed away. See
				// TestDirectDebitMessageDatesTheSignatureFromTheMandate.
				DtOfSgntr: iso20022.ISODate{Time: m.CreatedAt},
			},
			CdtrSchmeId: creditorSchemeIdentification(cdtrIBAN),
		},
		Cdtr:     cdtr,
		CdtrAcct: cashAccount(cdtrIBAN),
		CdtrAgt:  agentOf(creditor.BIC),
		Dbtr:     dbtr,
		DbtrAcct: cashAccount(dbtrIBAN),
		DbtrAgt:  agentOf(debtor.BIC),
		RmtInf:   remittanceOf(p.Description),
	}}

	doc := &iso20022.Pacs003{FIToFICstmrDrctDbt: iso20022.FIToFICustomerDirectDebit{
		GrpHdr: iso20022.DirectDebitGroupHeader{
			MsgId:         mc.MsgID,
			CreDtTm:       iso20022.ISODateTime{Time: mc.Now},
			NbOfTxs:       strconv.Itoa(len(txs)),
			IntrBkSttlmDt: settlementDateOf(p, mc),
			SttlmInf:      clearingSettlement(),
		},
		DrctDbtTxInf: txs,
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// creditorSchemeIdentification is AT-02, the Creditor Identifier — and it is
// the creditor's own IBAN, which is the second elision this message makes and
// the larger of the two.
//
// A real Creditor Identifier is issued by a national scheme: a country's
// central bank or an equivalent authority assigns it, which is precisely what
// makes it unforgeable and therefore worth checking a mandate against. This
// repository models no such authority, so it has no such value, and deriving
// one from a name or a BIC would fabricate the one property the element has.
// The creditor's IBAN is used instead because it identifies the creditor
// uniquely within this network, which is the property a debtor's bank reads the
// element FOR. What it is not is a Creditor Identifier: a debtor's bank holding
// a mandate scoped to a real CI would find a value it cannot look up. That is
// the cost, recorded rather than assumed away. See
// TestDirectDebitMessageStandsTheCreditorsIBANInForTheCreditorIdentifier.
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
//
// A status report refers BACKWARDS and by nothing else: the sender of the
// original has no other way to find what this is about, which is what makes
// clearing asynchronous rather than merely delayed.
type OriginalMessage struct {
	MsgID     string
	MsgDefIdr string
	CreDtTm   time.Time
}

// TransactionStatusReport is the outcome of one transaction in that message.
//
// Code and Text are both carried because they say different things: the code is
// what makes a rejection machine-actionable, and the text is what says the part
// no code can — which ceiling was exceeded, what the available balance was. See
// iso20022.StatusReasonInformation.
type TransactionStatusReport struct {
	EndToEndID string
	TxID       string
	Status     iso20022.TransactionStatus
	Code       iso20022.StatusReason
	Text       string
}

// StatusMessage renders the fate of an earlier message as the pacs.002 that
// reports it.
//
// This is not a Network method because nothing in it comes from the store: a
// status is about a message, and the message is what the caller has.
func StatusMessage(orig OriginalMessage, sts []TransactionStatusReport, mc MessageContext) (iso20022.Envelope, error) {
	txs := make([]iso20022.PaymentTransactionStatus, 0, len(sts))
	for i, s := range sts {
		txs = append(txs, iso20022.PaymentTransactionStatus{
			// StsId names THIS status, not the payment it is about — see
			// iso20022.PaymentTransactionStatus. It is derived from the
			// message's own identifier and the position within it, so that two
			// statuses in one report are distinguishable and a later query can
			// name one of them.
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
// the element entirely when the caller does not know. The alternative is
// 0001-01-01T00:00:00Z, a timestamp asserting that the original was written two
// thousand years ago — the fabrication AppHdr.CreDt's own validation exists to
// stop.
func originalCreationOf(orig OriginalMessage) *iso20022.ISODateTime {
	if orig.CreDtTm.IsZero() {
		return nil
	}
	return &iso20022.ISODateTime{Time: orig.CreDtTm}
}

// statusReasonOf is why, and it is present only for a rejection.
//
// An acceptance has no reason: StatusReasonChoice requires exactly one of a
// code and a proprietary text, so an accepted transaction with a reason element
// would have to invent one. A rejection with no code is likewise not
// representable, and is not reachable either — reasonFor returns MS03 rather
// than the empty string for every error, including ones it has never heard of —
// so a rejection whose Code is empty is a caller that built the report by hand
// and left it out. It gets MS03 for the same reason reasonFor's default does:
// "this agent refused it and the reason has no code" is the weakest true
// statement available, and it is better than a rejection a receiver cannot act
// on at all.
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
//
// It is derived and the per-transaction statuses are not, which is the one
// place in this file where a derivation is the right answer: GrpSts is a
// summary of what the sender is saying in the SAME message, not a count a
// receiver would use to detect a loss. A file of a thousand transfers accepted
// with fifty rejections is neither accepted nor rejected, and PART is the only
// truthful answer — see iso20022.OriginalGroupHeader.
//
// A report with no transaction statuses at all leaves the element empty rather
// than guessing: it is optional in the standard precisely so that a report can
// decline to characterise a group it says nothing about.
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
//
// It is a distinct MESSAGE and not a status precisely because settlement was
// final: a pacs.002 says an instruction was refused, a pacs.004 says a
// completed payment is being reversed by sending an equal and opposite one.
//
// The reason is an iso20022.ReturnReason and not a StatusReason, and the two
// types are separate even where their members coincide. Payment.RejectCode is
// therefore NOT the source of it — see that field — and the caller supplies the
// return's own reason.
//
// This is a Network method although it reads no store, and it takes no context
// for exactly that reason — the asymmetry with CreditTransferMessage and
// DirectDebitMessage is the point rather than an oversight. It is a method
// because the amount's scale comes from the scheme's asset and only the Network
// holds the scheme registry, which is an in-memory map; there is no I/O here to
// cancel. A pacs.004 names no parties: it refers to the original payment by
// identifier and carries amounts, so nothing in it needs a BIC or an account
// holder's name looked up.
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
		// ReturnPayment takes no amount. They are two elements because the
		// standard is shaped for partial returns; see iso20022.ReturnTransaction.
		OrgnlIntrBkSttlmAmt: amt,
		RtrdIntrBkSttlmAmt:  amt,
		ChrgBr:              iso20022.ChargeBearerFollowingServiceLevel,
		RtrRsnInf: &iso20022.ReturnReasonInformation{
			Orgtr:    mc.orgtr(),
			Rsn:      iso20022.ReturnReasonChoice{Cd: &reason},
			AddtlInf: text,
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

// SettlementLeg is one bank's movement in a settlement instruction: who pays,
// who is paid, how much, and which closed cycle it discharges.
//
// It carries its own Asset because a settlement message may hold legs from
// several cycles, and a cycle's asset is a property of its scheme. Both parties
// are BICs rather than participant ids because this message goes to the central
// bank, which knows banks by BIC and has never heard of this system's ids.
type SettlementLeg struct {
	From      iso20022.BIC
	To        iso20022.BIC
	Amount    ledger.Amount
	Asset     ledger.AssetCode
	Reference string
}

// SettlementMessage renders a closed cycle's net positions as the pacs.009 that
// instructs the central bank to settle them.
//
// A pacs.009 and not a pacs.008 because BOTH parties are banks: a pacs.008
// moves a customer's money and names two customers, this moves a bank's own
// money and names two banks. The compiler enforces the difference — Dbtr and
// Cdtr here are agents, not parties — which is why a customer cannot end up in
// a settlement instruction by mistake.
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
			// NbOfTxs is what the SENDER asserts, not a derivation the receiver
			// would recompute. A receiver that recomputed it instead of checking
			// it would never notice a truncated file, which is the whole reason
			// the element exists — and it is a settlement instruction, where a
			// silently dropped leg is a bank that does not get paid. See
			// TestSettlementMessageNbOfTxsSurvivesATruncatedFile.
			NbOfTxs: strconv.Itoa(len(txs)),
			// TtlIntrBkSttlmAmt is deliberately absent. The legs of one message
			// may be denominated in different assets, and a sum across assets is
			// not a number; the standard's single total has nowhere to say which
			// asset it is in. Omitting an optional element beats asserting a
			// figure that is only correct when every leg happens to agree.
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
