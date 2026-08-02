package api

import (
	"net/http"
	"sort"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// handleAddParticipant admits a bank: its participant row, its chart of
// accounts, its reserve account at the central bank — and its ACTOR.
//
// The last of those is what the mesh added. A bank with no actor is not a slow
// bank, it is an unreachable one: its customers' instructions are refused by
// Mesh.Submit because there is nobody to hand them to, and every pacs.008
// addressed to it comes back RC01 from the clearing house because its BIC is in
// no routing table. The mesh reads the roster once, at startup, so a bank
// admitted while the process is running has to be registered here or it never is.
//
// # The two steps are not one, and cannot be
//
// Admission is a unit of work in the store; registering an actor is a map entry
// and a goroutine, and no transaction spans the two. So a mesh that refuses —
// which it does for a BIC another bank already answers to — leaves a bank in the
// roster with no actor, and there is no rolling that back from here.
//
// It is reported rather than hidden, and the response says which half happened,
// because the alternative is worse in both directions: a 201 would hand back a
// bank that cannot pay or be paid, and a silent retry-later would be a promise
// nothing in this process keeps. The operator's fix is to admit the bank on an
// address of its own — which is the real-world fix too, since a BIC identifies
// an institution and two banks cannot share one.
func (s *Server) handleAddParticipant(w http.ResponseWriter, r *http.Request) {
	var req createParticipantRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	// An empty (or absent) Assets means the network's default joining set —
	// see AddParticipant. That is a default for which assets a bank joins
	// with, not for the asset of any account, which is always named by its
	// caller.
	assets := make([]ledger.AssetCode, len(req.Assets))
	for i, a := range req.Assets {
		assets[i] = ledger.AssetCode(a)
	}
	// BIC is required, but its shape is a business rule (iso20022.BIC.Validate,
	// run inside AddParticipant) rather than a decoding failure, so a malformed
	// or missing value is left to surface as the 422 writeError already maps
	// iso20022.ErrBICFormat to, not a 400 raised here.
	p, err := s.network().AddParticipant(r.Context(), req.Name, iso20022.BIC(req.BIC), assets)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.mesh.AddBank(p); err != nil {
		s.log.Error("a bank was admitted that the mesh cannot route to",
			"participant", p.ID, "bic", p.BIC, "error", err)
		writeUnprocessable(w, "this bank was admitted and has no actor, so it can neither pay nor be paid: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toParticipantDTO(p))
}

func (s *Server) handleListParticipants(w http.ResponseWriter, r *http.Request) {
	parts, err := s.network().ListParticipants(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]participantDTO, len(parts))
	for i, p := range parts {
		out[i] = toParticipantDTO(p)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetParticipant(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toParticipantDTO(p))
}

func (s *Server) handleFundDeposit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req fundRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	// No asset on the wire: funding raises the reserve of whichever asset the
	// funded account is denominated in, which the network reads for itself.
	if err := s.network().Deposit(r.Context(), p.ID, deposit.AccountID(req.Account), ledger.Amount(req.Amount), req.Description); err != nil {
		writeError(w, err)
		return
	}
	acct, err := p.Deposit.GetAccount(r.Context(), deposit.AccountID(req.Account))
	if err != nil {
		writeError(w, err)
		return
	}
	bal, err := p.Deposit.GetBalance(r.Context(), deposit.AccountID(req.Account))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toBalanceDTO(bal, acct.Asset))
}

func (s *Server) handleListSchemes(w http.ResponseWriter, r *http.Request) {
	schemes := s.network().ListSchemes()
	out := make([]schemeDTO, len(schemes))
	for i, sc := range schemes {
		out[i] = toSchemeDTO(sc)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListReserves reports every bank's reserve in every asset it operates
// in — one row per (participant, asset), because a reserve in one asset says
// nothing about a reserve in another and the two must not be added up.
func (s *Server) handleListReserves(w http.ResponseWriter, r *http.Request) {
	parts, err := s.network().ListParticipants(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]reserveDTO, 0, len(parts))
	for _, p := range parts {
		rows, err := s.reserveRows(r, p)
		if err != nil {
			writeError(w, err)
			return
		}
		out = append(out, rows...)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetReserve reports one bank's reserves, one row per asset, for the
// same reason handleListReserves does.
func (s *Server) handleGetReserve(w http.ResponseWriter, r *http.Request) {
	p, err := s.network().GetParticipant(r.Context(), payment.ParticipantID(r.PathValue("pid")))
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := s.reserveRows(r, p)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// reserveRows reads one participant's reserve in each of its assets, in asset
// order so the response does not reshuffle between identical requests.
func (s *Server) reserveRows(r *http.Request, p *payment.Participant) ([]reserveDTO, error) {
	codes := make([]string, 0, len(p.Assets))
	for code := range p.Assets {
		codes = append(codes, string(code))
	}
	sort.Strings(codes)

	out := make([]reserveDTO, 0, len(codes))
	for _, code := range codes {
		bal, err := s.network().ReserveBalance(r.Context(), p.ID, ledger.AssetCode(code))
		if err != nil {
			return nil, err
		}
		out = append(out, reserveDTO{Participant: string(p.ID), Asset: code, Reserve: int64(bal)})
	}
	return out, nil
}
