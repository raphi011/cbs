package api

import (
	"net/http"
	"time"

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
func (s *Server) registerBankIdentifierRoutes(mux *router) {
	mux.HandleFunc("POST /deposit-accounts/{did}/identifiers", s.handleAddIdentifier)
	mux.HandleFunc("DELETE /deposit-accounts/{did}/identifiers/{scheme}/{value}", s.handleRemoveIdentifier)
}

// accountDirectoryEntryDTO is what GET /directory/accounts answers: which of this
// bank's accounts holds the address, plus the identifier that was resolved,
// echoed back for a client that fired several lookups at once.
//
// # It carries no Name and no Asset
//
// Both would be a JOIN on top of the resolution: bind the resolved bank's
// deposit register and read the account's display name and asset out of it. On a
// BANK's port — where this route is registered — that is one bank reading
// another bank's register for the payee's name, over HTTP rather than through a
// message.
//
// Removing them is not a loss of information the caller was entitled to. A bank
// resolving its OWN customer has the name and the asset already, from every
// other route it has about that account; a bank asking about somebody else's
// gets ErrIdentifierNotFound now, and the name of another bank's customer is not
// something this system will tell it by any route at all. What a payer knows
// about a payee is what the payer typed — see payment.PartyDetails.
type accountDirectoryEntryDTO struct {
	// Agent is the BIC of the bank the address resolves at, and it is always this
	// listener's own — the lookup searches this bank's register and no other, so
	// there is no other answer it could carry. It was `participant`, off the ref
	// the domain returned; a payment.PartyRef names no bank now, and this is the
	// asking bank naming itself. See payment.ResolveIdentifierTx.
	Agent      string        `json:"agent"`
	Account    string        `json:"account"`
	Identifier identifierDTO `json:"identifier"`
}

func (s *Server) handleResolveIdentifier(w http.ResponseWriter, r *http.Request) {
	scheme := r.URL.Query().Get("scheme")
	value := r.URL.Query().Get("value")
	if scheme == "" || value == "" {
		writeBadRequest(w, "scheme and value are both required")
		return
	}
	ident := deposit.Identifier{Scheme: deposit.IdentifierScheme(scheme), Value: value}
	// The listener's own bank, and it is the whole of the scope. It is not passed:
	// this Server's network IS the bank's, so there is no argument here that could
	// name another one. Registered on a surface with no bank it answers
	// payment.ErrNotThisInstitutionsAct.
	ref, err := s.network().ResolveIdentifier(r.Context(), ident)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, accountDirectoryEntryDTO{
		Agent:      string(s.boundBIC()),
		Account:    string(ref.Account),
		Identifier: identifierDTO{Scheme: scheme, Value: value},
	})
}

// routingEntryDTO is one row of the copy of the scheme's routing directory that
// THIS bank holds. See payment.DirectoryEntry.
//
// It carries a BIC and no name, which is the whole of what the roster it was
// copied from carries. A client resolving an IBAN gets AURODEFFXXX back and
// cannot get "Aurora Bank", and that is the documented absence rather than a
// field this DTO trims.
//
// RefreshedAt is on every row rather than beside the list, because it is a fact
// about the row: a snapshot replaces the table wholesale, so every row of one
// pull carries one instant, and a client can render "refreshed 3 days ago" from
// any of them. It is the only field here that is about the COPY rather than
// about the member, and it is what makes the subscription visible in a console.
type routingEntryDTO struct {
	Country     string    `json:"country"`
	BankCode    string    `json:"bankCode"`
	BIC         string    `json:"bic"`
	RefreshedAt time.Time `json:"refreshedAt"`
}

func routingEntryOf(e payment.DirectoryEntry) routingEntryDTO {
	return routingEntryDTO{
		Country:     string(e.Issuer.Country),
		BankCode:    string(e.Issuer.BankCode),
		BIC:         string(e.BIC),
		RefreshedAt: e.RefreshedAt,
	}
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
func (s *Server) handleResolveBankCode(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	country, bankCode, address := q.Get("country"), q.Get("bankCode"), q.Get("iban")
	if address != "" && (country != "" || bankCode != "") {
		writeBadRequest(w, "iban names an allocation, so it is not given with country and bankCode")
		return
	}
	if (country == "") != (bankCode == "") {
		writeBadRequest(w, "country and bankCode are given together or not at all")
		return
	}
	if country == "" && address == "" {
		entries, err := s.network().ListDirectory(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		out := make([]routingEntryDTO, len(entries))
		for i, e := range entries {
			out[i] = routingEntryOf(e)
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	issuer := iban.Issuer{Country: iban.Country(country), BankCode: iban.BankCode(bankCode)}
	if address != "" {
		parsed, err := iban.Parse(address)
		if err != nil {
			writeError(w, err)
			return
		}
		code, err := parsed.BankCode()
		if err != nil {
			writeError(w, err)
			return
		}
		issuer = iban.Issuer{Country: parsed.Country(), BankCode: code}
	}
	e, err := s.network().ResolveBankCode(r.Context(), issuer)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, routingEntryOf(e))
}

// handleRefreshDirectory is this bank subscribing: pull the scheme's published
// roster and replace this bank's copy with it.
//
// It goes through the MESH rather than through this listener's own network,
// because the roster is the clearing house's table in the clearing house's
// database and no bank may open it. What the mesh stands in for is the vendor
// delivering a file; see mesh.Mesh.RefreshDirectory.
//
// It answers 200 with the new copy rather than 202, and that is the difference
// between this and every other route here that reaches two institutions. A
// refresh is not a conversation: nothing is sent, nobody answers later, and by
// the time this returns the copy is the one the next payment will route from.
func (s *Server) handleRefreshDirectory(w http.ResponseWriter, r *http.Request) {
	entries, err := s.mesh.RefreshDirectory(r.Context(), s.boundBIC())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]routingEntryDTO, len(entries))
	for i, e := range entries {
		out[i] = routingEntryOf(e)
	}
	writeJSON(w, http.StatusOK, out)
}

// rosterEntryDTO is one row of the clearing house's routing directory.
//
// It is what payment.RosterEntry holds and nothing more, which is the point of
// putting it on a surface at all: this row is the whole of what one institution
// knows about another in this system. An address, the assets it clears in, the
// admission it was admitted under, and when.
//
// No NAME, and its absence is domain content rather than an omission. The
// acknowledgement this row is written from carries none, so the clearing house
// has never been told one; the name a console shows beside a BIC comes from GET
// /members, which is a different question asked of a different table. See
// payment.RosterEntry.
type rosterEntryDTO struct {
	BIC string `json:"bic"`
	// The allocation this member issues its customers' addresses under, which is
	// what makes this list a routing directory rather than a list of addresses
	// the scheme will talk to. Two fields, because a bank code is unique within
	// one country and this roster has members in four.
	//
	// It is what a member COPIES. Everything else on this row stays here: the
	// assets are the clearing house's own membership check and a copy of them
	// would let a stale subscriber refuse what the clearing house would accept,
	// and the admission reference decides between two institutions contending for
	// an address, which is nobody's question but this one's.
	Country      string    `json:"country"`
	BankCode     string    `json:"bankCode"`
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
			Country:      string(e.Issuer.Country),
			BankCode:     string(e.Issuer.BankCode),
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
