package api

import (
	"time"

	"github.com/raphi011/cbs/payment"
)

// The wire shapes of a bank checking its own books: the reconciliation run, the
// statements it was sent, and the two in-transit accounts decomposed by age.
//
// Every one of these is a REPORT and none of them is a row. There is no findings
// table behind the run — a finding is a pure function of the books at a moment,
// so a stored one is a cache that can disagree with them — so these types
// describe an answer computed for this request and nothing a client can come back
// and fetch by id. What survives a run is the audit event it appended, on GET
// /audit, and the books it read.

// ReconciliationDTO is what POST /reconciliation answers.
//
// Breaks and positions are separate fields because they are different findings
// and only one is a defect: a break is something this bank's own books say
// cannot be true, and a position is money legitimately in flight with how long it
// has been. A client rendering them in one list would be telling an operator to
// investigate every payment that has not settled yet.
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
	// statement it has booked and what that statement quoted. LodgedSince is the
	// whole of the difference between the two, and it is never negative.
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
//
// # Payment is absent on a clearing suspense and present on an unclaimed balance
//
// That is a fact about the postings rather than a gap here. Every credit into
// Unclaimed is one payment's diverted leg and carries its id; a clearing
// suspense is discharged by a NETTED mirror leg that names no payment at all, so
// what a lot there survives with is an age and an order. See
// payment.AgeClearingSuspense.
//
// It carries no metadata map beside these fields. The two keys a reader wants
// off it — the payment and the scheme — are lifted onto the lot by the domain,
// and putting the raw map on the wire as well would publish an internal posting
// convention as an interface.
type AgedLotDTO struct {
	Transaction string    `json:"transaction"`
	Since       time.Time `json:"since"`
	// Days is whole days from Since, same-day being 0. Calendar days: there is
	// no business date in this system. See payment.ReturnWindowDays.
	Days        int    `json:"days"`
	Amount      int64  `json:"amount"`
	Description string `json:"description,omitempty"`

	Payment string `json:"payment,omitempty"`
	Scheme  string `json:"scheme,omitempty"`

	// Deadline is the rulebook window in whole days and is ABSENT where no
	// rulebook puts a clock on this money — a clearing suspense is discharged by
	// a conversation, and a lot this bank has no instrument to clear carries no
	// deadline either.
	Deadline int `json:"deadline,omitempty"`
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
//
// Reference is opaque and stays opaque on the wire: it is the AcctSvcrRef the
// statement carried — a cycle id at a cut-off, a payment id at a return — and a
// member bank holds neither, so it cannot tell the two apart and has no reason
// to. See payment.SettlementAdvice.
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
			Overdue:     l.Overdue(),
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
