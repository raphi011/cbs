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

// ParticipantAccountsDTO is one asset's worth of a bank's internal accounts.
type ParticipantAccountsDTO struct {
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
	VaultCash string `json:"vaultCash"`
	// Settlement is this bank's account in the SETTLEMENT AGENT's book, and the
	// only account here that is not in the bank's own. It is the account holder's
	// note of a number another institution allocated it.
	//
	// It is also what says how far through provisioning a bank got, and there is
	// no separate field saying so. Empty means no settlement agent has opened an
	// account for this bank in this asset: it has a book, a chart of accounts and
	// a product, it opens no customer account at all because no registry has
	// allocated it a bank code, it cannot put its vault cash on reserve, and a
	// payment to it dies at the payer's bank because no roster carries its
	// address. A whole state rather than a broken one, and what a deployment
	// whose provisioning stopped part-way leaves behind.
	Settlement string `json:"settlement"`
}

// ParticipantDTO renders the internal accounts as a list rather than four flat
// fields, because a bank holds one set per asset it operates in.
//
// A list rather than an object keyed by code so the order is the API's to
// choose: Go randomises map iteration, and a wire format that reorders itself
// between two identical requests is not one a client can diff.
type ParticipantDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// BIC is this bank's ISO 9362 business identifier code — what a
	// counterparty addresses it by, and what the mesh routes on.
	BIC string `json:"bic"`
	// ProductID is the bank's default deposit product, created with its chart
	// of accounts at onboarding. Every deposit account is opened FROM a
	// product, so a client that has just created a bank needs to be told which
	// one it may open accounts from before it can open any.
	ProductID         string                   `json:"productId"`
	CustomerSubledger string                   `json:"customerSubledger"`
	Assets            []ParticipantAccountsDTO `json:"assets"`
}

func ToParticipantDTO(p *payment.Bank) ParticipantDTO {
	assets := make([]ParticipantAccountsDTO, 0, len(p.Assets))
	for code, accts := range p.Assets {
		assets = append(assets, ParticipantAccountsDTO{
			Asset:      string(code),
			Suspense:   string(accts.Suspense),
			Reserve:    string(accts.Reserve),
			Unclaimed:  string(accts.Unclaimed),
			VaultCash:  string(accts.VaultCash),
			Settlement: string(accts.Settlement),
		})
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Asset < assets[j].Asset })

	return ParticipantDTO{
		ID:                string(p.ID),
		Name:              p.Name,
		BIC:               string(p.BIC),
		ProductID:         string(p.ProductID),
		CustomerSubledger: string(p.CustomerSubledger),
		Assets:            assets,
	}
}

type IdentifierDTO struct {
	Scheme string `json:"scheme"`
	Value  string `json:"value"`
}

// ToIdentifierDTOs renders an account's identifiers as a non-nil empty slice
// rather than nil, so the JSON key is "[]" and not "null" — a web client that
// renders a list of addresses should not have to distinguish "no identifiers"
// from "the field was missing".
func ToIdentifierDTOs(idents []deposit.Identifier) []IdentifierDTO {
	out := make([]IdentifierDTO, 0, len(idents))
	for _, i := range idents {
		out = append(out, IdentifierDTO{Scheme: string(i.Scheme), Value: i.Value})
	}
	return out
}

// PartyRefDTO is one side of a payment or a mandate: the account, and the
// address quoted to reach it.
//
// It carried a `participant` naming the bank, and does not, for the reason
// payment.PartyRef does not: a bank's id is its BIC now, so that field and the
// agent beside it were one value spelled twice. Which bank a party is at travels
// as `debtorAgent`/`creditorAgent` on the enclosing payment or mandate — the same
// names an instruction already used on the way IN, so the two directions finally
// agree.
type PartyRefDTO struct {
	Account string `json:"account"`
	// Identifier is the external address quoted for this party — an IBAN today.
	// Absent for a party addressed only by its ids.
	Identifier *IdentifierDTO `json:"identifier,omitempty"`
}

func ToPartyRefDTO(r payment.PartyRef) PartyRefDTO {
	out := PartyRefDTO{Account: string(r.Account)}
	if r.Identifier != (deposit.Identifier{}) {
		out.Identifier = &IdentifierDTO{Scheme: string(r.Identifier.Scheme), Value: r.Identifier.Value}
	}
	return out
}

