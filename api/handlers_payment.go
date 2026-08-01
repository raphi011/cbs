package api

import (
	"context"
	"net/http"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// mandateAsset resolves a mandate's asset from the debtor's own deposit
// account. A mandate carries no scheme (see toMandateDTO), so there is no
// scheme to derive an asset from the way a payment's is; what a mandate does
// fix at creation is the debtor account being authorized, and that account's
// own asset (see deposit.Account.Asset) is what MaxAmount is denominated in.
func (s *Server) mandateAsset(ctx context.Context, m payment.Mandate) (string, error) {
	assets, err := s.mandateAssets(ctx, []payment.Mandate{m})
	if err != nil {
		return "", err
	}
	return assets[m.ID], nil
}

// mandateAssets resolves a whole batch of mandates' assets at once.
//
// The single-mandate path costs two store.View calls — a full BEGIN…COMMIT
// each on store/pg — so calling it once per row turned GET /mandates into
// 2N+1 round trips for what is a handful of reads. This is the same batch
// shape as entryAssets (see api/dto_ledger.go): resolve per distinct
// *participant* rather than per row, and read that participant's deposit
// accounts in one listing rather than one at a time. A listing of any number
// of mandates over k banks now costs k+1 round trips, and k is the number of
// member banks, not the number of results.
func (s *Server) mandateAssets(ctx context.Context, mandates []payment.Mandate) (map[payment.MandateID]string, error) {
	byParticipant := make(map[payment.ParticipantID]map[deposit.AccountID]ledger.AssetCode)
	for _, m := range mandates {
		if _, done := byParticipant[m.Debtor.Participant]; done {
			continue
		}
		p, err := s.network().GetParticipant(ctx, m.Debtor.Participant)
		if err != nil {
			return nil, err
		}
		accounts, err := p.Deposit.ListAccounts(ctx)
		if err != nil {
			return nil, err
		}
		byAccount := make(map[deposit.AccountID]ledger.AssetCode, len(accounts))
		for _, a := range accounts {
			byAccount[a.ID] = a.Asset
		}
		byParticipant[m.Debtor.Participant] = byAccount
	}

	out := make(map[payment.MandateID]string, len(mandates))
	for _, m := range mandates {
		asset, ok := byParticipant[m.Debtor.Participant][m.Debtor.Account]
		if !ok {
			// The mandate names an account that is not in its debtor's book.
			// GetAccount is what says so, in the vocabulary the rest of the
			// package maps to a status code.
			p, err := s.network().GetParticipant(ctx, m.Debtor.Participant)
			if err != nil {
				return nil, err
			}
			if _, err := p.Deposit.GetAccount(ctx, m.Debtor.Account); err != nil {
				return nil, err
			}
			return nil, deposit.ErrAccountNotFound
		}
		out[m.ID] = string(asset)
	}
	return out, nil
}

// settlementAsset resolves a settlement's asset via its cycle's scheme — a
// settlement carries a CycleID but no scheme of its own (see toSettlementDTO).
func (s *Server) settlementAsset(ctx context.Context, st payment.Settlement) (string, error) {
	c, err := s.network().GetCycle(ctx, st.CycleID)
	if err != nil {
		return "", err
	}
	return schemeAsset(c.Scheme, s.network().ListSchemes()), nil
}

// settlementAssets resolves a whole batch of settlements' assets at once.
//
// Per row, settlementAsset costs a GetCycle (one store.View) plus a fresh
// ListSchemes (which takes the network's lock). One ListCycles reads every
// cycle in a single View and the scheme list is fetched once, so a listing of
// N settlements costs 2 round trips rather than N+1.
func (s *Server) settlementAssets(ctx context.Context, settlements []payment.Settlement) (map[payment.SettlementID]string, error) {
	cycles, err := s.network().ListCycles(ctx)
	if err != nil {
		return nil, err
	}
	schemes := s.network().ListSchemes()
	byCycle := make(map[payment.CycleID]payment.SchemeID, len(cycles))
	for _, c := range cycles {
		byCycle[c.ID] = c.Scheme
	}

	out := make(map[payment.SettlementID]string, len(settlements))
	for _, st := range settlements {
		scheme, ok := byCycle[st.CycleID]
		if !ok {
			return nil, payment.ErrCycleNotFound
		}
		out[st.ID] = schemeAsset(scheme, schemes)
	}
	return out, nil
}

func (s *Server) registerPaymentRoutes(mux *router) {
	mux.HandleFunc("POST /mandates", s.handleCreateMandate)
	mux.HandleFunc("GET /mandates", s.handleListMandates)
	mux.HandleFunc("GET /mandates/{mid}", s.handleGetMandate)
	mux.HandleFunc("POST /mandates/{mid}/revoke", s.handleRevokeMandate)

	mux.HandleFunc("POST /payments", s.handleInitiatePayment)
	mux.HandleFunc("GET /payments", s.handleListPayments)
	mux.HandleFunc("GET /payments/{payid}", s.handleGetPayment)
	mux.HandleFunc("POST /payments/{payid}/reject", s.handleRejectPayment)
	mux.HandleFunc("POST /payments/{payid}/return", s.handleReturnPayment)

	mux.HandleFunc("POST /cycles", s.handleOpenCycle)
	mux.HandleFunc("GET /cycles", s.handleListCycles)
	mux.HandleFunc("GET /cycles/{cid}", s.handleGetCycle)
	mux.HandleFunc("POST /cycles/{cid}/close", s.handleCloseCycle)

	mux.HandleFunc("GET /settlements", s.handleListSettlements)
	mux.HandleFunc("GET /settlements/{sid}", s.handleGetSettlement)
}

func (s *Server) handleCreateMandate(w http.ResponseWriter, r *http.Request) {
	var req createMandateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	m, err := s.network().CreateMandate(r.Context(), req.Debtor.toDomain(), req.Creditor.toDomain(), ledger.Amount(req.MaxAmount))
	if err != nil {
		writeError(w, err)
		return
	}
	asset, err := s.mandateAsset(r.Context(), m)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toMandateDTO(m, asset))
}

