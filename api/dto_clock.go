package api

// The wire shapes of the deployment's business date: what day it is on, and
// what happened the last time somebody advanced it.
//
// These are the only DTOs in this package that no domain type maps into, and
// the reason is that the value they describe belongs to the DEPLOYMENT rather
// than to any institution — a business date is in none of the N+2 databases, and
// a day's report is what N+2 institutions did between two of them. The thing
// that holds both is a composition root, which no surface package may import.
// So these are the shape the report crosses in, and the composition root builds
// them.

// BusinessDateDTO is what day the deployment is on, and whether anything clears
// on it.
//
// # The date is a DATE and travels as one
//
// "2025-09-15", and not an instant. A business date is day-granular, and an
// instant carrying a time of day would be rendered by a browser in the reader's
// own zone — where "2025-09-15T09:00:00Z" is the 14th for anybody west of
// Greenwich, and the topbar would show a day the deployment is not on.
//
// # Two fields for closed, because there are two kinds
//
// SettlementDay false with an empty Closure is a weekend: shut, and for no
// reason that has a name. A Closure names a TARGET holiday. Keeping them apart
// is what lets a console say "closed, Easter Monday" where it would otherwise
// only be able to say "closed".
type BusinessDateDTO struct {
	Date          string `json:"date"`
	SettlementDay bool   `json:"settlementDay"`
	Closure       string `json:"closure,omitempty"`
}

// DayReportDTO is what one business day did, and it is what POST /clock/day
// answers with.
//
// Ran is the day that was worked and Next is where the clock stands afterwards.
// Both are here because they can differ in the way that matters: a Friday clears
// and leaves the deployment on a Saturday that will not.
//
// The three lists are what an operator watches. Files is every file that moved
// between institutions, Outcomes is every decision made about a payment, and
// Problems is everything an institution could not process — which is the channel
// for a failure there was nobody to answer, and is empty on a day that went
// well.
type DayReportDTO struct {
	Ran  BusinessDateDTO `json:"ran"`
	Next BusinessDateDTO `json:"next"`

	Files    []FileMovedDTO          `json:"files"`
	Outcomes []TransactionOutcomeDTO `json:"outcomes"`
	Problems []ProblemDTO            `json:"problems"`
}

// FileMovedDTO is one file that left one institution for another, under the
// order type it travelled as and the order id its host gave it.
type FileMovedDTO struct {
	From      string `json:"from"`
	To        string `json:"to"`
	OrderType string `json:"orderType"`
	OrderID   string `json:"orderId"`
}

// TransactionOutcomeDTO is one institution's decision about one payment.
//
// DecidedBy is the institution that MADE the decision and not one that carried
// it, so a payment's ordinary life is three entries by three different
// institutions: accepted by the bank that received the instruction, cleared by
// the clearing house, settled by the clearing house once the reserves moved.
type TransactionOutcomeDTO struct {
	DecidedBy string `json:"decidedBy"`
	Payment   string `json:"payment"`
	Status    string `json:"status"`
	Code      string `json:"code,omitempty"`
	Text      string `json:"text,omitempty"`
}

// ProblemDTO is a file an institution could not process.
//
// OrderID is absent where the failure was not about one file — a download that
// could not be made at all — because there is no order to name.
type ProblemDTO struct {
	Institution string `json:"institution"`
	OrderID     string `json:"orderId,omitempty"`
	Detail      string `json:"detail"`
}