// ToDomain converts the wire shape, canonicalising an address a PERSON typed.
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
func (r PartyRefDTO) ToDomain() payment.PartyRef {
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

type PaymentDTO struct {
	ID       string      `json:"id"`
	Scheme   string      `json:"scheme"`
	Asset    string      `json:"asset"`
	Debtor   PartyRefDTO `json:"debtor"`
	Creditor PartyRefDTO `json:"creditor"`
	// DebtorAgent and CreditorAgent are the BICs of the two banks, and they are
	// what says where each party banks now that a PartyRefDTO does not.
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

// ToPaymentDTO renders a payment, including the asset it settles in. A
// payment names a scheme, not an asset — the asset is the scheme's, fixed at
// registration — so rendering it means resolving the scheme. schemes is the
// network's registered set (payment.Network.ListSchemes), searched by ID
// rather than threaded through payment.Payment itself, which stays a pure
// domain record.
func ToPaymentDTO(p payment.Payment, schemes []payment.Scheme) PaymentDTO {
	var asset string
	for _, sc := range schemes {
		if sc.ID() == p.Scheme {
			asset = string(sc.Asset())
			break
		}
	}
	return PaymentDTO{
		ID:            string(p.ID),
		Scheme:        string(p.Scheme),
		Asset:         asset,
		Debtor:        ToPartyRefDTO(p.Debtor),
		Creditor:      ToPartyRefDTO(p.Creditor),
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

type MandateDTO struct {
	ID     string      `json:"id"`
	Debtor PartyRefDTO `json:"debtor"`
	// DebtorAgent is the bank a collection under this mandate is sent to. There
	// is no creditorAgent beside it and there is no row to fill one from: a
	// mandate is the creditor's bank's, so the creditor's agent is whichever bank
	// this listener is bound to. See payment.Mandate.DebtorAgent.
	DebtorAgent string      `json:"debtorAgent,omitempty"`
	Creditor    PartyRefDTO `json:"creditor"`
	Asset       string      `json:"asset"`
	MaxAmount   int64       `json:"maxAmount"`
	Status      string      `json:"status"`
	CreatedAt   time.Time   `json:"createdAt"`
}

// ToMandateDTO renders a mandate, including the asset its MaxAmount is
// denominated in. A mandate names no scheme — CreateMandate takes none, and the
// mandate can later be presented to any pull scheme that accepts it — so unlike
// PaymentDTO there is no scheme to resolve an asset from.
//
// It comes off the ROW. Resolving it would read the debtor's bank's deposit
// register — see payment.Mandate.Asset.
func ToMandateDTO(m payment.Mandate) MandateDTO {
	return MandateDTO{
		ID:          string(m.ID),
		Debtor:      ToPartyRefDTO(m.Debtor),
		DebtorAgent: string(m.DebtorAgent),
		Creditor:    ToPartyRefDTO(m.Creditor),
		Asset:       string(m.Asset),
		MaxAmount:   int64(m.MaxAmount),
		Status:      m.Status.String(),
		CreatedAt:   m.CreatedAt,
	}
}

type ClearingCycleDTO struct {
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

// ToClearingCycleDTO renders a cycle, including the asset it clears in. A
// cycle names a scheme (unlike a mandate), so its asset is resolved the same
// way ToPaymentDTO resolves a payment's: by looking the scheme up in the
// network's registered set.
func ToClearingCycleDTO(c payment.ClearingCycle, schemes []payment.Scheme) ClearingCycleDTO {
	ids := make([]string, len(c.PaymentIDs))
	for i, id := range c.PaymentIDs {
		ids[i] = string(id)
	}
	return ClearingCycleDTO{
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
// registered set — the same resolution ToPaymentDTO inlines for a payment's
// asset, factored out here so ToClearingCycleDTO (and, transitively, a
// settlement via its cycle) can share it.
func schemeAsset(id payment.SchemeID, schemes []payment.Scheme) string {
	for _, sc := range schemes {
		if sc.ID() == id {
			return string(sc.Asset())
		}
	}
	return ""
}

type SettlementDTO struct {
	ID           string           `json:"id"`
	CycleID      string           `json:"cycleId"`
	Asset        string           `json:"asset"`
	NetPositions map[string]int64 `json:"netPositions"`
	SettlementTx string           `json:"settlementTx"`
	ValueDate    time.Time        `json:"valueDate"`
	SettledAt    time.Time        `json:"settledAt"`
}

// ToSettlementDTO renders a settlement, including the asset it settles.
//
// The asset comes off the ROW. Deriving it as settlement -> its cycle -> the
// cycle's scheme crosses an institution boundary the settlement agent cannot
// cross — see payment.Settlement.Asset.
func ToSettlementDTO(s payment.Settlement) SettlementDTO {
	return SettlementDTO{
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

type SchemeDTO struct {
	ID              string `json:"id"`
	Asset           string `json:"asset"`
	Direction       string `json:"direction"`
	SettlementModel string `json:"settlementModel"`
	RequiresMandate bool   `json:"requiresMandate"`
	AllowsReturn    bool   `json:"allowsReturn"`
	SettlementDelay string `json:"settlementDelay"`
}

func ToSchemeDTO(s payment.Scheme) SchemeDTO {
	return SchemeDTO{
		ID:              string(s.ID()),
		Asset:           string(s.Asset()),
		Direction:       s.Direction().String(),
		SettlementModel: s.SettlementModel().String(),
		RequiresMandate: s.RequiresMandate(),
		AllowsReturn:    s.AllowsReturn(),
		SettlementDelay: s.SettlementDelay().String(),
	}
}

// ReserveDTO is one bank's reserve at the central bank, in one asset. A bank
// holds one reserve account per asset, so a reserve is only meaningful once
// the asset is named alongside it.
//
// The bank is named by its BIC in a field called `agent`, and it was a
// participant id in a field called `participant`. Both halves of that follow from
// the same fact: the settlement agent's register is keyed by address and holds no
// participant ids, because an id the network allocates is not something a message
// ever tells anybody (payment.SettlementMember). The name matches
// directoryEntryDTO's, which changed for the same reason.
type ReserveDTO struct {
	Agent   string `json:"agent"`
	Asset   string `json:"asset"`
	Reserve int64  `json:"reserve"`
}

// CreateMandateRequest names the two accounts and the ceiling, and names no bank.
//
// The address the collection is sent to is DERIVED, from the debtor's own IBAN
// through this bank's routing directory, and it is derived once — a mandate
// authorises debits from an account at the bank the debtor signed up against, so
// the answer is fixed at signature rather than re-asked at every collection. See
// payment.CreateMandateTx.
// SubmittedPaymentDTO is what a bank answers a customer's instruction with: an
// identifier to ask about, not an outcome.
//
// Named for submission and not for acceptance, because since the split the two
// are different things and this is the first: the 202 is HTTP's "I have taken
// this in", and the payment it names is Initiated, in no cycle, and not yet
// seen by the counterparty. Calling the type accepted — as it was while
// submission produced an Accepted payment — now reads as a claim the response
// does not make.
//
// The clearing house's return route answers with it too, for the same reason and
// with less choice: a return's outcome is decided four hops away and there is no
// intermediate resource to describe at all.
type SubmittedPaymentDTO struct {
	PaymentID string `json:"paymentId"`
}

type CreateMandateRequest struct {
	Debtor    PartyRefDTO `json:"debtor"`
	Creditor  PartyRefDTO `json:"creditor"`
	MaxAmount int64       `json:"maxAmount"`
}

type InitiatePaymentRequest struct {
	Scheme      string            `json:"scheme"`
	Debtor      PartyRefDTO       `json:"debtor"`
	Creditor    PartyRefDTO       `json:"creditor"`
	Amount      int64             `json:"amount"`
	MandateID   string            `json:"mandateId"`
	EndToEndID  string            `json:"endToEndId"`
	Description string            `json:"description"`
	Metadata    map[string]string `json:"metadata"`

	// DebtorName and CreditorName are the names on the two accounts, and only the
	// COUNTERPARTY's is required — the creditor's on a push, the debtor's on a
	// pull. Nothing looks it up: the account is at another bank, so the name a
	// payer types is the only one there is. See payment.ErrCounterpartyNotNamed.
	//
	// # There is no agent field, and its absence is what makes this IBAN-only
	//
	// The BIC goes on the wire as CdtrAgt/DbtrAgt and the clearing house routes on
	// it, so a payer able to type one is a payer able to choose which bank
	// receives their money. It is DERIVED instead, from the counterparty's own
	// address through this bank's copy of the scheme's routing directory — see
	// payment.SubmitPaymentTx — so an instruction carries an address and a name
	// and there is no field left to put a wrong bank in.
	//
	// What that costs is a new refusal: an address whose bank code is not in this
	// bank's copy is payment.ErrBankCodeUnknown, and the remedy is a refresh or
	// giving up, because a subscriber cannot tell those apart. What it buys is
	// that the two cannot disagree.
	//
	// The submitting bank's OWN name is still ignored. A payer does not rename
	// themselves; it comes from the register.
	DebtorName   string `json:"debtorName,omitempty"`
	CreditorName string `json:"creditorName,omitempty"`
}

func (req InitiatePaymentRequest) ToDomain() payment.InitiatePaymentRequest {
	return payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeID(req.Scheme),
		Debtor:      req.Debtor.ToDomain(),
		Creditor:    req.Creditor.ToDomain(),
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
		//
		// Neither carries an Agent, because this request has no field for one and
		// SubmitPaymentTx derives it. A zero BIC here is the honest value: it is
		// what the instruction said about routing, which is nothing.
		DebtorDetails:   payment.PartyDetails{Name: req.DebtorName},
		CreditorDetails: payment.PartyDetails{Name: req.CreditorName},
	}
}

type OpenCycleRequest struct {
	Scheme string `json:"scheme"`
}

// LodgementDTO is what POST /lodgements answers: the instruction that went out,
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
type LodgementDTO struct {
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

func ToLodgementDTO(in payment.LodgementInstruction) LodgementDTO {
	return LodgementDTO{
		Ref:     in.Ref,
		Asset:   string(in.Asset),
		Amount:  int64(in.Amount),
		Account: string(in.Account),
		Agent:   string(in.Agent),
	}
}

// LodgementRequest is a bank asking its central bank to move vault cash onto the
// bank's reserve account.
//
// It names an ASSET where FundRequest above names an account, and the contrast is
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
type LodgementRequest struct {
	Asset  string `json:"asset"`
	Amount int64  `json:"amount"`
}
