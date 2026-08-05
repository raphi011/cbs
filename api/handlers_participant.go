package api

import (
	"errors"
	"net/http"
	"sort"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/mesh"
	"github.com/raphi011/cbs/payment"
)

// handleAddParticipant founds a bank and applies to the scheme for it.
//
// # The orphan is gone, and what replaced it is a bank that has not joined yet
//
// This used to be two steps that could not be one: the participant row was
// written, and the mesh was then asked for the address. The address is the only
// thing in the whole operation that can clash, so the irreversible step ran
// first and the refusable step second — and a refusal left a bank in the roster
// that could neither pay nor be paid, with no way back. The four paragraphs that
// used to stand here documented that consequence at length and did not say that
// reversing the two would remove it.
//
// mesh.Mesh.Admit reverses them. The address is claimed at the mesh before
// anything is written and released again if the write fails, so a clash now
// costs nothing at all: no row, no actor, and an error the operator can act on.
//
// What the caller gets back is a FOUNDED bank. Its book, its chart of accounts
// and its default product exist and it can open customer accounts; it cannot pay
// or be paid, because no settlement agent holds an account for it and no clearing
// house routes to it yet. Whether the scheme accepts arrives later, at two other
// institutions, as a message — the same shape POST /payments has, and for the
// same reason.
//
// An interrupted admission therefore leaves a founded bank rather than an orphan,
// and calling this again on the same name and BIC RE-DRIVES it: nothing is
// founded twice and the application goes out again. See mesh.Mesh.Admit.
//
// # What is left for Task 17e
//
// The status code and the response body. This still answers 201 Created with a
// participant DTO that does not carry the bank's status, which is the answer the
// atomic call gave and is no longer the whole truth. The DTO, the status code
// and the web types are 17e's, together and in one change.
func (s *Server) handleAddParticipant(w http.ResponseWriter, r *http.Request) {
	var req createParticipantRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	// An empty (or absent) Assets means the network's default joining set —
	// payment applies that itself, at founding. That is a default for which
	// assets a bank joins with, not for the asset of any account, which is
	// always named by its caller.
	assets := make([]ledger.AssetCode, len(req.Assets))
	for i, a := range req.Assets {
		assets[i] = ledger.AssetCode(a)
	}
	// BIC is required, but its shape is a business rule (iso20022.BIC.Validate,
	// run by Admit before it claims the address) rather than a decoding failure,
	// so a malformed or missing value is left to surface as the 422 writeError
	// already maps iso20022.ErrBICFormat to, not a 400 raised here.
	p, err := s.mesh.Admit(r.Context(), req.Name, iso20022.BIC(req.BIC), assets)
	if err != nil {
		if errors.Is(err, mesh.ErrAddressTaken) {
			writeUnprocessable(w, "another institution already answers to this BIC, and nothing was written; "+
				"admit this bank on an address of its own: "+err.Error())
			return
		}
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toParticipantDTO(p))
}

func (s *Server) handleListParticipants(w http.ResponseWriter, r *http.Request) {
	parts, err := s.network().ListBanks(r.Context())
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
	parts, err := s.network().ListBanks(r.Context())
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
	p, err := s.network().GetBank(r.Context(), payment.ParticipantID(r.PathValue("pid")))
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
func (s *Server) reserveRows(r *http.Request, p *payment.Bank) ([]reserveDTO, error) {
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
