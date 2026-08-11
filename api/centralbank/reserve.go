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
//
// surface is driven by, which is where the cost and the reason no institution
// can answer it are set out. What is worth saying here is only why the route is
// on this listener: it reaches past this institution to databases that are not
// the settlement agent's, and the router below is where that exception is
// written down.
func (s *surface) handleListParticipants(w http.ResponseWriter, r *http.Request) {
	banks, err := s.inst.Members(r.Context())
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := make([]api.ParticipantDTO, 0, len(banks))
	for _, p := range banks {
		out = append(out, api.ToParticipantDTO(p))
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// handleListReserves reports every reserve this central bank holds — one row per
// (member, asset), because a reserve in one asset says nothing about a reserve
// in another and the two must not be added up.
//
// The list is the settlement agent's OWN register. See reserveRows.
func (s *surface) handleListReserves(w http.ResponseWriter, r *http.Request) {
	members, err := s.network().ListSettlementMembers(r.Context())
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := make([]api.ReserveDTO, 0, len(members))
	for _, m := range members {
		rows, err := s.reserveRows(r, m)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		out = append(out, rows...)
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// handleGetReserve reports one member's reserves, one row per asset, for the
// same reason handleListReserves does.
//
// The path segment is a BIC, which is what the settlement agent's records are
// keyed by and the only name for a bank it is ever told. A bank it holds no
// account for is the 422 the sentinel already mapped to rather than a 404 about
// a bank row this institution has no table for.
func (s *surface) handleGetReserve(w http.ResponseWriter, r *http.Request) {
	m, err := s.network().GetSettlementMember(r.Context(), iso20022.BIC(r.PathValue("bic")))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out, err := s.reserveRows(r, m)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// reserveRows reads one member's reserve in each asset the settlement agent
// holds an account for it in, in asset order so the response does not reshuffle
// between identical requests.
//
// # One rule: a row for every account this institution holds, and nothing else
//
// The assets come off the MEMBER's row — payment.SettlementMember.Accounts, one
// account per asset, written by the agent when it answered the bank's request —
// so every row this builds is a reserve this institution actually keeps.
//
// It reads the agent's own register straight, which is the only version of this
// endpoint the central bank's database can serve: it has no banks table to
// compare against. A bank with no member row is not in ListSettlementMembers to
// begin with, and an asset with no account is not in this map.
//
// # What that costs, and which stuck bank it was actually about
//
// What this reading cannot show is worth naming, because the obvious candidate
// is not one of them.
//
// A two-asset bank whose provisioning stopped between its two requests leaves the
// agent holding a row without the second asset, permanently. So does a request
// the agent REFUSED: nothing about a bank changes when it is refused an account,
// so the bank's own row carries an empty reference and the agent holds none. The
// two are indistinguishable from each other — "it never asked" and "it was told
// no" leave identical rows — and both look, from this endpoint, exactly like a
// one-asset member, which is also what a two-asset admission looks like between
// its two commits. The difference is TIME and nothing here watches a clock.
//
// The half-admitted bank is a DIFFERENT state and this endpoint reports it fully.
// There every account was opened and the bank's own act never ran, so the agent's
// row has both assets and this reports two rows — the second showing a reserve
// the bank cannot spend, since payment.DepositTx resolves through the BANK's
// record and that one has no reference. That is precisely what
// payment.RecordMembershipTx describes: a deposit in that asset fails while the
// operator console cheerfully reports the reserve.
//
// Finding any of them needs the bank's own assets beside these rows, which GET
// /me on that bank's own port carries and which no screen in this repository
// renders — so it is two reads at two institutions, not something one console
// can show. Putting them side by side is a reconciliation; see payment/recon.
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
