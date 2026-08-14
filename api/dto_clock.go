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

// MovementsDTO is what a run of the business day moved. It is embedded rather
// than nested, so a whole day's report and one phase's carry the same three
// fields under the same names.
type MovementsDTO struct {
	Files    []FileMovedDTO          `json:"files"`
	Outcomes []TransactionOutcomeDTO `json:"outcomes"`
	Problems []ProblemDTO            `json:"problems"`
}

// DayReportDTO is what one business day did, and it is what POST /clock/day
// answers with.
type DayReportDTO struct {
	Ran  BusinessDateDTO `json:"ran"`
	Next BusinessDateDTO `json:"next"`

	// The phase keys THIS call ran, in order. A day some of whose phases were
	// stepped by hand runs only the rest, and this is where it says so.
	Phases []string `json:"phases"`

	MovementsDTO
}

// PhaseDTO is one step of the business day, as the door an operator opens it
// through. Key is the name spelt for a URL, and it is the whole of what a door
// takes: a phase is named, never parameterised.
type PhaseDTO struct {
	Key            string `json:"key"`
	Name           string `json:"name"`
	SettlementOnly bool   `json:"settlementOnly"`
	// AfterClock says the phase runs once the date has moved, which is where the
	// day's own advance sits among the doors.
	AfterClock bool `json:"afterClock"`
	// Completed says the day the clock stands on has run this phase, so advancing
	// the day will not run it again. A phase stepped out of turn is not completed:
	// it ran, and the day will run it in its place.
	Completed bool `json:"completed"`
}

// PhaseReportDTO is what one phase moved. There is no next date: a phase is a
// step inside a day, and only advancing the day moves the clock.
type PhaseReportDTO struct {
	Phase PhaseDTO        `json:"phase"`
	Ran   BusinessDateDTO `json:"ran"`

	MovementsDTO
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