func (s *Server) handleListMandates(w http.ResponseWriter, r *http.Request) {
	mandates, err := s.network().ListMandates(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	assets, err := s.mandateAssets(r.Context(), mandates)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]mandateDTO, len(mandates))
	for i, m := range mandates {
		out[i] = toMandateDTO(m, assets[m.ID])
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetMandate(w http.ResponseWriter, r *http.Request) {
	m, err := s.network().GetMandate(r.Context(), payment.MandateID(r.PathValue("mid")))
	if err != nil {
		writeError(w, err)
		return
	}
	asset, err := s.mandateAsset(r.Context(), m)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toMandateDTO(m, asset))
}

func (s *Server) handleRevokeMandate(w http.ResponseWriter, r *http.Request) {
	if err := s.network().RevokeMandate(r.Context(), payment.MandateID(r.PathValue("mid"))); err != nil {
		writeError(w, err)
		return
	}
	m, err := s.network().GetMandate(r.Context(), payment.MandateID(r.PathValue("mid")))
	if err != nil {
		writeError(w, err)
		return
	}
	asset, err := s.mandateAsset(r.Context(), m)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toMandateDTO(m, asset))
}

// initiateWholePayment runs all three halves of an initiation — the
// submitting bank's, the receiving bank's and the clearing house's — in one
// unit of work.
//
// One process playing all three actors is exactly what the mesh replaces, and
// this route is the last place it still happens: the operator's POST /payments
// answers 201 with an Accepted payment, so it has to perform the whole
// choreography itself. Sub-project 7b's api handoff is what removes it.
//
// The three calls share a Tx deliberately. Run as three units of work, an
// instruction the far side refuses would leave an Initiated payment behind
// with the payer's money already in suspense — a real outcome in the mesh,
// where the two banks genuinely are two actors, but not one this synchronous
// route has ever produced or its callers know how to read.
func (s *Server) initiateWholePayment(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error) {
	net := s.network()
	var out payment.Payment
	err := net.Store().Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		p, err := net.SubmitPaymentTx(ctx, tx, req)
		if err != nil {
			return err
		}
		if err := net.AcceptInboundTx(ctx, tx, p); err != nil {
			return err
		}
		out, err = net.AcceptAtCSMTx(ctx, tx, p.ID)
		return err
	})
	return out, err
}

