package bank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

func (s *surface) handleGetParticipant(r *http.Request, p *payment.Bank) (api.ParticipantDTO, error) {
	return api.ToParticipantDTO(p), nil
}

func (s *surface) handleFundDeposit(r *http.Request, p *payment.Bank, req api.FundRequest) (api.BalanceDTO, error) {
	// No asset on the wire: the cash lands in the vault of whichever asset the
	// funded account is denominated in, which the network reads for itself.
	did := deposit.AccountID(req.Account)
	if err := s.network().Deposit(r.Context(), p.ID, did, ledger.Amount(req.Amount), req.Description); err != nil {
		return api.BalanceDTO{}, err
	}
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

// handleLodgeReserves is a bank placing its own vault cash on reserve at the
// central bank.
func (s *surface) handleLodgeReserves(r *http.Request, p *payment.Bank, req api.LodgementRequest) (api.LodgementDTO, error) {
	if req.Asset == "" {
		return api.LodgementDTO{}, api.BadRequest("asset is required: a bank holds one pot of vault cash per asset and nothing else in this request says which")
	}
	in, err := s.inst.Lodge(r.Context(), ledger.AssetCode(req.Asset), ledger.Amount(req.Amount))
	if err != nil {
		// A lodgement that committed and could not be SENT hands the instruction back
		// beside the error, as Deployment.Submit does with its payment: this bank's
		// vault is down and its reserve mirror up, with nothing on its way to the
		// central bank to match it.
		if in.Ref != "" {
			s.inst.Log().Error("api: a lodgement committed and its instruction did not go out",
				"bank", p.BIC, "lodgement", in.Ref, "asset", in.Asset, "amount", in.Amount, "err", err)
		}
		return api.LodgementDTO{}, err
	}
	return api.ToLodgementDTO(in), nil
}
