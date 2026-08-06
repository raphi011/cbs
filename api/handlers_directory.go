package api

import (
	"net/http"
	"time"

	"github.com/raphi011/cbs/deposit"
)

// Account addressing: one bank's own directory, and the per-account identifier
// endpoints that populate it.
//
// GET /directory used to be network-scoped, on the grounds that resolving an
// address is exactly the question "which bank?" and a route that already named
// the bank would answer nothing. Task 18a retired both halves of that.
//
// The question it answered was answerable only by a SWEEP — one bank reading
// every other bank's register over HTTP — and that is the crossing
// payment.ResolveIdentifier no longer makes. A bank holds its own register and
// no other, so what this route can honestly answer is "is this address one of
// mine", and a route that names the bank is exactly right for it.
//
// What the network-wide question needs is a directory SERVICE, which this
// system does not have and which no bank could assemble out of the others'
// books. See payment.ResolveIdentifier for the whole of that argument, and the
// clearing house's GET /roster for the routing directory one institution here
// genuinely does own.
func (s *Server) registerBankIdentifierRoutes(mux *router) {
	mux.HandleFunc("POST /deposit-accounts/{did}/identifiers", s.handleAddIdentifier)
	mux.HandleFunc("DELETE /deposit-accounts/{did}/identifiers/{scheme}/{value}", s.handleRemoveIdentifier)
}

// directoryEntryDTO is what GET /directory answers: which of this bank's
// accounts holds the address, plus the identifier that was resolved, echoed
// back for a client that fired several lookups at once.
//
// # It used to carry Name and Asset, and they died with the sweep
//
// They were a JOIN on top of the resolution: the handler took the participant
// the sweep returned, bound that bank's deposit register, and read the account's
// display name and asset out of it. On a BANK's port — this route is registered
// there too (api/surface.go) — that was one bank reading another bank's register
// for the payee's name, over HTTP rather than through a message, which is the
// crossing Task 14 closed on the payment path and left open here. The web send
// form called it and displayed the result, so the UI demonstrated the negation
// of the fact the branch existed to establish.
//
// Removing them is not a loss of information the caller was entitled to. A bank
// resolving its OWN customer has the name and the asset already, from every
// other route it has about that account; a bank asking about somebody else's
// gets ErrIdentifierNotFound now, and the name of another bank's customer is not
// something this system will tell it by any route at all. What a payer knows
// about a payee is what the payer typed — see payment.PartyDetails.
type directoryEntryDTO struct {
	Participant string        `json:"participant"`
	Account     string        `json:"account"`
	Identifier  identifierDTO `json:"identifier"`
}

func (s *Server) handleResolveIdentifier(w http.ResponseWriter, r *http.Request) {
	scheme := r.URL.Query().Get("scheme")
	value := r.URL.Query().Get("value")
	if scheme == "" || value == "" {
		writeBadRequest(w, "scheme and value are both required")
		return
	}
	ident := deposit.Identifier{Scheme: deposit.IdentifierScheme(scheme), Value: value}
	// The listener's own bank, and it is the whole of the scope. An unbound
	// Server would ask about the participant "", which is a clean not-found —
	// the failure mode worth having if this is ever registered on a surface that
	// has no identity, as it was on the clearing house's until Task 18a.
	ref, err := s.network().ResolveIdentifier(r.Context(), s.boundPID, ident)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, directoryEntryDTO{
		Participant: string(ref.Participant),
		Account:     string(ref.Account),
		Identifier:  identifierDTO{Scheme: scheme, Value: value},
	})
}

// rosterEntryDTO is one row of the clearing house's routing directory.
//
// It is what payment.RosterEntry holds and nothing more, which is the point of
// putting it on a surface at all: this row is the whole of what one institution
// knows about another in this system. An address, the assets it clears in, the
// admission it was admitted under, and when.
//
// No NAME, and its absence is domain content rather than an omission. An
// acmt.010 carries none, so the clearing house has never been told one; the
// name a console shows beside a BIC comes from GET /members, which is a
// different question asked of a different table. See payment.RosterEntry.
type rosterEntryDTO struct {
	BIC          string    `json:"bic"`
	Assets       []string  `json:"assets"`
	AdmissionRef string    `json:"admissionRef"`
	AdmittedAt   time.Time `json:"admittedAt"`
}

// handleListRoster answers the clearing house's routing directory.
//
// It is the successor to GET /directory on this surface and it answers a
// different question, which is the whole finding: the question the sweep
// answered — "who holds this IBAN" — is one no institution here can answer, and
// the question this institution CAN answer is "who may be addressed". A bank
// absent from this list exists perfectly well; it is simply not somewhere this
// scheme will send anything. A bank present in it may be addressed and nothing
// about its customers is knowable from here.
func (s *Server) handleListRoster(w http.ResponseWriter, r *http.Request) {
	entries, err := s.network().ListRosterEntries(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]rosterEntryDTO, len(entries))
	for i, e := range entries {
		assets := make([]string, len(e.Assets))
		for j, a := range e.Assets {
			assets[j] = string(a)
		}
		out[i] = rosterEntryDTO{
			BIC:          string(e.BIC),
			Assets:       assets,
			AdmissionRef: e.AdmissionRef,
			AdmittedAt:   e.AdmittedAt,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAddIdentifier(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req identifierDTO
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	err := p.Deposit.AddIdentifier(r.Context(), deposit.AccountID(r.PathValue("did")),
		deposit.Identifier{Scheme: deposit.IdentifierScheme(req.Scheme), Value: req.Value})
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRemoveIdentifier(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	err := p.Deposit.RemoveIdentifier(r.Context(), deposit.AccountID(r.PathValue("did")),
		deposit.Identifier{
			Scheme: deposit.IdentifierScheme(r.PathValue("scheme")),
			Value:  r.PathValue("value"),
		})
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
