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
	// Unclaimed is where a credit goes when the payee's account will not take it.
	Unclaimed string `json:"unclaimed"`
	// VaultCash is the cash the bank is holding, and the only account here that is
	// nobody else's promise.
	VaultCash string `json:"vaultCash"`
	// Settlement is this bank's account in the SETTLEMENT AGENT's book, and the
	// only account here that is not in the bank's own. It is the account holder's
	// note of a number another institution allocated it.
	Settlement string `json:"settlement"`
}

// ParticipantDTO renders the internal accounts as a list rather than four flat
// fields, because a bank holds one set per asset it operates in.
type ParticipantDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// BIC is this bank's ISO 9362 business identifier code — what a
	// counterparty addresses it by, and what the clearing house routes on.
	BIC string `json:"bic"`
	// ProductID is the bank's default deposit product, created with its chart of
	// accounts at onboarding.
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

// ToPaymentDTO renders a payment, including the asset it settles in.
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
	// DebtorAgent is the bank a collection under this mandate is sent to.
	DebtorAgent string      `json:"debtorAgent,omitempty"`
	Creditor    PartyRefDTO `json:"creditor"`
	Asset       string      `json:"asset"`
	MaxAmount   int64       `json:"maxAmount"`
	Status      string      `json:"status"`
	CreatedAt   time.Time   `json:"createdAt"`
}

// ToMandateDTO renders a mandate, including the asset its MaxAmount is
// denominated in.
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
	// absence: the settlement's id belongs to the SETTLEMENT AGENT and the
	// clearing house this DTO is rendered from cannot learn it.
}

// ToClearingCycleDTO renders a cycle, including the asset it clears in.
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
// holds one reserve account per asset, so a reserve is only meaningful once the
// asset is named alongside it.
type ReserveDTO struct {
	Agent   string `json:"agent"`
	Asset   string `json:"asset"`
	Reserve int64  `json:"reserve"`
}

// CreateMandateRequest names the two accounts and the ceiling, and names no
// bank.
type SubmittedPaymentDTO struct {
	PaymentID string `json:"paymentId"`
}

// CutoffDTO is what a bank answers a cut-off with: the order id of every file
// it uploaded, and nothing about what is in them.
type CutoffDTO struct {
	OrderIDs []string `json:"orderIds"`
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
	// pull.
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
		// Both sides are passed through.
		DebtorDetails:   payment.PartyDetails{Name: req.DebtorName},
		CreditorDetails: payment.PartyDetails{Name: req.CreditorName},
	}
}

type OpenCycleRequest struct {
	Scheme string `json:"scheme"`
}

// LodgementDTO is what POST /lodgements answers: the instruction that went out,
// and not a balance.
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

// LodgementRequest is a bank asking its central bank to move vault cash onto
// the bank's reserve account.
type LodgementRequest struct {
	Asset  string `json:"asset"`
	Amount int64  `json:"amount"`
}
