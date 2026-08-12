package bank

import (
	"net/http"
	"time"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

func (s *surface) registerLedgerRoutes(mux *api.Router) {
	mux.HandleFunc("POST /ledgers", handleBody(s, http.StatusCreated, s.handleCreateLedger))
	mux.HandleFunc("GET /ledgers", handle(s, http.StatusOK, s.handleListLedgers))
	mux.HandleFunc("GET /ledgers/{lid}", handle(s, http.StatusOK, s.handleGetLedger))

	mux.HandleFunc("POST /ledgers/{lid}/subledgers", handleBody(s, http.StatusCreated, s.handleCreateSubledger))
	mux.HandleFunc("GET /ledgers/{lid}/subledgers", handle(s, http.StatusOK, s.handleListSubledgers))
	mux.HandleFunc("GET /subledgers/{sid}", handle(s, http.StatusOK, s.handleGetSubledger))

	mux.HandleFunc("POST /subledgers/{sid}/accounts", handleBody(s, http.StatusCreated, s.handleCreateAccount))
	mux.HandleFunc("GET /subledgers/{sid}/accounts", handle(s, http.StatusOK, s.handleListAccounts))
	mux.HandleFunc("GET /accounts/{aid}", handle(s, http.StatusOK, s.handleGetAccount))
	mux.HandleFunc("GET /accounts/{aid}/balance", handle(s, http.StatusOK, s.handleBookBalance))
	mux.HandleFunc("GET /accounts/{aid}/subsidiaries", handle(s, http.StatusOK, s.handleAccountSubsidiaries))

	mux.HandleFunc("POST /transactions", handleBody(s, http.StatusCreated, s.handlePostTransaction))
	mux.HandleFunc("GET /transactions", handle(s, http.StatusOK, s.handleListTransactions))
	mux.HandleFunc("GET /transactions/{tid}", handle(s, http.StatusOK, s.handleGetTransaction))
	mux.HandleFunc("POST /transactions/{tid}/reversal", handleBody(s, http.StatusCreated, s.handleReverseTransaction))
}

func (s *surface) handleCreateLedger(r *http.Request, p *payment.Bank, req api.NameRequest) (api.LedgerDTO, error) {
	l, err := p.Ledger.CreateLedger(r.Context(), req.Name)
	if err != nil {
		return api.LedgerDTO{}, err
	}
	return api.ToLedgerDTO(l), nil
}

func (s *surface) handleListLedgers(r *http.Request, p *payment.Bank) ([]api.LedgerDTO, error) {
	ledgers, err := p.Ledger.ListLedgers(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]api.LedgerDTO, len(ledgers))
	for i, l := range ledgers {
		out[i] = api.ToLedgerDTO(l)
	}
	return out, nil
}

func (s *surface) handleGetLedger(r *http.Request, p *payment.Bank) (api.LedgerDTO, error) {
	l, err := p.Ledger.GetLedger(r.Context(), ledger.LedgerID(r.PathValue("lid")))
	if err != nil {
		return api.LedgerDTO{}, err
	}
	return api.ToLedgerDTO(l), nil
}

func (s *surface) handleCreateSubledger(r *http.Request, p *payment.Bank, req api.NameRequest) (api.SubledgerDTO, error) {
	sl, err := p.Ledger.CreateSubledger(r.Context(), ledger.LedgerID(r.PathValue("lid")), req.Name)
	if err != nil {
		return api.SubledgerDTO{}, err
	}
	return api.ToSubledgerDTO(sl), nil
}

func (s *surface) handleListSubledgers(r *http.Request, p *payment.Bank) ([]api.SubledgerDTO, error) {
	subs, err := p.Ledger.ListSubledgers(r.Context(), ledger.LedgerID(r.PathValue("lid")))
	if err != nil {
		return nil, err
	}
	out := make([]api.SubledgerDTO, len(subs))
	for i, sl := range subs {
		out[i] = api.ToSubledgerDTO(sl)
	}
	return out, nil
}

func (s *surface) handleGetSubledger(r *http.Request, p *payment.Bank) (api.SubledgerDTO, error) {
	sl, err := p.Ledger.GetSubledger(r.Context(), ledger.SubledgerID(r.PathValue("sid")))
	if err != nil {
		return api.SubledgerDTO{}, err
	}
	return api.ToSubledgerDTO(sl), nil
}

func (s *surface) handleCreateAccount(r *http.Request, p *payment.Bank, req api.CreateAccountRequest) (api.AccountDTO, error) {
	acctType, err := api.AccountTypeFromString(req.Type)
	if err != nil {
		return api.AccountDTO{}, err
	}
	if req.Asset == "" {
		return api.AccountDTO{}, api.BadRequest("asset is required")
	}
	acct, err := p.Ledger.CreateAccount(r.Context(), ledger.SubledgerID(r.PathValue("sid")), req.Name, acctType, ledger.AssetCode(req.Asset))
	if err != nil {
		return api.AccountDTO{}, err
	}
	return api.ToAccountDTO(acct), nil
}

