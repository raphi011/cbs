package bank

import (
	"net/http"
	"time"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
)

func (s *surface) registerDepositRoutes(mux *api.Router) {
	mux.HandleFunc("POST /deposit-accounts", handleBody(s, http.StatusCreated, s.handleOpenDepositAccount))
	mux.HandleFunc("GET /deposit-accounts", handle(s, http.StatusOK, s.handleListDepositAccounts))
	mux.HandleFunc("GET /deposit-accounts/{did}", handle(s, http.StatusOK, s.handleGetDepositAccount))
	mux.HandleFunc("GET /deposit-accounts/{did}/balance", handle(s, http.StatusOK, s.handleDepositBalance))
	mux.HandleFunc("POST /deposit-accounts/{did}/status", handleBody(s, http.StatusOK, s.handleDepositStatus))
	mux.HandleFunc("DELETE /deposit-accounts/{did}", handle(s, http.StatusNoContent, s.handleCloseDepositAccount))

	// The three writes the old overdraft-terms POST conflated, split along the
	// pinned/floating seam: a limit is an underwriting decision about one
	// customer, a price is the product's, and a migration is neither.
	mux.HandleFunc("POST /deposit-accounts/{did}/overdraft-limit", handleBody(s, http.StatusOK, s.handleSetOverdraftLimit))
	mux.HandleFunc("POST /deposit-accounts/{did}/overdraft-pricing", handleBody(s, http.StatusOK, s.handleSetOverdraftPricing))
	mux.HandleFunc("POST /deposit-accounts/{did}/product", handleBody(s, http.StatusOK, s.handleChangeProduct))
	mux.HandleFunc("GET /deposit-accounts/{did}/overdraft-terms", handle(s, http.StatusOK, s.handleListOverdraftTerms))
	mux.HandleFunc("POST /deposit-accounts/{did}/interest-charge", handleBody(s, http.StatusOK, s.handleChargeOverdraftInterest))

	mux.HandleFunc("POST /deposit-accounts/{did}/holds", handleBody(s, http.StatusCreated, s.handleCreateHold))
	mux.HandleFunc("GET /deposit-accounts/{did}/holds", handle(s, http.StatusOK, s.handleListHolds))
	mux.HandleFunc("GET /holds/{hid}", handle(s, http.StatusOK, s.handleGetHold))
	mux.HandleFunc("POST /holds/{hid}/release", handle(s, http.StatusNoContent, s.handleReleaseHold))
	mux.HandleFunc("POST /holds/{hid}/capture", handleBody(s, http.StatusCreated, s.handleCaptureHold))

	mux.HandleFunc("POST /deposit-accounts/{did}/snapshots", handleBody(s, http.StatusCreated, s.handleTakeSnapshot))
	mux.HandleFunc("GET /deposit-accounts/{did}/snapshots", handle(s, http.StatusOK, s.handleGetSnapshots))
}

// handleTransfer moves money between two of this bank's own deposit accounts.
//
// # 200, because the act is finished when it returns
//
// POST /deposits is the precedent and the argument carries over unchanged: one
// institution, one posting, and nobody else to ask. 202 is for the routes where
// another institution has still to decide — see handleLodgeReserves — and
// nobody has to agree to a book transfer.
//
// # An address this bank does not hold is a 404, and that is the boundary
//
// An address that resolves anywhere else is not a transfer at all; it is a
// payment, and POST /payments is where it goes. The two routes state one rule
// from opposite sides: this one refuses an address that is not here, and
// submission refuses one that is (payment.ErrOnUsPayment).
func (s *surface) handleTransfer(r *http.Request, p *payment.Bank, req api.TransferRequest) (api.TransferDTO, error) {
	// The address is resolved BEFORE the transfer's unit of work, and it cannot
	// go stale in between: an IBAN at this bank never moves between accounts.
	// Reissuing gives one account a fresh address rather than handing an old one
	// to another account, and AddIdentifier refuses the scheme outright. So the
	// account this answers is the account that address means, then and later.
	payee, err := p.Deposit.ResolveIdentifier(r.Context(), deposit.Identifier{
		Scheme: deposit.IdentifierIBAN,
		Value:  req.To,
	})
	if err != nil {
		return api.TransferDTO{}, err
	}
	from := deposit.AccountID(req.From)
	glTx, err := p.Deposit.Transfer(r.Context(), from, payee.ID, ledger.Amount(req.Amount), req.Description)
	if err != nil {
		return api.TransferDTO{}, err
	}
	payer, err := p.Deposit.GetAccount(r.Context(), from)
	if err != nil {
		return api.TransferDTO{}, err
	}
	bal, err := p.Deposit.GetBalance(r.Context(), from)
	if err != nil {
		return api.TransferDTO{}, err
	}
	return api.TransferDTO{
		TransactionID: string(glTx.ID),
		From:          string(from),
		To:            string(payee.ID),
		Balance:       api.ToBalanceDTO(bal, payer.Asset),
	}, nil
}

