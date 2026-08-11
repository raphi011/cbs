package bank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
)

func (s *surface) handleGetParticipant(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ToParticipantDTO(p))
}

func (s *surface) handleFundDeposit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req api.FundRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	// No asset on the wire: the cash lands in the vault of whichever asset the
	// funded account is denominated in, which the network reads for itself. A
	// LODGEMENT does name one, because it is about the bank rather than about an
	// account — see handleLodgeReserves.
	if err := s.network().Deposit(r.Context(), p.ID, deposit.AccountID(req.Account), ledger.Amount(req.Amount), req.Description); err != nil {
		api.WriteError(w, err)
		return
	}
	acct, err := p.Deposit.GetAccount(r.Context(), deposit.AccountID(req.Account))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	bal, err := p.Deposit.GetBalance(r.Context(), deposit.AccountID(req.Account))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ToBalanceDTO(bal, acct.Asset))
}

// handleLodgeReserves is a bank placing its own vault cash on reserve at the
// central bank.
//
// # Why this is a route at all, and why it is on the BANK's port
//
// It is on this port because a lodgement is the BANK's decision about its own
// liquidity. The clearing house has no business in it, and the central bank does
// not initiate it. Cash paid in over the counter is a separate act — see POST
// /deposits, which lands it in this bank's vault.
//
// # 202 and not 200, and that is the substance
//
// POST /deposits answers 200 with the new balance, because a deposit is finished
// when it returns: one institution, one posting. This answers 202 with the
// instruction that was sent, because the reserve credit is another institution's
// to make and has not happened yet — the camt.050 is on the wire and the camt.025
// comes back to bank.receiveLodgementReceipt.
//
// So a caller that reads a reserve immediately after this may see the old figure,
// and that is honest rather than a defect: it is the same asynchrony
// POST /payments has, and for the same reason. What HAS happened by the time this
// returns is the bank's own leg — its vault is down and its reserve mirror is up.
//
// # The refusal a founded bank gets
//
// A bank that cannot name its own settlement account is refused with
// payment.ErrSettlementMemberNotFound and a 422: a bank with no reserve account
// has no reserve to lodge into. Taking cash in is not refused the same way —
// cash over the counter is the bank's own money in its own book, and
// mesh.TestTakingCashInReachesNoOtherInstitution is where that is held.
func (s *surface) handleLodgeReserves(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req api.LodgementRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	if req.Asset == "" {
		api.WriteBadRequest(w, "asset is required: a bank holds one pot of vault cash per asset and nothing else in this request says which")
		return
	}
	in, err := s.inst.Lodge(r.Context(), ledger.AssetCode(req.Asset), ledger.Amount(req.Amount))
	if err != nil {
		// A lodgement that committed and could not be SENT hands the instruction
		// back beside the error, as Mesh.Submit does with its payment: this bank's
		// vault is down and its reserve mirror up, with nothing on its way to the
		// central bank to match it. It is the one place that half-happened state
		// can be recorded, because this system keeps no lodgement row.
		if in.Ref != "" {
			s.inst.Log().Error("api: a lodgement committed and its instruction did not go out",
				"bank", p.BIC, "lodgement", in.Ref, "asset", in.Asset, "amount", in.Amount, "err", err)
		}
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, api.ToLodgementDTO(in))
}