func (s *surface) handleListAccounts(r *http.Request, p *payment.Bank) ([]api.AccountDTO, error) {
	accts, err := p.Ledger.ListAccounts(r.Context(), ledger.SubledgerID(r.PathValue("sid")))
	if err != nil {
		return nil, err
	}
	out := make([]api.AccountDTO, len(accts))
	for i, a := range accts {
		out[i] = api.ToAccountDTO(a)
	}
	return out, nil
}

func (s *surface) handleGetAccount(r *http.Request, p *payment.Bank) (api.AccountDTO, error) {
	acct, err := p.Ledger.GetAccount(r.Context(), ledger.AccountID(r.PathValue("aid")))
	if err != nil {
		return api.AccountDTO{}, err
	}
	return api.ToAccountDTO(acct), nil
}

func (s *surface) handleBookBalance(r *http.Request, p *payment.Bank) (api.AccountBalanceDTO, error) {
	aid := ledger.AccountID(r.PathValue("aid"))
	// Parsed before anything is read: a malformed asOf is a 400 whatever the store
	// says, so validating it first keeps a bad request from costing two store
	// round trips before being refused.
	asOf := time.Now()
	if raw := r.URL.Query().Get("asOf"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return api.AccountBalanceDTO{}, api.BadRequest("asOf must be an RFC 3339 timestamp")
		}
		asOf = parsed
	}
	// The asset comes back with the number.
	acct, err := p.Ledger.GetAccount(r.Context(), aid)
	if err != nil {
		return api.AccountBalanceDTO{}, err
	}
	// A subsidiary's balance, or the whole account's when none is named. On a
	// control account the second is the sum of the first over every subsidiary,
	// which is what makes the drill-down below add up.
	pos := aid.For(r.URL.Query().Get("subsidiary"))
	bal, err := p.Ledger.BookBalance(r.Context(), pos)
	if err != nil {
		return api.AccountBalanceDTO{}, err
	}
	valueDated, err := p.Ledger.ValueDateBalance(r.Context(), pos, asOf)
	if err != nil {
		return api.AccountBalanceDTO{}, err
	}
	return api.AccountBalanceDTO{
		AccountID:        string(aid),
		Subsidiary:       pos.Subsidiary,
		Asset:            string(acct.Asset),
		Balance:          int64(bal),
		ValueDateBalance: int64(valueDated),
	}, nil
}

// handleAccountSubsidiaries is the drill-down: which subsidiaries a control
// account is holding money for, and how much of the line is each one's.
func (s *surface) handleAccountSubsidiaries(r *http.Request, p *payment.Bank) ([]api.SubsidiaryBalanceDTO, error) {
	aid := ledger.AccountID(r.PathValue("aid"))
	acct, err := p.Ledger.GetAccount(r.Context(), aid)
	if err != nil {
		return nil, err
	}
	rows, err := p.Ledger.SubsidiaryBalances(r.Context(), aid)
	if err != nil {
		return nil, err
	}
	out := make([]api.SubsidiaryBalanceDTO, len(rows))
	for i, row := range rows {
		out[i] = api.SubsidiaryBalanceDTO{
			Subsidiary: row.Subsidiary,
			Asset:      string(acct.Asset),
			Balance:    int64(row.Balance),
		}
	}
	return out, nil
}

func (s *surface) handlePostTransaction(r *http.Request, p *payment.Bank, req api.PostTransactionRequest) (api.TransactionDTO, error) {
	domainReq, err := req.ToDomain()
	if err != nil {
		return api.TransactionDTO{}, err
	}
	tx, err := p.Ledger.PostTransaction(r.Context(), domainReq)
	if err != nil {
		return api.TransactionDTO{}, err
	}
	return api.ResolveTransactionDTO(r.Context(), p.Ledger, tx)
}

func (s *surface) handleListTransactions(r *http.Request, p *payment.Bank) ([]api.TransactionDTO, error) {
	// account alone is the WHOLE account, which on a control line is every
	// subsidiary's postings; account plus subsidiary is one of them.
	var txs []ledger.Transaction
	var err error
	if acct := r.URL.Query().Get("account"); acct != "" {
		pos := ledger.AccountID(acct).For(r.URL.Query().Get("subsidiary"))
		txs, err = p.Ledger.ListTransactionsForPosition(r.Context(), pos)
	} else {
		txs, err = p.Ledger.ListTransactions(r.Context())
	}
	if err != nil {
		return nil, err
	}
	return api.ResolveTransactionDTOs(r.Context(), p.Ledger, txs)
}

func (s *surface) handleGetTransaction(r *http.Request, p *payment.Bank) (api.TransactionDTO, error) {
	tx, err := p.Ledger.GetTransaction(r.Context(), ledger.TransactionID(r.PathValue("tid")))
	if err != nil {
		return api.TransactionDTO{}, err
	}
	return api.ResolveTransactionDTO(r.Context(), p.Ledger, tx)
}

func (s *surface) handleReverseTransaction(r *http.Request, p *payment.Bank, req api.DescriptionRequest) (api.TransactionDTO, error) {
	tx, err := p.Ledger.ReverseTransaction(r.Context(), ledger.TransactionID(r.PathValue("tid")), req.Description)
	if err != nil {
		return api.TransactionDTO{}, err
	}
	return api.ResolveTransactionDTO(r.Context(), p.Ledger, tx)
}
