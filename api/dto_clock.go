package api

// The wire shapes of the deployment's business date: what day it is on, and
// what happened the last time somebody advanced it.

// BusinessDateDTO is what day the deployment is on, and whether anything clears
// on it.
type BusinessDateDTO struct {
	Date          string `json:"date"`
	SettlementDay bool   `json:"settlementDay"`
	Closure       string `json:"closure,omitempty"`
}

// DayReportDTO is what one business day did, and it is what POST /clock/day
// answers with.
type DayReportDTO struct {
	Ran  BusinessDateDTO `json:"ran"`
	Next BusinessDateDTO `json:"next"`

	Files    []FileMovedDTO          `json:"files"`
	Outcomes []TransactionOutcomeDTO `json:"outcomes"`
	Problems []ProblemDTO            `json:"problems"`
}

// FileMovedDTO is one file that left one institution for another, under the
// order type it travelled as and the order id its host gave it. The movement is
// which half of the crossing it is: put where the recipient can reach it, taken.
type FileMovedDTO struct {
	From      string `json:"from"`
	To        string `json:"to"`
	OrderType string `json:"orderType"`
	OrderID   string `json:"orderId"`
	Movement  string `json:"movement"`
}

// TransactionOutcomeDTO is one institution's decision about one payment.
type TransactionOutcomeDTO struct {
	DecidedBy string `json:"decidedBy"`
	Payment   string `json:"payment"`
	Status    string `json:"status"`
	Code      string `json:"code,omitempty"`
	Text      string `json:"text,omitempty"`
}

// ProblemDTO is a file an institution could not process.
type ProblemDTO struct {
	Institution string `json:"institution"`
	OrderID     string `json:"orderId,omitempty"`
	Detail      string `json:"detail"`
}
