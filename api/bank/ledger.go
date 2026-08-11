package bank

import (
	"net/http"
	"time"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/ledger"
)

func (s *surface) registerLedgerRoutes(mux *api.Router) {
	mux.HandleFunc("POST /ledgers", s.handleCreateLedger)
	mux.HandleFunc("GET /ledgers", s.handleListLedgers)
	mux.HandleFunc("GET /ledgers/{lid}", s.handleGetLedger)

	mux.HandleFunc("POST /ledgers/{lid}/subledgers", s.handleCreateSubledger)
	mux.HandleFunc("GET /ledgers/{lid}/subledgers", s.handleListSubledgers)
	mux.HandleFunc("GET /subledgers/{sid}", s.handleGetSubledger)

	mux.HandleFunc("POST /subledgers/{sid}/accounts", s.handleCreateAccount)
	mux.HandleFunc("GET /subledgers/{sid}/accounts", s.handleListAccounts)
	mux.HandleFunc("GET /accounts/{aid}", s.handleGetAccount)
	mux.HandleFunc("GET /accounts/{aid}/balance", s.handleBookBalance)
	mux.HandleFunc("GET /accounts/{aid}/subsidiaries", s.handleAccountSubsidiaries)

	mux.HandleFunc("POST /transactions", s.handlePostTransaction)
	mux.HandleFunc("GET /transactions", s.handleListTransactions)
	mux.HandleFunc("GET /transactions/{tid}", s.handleGetTransaction)
	mux.HandleFunc("POST /transactions/{tid}/reversal", s.handleReverseTransaction)
}

func (s *surface) handleCreateLedger(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req api.NameRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	l, err := p.Ledger.CreateLedger(r.Context(), req.Name)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, api.ToLedgerDTO(l))
}

func (s *surface) handleListLedgers(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	ledgers, err := p.Ledger.ListLedgers(r.Context())
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := make([]api.LedgerDTO, len(ledgers))
	for i, l := range ledgers {
		out[i] = api.ToLedgerDTO(l)
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func (s *surface) handleGetLedger(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	l, err := p.Ledger.GetLedger(r.Context(), ledger.LedgerID(r.PathValue("lid")))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ToLedgerDTO(l))
}

func (s *surface) handleCreateSubledger(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req api.NameRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	sl, err := p.Ledger.CreateSubledger(r.Context(), ledger.LedgerID(r.PathValue("lid")), req.Name)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, api.ToSubledgerDTO(sl))
}

func (s *surface) handleListSubledgers(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	subs, err := p.Ledger.ListSubledgers(r.Context(), ledger.LedgerID(r.PathValue("lid")))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := make([]api.SubledgerDTO, len(subs))
	for i, sl := range subs {
		out[i] = api.ToSubledgerDTO(sl)
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func (s *surface) handleGetSubledger(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	sl, err := p.Ledger.GetSubledger(r.Context(), ledger.SubledgerID(r.PathValue("sid")))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ToSubledgerDTO(sl))
}

func (s *surface) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req api.CreateAccountRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	acctType, err := api.AccountTypeFromString(req.Type)
	if err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	if req.Asset == "" {
		api.WriteBadRequest(w, "asset is required")
		return
	}
	acct, err := p.Ledger.CreateAccount(r.Context(), ledger.SubledgerID(r.PathValue("sid")), req.Name, acctType, ledger.AssetCode(req.Asset))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, api.ToAccountDTO(acct))
}

func (s *surface) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	accts, err := p.Ledger.ListAccounts(r.Context(), ledger.SubledgerID(r.PathValue("sid")))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := make([]api.AccountDTO, len(accts))
	for i, a := range accts {
		out[i] = api.ToAccountDTO(a)
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func (s *surface) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	acct, err := p.Ledger.GetAccount(r.Context(), ledger.AccountID(r.PathValue("aid")))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ToAccountDTO(acct))
}

