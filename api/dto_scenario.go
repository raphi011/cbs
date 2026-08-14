package api

// The wire shapes of a scenario: a named thing an operator can make happen, and
// what happened when they did.

// ScenarioDTO is one scenario as the picker lists it.
type ScenarioDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ScenarioReportDTO is what running one moved. It carries a next date because a
// scenario advances the shared clock, which is the cost the picker warns about.
type ScenarioReportDTO struct {
	Scenario ScenarioDTO     `json:"scenario"`
	Ran      BusinessDateDTO `json:"ran"`
	Next     BusinessDateDTO `json:"next"`

	MovementsDTO
}
