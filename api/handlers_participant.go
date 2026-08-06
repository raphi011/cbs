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
// # 202, and what an operator has when it answers
//
// A founded bank. Its book, its chart of accounts and its default product exist,
// so it can open customer accounts straight away. It cannot pay or be paid — no
// clearing house routes to it — and it cannot take a cash deposit either, since
// funding raises a reserve and no settlement agent holds an account for it yet.
// The DTO says which of the two states it is in: Founded here, and Member once
// the scheme has answered.
//
// Whether the scheme accepts is not this call's to report. It is decided at two
// other institutions and arrives as a message, so the honest status code is 202
// Accepted: the application has been made and nothing about its outcome is
// known. POST /payments has the same shape for the same reason — a handler that
// answered 201 Created would be naming a resource whose most important property
// it had not waited for. An operator learns the answer by reading the bank back.
//
// # A clash costs nothing, and a re-drive is this same call
//
// The address is the only thing in the operation that can clash, and
// mesh.Mesh.Admit claims it before anything is written and releases it again if
// the write fails. So a refusal leaves no row, no actor and nothing to clean up
// — which is the reverse of the ordering this endpoint used to have, where the
// participant row was written first and a refused address left a bank in the
// roster that could neither pay nor be paid.
//
// An interrupted admission therefore leaves a founded bank rather than an orphan,
// and calling this again on the same name and BIC RE-DRIVES it: nothing is
// founded twice and the application goes out again. See mesh.Mesh.Admit.
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
		// The two refusals about the address, which need different advice.
		//
		// An admission ALREADY UNDER WAY on this BIC is checked first, because it
		// is a case of the sentinel below and would otherwise be answered by it —
		// wrongly on both clauses. Nothing is written YET rather than not at all,
		// the address is not another institution's but this bank's own, and a
		// second address would not help; what helps is waiting for the first
		// application to be answered and reading the bank back.
		if errors.Is(err, mesh.ErrAdmissionInFlight) {
			writeUnprocessable(w, "an admission on this BIC is already under way and nothing has been written for this "+
				"request; wait for it to be answered and read the bank back: "+err.Error())
			return
		}
		if errors.Is(err, mesh.ErrAddressTaken) {
			writeUnprocessable(w, "another institution already answers to this BIC, and nothing was written; "+
				"admit this bank on an address of its own: "+err.Error())
			return
		}
		writeError(w, err)
		return
	}
	// 202, not 201: what exists is a founded bank, and whether the scheme admits
	// it is answered at two other institutions and arrives as a message.
	writeJSON(w, http.StatusAccepted, toParticipantDTO(p))
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
//
// # A bank the central bank holds no account for reports NO ROW for that asset
//
// That is a state this system can be in for the first time: admission is a
// conversation, so between founding and the scheme's answer there is a real bank
// with a real book and no settlement account anywhere. ReserveBalance says so
// with payment.ErrSettlementMemberNotFound, and it is not an error about this
// request — the bank exists, the asset exists, and the settlement agent simply
// holds nothing to report a balance from.
//
// Answering with a zero would be worse than answering with nothing: zero is a
// balance, and a console that showed one would be reporting an account that has
// not been opened. So the asset is skipped, GET on a founded bank's reserves is
// an empty list, and the list route reports the members and passes over the
// applicants. What tells an operator which is which is the bank's own status,
// which participantDTO carries.
//
// Per ASSET rather than per bank, because the two sentinels are two different
// facts and a partly admitted bank has one of each: the settlement agent holds
// no member row at all (skipped here), or holds one without this asset in it —
// payment.ErrParticipantAssetNotFound, which is left to surface, because the
// bank's own row says it operates in an asset the agent has no account for and
// that is a disagreement between two institutions rather than a bank waiting to
// be admitted.
func (s *Server) reserveRows(r *http.Request, p *payment.Bank) ([]reserveDTO, error) {
	codes := make([]string, 0, len(p.Assets))
	for code := range p.Assets {
		codes = append(codes, string(code))
	}
	sort.Strings(codes)

	out := make([]reserveDTO, 0, len(codes))
	for _, code := range codes {
		bal, err := s.network().ReserveBalance(r.Context(), p.ID, ledger.AssetCode(code))
		if errors.Is(err, payment.ErrSettlementMemberNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, reserveDTO{Participant: string(p.ID), Asset: code, Reserve: int64(bal)})
	}
	return out, nil
}
