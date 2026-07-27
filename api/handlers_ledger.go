package api

import (
	"net/http"

	"github.com/raphi011/cbs/ledger"
)

func (s *Server) registerLedgerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /participants/{pid}/ledgers", s.handleCreateLedger)
	mux.HandleFunc("GET /participants/{pid}/ledgers", s.handleListLedgers)
	mux.HandleFunc("GET /participants/{pid}/ledgers/{lid}", s.handleGetLedger)

	mux.HandleFunc("POST /participants/{pid}/ledgers/{lid}/subledgers", s.handleCreateSubledger)
	mux.HandleFunc("GET /participants/{pid}/ledgers/{lid}/subledgers", s.handleListSubledgers)
	mux.HandleFunc("GET /participants/{pid}/subledgers/{sid}", s.handleGetSubledger)

	mux.HandleFunc("POST /participants/{pid}/subledgers/{sid}/accounts", s.handleCreateAccount)
	mux.HandleFunc("GET /participants/{pid}/subledgers/{sid}/accounts", s.handleListAccounts)
	mux.HandleFunc("GET /participants/{pid}/accounts/{aid}", s.handleGetAccount)
	mux.HandleFunc("GET /participants/{pid}/accounts/{aid}/balance", s.handleBookBalance)

	mux.HandleFunc("POST /participants/{pid}/transactions", s.handlePostTransaction)
	mux.HandleFunc("GET /participants/{pid}/transactions", s.handleListTransactions)
	mux.HandleFunc("GET /participants/{pid}/transactions/{tid}", s.handleGetTransaction)
	mux.HandleFunc("POST /participants/{pid}/transactions/{tid}/reversal", s.handleReverseTransaction)
}

func (s *Server) handleCreateLedger(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req nameRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	l, err := p.Ledger.CreateLedger(r.Context(), req.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toLedgerDTO(l))
}

func (s *Server) handleListLedgers(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	ledgers, err := p.Ledger.ListLedgers(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]ledgerDTO, len(ledgers))
	for i, l := range ledgers {
		out[i] = toLedgerDTO(l)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetLedger(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	l, err := p.Ledger.GetLedger(r.Context(), ledger.LedgerID(r.PathValue("lid")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toLedgerDTO(l))
}

func (s *Server) handleCreateSubledger(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req nameRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	sl, err := p.Ledger.CreateSubledger(r.Context(), ledger.LedgerID(r.PathValue("lid")), req.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSubledgerDTO(sl))
}

func (s *Server) handleListSubledgers(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	subs, err := p.Ledger.ListSubledgers(r.Context(), ledger.LedgerID(r.PathValue("lid")))
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]subledgerDTO, len(subs))
	for i, sl := range subs {
		out[i] = toSubledgerDTO(sl)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetSubledger(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	sl, err := p.Ledger.GetSubledger(r.Context(), ledger.SubledgerID(r.PathValue("sid")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSubledgerDTO(sl))
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req createAccountRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	acctType, err := accountTypeFromString(req.Type)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	acct, err := p.Ledger.CreateAccount(r.Context(), ledger.SubledgerID(r.PathValue("sid")), req.Name, acctType)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAccountDTO(acct))
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	accts, err := p.Ledger.ListAccounts(r.Context(), ledger.SubledgerID(r.PathValue("sid")))
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]accountDTO, len(accts))
	for i, a := range accts {
		out[i] = toAccountDTO(a)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	acct, err := p.Ledger.GetAccount(r.Context(), ledger.AccountID(r.PathValue("aid")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toAccountDTO(acct))
}

func (s *Server) handleBookBalance(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	aid := ledger.AccountID(r.PathValue("aid"))
	bal, err := p.Ledger.BookBalance(r.Context(), aid)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accountId": string(aid), "balance": bal})
}

func (s *Server) handlePostTransaction(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req postTransactionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	domainReq, err := req.toDomain()
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	tx, err := p.Ledger.PostTransaction(r.Context(), domainReq)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTransactionDTO(tx))
}

func (s *Server) handleListTransactions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var txs []ledger.Transaction
	var err error
	if acct := r.URL.Query().Get("account"); acct != "" {
		txs, err = p.Ledger.ListTransactionsForAccount(r.Context(), ledger.AccountID(acct))
	} else {
		txs, err = p.Ledger.ListTransactions(r.Context())
	}
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]transactionDTO, len(txs))
	for i, tx := range txs {
		out[i] = toTransactionDTO(tx)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetTransaction(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	tx, err := p.Ledger.GetTransaction(r.Context(), ledger.TransactionID(r.PathValue("tid")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTransactionDTO(tx))
}

func (s *Server) handleReverseTransaction(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req descriptionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	tx, err := p.Ledger.ReverseTransaction(r.Context(), ledger.TransactionID(r.PathValue("tid")), req.Description)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTransactionDTO(tx))
}
