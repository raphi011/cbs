package api

import (
	"sort"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// Wire format for the interbank payment layer: participants, party refs,
// payments, mandates, clearing cycles, settlements, schemes, and requests.

// participantAccountsDTO is one asset's worth of a bank's internal accounts.
type participantAccountsDTO struct {
	Asset    string `json:"asset"`
	Suspense string `json:"suspense"`
	Reserve  string `json:"reserve"`
	// Unclaimed is where a credit goes when the payee's account will not take
	// it. It is exposed beside the other three because a balance sitting on it
	// is a real operational queue — money the bank owes somebody it has not yet
	// identified — and an account nothing renders is one nobody goes looking at.
	Unclaimed string `json:"unclaimed"`
	// VaultCash is the cash the bank is holding, and the only account here that is
	// nobody else's promise. It is the CONTRA LEG OF EVERY DEPOSIT, which is the
	// most common transaction in this system; a statement renders a well-known
	// account as a word and an unknown one as an opaque id, so leaving it out makes
	// every customer's cash-in read as a bare account number — see
	// buildKnownAccounts in web/src/lib/statement.ts, this field's only consumer.
	VaultCash  string `json:"vaultCash"`
	Settlement string `json:"settlement"`
}

// participantDTO renders the internal accounts as a list rather than four flat
// fields, because a bank holds one set per asset it operates in.
//
// A list rather than an object keyed by code so the order is the API's to
// choose: Go randomises map iteration, and a wire format that reorders itself
// between two identical requests is not one a client can diff.
type participantDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// BIC is this bank's ISO 9362 business identifier code — what a
	// counterparty addresses it by, and what the mesh routes on.
	BIC string `json:"bic"`
	// Status is "Founded" or "Member", and it is the field that says which of the
	// two a client is holding.
	//
	// A founded bank has a book, a chart of accounts and a product, and that part
	// of it is unrestricted: it opens customer accounts, publishes products, adds
	// ledgers. What it cannot do is anything needing another institution. It
	// cannot take a cash deposit — funding raises its reserve at the central bank
	// in the same step and no settlement agent holds an account for it to raise —
	// and the refusal is a 422 naming the membership rather than the account. Nor
	// can any cut-off it takes part in settle, because the instruction turns net
	// positions into addresses through a routing directory this bank is not in.
	//
	// It does NOT say the bank cannot be paid. The mesh routes on its ACTOR TABLE,
	// which Mesh.Admit fills at founding, so a payment addressed to a founded bank
	// is relayed, accepted and reaches Cleared like any other and the cut-off
	// carrying it is what fails. What refuses it is payment.ErrBankNotAdmitted, at
	// Mesh.Submit and at the clearing house.
	//
	// It became a state a client can SEE when admission became a conversation: POST /members
	// answers 202 with a founded bank, and the scheme's answer arrives at two
	// other institutions afterwards. Before that the two were one commit and
	// every bank a caller could read was a member.
	//
	// It is a string of the domain's own values rather than a boolean, because
	// "not a member" is not one condition: an admission can stop halfway, and a
	// bool would have to be widened on the day it names one.
	Status string `json:"status"`
	// ProductID is the bank's default deposit product, created with its chart
	// of accounts at onboarding. Every deposit account is opened FROM a
	// product, so a client that has just created a bank needs to be told which
	// one it may open accounts from before it can open any.
	ProductID         string                   `json:"productId"`
	CustomerSubledger string                   `json:"customerSubledger"`
	Assets            []participantAccountsDTO `json:"assets"`
}

func toParticipantDTO(p *payment.Bank) participantDTO {
	assets := make([]participantAccountsDTO, 0, len(p.Assets))
	for code, accts := range p.Assets {
		assets = append(assets, participantAccountsDTO{
			Asset:      string(code),
			Suspense:   string(accts.Suspense),
			Reserve:    string(accts.Reserve),
			Unclaimed:  string(accts.Unclaimed),
			VaultCash:  string(accts.VaultCash),
			Settlement: string(accts.Settlement),
		})
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Asset < assets[j].Asset })

	return participantDTO{
		ID:                string(p.ID),
		Name:              p.Name,
		BIC:               string(p.BIC),
		Status:            string(p.Status),
		ProductID:         string(p.ProductID),
		CustomerSubledger: string(p.CustomerSubledger),
		Assets:            assets,
	}
}

