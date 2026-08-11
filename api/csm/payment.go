package csm

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// Every asset on this surface comes off the ROW it is rendered from, and no
// handler resolves one.
//
// A mandate's would be its DEBTOR's bank's deposit register — one institution
// reading another's over HTTP, for a display field, out of a shape with no
// deposit register in it. A settlement's would be settlement -> its CYCLE ->
// that cycle's scheme, which is the clearing house's row read on the settlement
// agent's port. payment.Mandate and payment.Settlement each carry their Asset,
// written by the institution that always knew it, so the question is answered by
// the row and no register is opened by anybody.
func (s *surface) registerPaymentRoutes(mux *api.Router) {
	mux.HandleFunc("POST /payments", api.HandleBody(http.StatusAccepted, s.handleInitiatePayment))
	mux.HandleFunc("GET /payments", api.Handle(http.StatusOK, s.handleListPayments))
	mux.HandleFunc("GET /payments/{payid}", api.Handle(http.StatusOK, s.handleGetPayment))
	mux.HandleFunc("POST /payments/{payid}/reject", api.HandleBody(http.StatusAccepted, s.handleRejectPayment))
	mux.HandleFunc("POST /payments/{payid}/return", api.HandleBody(http.StatusAccepted, s.handleReturnPayment))

	mux.HandleFunc("POST /cycles", api.HandleBody(http.StatusCreated, s.handleOpenCycle))
	mux.HandleFunc("GET /cycles", api.Handle(http.StatusOK, s.handleListCycles))
	mux.HandleFunc("GET /cycles/{cid}", api.Handle(http.StatusOK, s.handleGetCycle))
	mux.HandleFunc("POST /cycles/{cid}/close", api.Handle(http.StatusOK, s.handleCloseCycle))
	mux.HandleFunc("POST /cycles/{cid}/settle", api.Handle(http.StatusAccepted, s.handleSettleCycle))

	// GET /settlements is NOT here: this function is the clearing house's router,
	// and a settlement is the SETTLEMENT AGENT's row. The csm shape has no
	// settlements table. It lives on the central bank's listener, next to the
	// reserves it moved; see centralBankRouter. A caller holding a cycle finds its
	// settlement by matching cycleId there — ClearingCycleDTO carries no settlement
	// id, for the reason set out on it.
}

// handleInitiatePayment hands a payment instruction to the bank whose act it is,
// and answers with what that bank did.
//
// # Why it answers 202 and not 201
//
// Running all three halves of an initiation — the submitting bank's, the
// receiving bank's and the clearing house's — in a single unit of work would be
// one process playing three institutions, which is exactly what the transport
// replaces.
//
// It calls Deployment.Submit, which runs the submitting bank's half and uploads. The
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
// difference the transport makes, and it is why the client shape is "here is an
// identifier, ask again". Nothing is dropped: the payment's own row is where the
// answer lands, and a failure nobody could be told about is a line in the day's report.
//
// # This is the clearing house's console, and it does not submit
//
// The route lives on the clearing house's surface because that is the operator
// who can see every payment in the network, but the WORK is a member bank's:
// Deployment.Submit reads the scheme's direction and hands the instruction to the
// payer's bank on a push and the payee's on a pull. A customer's own instruction
// goes to their bank's own POST /payments (see handleSubmitPayment), which is
// the only door a retail client has.
func (s *surface) handleInitiatePayment(r *http.Request, req api.InitiatePaymentRequest) (api.PaymentDTO, error) {
	dom := req.ToDomain()
	// Which bank's act this is, read out of the SUBMITTING side's address.
	//
	// An instruction names no bank at all now — the counterparty's is derived by
	// the submitting bank from its own directory copy — so this console has
	// nothing to hand the instruction to until it derives one. A bank's own port
	// fills this in from the LISTENER (handleSubmitPayment); this console is not
	// any bank, so it reads the roster it publishes instead. Same fact, two
	// sources, and each is the only one its caller has.
	//
	// A push is submitted by the payer's bank and a pull by the payee's, which is
	// the same rule Deployment.Submit applies a moment later to pick the bank.
	sc, ok := s.network().Scheme(dom.Scheme)
	if !ok {
		return api.PaymentDTO{}, payment.ErrSchemeNotFound
	}
	submitting := &dom.DebtorDetails
	address := dom.Debtor.Identifier
	if sc.Direction() == payment.Pull {
		submitting, address = &dom.CreditorDetails, dom.Creditor.Identifier
	}
	if submitting.Agent == "" {
		agent, err := s.network().RosterAgentFor(r.Context(), address)
		if err != nil {
			return api.PaymentDTO{}, err
		}
		submitting.Agent = agent
	}
	p, err := s.inst.Submit(r.Context(), dom)
	if err != nil {
		// A submission that committed and could not be SENT hands the payment
		// back beside the error: Initiated, its debtor leg posted on a push, and
		// no counterparty aware it exists. Refusals reach here with no payment
		// at all and log nothing, which is the distinction worth keeping — a
		// refused instruction moved nothing.
		if p.ID != "" {
			s.inst.Log().Error("api: a submission committed and its instruction did not go out",
				"payment", p.ID, "status", p.Status, "error", err)
		}
		return api.PaymentDTO{}, err
	}
	return api.ToPaymentDTO(p, s.network().ListSchemes()), nil
}

