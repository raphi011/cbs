package api

import (
	"net/http"

	"github.com/raphi011/cbs/deposit"
)

// Account addressing: the network directory, and the per-account identifier
// endpoints that populate it.
//
// GET /directory is network-scoped rather than participant-scoped on purpose:
// resolving an address is exactly the question "which bank?", so a route that
// already named the bank would answer nothing.
func (s *Server) registerDirectoryRoutes(mux *router) {
	mux.HandleFunc("GET /directory", s.handleResolveIdentifier)
	s.registerBankIdentifierRoutes(mux)
}

// registerBankIdentifierRoutes is the half of the directory that belongs to one
// bank: issuing and withdrawing the addresses of its own accounts. Resolving an
// address is the network-wide half and is registered separately, because the
// two answer different questions — "give this account an IBAN" is the account's
// bank's act, and "who holds this IBAN?" is a question about everybody.
func (s *Server) registerBankIdentifierRoutes(mux *router) {
	mux.HandleFunc("POST /participants/{pid}/deposit-accounts/{did}/identifiers", s.handleAddIdentifier)
	mux.HandleFunc("DELETE /participants/{pid}/deposit-accounts/{did}/identifiers/{scheme}/{value}", s.handleRemoveIdentifier)
}

// directoryEntryDTO is what GET /directory answers: enough to tell a caller
// who an address belongs to before they pay it — which bank, which account,
// its display name and the asset it holds — plus the identifier that was
// resolved, echoed back for a client that fired several lookups at once.
type directoryEntryDTO struct {
	Participant string        `json:"participant"`
	Account     string        `json:"account"`
	Name        string        `json:"name"`
	Asset       string        `json:"asset"`
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
	ref, err := s.network().ResolveIdentifier(r.Context(), ident)
	if err != nil {
		writeError(w, err)
		return
	}
	// The name and asset need the account itself; the ref only names it.
	p, err := s.network().GetParticipant(r.Context(), ref.Participant)
	if err != nil {
		writeError(w, err)
		return
	}
	acct, err := p.Deposit.GetAccount(r.Context(), ref.Account)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, directoryEntryDTO{
		Participant: string(ref.Participant),
		Account:     string(ref.Account),
		Name:        acct.Name,
		Asset:       string(acct.Asset),
		Identifier:  identifierDTO{Scheme: scheme, Value: value},
	})
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
