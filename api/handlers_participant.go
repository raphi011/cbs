package api

import (
	"net/http"
	"sort"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// handleListParticipants answers every bank this deployment holds a database
// for, each read out of its own database, ascending by address.
//
// # No institution is asked, because none of them knows
//
// It is the SETTLEMENT AGENT's read and not the clearing house's: the csm shape
// has no banks table, and the roster is not a substitute — a bank founded and
// not yet admitted has no roster entry and is precisely the bank this listing
// exists to show, its empty settlement references being what says so.
//
// payment.Stores.Banks is the question with an answer: every bank whose DATABASE
// exists. Its doc says nothing in the domain calls it and nothing should — an
// institution asking which other institutions exist is the crossing this task
// removes — and this is not the domain asking. It is the process asking what it
// has, and then asking each of those banks for its own row.
//
// # It reaches past this listener's binding, and surface.go names why
//
// Server.as binds one institution's Network to a router and every other handler
// here goes through s.network(). This one goes through s.nets, to N databases
// that are not this listener's. It is the operator's act over a deployment
// rather than the settlement agent's over its own books; the router comment in
// surface.go is where that exception is written down.
//
// # The cost, stated rather than implied
//
// One request opens and reads every bank's database. It is the widest read in
// this process and there is no narrower version of it — a list of N banks is N
// banks' rows and each row is in a different file. The consolation is that it is
// the only such read: the seed's idempotency probe and cmd/server's listener plan
// ask Stores.Banks for the ADDRESSES alone and open nothing.
func (s *Server) handleListParticipants(w http.ResponseWriter, r *http.Request) {
	bics, err := s.nets.Stores().Banks(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]participantDTO, 0, len(bics))
	for _, bic := range bics {
		id := payment.ParticipantID(bic)
		net, err := s.nets.Bank(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		p, err := net.GetBank(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		out = append(out, toParticipantDTO(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetParticipant(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toParticipantDTO(p))
}

func (s *Server) handleFundDeposit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req fundRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	// No asset on the wire: the cash lands in the vault of whichever asset the
	// funded account is denominated in, which the network reads for itself. A
	// LODGEMENT does name one, because it is about the bank rather than about an
	// account — see handleLodgeReserves.
	if err := s.network().Deposit(r.Context(), p.ID, deposit.AccountID(req.Account), ledger.Amount(req.Amount), req.Description); err != nil {
		writeError(w, err)
		return
	}
	acct, err := p.Deposit.GetAccount(r.Context(), deposit.AccountID(req.Account))
	if err != nil {
		writeError(w, err)
		return
	}
	bal, err := p.Deposit.GetBalance(r.Context(), deposit.AccountID(req.Account))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toBalanceDTO(bal, acct.Asset))
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
func (s *Server) handleLodgeReserves(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req lodgementRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	if req.Asset == "" {
		writeBadRequest(w, "asset is required: a bank holds one pot of vault cash per asset and nothing else in this request says which")
		return
	}
	in, err := s.mesh.Lodge(r.Context(), p.BIC, ledger.AssetCode(req.Asset), ledger.Amount(req.Amount))
	if err != nil {
		// A lodgement that committed and could not be SENT hands the instruction
		// back beside the error, as Mesh.Submit does with its payment: this bank's
		// vault is down and its reserve mirror up, with nothing on its way to the
		// central bank to match it. It is the one place that half-happened state
		// can be recorded, because this system keeps no lodgement row.
		if in.Ref != "" {
			s.log.Error("api: a lodgement committed and its instruction did not go out",
				"bank", p.BIC, "lodgement", in.Ref, "asset", in.Asset, "amount", in.Amount, "err", err)
		}
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toLodgementDTO(in))
}

func (s *Server) handleListSchemes(w http.ResponseWriter, r *http.Request) {
	schemes := s.network().ListSchemes()
	out := make([]schemeDTO, len(schemes))
	for i, sc := range schemes {
		out[i] = toSchemeDTO(sc)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListReserves reports every reserve this central bank holds — one row per
// (member, asset), because a reserve in one asset says nothing about a reserve
// in another and the two must not be added up.
//
// The list is the settlement agent's OWN register. See reserveRows.
func (s *Server) handleListReserves(w http.ResponseWriter, r *http.Request) {
	members, err := s.network().ListSettlementMembers(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]reserveDTO, 0, len(members))
	for _, m := range members {
		rows, err := s.reserveRows(r, m)
		if err != nil {
			writeError(w, err)
			return
		}
		out = append(out, rows...)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetReserve reports one member's reserves, one row per asset, for the
// same reason handleListReserves does.
//
// The path segment is a BIC, which is what the settlement agent's records are
// keyed by and the only name for a bank it is ever told. A bank it holds no
// account for is the 422 the sentinel already mapped to rather than a 404 about
// a bank row this institution has no table for.
func (s *Server) handleGetReserve(w http.ResponseWriter, r *http.Request) {
	m, err := s.network().GetSettlementMember(r.Context(), iso20022.BIC(r.PathValue("bic")))
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := s.reserveRows(r, m)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// reserveRows reads one member's reserve in each asset the settlement agent
// holds an account for it in, in asset order so the response does not reshuffle
// between identical requests.
//
// # One rule: a row for every account this institution holds, and nothing else
//
// The assets come off the MEMBER's row — payment.SettlementMember.Accounts, one
// account per asset, written by the agent when it answered the bank's request —
// so every row this builds is a reserve this institution actually keeps.
//
// It reads the agent's own register straight, which is the only version of this
// endpoint the central bank's database can serve: it has no banks table to
// compare against. A bank with no member row is not in ListSettlementMembers to
// begin with, and an asset with no account is not in this map.
//
// # What that costs, and which stuck bank it was actually about
//
// What this reading cannot show is worth naming, because the obvious candidate
// is not one of them.
//
// A two-asset bank whose provisioning stopped between its two requests leaves the
// agent holding a row without the second asset, permanently. So does a request
// the agent REFUSED: nothing about a bank changes when it is refused an account,
// so the bank's own row carries an empty reference and the agent holds none. The
// two are indistinguishable from each other — "it never asked" and "it was told
// no" leave identical rows — and both look, from this endpoint, exactly like a
// one-asset member, which is also what a two-asset admission looks like between
// its two commits. The difference is TIME and nothing here watches a clock.
//
// The half-admitted bank is a DIFFERENT state and this endpoint reports it fully.
// There every account was opened and the bank's own act never ran, so the agent's
// row has both assets and this reports two rows — the second showing a reserve
// the bank cannot spend, since payment.DepositTx resolves through the BANK's
// record and that one has no reference. That is precisely what
// payment.RecordMembershipTx describes: a deposit in that asset fails while the
// operator console cheerfully reports the reserve.
//
// Finding any of them needs the bank's own assets beside these rows, which GET
// /me on that bank's own port carries and which no screen in this repository
// renders — so it is two reads at two institutions, not something one console
// can show. Putting them side by side is a reconciliation; see payment/recon.
func (s *Server) reserveRows(r *http.Request, m payment.SettlementMember) ([]reserveDTO, error) {
	codes := make([]string, 0, len(m.Accounts))
	for code := range m.Accounts {
		codes = append(codes, string(code))
	}
	sort.Strings(codes)

	out := make([]reserveDTO, 0, len(codes))
	for _, code := range codes {
		bal, err := s.network().ReserveBalance(r.Context(), m.BIC, ledger.AssetCode(code))
		if err != nil {
			return nil, err
		}
		out = append(out, reserveDTO{Agent: string(m.BIC), Asset: code, Reserve: int64(bal)})
	}
	return out, nil
}