func (s *surface) handleListPayments(r *http.Request) ([]api.PaymentDTO, error) {
	payments, err := s.network().ListPayments(r.Context())
	if err != nil {
		return nil, err
	}
	schemes := s.network().ListSchemes()
	out := make([]api.PaymentDTO, len(payments))
	for i, p := range payments {
		out[i] = api.ToPaymentDTO(p, schemes)
	}
	return out, nil
}

func (s *surface) handleGetPayment(r *http.Request) (api.PaymentDTO, error) {
	p, err := s.network().GetPayment(r.Context(), payment.PaymentID(r.PathValue("payid")))
	if err != nil {
		return api.PaymentDTO{}, err
	}
	return api.ToPaymentDTO(p, s.network().ListSchemes()), nil
}

// handleRejectPayment declines an in-flight payment on the operator's say-so.
//
// Both halves of a rejection — the clearing house's and the payer's bank's —
// cannot share a transaction: two institutions, two databases. ClearingHouse.Reject is
// what runs them.
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
// nobody is left to tell, so it becomes a line in the day's report and the payment's own
// row is where it shows, Rejected with the money still in suspense.
func (s *surface) handleRejectPayment(r *http.Request, req api.ReasonRequest) (api.PaymentDTO, error) {
	// An operator-initiated rejection carries no more specific external status
	// reason than MS03: the API exposes no way for a caller to name a code, so
	// there is no more honest choice than the one that says exactly that.
	p, err := s.inst.Reject(r.Context(), payment.PaymentID(r.PathValue("payid")), iso20022.StatusReasonNotSpecifiedAgentGenerated, req.Reason)
	if err != nil {
		// ClearingHouse.Reject hands the payment back beside the error when its own half
		// committed and the pacs.002 did not go out — the payment is Rejected
		// and out of its cycle, and the payer's money is still in their bank's
		// suspense with nothing on its way to release it. The 5xx below carries
		// only the error, so this is the one place that half-happened state can
		// be recorded at all.
		if p.ID != "" {
			s.inst.Log().Error("api: a rejection committed and the bank that has to refund was not told",
				"payment", p.ID, "status", p.Status, "error", err)
		}
		return api.PaymentDTO{}, err
	}
	return api.ToPaymentDTO(p, s.network().ListSchemes()), nil
}

// handleReturnPayment sends a settled payment back.
//
// The returning bank is not named in the request and is not this operator: a
// return is sent by the bank that RECEIVED the original instruction — the
// payee's bank on a push, the payer's on a pull — and Deployment.Return works that out
// from the payment's own scheme. That bank posts its OWN customer leg and then
// builds and sends a pacs.004; the reserve reversal is the settlement agent's,
// because it moves central-bank money, and the other bank's customer leg is that
// bank's, posted from the same message once the return is final.
//
// # A refusal here can be about this operator's own beneficiary
//
// That is new, and it is the one thing a caller of this route has to be ready
// for beyond "no such payment". On a PUSH the returning bank holds the CLAWBACK,
// so a payee who has already spent the money stops the return dead: nothing is
// posted, no message is sent, and what comes back is AM04 — a beneficiary who
// cannot repay. The error mapping treats it like any other domain refusal. On a
// pull the returning bank holds the refund, which is unconditional, so this
// cannot arise.
//
// # It answers with an identifier and no payment
//
// Deliberately, and it is the one response on this surface that carries no
// resource. Deployment.Return returns no payment either: the Payment there is to hand
// back is one the caller could already read, and it is still Settled, because
// the return is not finished until the OTHER bank posts, four hops away.
// Re-reading the row after the send would be a race dressed up as a result. Ask
// again with the identifier; that is what 202 means here.
//
// The reason code is MS03 for handleRejectPayment's reason: this API gives a
// caller no way to name one, and the free text they did give travels beside it.
// The two code sets are different — a return reason answers "why is this money
// coming back" and a status reason answers "why was this refused" — which is why
// iso20022 keeps them apart and why this names the return one.
func (s *surface) handleReturnPayment(r *http.Request, req api.ReasonRequest) (api.SubmittedPaymentDTO, error) {
	id := payment.PaymentID(r.PathValue("payid"))
	if err := s.inst.Return(r.Context(), id, iso20022.ReturnReasonNotSpecifiedAgentGenerated, req.Reason); err != nil {
		return api.SubmittedPaymentDTO{}, err
	}
	return api.SubmittedPaymentDTO{PaymentID: string(id)}, nil
}

func (s *surface) handleOpenCycle(r *http.Request, req api.OpenCycleRequest) (api.ClearingCycleDTO, error) {
	c, err := s.network().OpenCycle(r.Context(), payment.SchemeID(req.Scheme))
	if err != nil {
		return api.ClearingCycleDTO{}, err
	}
	return api.ToClearingCycleDTO(c, s.network().ListSchemes()), nil
}

