package api

import (
	"sort"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// Wire format for the interbank payment layer: participants, party refs,
// payments, mandates, clearing cycles, settlements, schemes, and requests.

// participantAccountsDTO is one asset's worth of a bank's internal accounts.
type participantAccountsDTO struct {
	Asset      string `json:"asset"`
	Suspense   string `json:"suspense"`
	Reserve    string `json:"reserve"`
	Settlement string `json:"settlement"`
}

// participantDTO renders the internal accounts as a list rather than three
// flat fields, because a bank holds one set per asset it operates in.
//
// A list rather than an object keyed by code so the order is the API's to
// choose: Go randomises map iteration, and a wire format that reorders itself
// between two identical requests is not one a client can diff.
type participantDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// ProductID is the bank's default deposit product, created with its chart
	// of accounts at onboarding. Every deposit account is opened FROM a
	// product, so a client that has just created a bank needs to be told which
	// one it may open accounts from before it can open any.
	ProductID         string                   `json:"productId"`
	CustomerSubledger string                   `json:"customerSubledger"`
	Assets            []participantAccountsDTO `json:"assets"`
}

func toParticipantDTO(p *payment.Participant) participantDTO {
	assets := make([]participantAccountsDTO, 0, len(p.Assets))
	for code, accts := range p.Assets {
		assets = append(assets, participantAccountsDTO{
			Asset:      string(code),
			Suspense:   string(accts.Suspense),
			Reserve:    string(accts.Reserve),
			Settlement: string(accts.Settlement),
		})
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Asset < assets[j].Asset })

	return participantDTO{
		ID:                string(p.ID),
		Name:              p.Name,
		ProductID:         string(p.ProductID),
		CustomerSubledger: string(p.CustomerSubledger),
		Assets:            assets,
	}
}

// createParticipantRequest carries the participant's name and, optionally,
// the set of assets it joins with. An absent or empty Assets defaults to
// ["EUR"] — AddParticipant applies that default itself, so an empty slice is
// forwarded unchanged rather than special-cased here.
//
// This is the one deliberate default anywhere in the asset dimension: it
// preserves existing behaviour for callers that do not care which assets a
// bank joins with. It is not a default for the asset of any account — every
// account, deposit account and transaction still names its asset explicitly.
type createParticipantRequest struct {
	Name   string   `json:"name"`
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

type partyRefDTO struct {
	Participant string `json:"participant"`
	Account     string `json:"account"`
	// Identifier is the external address quoted for this party — an IBAN today.
	// Absent for a party addressed only by its ids.
	Identifier *identifierDTO `json:"identifier,omitempty"`
}

func toPartyRefDTO(r payment.PartyRef) partyRefDTO {
	out := partyRefDTO{Participant: string(r.Participant), Account: string(r.Account)}
	if r.Identifier != (deposit.Identifier{}) {
		out.Identifier = &identifierDTO{Scheme: string(r.Identifier.Scheme), Value: r.Identifier.Value}
	}
	return out
}

func (r partyRefDTO) toDomain() payment.PartyRef {
	out := payment.PartyRef{
		Participant: payment.ParticipantID(r.Participant),
		Account:     deposit.AccountID(r.Account),
	}
	if r.Identifier != nil {
		out.Identifier = deposit.Identifier{
			Scheme: deposit.IdentifierScheme(r.Identifier.Scheme),
			Value:  r.Identifier.Value,
		}
	}
	return out
}

type paymentDTO struct {
	ID            string            `json:"id"`
	Scheme        string            `json:"scheme"`
	Asset         string            `json:"asset"`
	Debtor        partyRefDTO       `json:"debtor"`
	Creditor      partyRefDTO       `json:"creditor"`
	Amount        int64             `json:"amount"`
	MandateID     string            `json:"mandateId,omitempty"`
	EndToEndID    string            `json:"endToEndId,omitempty"`
	Status        string            `json:"status"`
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
		Amount:        int64(p.Amount),
		MandateID:     string(p.MandateID),
		EndToEndID:    p.EndToEndID,
		Status:        p.Status.String(),
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
	ID        string      `json:"id"`
	Debtor    partyRefDTO `json:"debtor"`
	Creditor  partyRefDTO `json:"creditor"`
	Asset     string      `json:"asset"`
	MaxAmount int64       `json:"maxAmount"`
	Status    string      `json:"status"`
	CreatedAt time.Time   `json:"createdAt"`
}

// toMandateDTO renders a mandate, including the asset its MaxAmount is
// denominated in. A mandate names no scheme — CreateMandate takes none, and
// the mandate can later be presented to any pull scheme that accepts it — so
// unlike paymentDTO there is no scheme to resolve an asset from. What is
// fixed at creation is the debtor account being authorized to pull from, and
// its asset is what a pulled amount is denominated in; callers resolve it via
// (*Server).mandateAsset and pass it in here rather than this function doing
// its own I/O.
func toMandateDTO(m payment.Mandate, asset string) mandateDTO {
	return mandateDTO{
		ID:        string(m.ID),
		Debtor:    toPartyRefDTO(m.Debtor),
		Creditor:  toPartyRefDTO(m.Creditor),
		Asset:     asset,
		MaxAmount: int64(m.MaxAmount),
		Status:    m.Status.String(),
		CreatedAt: m.CreatedAt,
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
	SettlementID string           `json:"settlementId,omitempty"`
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
		SettlementID: string(c.SettlementID),
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

// toSettlementDTO renders a settlement, including the asset it settles. A
// settlement carries no scheme itself, only a CycleID — its asset is one hop
// further than a cycle's: callers resolve it via (*Server).settlementAsset
// (settlement -> its cycle -> the cycle's scheme) and pass it in here.
func toSettlementDTO(s payment.Settlement, asset string) settlementDTO {
	return settlementDTO{
		ID:           string(s.ID),
		CycleID:      string(s.CycleID),
		Asset:        asset,
		NetPositions: positionsToMap(s.NetPositions),
		SettlementTx: string(s.SettlementTx),
		ValueDate:    s.ValueDate,
		SettledAt:    s.SettledAt,
	}
}

func positionsToMap(in map[payment.ParticipantID]ledger.Amount) map[string]int64 {
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
type reserveDTO struct {
	Participant string `json:"participant"`
	Asset       string `json:"asset"`
	Reserve     int64  `json:"reserve"`
}

type createMandateRequest struct {
	Debtor    partyRefDTO `json:"debtor"`
	Creditor  partyRefDTO `json:"creditor"`
	MaxAmount int64       `json:"maxAmount"`
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
	}
}

type openCycleRequest struct {
	Scheme string `json:"scheme"`
}

// settleCycleRequest names the cycle to settle.
//
// The cycle is the input; the resource created is a settlement, which is why
// this is the body of POST /settlements rather than a path parameter on an
// action against a cycle — and why the route is the central bank's, whose book
// the reserves actually move in.
type settleCycleRequest struct {
	CycleID string `json:"cycleId"`
}