func (s *surface) handleOpenDepositAccount(r *http.Request, p *payment.Bank, req api.OpenDepositAccountRequest) (api.DepositAccountDTO, error) {
	if req.Asset == "" {
		return api.DepositAccountDTO{}, api.BadRequest("asset is required")
	}
	if req.ProductID == "" {
		return api.DepositAccountDTO{}, api.BadRequest("productId is required")
	}
	idents := make([]deposit.Identifier, len(req.Identifiers))
	for i, ident := range req.Identifiers {
		idents[i] = deposit.Identifier{Scheme: deposit.IdentifierScheme(ident.Scheme), Value: ident.Value}
	}
	acct, err := p.Deposit.OpenAccount(r.Context(), req.Name,
		ledger.AssetCode(req.Asset), product.ID(req.ProductID), ledger.Amount(req.OverdraftLimit), idents...)
	if err != nil {
		return api.DepositAccountDTO{}, err
	}
	// The limit reaches the response through the account's opening terms row
	// rather than through the value returned above: it is a terms field now,
	// and reading it back is what keeps the created body and a later GET the
	// same shape.
	return accountWithTermsDTO(r, p, acct.ID)
}

func (s *surface) handleListDepositAccounts(r *http.Request, p *payment.Bank) ([]api.DepositAccountDTO, error) {
	accts, err := p.Deposit.ListAccountsWithTerms(r.Context())
	if err != nil {
		return nil, err
	}
	// One resolution per ASSET rather than per account: the line is the same
	// for every customer holding that currency, and a listing that asked the
	// chart of accounts once per row would put the customer base back into a
	// question that is about the institution.
	controls := map[ledger.AssetCode]ledger.AccountID{}
	out := make([]api.DepositAccountDTO, len(accts))
	for i, a := range accts {
		control, ok := controls[a.Account.Asset]
		if !ok {
			control, err = p.Deposit.ControlAccount(r.Context(), a.Account.Asset)
			if err != nil {
				return nil, err
			}
			controls[a.Account.Asset] = control
		}
		out[i] = api.ToDepositAccountDTO(a.Account, a.Terms, control)
	}
	return out, nil
}

func (s *surface) handleGetDepositAccount(r *http.Request, p *payment.Bank) (api.DepositAccountDTO, error) {
	return accountWithTermsDTO(r, p, deposit.AccountID(r.PathValue("did")))
}

func (s *surface) handleDepositBalance(r *http.Request, p *payment.Bank) (api.BalanceDTO, error) {
	did := deposit.AccountID(r.PathValue("did"))
	acct, err := p.Deposit.GetAccount(r.Context(), did)
	if err != nil {
		return api.BalanceDTO{}, err
	}
	bal, err := p.Deposit.GetBalance(r.Context(), did)
	if err != nil {
		return api.BalanceDTO{}, err
	}
	return api.ToBalanceDTO(bal, acct.Asset), nil
}

// handleDepositStatus dispatches a lifecycle transition based on the request's
// "action" field. This keeps the four transitions behind one URL, which suits a
// frontend status dropdown.
func (s *surface) handleDepositStatus(r *http.Request, p *payment.Bank, req api.StatusRequest) (api.DepositAccountDTO, error) {
	did := deposit.AccountID(r.PathValue("did"))
	var err error
	switch req.Action {
	case "freeze":
		err = p.Deposit.Freeze(r.Context(), did)
	case "unfreeze":
		err = p.Deposit.Unfreeze(r.Context(), did)
	case "markDormant":
		err = p.Deposit.MarkDormant(r.Context(), did)
	case "reactivate":
		err = p.Deposit.Reactivate(r.Context(), did)
	default:
		err = api.BadRequest("invalid action (want freeze, unfreeze, markDormant, or reactivate)")
	}
	if err != nil {
		return api.DepositAccountDTO{}, err
	}
	return accountWithTermsDTO(r, p, did)
}

func (s *surface) handleCloseDepositAccount(r *http.Request, p *payment.Bank) (any, error) {
	return nil, p.Deposit.Close(r.Context(), deposit.AccountID(r.PathValue("did")))
}

// The three handlers that replaced handleSetOverdraftTerms.
//
// Each 200 body is the account re-read WITH its resolved terms rather than the
// row that was just written, so the response shape is the same one every other
// deposit endpoint returns — and so a future-dated change does not come back as
// though it were already in force.

