package api

import (
	"fmt"
	"time"

	"github.com/raphi011/ledger"
	"github.com/raphi011/ledger/deposit"
	"github.com/raphi011/ledger/payment"
)

// This file defines the JSON wire format for the API. The domain packages use
// integer enums (rendered here via their String() methods) and typed string
// IDs (rendered as plain strings), so explicit DTOs keep the wire format
// readable and decoupled from the internal Go types. Monetary amounts stay as
// integer minor units (never floats or strings).

// ---------------------------------------------------------------------------
// Ledger layer
// ---------------------------------------------------------------------------

type ledgerDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

func toLedgerDTO(l ledger.Ledger) ledgerDTO {
	return ledgerDTO{ID: string(l.ID), Name: l.Name, CreatedAt: l.CreatedAt}
}

type subledgerDTO struct {
	ID        string    `json:"id"`
	LedgerID  string    `json:"ledgerId"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

func toSubledgerDTO(sl ledger.Subledger) subledgerDTO {
	return subledgerDTO{ID: string(sl.ID), LedgerID: string(sl.LedgerID), Name: sl.Name, CreatedAt: sl.CreatedAt}
}

type accountDTO struct {
	ID          string    `json:"id"`
	SubledgerID string    `json:"subledgerId"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	CreatedAt   time.Time `json:"createdAt"`
}

func toAccountDTO(a ledger.Account) accountDTO {
	return accountDTO{
		ID:          string(a.ID),
		SubledgerID: string(a.SubledgerID),
		Name:        a.Name,
		Type:        a.Type.String(),
		CreatedAt:   a.CreatedAt,
	}
}

type entryDTO struct {
	ID        string `json:"id,omitempty"`
	AccountID string `json:"accountId"`
	Amount    int64  `json:"amount"`
	Direction string `json:"direction"`
}

