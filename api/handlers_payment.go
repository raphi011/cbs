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

// handleInitiatePayment hands a payment instruction to the bank whose act it is,
// and answers with what that bank did.
//
// # What it used to be, and why the status code changed
//
// It used to run all three halves of an initiation — the submitting bank's, the
// receiving bank's and the clearing house's — in a single unit of work, so that
// it could answer 201 with an Accepted payment. One process playing three
// institutions is exactly what the mesh replaces, and this route was the last
// place it still happened.
//
// It now calls Mesh.Submit, which runs the submitting bank's half and sends. The
// counterparty's answer and the clearing house's acceptance arrive later, at
// other actors, as messages. So the payment in this response is INITIATED, in no
// cycle, and unseen by the far side — and 202 is what says that: the instruction
// has been taken in, and its fate is a separate question. A 201 Created here
// would still be defensible about the resource and dishonest about everything a
// caller reads next.
//
// # Which errors can come back this way, and which cannot
//
// Submit is synchronous up to the send, so everything the payer's own bank
// decides is answerable here, with a status code, in this response: no funds, an
// account that is not theirs, a mandate that does not authorise the collection,
// a scheme nobody has registered. Those are 4xx and they are the whole of what
// this handler can refuse.
//
// Everything after the send is not. A payee's bank that cannot apply the credit,
// a clearing house with no open cycle, a settlement agent short of reserves —
// each of those decides about a payment whose HTTP request finished long ago,
// and each answers with a pacs.002 rather than a status code. That is the
// difference the mesh makes, and it is why the client shape is "here is an
// identifier, ask again". Nothing is dropped: the payment's own row is where the
// answer lands, and a failure nobody could be told about is a mesh dead letter.
//
// # This is the clearing house's console, and it does not submit
//
// The route lives on the clearing house's surface because that is the operator
// who can see every payment in the network, but the WORK is a member bank's:
// Mesh.Submit reads the scheme's direction and hands the instruction to the
// payer's bank on a push and the payee's on a pull. A customer's own instruction
// goes to their bank's own POST /payments (see handleSubmitPayment), which is
// the only door a retail client has.
func (s *Server) handleInitiatePayment(w http.ResponseWriter, r *http.Request) {
	var req initiatePaymentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	p, err := s.mesh.Submit(r.Context(), req.toDomain())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toPaymentDTO(p, s.network().ListSchemes()))
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

// handleRejectPayment declines an in-flight payment on the operator's say-so.
//
// It used to run both halves of a rejection — the clearing house's and the
// payer's bank's — in one unit of work, so that it could answer 200 with a
// payment whose payer had already been refunded. Two institutions cannot share a
// transaction, and Mesh.Reject is what that became.
//
// So the payment in this response is REJECTED and out of its cycle, which is the
// clearing house's own half and really has happened. The payer's money is still
// in their bank's clearing suspense when this is written: giving it back is that
// bank's act, in that bank's book, and it happens when the pacs.002 reaches it.
// 202 is what says so.
//
// A rejection the clearing house refuses — a payment that has already settled, a
// payment id that names nothing — is decided inside its own unit of work and
// comes back here as a 4xx. A refund that then fails at the payer's bank cannot:
// nobody is left to tell, so it becomes a mesh dead letter and the payment's own
// row is where it shows, Rejected with the money still in suspense.
func (s *Server) handleRejectPayment(w http.ResponseWriter, r *http.Request) {
	var req reasonRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	// An operator-initiated rejection carries no more specific external status
	// reason than MS03: the API exposes no way for a caller to name a code, so
	// there is no more honest choice than the one that says exactly that.
	p, err := s.mesh.Reject(r.Context(), payment.PaymentID(r.PathValue("payid")), iso20022.StatusReasonNotSpecifiedAgentGenerated, req.Reason)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toPaymentDTO(p, s.network().ListSchemes()))
}

// handleReturnPayment sends a settled payment back.
//
// The returning bank is not named in the request and is not this operator: a
// return is sent by the bank that RECEIVED the original instruction — the
// payee's bank on a push, the payer's on a pull — and Mesh.Return works that out
// from the payment's own scheme. What that bank does is build a pacs.004 and
// send it; the three compensating postings are the settlement agent's, four hops
// away, because the middle one moves central-bank reserves.
//
// # It answers with an identifier and no payment
//
// Deliberately, and it is the one response on this surface that carries no
// resource. Mesh.Return returns no payment either, for the reason its doc gives:
// the returning bank's half posts nothing and decides nothing beyond whether
// there is a settled payment to return, so the only Payment there is to hand
// back is the one the caller could already read — still Settled — and re-reading
// the row after the send would be a race dressed up as a result. Ask again with
// the identifier; that is what 202 means here.
//
// The reason code is MS03 for handleRejectPayment's reason: this API gives a
// caller no way to name one, and the free text they did give travels beside it.
// The two code sets are different — a return reason answers "why is this money
// coming back" and a status reason answers "why was this refused" — which is why
// iso20022 keeps them apart and why this names the return one.
func (s *Server) handleReturnPayment(w http.ResponseWriter, r *http.Request) {
	var req reasonRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	id := payment.PaymentID(r.PathValue("payid"))
	if err := s.mesh.Return(r.Context(), id, iso20022.ReturnReasonNotSpecifiedAgentGenerated, req.Reason); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, submittedPaymentDTO{PaymentID: string(id)})
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

// handleCloseCycle reaches the cut-off, and is the only way a settlement is
// instructed in this system.
//
// It goes through the mesh rather than through the network, and that is the
// whole of what Task 12 changed here. Netting is the clearing house's own act
// and moves nothing — every payment in the batch becomes Cleared and the net
// positions are written onto the cycle — but DISCHARGING those positions moves
// central-bank reserves, which no clearing house may do. So the second step is a
// pacs.009 to the central bank, and calling payment.Network.CloseCycle directly
// from here would close the cycle and instruct nobody. There is no POST
// /settlements any more; see api/surface.go for why a second way to settle the
// same cycle was worse than none.
//
// 200 and the closed cycle, because that is a state the system is really in when
// this is written: Closed, with net positions on it. What is NOT in the response
// is the settlement — the central bank answers later, at another actor, and a
// caller that wants to know reads the cycle again. A cycle that is Closed with no
// settlement against it is an instruction the central bank refused, and the
// console is where that shows.
func (s *Server) handleCloseCycle(w http.ResponseWriter, r *http.Request) {
	c, err := s.mesh.CloseCycle(r.Context(), payment.CycleID(r.PathValue("cid")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toClearingCycleDTO(c, s.network().ListSchemes()))
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