// createParticipantRequest carries the participant's name and, optionally,
// the set of assets it joins with. An absent or empty Assets defaults to
// ["EUR"] — payment applies that default itself, when the bank is founded, so
// an empty slice is forwarded unchanged rather than special-cased here.
//
// This is the one deliberate default anywhere in the asset dimension: it
// preserves existing behaviour for callers that do not care which assets a
// bank joins with. It is not a default for the asset of any account — every
// account, deposit account and transaction still names its asset explicitly.
type createParticipantRequest struct {
	Name string `json:"name"`
	// BIC is required: a bank the mesh cannot address is not a member. It is
	// validated by mesh.Mesh.Admit, before it claims the address
	// (iso20022.BIC.Validate), which is what turns a malformed value into a 422
	// rather than a 400 — the field is present and well-typed, and what is
	// wrong with it is a business rule.
	BIC string `json:"bic"`

	// Country and BankCode are the allocation this bank issues its customers'
	// addresses under, and both are required: an account is opened with an IBAN
	// minted from them, so a bank founded without one can open no accounts.
	//
	// They are SUPPLIED and not derived from the BIC, because there is nothing
	// to derive from — a bank code is a national registry's allocation and a BIC
	// is SWIFT's, and "AURODEFFXXX" and "99900001" have no computable
	// relationship. That is the same argument payment.Bank.BIC already makes
	// about its own field, one identifier along.
	//
	// Validated by iban.Issuer.Validate, through Admit, which is what makes a
	// wrong-width code a 422 rather than a 400: the field is present and
	// well-typed, and what is wrong with it is a business rule.
	Country  string `json:"country"`
	BankCode string `json:"bankCode"`

	Assets []string `json:"assets"`
}

type identifierDTO struct {
	Scheme string `json:"scheme"`
	Value  string `json:"value"`
}

// toIdentifierDTOs renders an account's identifiers as a non-nil empty slice
// rather than nil, so the JSON key is "[]" and not "null" — a web client that
// renders a list of addresses should not have to distinguish "no identifiers"
// from "the field was missing".
func toIdentifierDTOs(idents []deposit.Identifier) []identifierDTO {
	out := make([]identifierDTO, 0, len(idents))
	for _, i := range idents {
		out = append(out, identifierDTO{Scheme: string(i.Scheme), Value: i.Value})
	}
	return out
}

// partyRefDTO is one side of a payment or a mandate: the account, and the
// address quoted to reach it.
//
// It carried a `participant` naming the bank, and does not, for the reason
// payment.PartyRef does not: a bank's id is its BIC now, so that field and the
// agent beside it were one value spelled twice. Which bank a party is at travels
// as `debtorAgent`/`creditorAgent` on the enclosing payment or mandate — the same
// names an instruction already used on the way IN, so the two directions finally
// agree.
type partyRefDTO struct {
	Account string `json:"account"`
	// Identifier is the external address quoted for this party — an IBAN today.
	// Absent for a party addressed only by its ids.
	Identifier *identifierDTO `json:"identifier,omitempty"`
}

func toPartyRefDTO(r payment.PartyRef) partyRefDTO {
	out := partyRefDTO{Account: string(r.Account)}
	if r.Identifier != (deposit.Identifier{}) {
		out.Identifier = &identifierDTO{Scheme: string(r.Identifier.Scheme), Value: r.Identifier.Value}
	}
	return out
}

