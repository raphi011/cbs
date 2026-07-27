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
	ID                string                   `json:"id"`
	Name              string                   `json:"name"`
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
		CustomerSubledger: string(p.CustomerSubledger),
		Assets:            assets,
	}
}

type partyRefDTO struct {
	Participant string `json:"participant"`
	Account     string `json:"account"`
	IBAN        string `json:"iban,omitempty"`
}

func toPartyRefDTO(r payment.PartyRef) partyRefDTO {
	return partyRefDTO{Participant: string(r.Participant), Account: string(r.Account), IBAN: r.IBAN}
}

func (r partyRefDTO) toDomain() payment.PartyRef {
	return payment.PartyRef{
		Participant: payment.ParticipantID(r.Participant),
		Account:     deposit.AccountID(r.Account),
		IBAN:        r.IBAN,
	}
}

type paymentDTO struct {
	ID            string            `json:"id"`
	Scheme        string            `json:"scheme"`
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

func toPaymentDTO(p payment.Payment) paymentDTO {
	return paymentDTO{
		ID:            string(p.ID),
		Scheme:        string(p.Scheme),
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
	MaxAmount int64       `json:"maxAmount"`
	Status    string      `json:"status"`
	CreatedAt time.Time   `json:"createdAt"`
}

func toMandateDTO(m payment.Mandate) mandateDTO {
	return mandateDTO{
		ID:        string(m.ID),
		Debtor:    toPartyRefDTO(m.Debtor),
		Creditor:  toPartyRefDTO(m.Creditor),
		MaxAmount: int64(m.MaxAmount),
		Status:    m.Status.String(),
		CreatedAt: m.CreatedAt,
	}
}

type clearingCycleDTO struct {
	ID           string           `json:"id"`
	Scheme       string           `json:"scheme"`
	Status       string           `json:"status"`
	PaymentIDs   []string         `json:"paymentIds"`
	NetPositions map[string]int64 `json:"netPositions,omitempty"`
	OpenedAt     time.Time        `json:"openedAt"`
	ClosedAt     time.Time        `json:"closedAt,omitempty"`
	SettlementID string           `json:"settlementId,omitempty"`
}

func toClearingCycleDTO(c payment.ClearingCycle) clearingCycleDTO {
	ids := make([]string, len(c.PaymentIDs))
	for i, id := range c.PaymentIDs {
		ids[i] = string(id)
	}
	return clearingCycleDTO{
		ID:           string(c.ID),
		Scheme:       string(c.Scheme),
		Status:       c.Status.String(),
		PaymentIDs:   ids,
		NetPositions: positionsToMap(c.NetPositions),
		OpenedAt:     c.OpenedAt,
		ClosedAt:     c.ClosedAt,
		SettlementID: string(c.SettlementID),
	}
}

type settlementDTO struct {
	ID           string           `json:"id"`
	CycleID      string           `json:"cycleId"`
	NetPositions map[string]int64 `json:"netPositions"`
	SettlementTx string           `json:"settlementTx"`
	ValueDate    time.Time        `json:"valueDate"`
	SettledAt    time.Time        `json:"settledAt"`
}

func toSettlementDTO(s payment.Settlement) settlementDTO {
	return settlementDTO{
		ID:           string(s.ID),
		CycleID:      string(s.CycleID),
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
	Direction       string `json:"direction"`
	SettlementModel string `json:"settlementModel"`
	RequiresMandate bool   `json:"requiresMandate"`
	AllowsReturn    bool   `json:"allowsReturn"`
	SettlementDelay string `json:"settlementDelay"`
}

func toSchemeDTO(s payment.Scheme) schemeDTO {
	return schemeDTO{
		ID:              string(s.ID()),
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