func (s *surface) handleSetOverdraftLimit(r *http.Request, p *payment.Bank, req api.SetOverdraftLimitRequest) (api.DepositAccountDTO, error) {
	did := deposit.AccountID(r.PathValue("did"))
	if _, err := p.Deposit.SetOverdraftLimit(r.Context(), did,
		ledger.Amount(req.Limit), effectiveFromOrToday(req.EffectiveFrom)); err != nil {
		return api.DepositAccountDTO{}, err
	}
	return accountWithTermsDTO(r, p, did)
}

func (s *surface) handleSetOverdraftPricing(r *http.Request, p *payment.Bank, req api.SetOverdraftPricingRequest) (api.DepositAccountDTO, error) {
	// A null pricing CLEARS the overlay, which is why this is a pointer all the
	// way down rather than a zero value: a zero-rate overlay is a real
	// interest-free product and must not be reachable by omission.
	var pricing *product.OverdraftPricing
	if req.Pricing != nil {
		dc, err := api.DayCountFromString(req.Pricing.DayCount)
		if err != nil {
			return api.DepositAccountDTO{}, err
		}
		pricing = &product.OverdraftPricing{
			Rate:           interest.Rate(req.Pricing.Rate),
			UnarrangedRate: interest.Rate(req.Pricing.UnarrangedRate),
			DayCount:       dc,
		}
	}
	did := deposit.AccountID(r.PathValue("did"))
	if _, err := p.Deposit.SetOverdraftPricingOverlay(r.Context(), did,
		pricing, effectiveFromOrToday(req.EffectiveFrom)); err != nil {
		return api.DepositAccountDTO{}, err
	}
	return accountWithTermsDTO(r, p, did)
}

func (s *surface) handleChangeProduct(r *http.Request, p *payment.Bank, req api.ChangeProductRequest) (api.DepositAccountDTO, error) {
	if req.ProductID == "" {
		return api.DepositAccountDTO{}, api.BadRequest("productId is required")
	}
	did := deposit.AccountID(r.PathValue("did"))
	if _, err := p.Deposit.ChangeProduct(r.Context(), did,
		product.ID(req.ProductID), effectiveFromOrToday(req.EffectiveFrom)); err != nil {
		return api.DepositAccountDTO{}, err
	}
	return accountWithTermsDTO(r, p, did)
}