type transactionDTO struct {
	ID             string            `json:"id"`
	IdempotencyKey string            `json:"idempotencyKey,omitempty"`
	Entries        []entryDTO        `json:"entries"`
	BookingDate    time.Time         `json:"bookingDate"`
	ValueDate      time.Time         `json:"valueDate"`
	Status         string            `json:"status"`
	Description    string            `json:"description,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	ReversalOf     string            `json:"reversalOf,omitempty"`
	CreatedAt      time.Time         `json:"createdAt"`
}

func toTransactionDTO(tx ledger.Transaction) transactionDTO {
	entries := make([]entryDTO, len(tx.Entries))
	for i, e := range tx.Entries {
		entries[i] = entryDTO{
			ID:        string(e.ID),
			AccountID: string(e.AccountID),
			Amount:    e.Amount,
			Direction: e.Direction.String(),
		}
	}
	return transactionDTO{
		ID:             string(tx.ID),
		IdempotencyKey: tx.IdempotencyKey,
		Entries:        entries,
		BookingDate:    tx.BookingDate,
		ValueDate:      tx.ValueDate,
		Status:         tx.Status.String(),
		Description:    tx.Description,
		Metadata:       tx.Metadata,
		ReversalOf:     string(tx.ReversalOf),
		CreatedAt:      tx.CreatedAt,
	}
}

type auditEventDTO struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	EntityID  string    `json:"entityId"`
}

func toLedgerAuditDTO(e ledger.AuditEvent) auditEventDTO {
	return auditEventDTO{ID: e.ID, Timestamp: e.Timestamp, Type: string(e.Type), EntityID: e.EntityID}
}

func toDepositAuditDTO(e deposit.AuditEvent) auditEventDTO {
	return auditEventDTO{ID: e.ID, Timestamp: e.Timestamp, Type: string(e.Type), EntityID: e.EntityID}
}

// ---------------------------------------------------------------------------
// Deposit layer
// ---------------------------------------------------------------------------

type depositAccountDTO struct {
	ID             string    `json:"id"`
	GLAccount      string    `json:"glAccount"`
	Name           string    `json:"name"`
	Status         string    `json:"status"`
	OverdraftLimit int64     `json:"overdraftLimit"`
	CreatedAt      time.Time `json:"createdAt"`
}

func toDepositAccountDTO(a deposit.Account) depositAccountDTO {
	return depositAccountDTO{
		ID:             string(a.ID),
		GLAccount:      string(a.GLAccount),
		Name:           a.Name,
		Status:         a.Status.String(),
		OverdraftLimit: a.OverdraftLimit,
		CreatedAt:      a.CreatedAt,
	}
}

type holdDTO struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"accountId"`
	Amount      int64     `json:"amount"`
	ExpiresAt   time.Time `json:"expiresAt,omitempty"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
}

func toHoldDTO(h deposit.Hold) holdDTO {
	return holdDTO{
		ID:          string(h.ID),
		AccountID:   string(h.AccountID),
		Amount:      h.Amount,
		ExpiresAt:   h.ExpiresAt,
		Description: h.Description,
		Status:      h.Status.String(),
		CreatedAt:   h.CreatedAt,
	}
}

type balanceDTO struct {
	Book      int64 `json:"book"`
	Holds     int64 `json:"holds"`
	Available int64 `json:"available"`
}

func toBalanceDTO(b deposit.Balance) balanceDTO {
	return balanceDTO{Book: b.Book, Holds: b.Holds, Available: b.Available}
}

type snapshotDTO struct {
	AccountID string     `json:"accountId"`
	Date      time.Time  `json:"date"`
	Balance   balanceDTO `json:"balance"`
	TakenAt   time.Time  `json:"takenAt"`
}

func toSnapshotDTO(s deposit.Snapshot) snapshotDTO {
	return snapshotDTO{AccountID: string(s.AccountID), Date: s.Date, Balance: toBalanceDTO(s.Balance), TakenAt: s.TakenAt}
}

// ---------------------------------------------------------------------------
// Payment layer
// ---------------------------------------------------------------------------

type participantDTO struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	CustomerSubledger string `json:"customerSubledger"`
	SuspenseAccount   string `json:"suspenseAccount"`
	ReserveAccount    string `json:"reserveAccount"`
	SettlementAccount string `json:"settlementAccount"`
}

func toParticipantDTO(p *payment.Participant) participantDTO {
	return participantDTO{
		ID:                string(p.ID),
		Name:              p.Name,
		CustomerSubledger: string(p.CustomerSubledger),
		SuspenseAccount:   string(p.SuspenseAccount),
		ReserveAccount:    string(p.ReserveAccount),
		SettlementAccount: string(p.SettlementAccount),
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
		Amount:        p.Amount,
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
		MaxAmount: m.MaxAmount,
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
		out[string(k)] = v
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

type reserveDTO struct {
	Participant string `json:"participant"`
	Reserve     int64  `json:"reserve"`
}

// ---------------------------------------------------------------------------
// Request DTOs
// ---------------------------------------------------------------------------

type nameRequest struct {
	Name string `json:"name"`
}

type createAccountRequest struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type postTransactionRequest struct {
	IdempotencyKey string            `json:"idempotencyKey"`
	Entries        []entryDTO        `json:"entries"`
	BookingDate    *time.Time        `json:"bookingDate"`
	ValueDate      *time.Time        `json:"valueDate"`
	Description    string            `json:"description"`
	Metadata       map[string]string `json:"metadata"`
}

// toDomain converts the request to a ledger.PostTransactionRequest, parsing
// each entry's direction. A bad direction string yields an error that the
// handler maps to 400.
func (req postTransactionRequest) toDomain() (ledger.PostTransactionRequest, error) {
	entries := make([]ledger.Entry, len(req.Entries))
	for i, e := range req.Entries {
		dir, err := directionFromString(e.Direction)
		if err != nil {
			return ledger.PostTransactionRequest{}, err
		}
		entries[i] = ledger.Entry{
			AccountID: ledger.AccountID(e.AccountID),
			Amount:    e.Amount,
			Direction: dir,
		}
	}
	out := ledger.PostTransactionRequest{
		IdempotencyKey: req.IdempotencyKey,
		Entries:        entries,
		Description:    req.Description,
		Metadata:       req.Metadata,
	}
	if req.BookingDate != nil {
		out.BookingDate = *req.BookingDate
	}
	if req.ValueDate != nil {
		out.ValueDate = *req.ValueDate
	}
	return out, nil
}

type descriptionRequest struct {
	Description string `json:"description"`
}

type reasonRequest struct {
	Reason string `json:"reason"`
}

type openDepositAccountRequest struct {
	Name           string `json:"name"`
	OverdraftLimit int64  `json:"overdraftLimit"`
}

type statusRequest struct {
	Action string `json:"action"`
}

type createHoldRequest struct {
	Amount      int64      `json:"amount"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	Description string     `json:"description"`
}

type captureHoldRequest struct {
	Counterparty string `json:"counterparty"`
	Amount       int64  `json:"amount"`
	Description  string `json:"description"`
}

type snapshotRequest struct {
	Date string `json:"date"`
}

type fundRequest struct {
	Account     string `json:"account"`
	Amount      int64  `json:"amount"`
	Description string `json:"description"`
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
		Amount:      req.Amount,
		MandateID:   payment.MandateID(req.MandateID),
		EndToEndID:  req.EndToEndID,
		Description: req.Description,
		Metadata:    req.Metadata,
	}
}

type openCycleRequest struct {
	Scheme string `json:"scheme"`
}

// ---------------------------------------------------------------------------
// Enum parsing
// ---------------------------------------------------------------------------

func accountTypeFromString(s string) (ledger.AccountType, error) {
	switch s {
	case "Asset":
		return ledger.Asset, nil
	case "Liability":
		return ledger.Liability, nil
	case "Equity":
		return ledger.Equity, nil
	case "Revenue":
		return ledger.Revenue, nil
	case "Expense":
		return ledger.Expense, nil
	default:
		return 0, fmt.Errorf("invalid account type %q (want Asset, Liability, Equity, Revenue, or Expense)", s)
	}
}

func directionFromString(s string) (ledger.Direction, error) {
	switch s {
	case "Debit":
		return ledger.Debit, nil
	case "Credit":
		return ledger.Credit, nil
	default:
		return 0, fmt.Errorf("invalid direction %q (want Debit or Credit)", s)
	}
}