func (s *surface) handleListCycles(r *http.Request) ([]api.ClearingCycleDTO, error) {
	cycles, err := s.network().ListCycles(r.Context())
	if err != nil {
		return nil, err
	}
	schemes := s.network().ListSchemes()
	out := make([]api.ClearingCycleDTO, len(cycles))
	for i, c := range cycles {
		out[i] = api.ToClearingCycleDTO(c, schemes)
	}
	return out, nil
}

func (s *surface) handleGetCycle(r *http.Request) (api.ClearingCycleDTO, error) {
	c, err := s.network().GetCycle(r.Context(), payment.CycleID(r.PathValue("cid")))
	if err != nil {
		return api.ClearingCycleDTO{}, err
	}
	return api.ToClearingCycleDTO(c, s.network().ListSchemes()), nil
}

// handleCloseCycle reaches the cut-off, and is the only way a settlement is
// instructed in this system.
//
// It goes through the clearing house rather than through the network. Netting is the
// clearing house's own act and moves nothing — every payment in the batch
// becomes Cleared and the net positions are written onto the cycle — but
// DISCHARGING those positions moves central-bank reserves, which no clearing
// house may do. So the second step is a pacs.009 to the central bank, and
// calling payment.Network.CloseCycle directly from here would close the cycle
// and instruct nobody. There is no POST /settlements; see handleSettleCycle for
// why a second way to settle the same cycle was worse than none.
//
// 200 and the closed cycle, because that is a state the system is really in when
// this is written: Closed, with net positions on it. What is NOT in the response
// is the settlement — the central bank answers later, at another actor, and a
// caller that wants to know reads the cycle again. A cycle that is Closed with no
// settlement against it is an instruction the central bank refused, and
// POST /cycles/{cid}/settle below is what the operator does about it.
func (s *surface) handleCloseCycle(r *http.Request) (api.ClearingCycleDTO, error) {
	c, err := s.inst.CloseCycle(r.Context(), payment.CycleID(r.PathValue("cid")))
	if err != nil {
		// The cycle comes back beside the error when the cut-off committed and
		// the instruction could not be sent, and that half-happened state is
		// exactly what nobody would otherwise be told: the payments are Cleared,
		// no settlement agent has been asked, and the response is a 5xx that
		// names an error rather than a cycle. Logged here because this is the
		// last frame that holds both.
		if c.ID != "" {
			s.inst.Log().Error("api: a cut-off committed and its settlement instruction did not go out",
				"cycle", c.ID, "status", c.Status, "error", err)
		}
		return api.ClearingCycleDTO{}, err
	}
	return api.ToClearingCycleDTO(c, s.network().ListSchemes()), nil
}

// handleSettleCycle asks the clearing house to instruct settlement of a closed
// cycle again, and is the way out of a refused settlement.
//
// # Why it has to exist
//
// The central bank refuses a batch whose net payer cannot cover it — AM04, one
// unit of work, nothing posted — and there is nobody to tell: every payment is
// exactly where the cut-off left it, so a bank told "rejected" would try to
// reverse a debit that must not be reversed (see cmd/server's csm.receiveSettlementStatus).
// What is left is a cycle that is Closed with no settlement, its payments
// Cleared, every payer debited into their own bank's clearing suspense and every
// payee unpaid — and no state transition out of that, for any object, through
// any other route: closing wants an open cycle, rejecting wants an Initiated or
// Accepted payment, returning wants a settled one. The remedy is to fund the
// short member and ask again, and until this route existed there was nothing to
// ask.
//
// # It is not the deleted POST /settlements
//
// That route was the central bank's, and it SETTLED: a human pressed a button
// and reserves moved, beside a transport that was instructing the same thing — two
// ways to settle one cycle, racing. This is on the CLEARING HOUSE and it moves
// nothing. It rebuilds the pacs.009 from the cycle's own stored net positions
// and sends it, and the settlement agent decides, exactly as it does at a
// cut-off. There is still one institution that settles and one message that
// asks it to.
//
// 202 and the cycle, for handleCloseCycle's reason: what this response reports
// is that the instruction went out. Whether it was discharged this time arrives
// later, at another actor, and a caller that wants to know reads the cycle
// again. A cycle that is not Closed — still open, or already settled — is 422
// and no message is sent at all.
func (s *surface) handleSettleCycle(r *http.Request) (api.ClearingCycleDTO, error) {
	c, err := s.inst.Settle(r.Context(), payment.CycleID(r.PathValue("cid")))
	if err != nil {
		if c.ID != "" {
			s.inst.Log().Error("api: a settlement instruction could not be re-sent",
				"cycle", c.ID, "status", c.Status, "error", err)
		}
		return api.ClearingCycleDTO{}, err
	}
	return api.ToClearingCycleDTO(c, s.network().ListSchemes()), nil
}
