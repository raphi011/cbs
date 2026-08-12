package api

import (
	"time"

	"github.com/raphi011/cbs/payment"
)

// The wire shapes of a bank checking its own books: the reconciliation run, the
// statements it was sent, and the two in-transit accounts decomposed by age.

// ReconciliationDTO is what POST /reconciliation answers.
type ReconciliationDTO struct {
	Bank  string    `json:"bank"`
	Asset string    `json:"asset"`
	AsOf  time.Time `json:"asOf"`
	// Reconciled is len(breaks) == 0, on the wire so that a client does not
	// re-derive the rule and get the positions wrong. See
	// payment.Reconciliation.Reconciled.
	Reconciled bool `json:"reconciled"`

	// Reserve is what this bank's own book says its claim on the central bank
	// stands at; Advised and Reference are the closing balance on the newest
	// statement it has booked and what that statement quoted.
	Reserve     int64  `json:"reserve"`
	Advised     int64  `json:"advised"`
	Reference   string `json:"reference,omitempty"`
	LodgedSince int64  `json:"lodgedSince"`

	Breaks    []FindingDTO      `json:"breaks"`
	Positions []AgeingReportDTO `json:"positions"`
}

// FindingDTO is one thing this bank's books say cannot be true: the account to
// go and look at, and the disagreement as one sentence with both figures in it.
type FindingDTO struct {
	Account string `json:"account"`
	What    string `json:"what"`
}

// AgeingReportDTO is one in-transit account decomposed into what its balance is
// made of and how long each part has been there.
type AgeingReportDTO struct {
	Bank    string       `json:"bank"`
	Asset   string       `json:"asset"`
	Account string       `json:"account"`
	AsOf    time.Time    `json:"asOf"`
	Balance int64        `json:"balance"`
	Lots    []AgedLotDTO `json:"lots"`
}

// AgedLotDTO is one part of a balance, with what put it there and what may now
// be done about it.
type AgedLotDTO struct {
	Transaction string    `json:"transaction"`
	Since       time.Time `json:"since"`
	// Days is whole CALENDAR days from Since, same-day being 0. A rulebook window
	// is counted in business days, so days can pass deadline without the lot
	// being overdue; due is the day that settles it.
	Days        int    `json:"days"`
	Amount      int64  `json:"amount"`
	Description string `json:"description,omitempty"`

	Payment string `json:"payment,omitempty"`
	Scheme  string `json:"scheme,omitempty"`

	// Deadline is the rulebook window in whole banking business days and is ABSENT
	// where no rulebook puts a clock on this money — a clearing suspense is
	// discharged by a conversation, and a lot this bank has no instrument to clear
	// carries no deadline either.
	Deadline int `json:"deadline,omitempty"`
	// Due is the day the window runs out, on the settlement calendar. Absent
	// with deadline.
	Due time.Time `json:"due,omitzero"`
	// Overdue is the line a report prints in bold, and it is on the wire rather
	// than left to the client for reconciled's reason: a lot with no deadline is
	// never overdue however old it is, and that rule is the domain's.
	Overdue bool `json:"overdue"`
	// Blocked says why this bank cannot clear this lot itself, and is absent when
	// it can. Prose, because a reader of it is deciding what to do next and the
	// reasons are not variants of one thing.
	Blocked string `json:"blocked,omitempty"`
}

// SettlementAdviceDTO is one statement this bank was sent, read back.
type SettlementAdviceDTO struct {
	Reference string `json:"reference"`
	Asset     string `json:"asset"`
	// Movement is signed: positive means this bank's reserve went up.
	Movement       int64 `json:"movement"`
	ClosingBalance int64 `json:"closingBalance"`
	// Status is "Advised" or "Posted", and every row a caller can reach is
	// Posted: the row and the mirror leg are written in one unit of work. See
	// payment.AdviceAdvised, which is the status this system does not commit.
	Status string `json:"status"`
	// MirrorTransaction is the leg this bank booked from the statement, and it is
	// what a reconciliation break naming this reference points at.
	MirrorTransaction string    `json:"mirrorTransaction,omitempty"`
	AdvisedAt         time.Time `json:"advisedAt"`
	PostedAt          time.Time `json:"postedAt"`
}

func ToReconciliationDTO(r payment.Reconciliation) ReconciliationDTO {
	out := ReconciliationDTO{
		Bank:        string(r.Bank),
		Asset:       string(r.Asset),
		AsOf:        r.AsOf,
		Reconciled:  r.Reconciled(),
		Reserve:     int64(r.Reserve),
		Advised:     int64(r.Advised),
		Reference:   r.Reference,
		LodgedSince: int64(r.LodgedSince),
		// Both built with a zero length rather than a nil slice, so a bank whose
		// books hold together answers [] and not null. A client rendering "no
		// breaks" should not have to tell an empty list from a missing field.
		Breaks:    make([]FindingDTO, 0, len(r.Breaks)),
		Positions: make([]AgeingReportDTO, 0, len(r.Positions)),
	}
	for _, b := range r.Breaks {
		out.Breaks = append(out.Breaks, FindingDTO{Account: string(b.Account), What: b.What})
	}
	for _, p := range r.Positions {
		out.Positions = append(out.Positions, ToAgeingReportDTO(p))
	}
	return out
}

func ToAgeingReportDTO(r payment.AgeingReport) AgeingReportDTO {
	out := AgeingReportDTO{
		Bank:    string(r.Bank),
		Asset:   string(r.Asset),
		Account: string(r.Account),
		AsOf:    r.AsOf,
		Balance: int64(r.Balance),
		Lots:    make([]AgedLotDTO, 0, len(r.Lots)),
	}
	for _, l := range r.Lots {
		out.Lots = append(out.Lots, AgedLotDTO{
			Transaction: string(l.Transaction),
			Since:       l.Since,
			Days:        l.Days,
			Amount:      int64(l.Amount),
			Description: l.Description,
			Payment:     string(l.Payment),
			Scheme:      string(l.Scheme),
			Deadline:    l.Deadline,
			Due:         l.Due,
			Overdue:     l.Overdue(r.AsOf),
			Blocked:     l.Blocked,
		})
	}
	return out
}

func ToSettlementAdviceDTO(a payment.SettlementAdvice) SettlementAdviceDTO {
	return SettlementAdviceDTO{
		Reference:         a.Reference,
		Asset:             string(a.Asset),
		Movement:          int64(a.Movement),
		ClosingBalance:    int64(a.ClosingBalance),
		Status:            a.Status.String(),
		MirrorTransaction: string(a.MirrorTx),
		AdvisedAt:         a.AdvisedAt,
		PostedAt:          a.PostedAt,
	}
}
