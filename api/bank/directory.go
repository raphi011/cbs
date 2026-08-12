package bank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/payment"
)

// A bank's two directories, and the per-account identifier endpoints that
// populate the first of them.
func (s *surface) registerBankIdentifierRoutes(mux *api.Router) {
	mux.HandleFunc("POST /deposit-accounts/{did}/identifiers", handleBody(s, http.StatusNoContent, s.handleAddIdentifier))
	mux.HandleFunc("DELETE /deposit-accounts/{did}/identifiers/{scheme}/{value}", handle(s, http.StatusNoContent, s.handleRemoveIdentifier))
}

func (s *surface) handleResolveIdentifier(r *http.Request) (api.AccountDirectoryEntryDTO, error) {
	scheme := r.URL.Query().Get("scheme")
	value := r.URL.Query().Get("value")
	if scheme == "" || value == "" {
		return api.AccountDirectoryEntryDTO{}, api.BadRequest("scheme and value are both required")
	}
	ident := deposit.Identifier{Scheme: deposit.IdentifierScheme(scheme), Value: value}
	// The listener's own bank, and it is the whole of the scope. It is not passed:
	// this institution's network IS the bank's, so there is no argument here that
	// could name another one.
	ref, err := s.network().ResolveIdentifier(r.Context(), ident)
	if err != nil {
		return api.AccountDirectoryEntryDTO{}, err
	}
	return api.AccountDirectoryEntryDTO{
		Agent:      string(s.boundBIC()),
		Account:    string(ref.Account),
		Identifier: api.IdentifierDTO{Scheme: scheme, Value: value},
	}, nil
}

// handleResolveBankCode answers GET /directory/banks, and how it is narrowed
// decides which question it answers.
func (s *surface) handleResolveBankCode(r *http.Request) (any, error) {
	q := r.URL.Query()
	country, bankCode, address := q.Get("country"), q.Get("bankCode"), q.Get("iban")
	if address != "" && (country != "" || bankCode != "") {
		return nil, api.BadRequest("iban names an allocation, so it is not given with country and bankCode")
	}
	if (country == "") != (bankCode == "") {
		return nil, api.BadRequest("country and bankCode are given together or not at all")
	}
	if country == "" && address == "" {
		entries, err := s.network().ListDirectory(r.Context())
		if err != nil {
			return nil, err
		}
		return routingEntryDTOs(entries), nil
	}
	issuer := iban.Issuer{Country: iban.Country(country), BankCode: iban.BankCode(bankCode)}
	if address != "" {
		parsed, err := iban.Parse(address)
		if err != nil {
			return nil, err
		}
		code, err := parsed.BankCode()
		if err != nil {
			return nil, err
		}
		issuer = iban.Issuer{Country: parsed.Country(), BankCode: code}
	}
	e, err := s.network().ResolveBankCode(r.Context(), issuer)
	if err != nil {
		return nil, err
	}
	return api.RoutingEntryOf(e), nil
}

// handleRefreshDirectory is this bank subscribing: pull the scheme's published
// roster and replace this bank's copy with it.
func (s *surface) handleRefreshDirectory(r *http.Request) ([]api.RoutingEntryDTO, error) {
	entries, err := s.inst.RefreshDirectory(r.Context())
	if err != nil {
		return nil, err
	}
	return routingEntryDTOs(entries), nil
}

func routingEntryDTOs(entries []payment.DirectoryEntry) []api.RoutingEntryDTO {
	out := make([]api.RoutingEntryDTO, len(entries))
	for i, e := range entries {
		out[i] = api.RoutingEntryOf(e)
	}
	return out
}

func (s *surface) handleAddIdentifier(r *http.Request, p *payment.Bank, req api.IdentifierDTO) (any, error) {
	return nil, p.Deposit.AddIdentifier(r.Context(), deposit.AccountID(r.PathValue("did")),
		deposit.Identifier{Scheme: deposit.IdentifierScheme(req.Scheme), Value: req.Value})
}

func (s *surface) handleRemoveIdentifier(r *http.Request, p *payment.Bank) (any, error) {
	return nil, p.Deposit.RemoveIdentifier(r.Context(), deposit.AccountID(r.PathValue("did")),
		deposit.Identifier{
			Scheme: deposit.IdentifierScheme(r.PathValue("scheme")),
			Value:  r.PathValue("value"),
		})
}
