package api

import (
	"net/http"

	"github.com/raphi011/ledger"
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

	mux.HandleFunc("GET /participants/{pid}/audit", s.handleLedgerAudit)
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
	l, err := p.Ledger.CreateLedger(req.Name)
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
	ledgers := p.Ledger.ListLedgers()
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
	l, err := p.Ledger.GetLedger(ledger.LedgerID(r.PathValue("lid")))
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
	sl, err := p.Ledger.CreateSubledger(ledger.LedgerID(r.PathValue("lid")), req.Name)
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
	subs := p.Ledger.ListSubledgers(ledger.LedgerID(r.PathValue("lid")))
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
	sl, err := p.Ledger.GetSubledger(ledger.SubledgerID(r.PathValue("sid")))
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
	acct, err := p.Ledger.CreateAccount(ledger.SubledgerID(r.PathValue("sid")), req.Name, acctType)
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
	accts := p.Ledger.ListAccounts(ledger.SubledgerID(r.PathValue("sid")))
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
	acct, err := p.Ledger.GetAccount(ledger.AccountID(r.PathValue("aid")))
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
	bal, err := p.Ledger.BookBalance(aid)
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
	tx, err := p.Ledger.PostTransaction(domainReq)
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
	if acct := r.URL.Query().Get("account"); acct != "" {
		txs = p.Ledger.ListTransactionsForAccount(ledger.AccountID(acct))
	} else {
		txs = p.Ledger.ListTransactions()
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
	tx, err := p.Ledger.GetTransaction(ledger.TransactionID(r.PathValue("tid")))
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
	tx, err := p.Ledger.ReverseTransaction(ledger.TransactionID(r.PathValue("tid")), req.Description)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTransactionDTO(tx))
}

func (s *Server) handleLedgerAudit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var events []ledger.AuditEvent
	if entity := r.URL.Query().Get("entity"); entity != "" {
		events = p.Ledger.GetAuditLogForEntity(entity)
	} else {
		events = p.Ledger.GetAuditLog()
	}
	out := make([]auditEventDTO, len(events))
	for i, e := range events {
		out[i] = toLedgerAuditDTO(e)
	}
	writeJSON(w, http.StatusOK, out)
}