// toDomain converts the wire shape, canonicalising an address a PERSON typed.
//
// This is the front door iban.Parse describes. A customer reads an IBAN off a
// statement, where it is grouped in fours, and types it in whatever case they
// were in; everything downstream works in the one canonical form. A quoted
// address for a party at ANOTHER bank is the case that needs it: this bank
// cannot reach that register, so nothing later replaces the quote with a stored
// value, and what the caller typed is what goes on the message. A document whose
// IBAN carries spaces or a lower-case country code is not schema-valid, and the
// codec is right to refuse it — see iso20022.IBAN.Compact, which deliberately
// does not fold case for exactly that reason. Folding what a person typed is
// this end's job, and this is that end.
//
// A value that will not parse is passed through UNCHANGED. The refusal is then
// deposit.Identifier.Validate's, which says what is wrong with the address the
// caller sent rather than with some tidied-up version of it.
func (r partyRefDTO) toDomain() payment.PartyRef {
	out := payment.PartyRef{Account: deposit.AccountID(r.Account)}
	if r.Identifier != nil {
		value := r.Identifier.Value
		if deposit.IdentifierScheme(r.Identifier.Scheme) == deposit.IdentifierIBAN {
			if parsed, err := iban.Parse(value); err == nil {
				value = string(parsed)
			}
		}
		out.Identifier = deposit.Identifier{
			Scheme: deposit.IdentifierScheme(r.Identifier.Scheme),
			Value:  value,
		}
	}
	return out
}

