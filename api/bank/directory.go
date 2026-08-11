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
//
// TWO SIBLING QUESTIONS, and the URLs say which is which. GET /directory/accounts
// asks which of THIS bank's accounts holds an address, out of a register this
// bank owns. GET /directory/banks asks which institution answers for a bank code,
// out of a COPY of a table this bank does not own and pulled from the clearing
// house. Both are lookups an address goes through and neither can stand in for
// the other: no bank can resolve another bank's customer, and no bank can be told
// where to send a message except by a directory somebody published.
//
// What is still absent is the third question. Neither route answers a NAME for a
// party at another institution: the copy has none, because the roster has none,
// because the acknowledgement it is written from delivers none. That absence
// arrives where a payer most expects a name.
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
	// could name another one. Driven by an institution that is not a bank it
	// answers payment.ErrNotThisInstitutionsAct.
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
//
// Unnarrowed it lists the whole copy, which is what a bank's console shows
// beside the refresh. Narrowed by country and bankCode it resolves ONE
// allocation, which is what a caller already holding one asks.
//
// # ?iban= is the third, and it exists because the structure table is Go's
//
// A send form has an ADDRESS, not an allocation, and pulling the allocation out
// of one takes the country table: variable-length codes at country-dependent
// offsets, which is the fact the whole of iban exists for. A browser narrowing
// this route itself would need that table, and a second copy of it with nothing
// holding the two together is precisely what iban's doc refuses. So the address
// is handed over whole and read here, by the same package that minted it — the
// caller's half stays mod-97, which needs no table and is the check a form runs
// per keystroke.
//
// An address that will not parse is 422, from the same group as every other
// malformed address; it is not a 400, because the parameter is present and
// well-typed and what is wrong with it is a rule.
//
// All three are answered from THIS bank's copy and from nothing else — the same
// read submission makes, so what a form shows and where the payment goes cannot
// disagree. A miss is payment.ErrBankCodeUnknown and 422, and it cannot say
// whether no such bank is in the scheme or this copy is simply behind.
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
//
// It goes through the MESH rather than through this listener's own network,
// because the roster is the clearing house's table in the clearing house's
// database and no bank may open it. What the mesh stands in for is the vendor
// delivering a file; see cmd/server's Mesh.RefreshDirectory.
//
// It answers 200 with the new copy rather than 202, and that is the difference
// between this and every other route here that reaches two institutions. A
// refresh is not a conversation: nothing is sent, nobody answers later, and by
// the time this returns the copy is the one the next payment will route from.
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