func (s *Server) handleInitiatePayment(w http.ResponseWriter, r *http.Request) {
	var req initiatePaymentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	p, err := s.initiateWholePayment(r.Context(), req.toDomain())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPaymentDTO(p, s.network().ListSchemes()))
}

func (s *Server) handleListPayments(w http.ResponseWriter, r *http.Request) {
	payments, err := s.network().ListPayments(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	schemes := s.network().ListSchemes()
	out := make([]paymentDTO, len(payments))
	for i, p := range payments {
		out[i] = toPaymentDTO(p, schemes)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetPayment(w http.ResponseWriter, r *http.Request) {
	p, err := s.network().GetPayment(r.Context(), payment.PaymentID(r.PathValue("payid")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPaymentDTO(p, s.network().ListSchemes()))
}

func (s *Server) handleRejectPayment(w http.ResponseWriter, r *http.Request) {
	var req reasonRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	// An operator-initiated rejection carries no more specific external status
	// reason than MS03: the API exposes no way for a caller to name a code, so
	// there is no more honest choice than the one that says exactly that.
	p, err := s.network().RejectPayment(r.Context(), payment.PaymentID(r.PathValue("payid")), iso20022.StatusReasonNotSpecifiedAgentGenerated, req.Reason)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPaymentDTO(p, s.network().ListSchemes()))
}

func (s *Server) handleReturnPayment(w http.ResponseWriter, r *http.Request) {
	var req reasonRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	p, err := s.network().ReturnPayment(r.Context(), payment.PaymentID(r.PathValue("payid")), req.Reason)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPaymentDTO(p, s.network().ListSchemes()))
}

func (s *Server) handleOpenCycle(w http.ResponseWriter, r *http.Request) {
	var req openCycleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	c, err := s.network().OpenCycle(r.Context(), payment.SchemeID(req.Scheme))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toClearingCycleDTO(c, s.network().ListSchemes()))
}

func (s *Server) handleListCycles(w http.ResponseWriter, r *http.Request) {
	cycles, err := s.network().ListCycles(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	schemes := s.network().ListSchemes()
	out := make([]clearingCycleDTO, len(cycles))
	for i, c := range cycles {
		out[i] = toClearingCycleDTO(c, schemes)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetCycle(w http.ResponseWriter, r *http.Request) {
	c, err := s.network().GetCycle(r.Context(), payment.CycleID(r.PathValue("cid")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toClearingCycleDTO(c, s.network().ListSchemes()))
}

func (s *Server) handleCloseCycle(w http.ResponseWriter, r *http.Request) {
	c, err := s.network().CloseCycle(r.Context(), payment.CycleID(r.PathValue("cid")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toClearingCycleDTO(c, s.network().ListSchemes()))
}

// handleSettleCycle answers POST /settlements on the CENTRAL BANK.
//
// The cycle is the input and a settlement is the resource created, which is why
// it is not an action on a cycle. The operator matters more: settlement moves
// reserves between accounts in the central bank's own book, and a clearing
// house that could do that would be a central bank. Before the operator split
// the CSM settled directly, because there was one server and nothing in the
// shape of the API could say otherwise.
//
// What is deliberately not modelled is the instruction — the message in which
// the clearing house tells the central bank which cycle to settle. Today it is
// out of band: an operator closes a cycle on one console and settles it on
// another.
func (s *Server) handleSettleCycle(w http.ResponseWriter, r *http.Request) {
	var req settleCycleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	settlement, err := s.network().SettleCycle(r.Context(), payment.CycleID(req.CycleID))
	if err != nil {
		writeError(w, err)
		return
	}
	asset, err := s.settlementAsset(r.Context(), settlement)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSettlementDTO(settlement, asset))
}

func (s *Server) handleListSettlements(w http.ResponseWriter, r *http.Request) {
	settlements, err := s.network().ListSettlements(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	assets, err := s.settlementAssets(r.Context(), settlements)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]settlementDTO, len(settlements))
	for i, st := range settlements {
		out[i] = toSettlementDTO(st, assets[st.ID])
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetSettlement(w http.ResponseWriter, r *http.Request) {
	st, err := s.network().GetSettlement(r.Context(), payment.SettlementID(r.PathValue("sid")))
	if err != nil {
		writeError(w, err)
		return
	}
	asset, err := s.settlementAsset(r.Context(), st)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSettlementDTO(st, asset))
}