// effectiveFromOrToday maps an absent date to the zero time, which every
// register setter reads as "today on the register's clock" — the same default
// ledger.PostTransactionRequest gives a zero booking date, and the reason a
// request that says nothing about when still lands on the day the rest of the
// system thinks it is.
func effectiveFromOrToday(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// accountWithTermsDTO renders one account, resolving the terms in force and the
// control line its money is pooled in. Every single-account response goes
// through here so that none of them can answer with the pair half-formed.
func accountWithTermsDTO(r *http.Request, p *payment.Bank, did deposit.AccountID) (api.DepositAccountDTO, error) {
	acct, err := p.Deposit.GetAccountWithTerms(r.Context(), did)
	if err != nil {
		return api.DepositAccountDTO{}, err
	}
	control, err := p.Deposit.ControlAccount(r.Context(), acct.Account.Asset)
	if err != nil {
		return api.DepositAccountDTO{}, err
	}
	return api.ToDepositAccountDTO(acct.Account, acct.Terms, control), nil
}

// handleListOverdraftTerms returns an account's whole effective-dated terms
// timeline, oldest first — including the opening row every account gets at
// OpenAccount, which carries the limit it was opened with and zero rates.
func (s *surface) handleListOverdraftTerms(r *http.Request, p *payment.Bank) ([]api.OverdraftTermsDTO, error) {
	rows, err := p.Deposit.OverdraftTermsHistory(r.Context(), deposit.AccountID(r.PathValue("did")))
	if err != nil {
		return nil, err
	}
	out := make([]api.OverdraftTermsDTO, len(rows))
	for i, t := range rows {
		out[i] = api.ToOverdraftTermsDTO(t)
	}
	return out, nil
}

// handleChargeOverdraftInterest capitalizes an account's accrued overdraft
// interest, clearing the receivable — the monthly event a customer actually
// sees.
//
// Nothing accrued means nothing posted, and nothing else happens either: unlike
// a revolving line's cycle, this appends no instalment. So the empty case is
// 204 No Content — the answer the rest of this API gives for "the request was
// fine and there is nothing to say" — rather than a 200 whose empty body a
// client has to guess at, or a Transaction with an empty ID rendered as though
// it were a real posting.
func (s *surface) handleChargeOverdraftInterest(r *http.Request, p *payment.Bank, req api.ChargeOverdraftInterestRequest) (any, error) {
	date, err := api.ParseDay("date", req.Date)
	if err != nil {
		return nil, err
	}
	tx, err := p.Deposit.ChargeOverdraftInterest(r.Context(), deposit.AccountID(r.PathValue("did")), date)
	if err != nil {
		return nil, err
	}
	if tx.ID == "" {
		return nil, nil
	}
	return api.ResolveTransactionDTO(r.Context(), p.Ledger, tx)
}

func (s *surface) handleCreateHold(r *http.Request, p *payment.Bank, req api.CreateHoldRequest) (api.HoldDTO, error) {
	holdReq := deposit.CreateHoldRequest{
		AccountID:   deposit.AccountID(r.PathValue("did")),
		Amount:      ledger.Amount(req.Amount),
		Description: req.Description,
	}
	if req.ExpiresAt != nil {
		holdReq.ExpiresAt = *req.ExpiresAt
	}
	hold, err := p.Deposit.CreateHold(r.Context(), holdReq)
	if err != nil {
		return api.HoldDTO{}, err
	}
	return api.ToHoldDTO(hold), nil
}

func (s *surface) handleListHolds(r *http.Request, p *payment.Bank) ([]api.HoldDTO, error) {
	holds, err := p.Deposit.ListHolds(r.Context(), deposit.AccountID(r.PathValue("did")))
	if err != nil {
		return nil, err
	}
	out := make([]api.HoldDTO, len(holds))
	for i, h := range holds {
		out[i] = api.ToHoldDTO(h)
	}
	return out, nil
}

func (s *surface) handleGetHold(r *http.Request, p *payment.Bank) (api.HoldDTO, error) {
	hold, err := p.Deposit.GetHold(r.Context(), deposit.HoldID(r.PathValue("hid")))
	if err != nil {
		return api.HoldDTO{}, err
	}
	return api.ToHoldDTO(hold), nil
}

func (s *surface) handleReleaseHold(r *http.Request, p *payment.Bank) (any, error) {
	return nil, p.Deposit.ReleaseHold(r.Context(), deposit.HoldID(r.PathValue("hid")))
}

func (s *surface) handleCaptureHold(r *http.Request, p *payment.Bank, req api.CaptureHoldRequest) (api.TransactionDTO, error) {
	tx, err := p.Deposit.CaptureHold(
		r.Context(),
		deposit.HoldID(r.PathValue("hid")),
		ledger.AccountID(req.Counterparty).For(req.Subsidiary),
		ledger.Amount(req.Amount),
		req.Description,
	)
	if err != nil {
		return api.TransactionDTO{}, err
	}
	return api.ResolveTransactionDTO(r.Context(), p.Ledger, tx)
}

func (s *surface) handleTakeSnapshot(r *http.Request, p *payment.Bank, req api.SnapshotRequest) (api.SnapshotDTO, error) {
	date, err := api.ParseDay("date", req.Date)
	if err != nil {
		return api.SnapshotDTO{}, err
	}
	did := deposit.AccountID(r.PathValue("did"))
	acct, err := p.Deposit.GetAccount(r.Context(), did)
	if err != nil {
		return api.SnapshotDTO{}, err
	}
	snap, err := p.Deposit.TakeEndOfDaySnapshot(r.Context(), did, date)
	if err != nil {
		return api.SnapshotDTO{}, err
	}
	return api.ToSnapshotDTO(snap, acct.Asset), nil
}

// handleGetSnapshots returns one snapshot when ?date=YYYY-MM-DD is given, or all
// snapshots for the account otherwise.
func (s *surface) handleGetSnapshots(r *http.Request, p *payment.Bank) (any, error) {
	did := deposit.AccountID(r.PathValue("did"))
	// Every snapshot of one account is in that account's asset — an account's
	// asset is fixed at creation — so it is resolved once here rather than per
	// row below.
	acct, err := p.Deposit.GetAccount(r.Context(), did)
	if err != nil {
		return nil, err
	}
	if dateStr := r.URL.Query().Get("date"); dateStr != "" {
		date, err := api.ParseDay("date", dateStr)
		if err != nil {
			return nil, err
		}
		snap, err := p.Deposit.GetSnapshot(r.Context(), did, date)
		if err != nil {
			return nil, err
		}
		return api.ToSnapshotDTO(snap, acct.Asset), nil
	}
	snaps, err := p.Deposit.ListSnapshots(r.Context(), did)
	if err != nil {
		return nil, err
	}
	out := make([]api.SnapshotDTO, len(snaps))
	for i, snap := range snaps {
		out[i] = api.ToSnapshotDTO(snap, acct.Asset)
	}
	return out, nil
}