type paymentDTO struct {
	ID       string      `json:"id"`
	Scheme   string      `json:"scheme"`
	Asset    string      `json:"asset"`
	Debtor   partyRefDTO `json:"debtor"`
	Creditor partyRefDTO `json:"creditor"`
	// DebtorAgent and CreditorAgent are the BICs of the two banks, and they are
	// what says where each party banks now that a partyRefDTO does not.
	DebtorAgent   string            `json:"debtorAgent,omitempty"`
	CreditorAgent string            `json:"creditorAgent,omitempty"`
	Amount        int64             `json:"amount"`
	MandateID     string            `json:"mandateId,omitempty"`
	EndToEndID    string            `json:"endToEndId,omitempty"`
	Status        string            `json:"status"`
	RejectCode    string            `json:"rejectCode,omitempty"`
	RejectReason  string            `json:"rejectReason,omitempty"`
	CycleID       string            `json:"cycleId,omitempty"`
	BookingDate   time.Time         `json:"bookingDate"`
	ValueDate     time.Time         `json:"valueDate"`
	Description   string            `json:"description,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	DebtorLegTx   string            `json:"debtorLegTx,omitempty"`
	CreditorLegTx string            `json:"creditorLegTx,omitempty"`
	CreatedAt     time.Time         `json:"createdAt"`
}

// toPaymentDTO renders a payment, including the asset it settles in. A
// payment names a scheme, not an asset — the asset is the scheme's, fixed at
// registration — so rendering it means resolving the scheme. schemes is the
// network's registered set (payment.Network.ListSchemes), searched by ID
// rather than threaded through payment.Payment itself, which stays a pure
// domain record.
func toPaymentDTO(p payment.Payment, schemes []payment.Scheme) paymentDTO {
	var asset string
	for _, sc := range schemes {
		if sc.ID() == p.Scheme {
			asset = string(sc.Asset())
			break
		}
	}
	return paymentDTO{
		ID:            string(p.ID),
		Scheme:        string(p.Scheme),
		Asset:         asset,
		Debtor:        toPartyRefDTO(p.Debtor),
		Creditor:      toPartyRefDTO(p.Creditor),
		DebtorAgent:   string(p.DebtorDetails.Agent),
		CreditorAgent: string(p.CreditorDetails.Agent),
		Amount:        int64(p.Amount),
		MandateID:     string(p.MandateID),
		EndToEndID:    p.EndToEndID,
		Status:        p.Status.String(),
		RejectCode:    string(p.RejectCode),
		RejectReason:  p.RejectReason,
		CycleID:       string(p.CycleID),
		BookingDate:   p.BookingDate,
		ValueDate:     p.ValueDate,
		Description:   p.Description,
		Metadata:      p.Metadata,
		DebtorLegTx:   string(p.DebtorLegTx),
		CreditorLegTx: string(p.CreditorLegTx),
		CreatedAt:     p.CreatedAt,
	}
}

type mandateDTO struct {
	ID     string      `json:"id"`
	Debtor partyRefDTO `json:"debtor"`
	// DebtorAgent is the bank a collection under this mandate is sent to. There
	// is no creditorAgent beside it and there is no row to fill one from: a
	// mandate is the creditor's bank's, so the creditor's agent is whichever bank
	// this listener is bound to. See payment.Mandate.DebtorAgent.
	DebtorAgent string      `json:"debtorAgent,omitempty"`
	Creditor    partyRefDTO `json:"creditor"`
	Asset       string      `json:"asset"`
	MaxAmount   int64       `json:"maxAmount"`
	Status      string      `json:"status"`
	CreatedAt   time.Time   `json:"createdAt"`
}

// toMandateDTO renders a mandate, including the asset its MaxAmount is
// denominated in. A mandate names no scheme — CreateMandate takes none, and the
// mandate can later be presented to any pull scheme that accepts it — so unlike
// paymentDTO there is no scheme to resolve an asset from.
//
// It comes off the ROW. Resolving it would read the debtor's bank's deposit
// register — see payment.Mandate.Asset.
func toMandateDTO(m payment.Mandate) mandateDTO {
	return mandateDTO{
		ID:          string(m.ID),
		Debtor:      toPartyRefDTO(m.Debtor),
		DebtorAgent: string(m.DebtorAgent),
		Creditor:    toPartyRefDTO(m.Creditor),
		Asset:       string(m.Asset),
		MaxAmount:   int64(m.MaxAmount),
		Status:      m.Status.String(),
		CreatedAt:   m.CreatedAt,
	}
}

type clearingCycleDTO struct {
	ID           string           `json:"id"`
	Scheme       string           `json:"scheme"`
	Asset        string           `json:"asset"`
	Status       string           `json:"status"`
	PaymentIDs   []string         `json:"paymentIds"`
	NetPositions map[string]int64 `json:"netPositions,omitempty"`
	OpenedAt     time.Time        `json:"openedAt"`
	ClosedAt     time.Time        `json:"closedAt,omitempty"`

	// There is no settlementId, and a client is being told something true by its
	// absence: the settlement's id belongs to the SETTLEMENT AGENT and the clearing
	// house this DTO is rendered from cannot learn it. See payment.ClearingCycle.
	// Status is what says a cut-off settled, and GET /settlements is where the
	// settlements are.
}

// toClearingCycleDTO renders a cycle, including the asset it clears in. A
// cycle names a scheme (unlike a mandate), so its asset is resolved the same
// way toPaymentDTO resolves a payment's: by looking the scheme up in the
// network's registered set.
func toClearingCycleDTO(c payment.ClearingCycle, schemes []payment.Scheme) clearingCycleDTO {
	ids := make([]string, len(c.PaymentIDs))
	for i, id := range c.PaymentIDs {
		ids[i] = string(id)
	}
	return clearingCycleDTO{
		ID:           string(c.ID),
		Scheme:       string(c.Scheme),
		Asset:        schemeAsset(c.Scheme, schemes),
		Status:       c.Status.String(),
		PaymentIDs:   ids,
		NetPositions: positionsToMap(c.NetPositions),
		OpenedAt:     c.OpenedAt,
		ClosedAt:     c.ClosedAt,
	}
}

// schemeAsset resolves a scheme ID's asset by looking it up in the network's
// registered set — the same resolution toPaymentDTO inlines for a payment's
// asset, factored out here so toClearingCycleDTO (and, transitively, a
// settlement via its cycle) can share it.
func schemeAsset(id payment.SchemeID, schemes []payment.Scheme) string {
	for _, sc := range schemes {
		if sc.ID() == id {
			return string(sc.Asset())
		}
	}
	return ""
}

type settlementDTO struct {
	ID           string           `json:"id"`
	CycleID      string           `json:"cycleId"`
	Asset        string           `json:"asset"`
	NetPositions map[string]int64 `json:"netPositions"`
	SettlementTx string           `json:"settlementTx"`
	ValueDate    time.Time        `json:"valueDate"`
	SettledAt    time.Time        `json:"settledAt"`
}

// toSettlementDTO renders a settlement, including the asset it settles.
//
// The asset comes off the ROW. Deriving it as settlement -> its cycle -> the
// cycle's scheme crosses an institution boundary the settlement agent cannot
// cross — see payment.Settlement.Asset.
func toSettlementDTO(s payment.Settlement) settlementDTO {
	return settlementDTO{
		ID:           string(s.ID),
		CycleID:      string(s.CycleID),
		Asset:        string(s.Asset),
		NetPositions: positionsToMap(s.NetPositions),
		SettlementTx: string(s.SettlementTx),
		ValueDate:    s.ValueDate,
		SettledAt:    s.SettledAt,
	}
}

func positionsToMap(in map[iso20022.BIC]ledger.Amount) map[string]int64 {
	if in == nil {
		return nil
	}
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[string(k)] = int64(v)
	}
	return out
}

type schemeDTO struct {
	ID              string `json:"id"`
	Asset           string `json:"asset"`
	Direction       string `json:"direction"`
	SettlementModel string `json:"settlementModel"`
	RequiresMandate bool   `json:"requiresMandate"`
	AllowsReturn    bool   `json:"allowsReturn"`
	SettlementDelay string `json:"settlementDelay"`
}

func toSchemeDTO(s payment.Scheme) schemeDTO {
	return schemeDTO{
		ID:              string(s.ID()),
		Asset:           string(s.Asset()),
		Direction:       s.Direction().String(),
		SettlementModel: s.SettlementModel().String(),
		RequiresMandate: s.RequiresMandate(),
		AllowsReturn:    s.AllowsReturn(),
		SettlementDelay: s.SettlementDelay().String(),
	}
}

// reserveDTO is one bank's reserve at the central bank, in one asset. A bank
// holds one reserve account per asset, so a reserve is only meaningful once
// the asset is named alongside it.
//
// The bank is named by its BIC in a field called `agent`, and it was a
// participant id in a field called `participant`. Both halves of that follow from
// the same fact: the settlement agent's register is keyed by address and holds no
// participant ids, because an id the network allocates is not something a message
// ever tells anybody (payment.SettlementMember). The name matches
// directoryEntryDTO's, which changed for the same reason.
type reserveDTO struct {
	Agent   string `json:"agent"`
	Asset   string `json:"asset"`
	Reserve int64  `json:"reserve"`
}

type createMandateRequest struct {
	Debtor partyRefDTO `json:"debtor"`
	// DebtorAgent is required: it is the address the collection is sent to, and
	// the creditor's bank has no way to derive it — the debtor's account is in
	// the debtor bank's own register. It is the mandate's version of
	// initiatePaymentRequest's counterparty agent, and it arrived for the same
	// reason. See payment.CreateMandateTx.
	DebtorAgent string      `json:"debtorAgent"`
	Creditor    partyRefDTO `json:"creditor"`
	MaxAmount   int64       `json:"maxAmount"`
}

type initiatePaymentRequest struct {
	Scheme      string            `json:"scheme"`
	Debtor      partyRefDTO       `json:"debtor"`
	Creditor    partyRefDTO       `json:"creditor"`
	Amount      int64             `json:"amount"`
	MandateID   string            `json:"mandateId"`
	EndToEndID  string            `json:"endToEndId"`
	Description string            `json:"description"`
	Metadata    map[string]string `json:"metadata"`

	// DebtorName and CreditorName are the names on the two accounts, and
	// DebtorAgent and CreditorAgent the BICs of the two banks. Only the
	// COUNTERPARTY's pair is required — the creditor's on a push, the debtor's on
	// a pull — because submission looks neither up: the account is at another
	// bank, and nothing on the path that builds a payment reads another bank's
	// register. See payment.ErrCounterpartyNotNamed and
	// payment.ErrCounterpartyAgentNotNamed.
	//
	// # The agent fields have been here, then not, and are here again
	//
	// The agent goes on the wire as CdtrAgt/DbtrAgt and the clearing house routes
	// on it, so a payer who types the wrong BIC chooses which bank receives the
	// payment. It is asserted anyway, because the row it could be derived from is
	// the counterparty's own and a bank holds only its own — see
	// payment.SubmitPaymentTx. What makes an asserted agent safe is that a bank
	// resolves an address in its own register only, so a misdirected payment is
	// refused with AC01 by the bank that was named rather than silently accepted
	// for another bank's customer.
	//
	// The submitting bank's OWN side is still ignored on both fields. A payer
	// does not rename themselves and does not reroute their own bank; both come
	// from the register and the row the listener is bound to.
	DebtorName    string `json:"debtorName,omitempty"`
	CreditorName  string `json:"creditorName,omitempty"`
	DebtorAgent   string `json:"debtorAgent,omitempty"`
	CreditorAgent string `json:"creditorAgent,omitempty"`
}

func (req initiatePaymentRequest) toDomain() payment.InitiatePaymentRequest {
	return payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeID(req.Scheme),
		Debtor:      req.Debtor.toDomain(),
		Creditor:    req.Creditor.toDomain(),
		Amount:      ledger.Amount(req.Amount),
		MandateID:   payment.MandateID(req.MandateID),
		EndToEndID:  req.EndToEndID,
		Description: req.Description,
		Metadata:    req.Metadata,
		// Both sides are passed through. Which one is the counterparty is the
		// scheme's direction to decide, and SubmitPaymentTx overwrites the
		// submitting bank's own side from its own register either way — so
		// forwarding both is correct and forwarding only the one this layer
		// guessed at would be a second place that has to know the direction.
		DebtorDetails:   payment.PartyDetails{Agent: iso20022.BIC(req.DebtorAgent), Name: req.DebtorName},
		CreditorDetails: payment.PartyDetails{Agent: iso20022.BIC(req.CreditorAgent), Name: req.CreditorName},
	}
}

type openCycleRequest struct {
	Scheme string `json:"scheme"`
}

// lodgementDTO is what POST /lodgements answers: the instruction that went out,
// and not a balance.
//
// It carries no balance BECAUSE the reserve credit has not happened yet — it is
// the central bank's to make, on a message still in flight. A DTO with a reserve
// figure on it would be answering a question this handler cannot answer, and the
// figure would be the bank's own mirror rather than the reserve itself. See
// handleLodgeReserves on the 202.
//
// Ref is what a caller can correlate with: the message identifier the camt.025
// quotes back. Nothing in the store is keyed by it — this system holds no
// lodgement row — so it is useful for reading the log rather than for a follow-up
// request, and saying so here is better than implying a GET that does not exist.
type lodgementDTO struct {
	Ref    string `json:"ref"`
	Asset  string `json:"asset"`
	Amount int64  `json:"amount"`
	// Account is the reserve account the credit was asked for, as this bank knows
	// it: the number it learned from its own admission acknowledgement.
	Account string `json:"account"`
	// Agent is the central bank asked. It is on the DTO because a bank's operator
	// reading this back should see WHO was asked, which is the one party to the
	// conversation the request did not name.
	Agent string `json:"agent"`
}

func toLodgementDTO(in payment.LodgementInstruction) lodgementDTO {
	return lodgementDTO{
		Ref:     in.Ref,
		Asset:   string(in.Asset),
		Amount:  int64(in.Amount),
		Account: string(in.Account),
		Agent:   string(in.Agent),
	}
}

// lodgementRequest is a bank asking its central bank to move vault cash onto the
// bank's reserve account.
//
// It names an ASSET where fundRequest above names an account, and the contrast is
// the whole difference between the two operations. A deposit is about one
// customer's account, so the asset follows from it and there is nothing for a
// caller to choose. A lodgement is about the BANK: it moves the bank's own cash,
// of which there is one pot per asset, and nothing else in the request says which.
//
// There is deliberately no default. A bank founded in dollars would have a euro
// lodgement invented for it by one, and the refusal it would get
// (ErrParticipantAssetNotFound) would name an asset the caller never mentioned.
//
// No description either, and that is not an omission. A deposit's description is
// the teller's note about a customer's transaction; a lodgement's counterparty is
// a central bank, which is told what this is by the message definition, and the
// posting's own description is written by the domain. See payment.LodgeReservesTx.
type lodgementRequest struct {
	Asset  string `json:"asset"`
	Amount int64  `json:"amount"`
}