func (s *surface) handleBookBalance(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	aid := ledger.AccountID(r.PathValue("aid"))
	// Parsed before anything is read: a malformed asOf is a 400 whatever the
	// store says, so validating it first keeps a bad request from costing two
	// store round trips before being refused. Omitted, it defaults to now, and
	// the value-dated figure agrees with the book balance unless something is
	// value-dated away from its booking date.
	asOf := time.Now()
	if raw := r.URL.Query().Get("asOf"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			api.WriteBadRequest(w, "asOf must be an RFC 3339 timestamp")
			return
		}
		asOf = parsed
	}
	// The asset comes back with the number. It is an integer in the account's
	// minor units and cannot be rendered without the scale its code implies;
	// leaving it out would make displaying one balance cost three requests,
	// and would put the digits on screen before the thing that gives them a
	// magnitude.
	acct, err := p.Ledger.GetAccount(r.Context(), aid)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	// A subsidiary's balance, or the whole account's when none is named. On a
	// control account the second is the sum of the first over every subsidiary,
	// which is what makes the drill-down below add up.
	pos := aid.For(r.URL.Query().Get("subsidiary"))
	bal, err := p.Ledger.BookBalance(r.Context(), pos)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	valueDated, err := p.Ledger.ValueDateBalance(r.Context(), pos, asOf)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.AccountBalanceDTO{
		AccountID:        string(aid),
		Subsidiary:       pos.Subsidiary,
		Asset:            string(acct.Asset),
		Balance:          int64(bal),
		ValueDateBalance: int64(valueDated),
	})
}

// handleAccountSubsidiaries is the drill-down: which subsidiaries a control account
// is holding money for, and how much of the line is each one's.
//
// A plain account answers with an empty list rather than a 404 or an error. It
// pools nobody, so there is no detail under it — and a client rendering an
// account page should not have to know which kind it is before it asks.
func (s *surface) handleAccountSubsidiaries(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	aid := ledger.AccountID(r.PathValue("aid"))
	acct, err := p.Ledger.GetAccount(r.Context(), aid)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	rows, err := p.Ledger.SubsidiaryBalances(r.Context(), aid)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := make([]api.SubsidiaryBalanceDTO, len(rows))
	for i, row := range rows {
		out[i] = api.SubsidiaryBalanceDTO{
			Subsidiary: row.Subsidiary,
			Asset:      string(acct.Asset),
			Balance:    int64(row.Balance),
		}
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func (s *surface) handlePostTransaction(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req api.PostTransactionRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	domainReq, err := req.ToDomain()
	if err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	tx, err := p.Ledger.PostTransaction(r.Context(), domainReq)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	assets, err := api.EntryAssets(r.Context(), p.Ledger, []ledger.Transaction{tx})
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, api.ToTransactionDTO(tx, assets))
}

func (s *surface) handleListTransactions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	// account alone is the WHOLE account, which on a control line is every
	// subsidiary's postings; account plus subsidiary is one of them. That is the
	// same rule ledger.Position states, and it is why there is no separate
	// route for a customer's statement: a customer is a position.
	var txs []ledger.Transaction
	var err error
	if acct := r.URL.Query().Get("account"); acct != "" {
		pos := ledger.AccountID(acct).For(r.URL.Query().Get("subsidiary"))
		txs, err = p.Ledger.ListTransactionsForPosition(r.Context(), pos)
	} else {
		txs, err = p.Ledger.ListTransactions(r.Context())
	}
	if err != nil {
		api.WriteError(w, err)
		return
	}
	assets, err := api.EntryAssets(r.Context(), p.Ledger, txs)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := make([]api.TransactionDTO, len(txs))
	for i, tx := range txs {
		out[i] = api.ToTransactionDTO(tx, assets)
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func (s *surface) handleGetTransaction(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	tx, err := p.Ledger.GetTransaction(r.Context(), ledger.TransactionID(r.PathValue("tid")))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	assets, err := api.EntryAssets(r.Context(), p.Ledger, []ledger.Transaction{tx})
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ToTransactionDTO(tx, assets))
}

func (s *surface) handleReverseTransaction(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req api.DescriptionRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	tx, err := p.Ledger.ReverseTransaction(r.Context(), ledger.TransactionID(r.PathValue("tid")), req.Description)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	assets, err := api.EntryAssets(r.Context(), p.Ledger, []ledger.Transaction{tx})
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, api.ToTransactionDTO(tx, assets))
}
