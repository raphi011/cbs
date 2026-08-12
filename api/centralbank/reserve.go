package centralbank

import (
	"net/http"
	"sort"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// handleListParticipants answers every bank this deployment holds a database
// for, each read out of its own database, ascending by address.
func (s *surface) handleListParticipants(r *http.Request) ([]api.ParticipantDTO, error) {
	banks, err := s.inst.Members(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]api.ParticipantDTO, 0, len(banks))
	for _, p := range banks {
		out = append(out, api.ToParticipantDTO(p))
	}
	return out, nil
}

// handleListReserves reports every reserve this central bank holds — one row
// per (member, asset), because a reserve in one asset says nothing about a
// reserve in another and the two must not be added up.
func (s *surface) handleListReserves(r *http.Request) ([]api.ReserveDTO, error) {
	members, err := s.network().ListSettlementMembers(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]api.ReserveDTO, 0, len(members))
	for _, m := range members {
		rows, err := s.reserveRows(r, m)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// handleGetReserve reports one member's reserves, one row per asset, for the
// same reason handleListReserves does.
func (s *surface) handleGetReserve(r *http.Request) ([]api.ReserveDTO, error) {
	m, err := s.network().GetSettlementMember(r.Context(), iso20022.BIC(r.PathValue("bic")))
	if err != nil {
		return nil, err
	}
	return s.reserveRows(r, m)
}

// reserveRows reads one member's reserve in each asset the settlement agent
// holds an account for it in, in asset order so the response does not reshuffle
// between identical requests.
func (s *surface) reserveRows(r *http.Request, m payment.SettlementMember) ([]api.ReserveDTO, error) {
	codes := make([]string, 0, len(m.Accounts))
	for code := range m.Accounts {
		codes = append(codes, string(code))
	}
	sort.Strings(codes)

	out := make([]api.ReserveDTO, 0, len(codes))
	for _, code := range codes {
		bal, err := s.network().ReserveBalance(r.Context(), m.BIC, ledger.AssetCode(code))
		if err != nil {
			return nil, err
		}
		out = append(out, api.ReserveDTO{Agent: string(m.BIC), Asset: code, Reserve: int64(bal)})
	}
	return out, nil
}
