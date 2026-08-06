package api

import (
	"errors"
	"net/http"
	"sort"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/mesh"
	"github.com/raphi011/cbs/payment"
)

// handleAddParticipant founds a bank and applies to the scheme for it.
//
// # 202, and what an operator has when it answers
//
// A founded bank. Its book, its chart of accounts and its default product exist,
// so it can open customer accounts straight away, and it can take cash in: a
// deposit lands in this bank's own vault and involves nobody else. What it cannot
// do is LODGE that cash on reserve, since only the central bank can credit an
// account in the central bank's book and none is held for this bank yet — 422 from
// POST /lodgements, naming the reserve account it cannot name. It is in no routing
// directory either, and the cost of that is wider than this bank: nothing STOPS
// a payment being addressed to it, and a cut-off carrying one cannot be
// instructed at all, so EVERY member in that cycle is left with its payments
// Cleared, its payees unpaid and its payers' money in suspense until this bank
// is admitted. mesh/doc.go measures it and records that no test pins it; "no
// clearing house routes to it" is not a check anybody makes. The DTO says which
// of the two states this bank is in: Founded here, and Member once the scheme
// has answered.
//
// Whether the scheme accepts is not this call's to report. It is decided at two
// other institutions and arrives as a message, so the honest status code is 202
// Accepted: the application has been made and nothing about its outcome is
// known. POST /payments has the same shape for the same reason — a handler that
// answered 201 Created would be naming a resource whose most important property
// it had not waited for. An operator learns the answer by reading the bank back.
//
// # A clash costs nothing, and a re-drive is this same call
//
// The address is the only thing in the operation that can clash, and
// mesh.Mesh.Admit claims it before anything is written and releases it again if
// the write fails. So a refusal leaves no row, no actor and nothing to clean up
// — which is the reverse of the ordering this endpoint used to have, where the
// participant row was written first and a refused address left a bank in the
// network that could neither pay nor be paid, with no way back.
//
// An interrupted admission therefore leaves a founded bank rather than an orphan,
// and calling this again on the same name and BIC RE-DRIVES it: nothing is
// founded twice and the application goes out again. See mesh.Mesh.Admit.
func (s *Server) handleAddParticipant(w http.ResponseWriter, r *http.Request) {
	var req createParticipantRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	// An empty (or absent) Assets means the network's default joining set —
	// payment applies that itself, at founding. That is a default for which
	// assets a bank joins with, not for the asset of any account, which is
	// always named by its caller.
	assets := make([]ledger.AssetCode, len(req.Assets))
	for i, a := range req.Assets {
		assets[i] = ledger.AssetCode(a)
	}
	// BIC is required, but its shape is a business rule (iso20022.BIC.Validate,
	// run by Admit before it claims the address) rather than a decoding failure,
	// so a malformed or missing value is left to surface as the 422 writeError
	// already maps iso20022.ErrBICFormat to, not a 400 raised here.
	p, err := s.mesh.Admit(r.Context(), req.Name, iso20022.BIC(req.BIC), assets)
	if err != nil {
		// The two refusals about the address, which need different advice.
		//
		// An admission ALREADY UNDER WAY on this BIC is checked first, because it
		// is a case of the sentinel below and would otherwise be answered by it —
		// wrongly on both clauses. Nothing is written YET rather than not at all,
		// the address is not another institution's but this bank's own, and a
		// second address would not help; what helps is waiting for the first
		// application to be answered and reading the bank back.
		//
		// The advice is all this adds, and the sentence about the address comes
		// from the error alone. Saying it here too is how the first version of
		// this branch came out reading the same thing twice — and, before
		// mesh.ErrAdmissionInFlight had a type of its own, saying "another actor
		// already answers to this BIC" underneath, which is the statement the
		// whole branch exists to stop making.
		if errors.Is(err, mesh.ErrAdmissionInFlight) {
			writeUnprocessable(w, "nothing has been written for this request; wait for the scheme to answer the "+
				"application that is already out, then read the bank back: "+err.Error())
			return
		}
		if errors.Is(err, mesh.ErrAddressTaken) {
			writeUnprocessable(w, "another institution already answers to this BIC, and nothing was written; "+
				"admit this bank on an address of its own: "+err.Error())
			return
		}
		writeError(w, err)
		return
	}
	// 202, not 201: what exists is a founded bank, and whether the scheme admits
	// it is answered at two other institutions and arrives as a message.
	writeJSON(w, http.StatusAccepted, toParticipantDTO(p))
}

func (s *Server) handleListParticipants(w http.ResponseWriter, r *http.Request) {
	parts, err := s.network().ListBanks(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]participantDTO, len(parts))
	for i, p := range parts {
		out[i] = toParticipantDTO(p)
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
// Funding a reserve used to be a side effect of POST /deposits: cash paid in
// raised the customer's balance and the bank's reserve in one unit of work,
// because one store held both books. Task 18a splits them, so the second half
// needs a door of its own — and it is on this port because a lodgement is the
// BANK's decision about its own liquidity. The clearing house has no business in
// it, and the central bank does not initiate it.
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
// payment.ErrSettlementMemberNotFound and a 422. That refusal used to be given by
// POST /deposits and was wrong there — it said cash could not be paid in at a
// bank the scheme had not answered for — and it is right here: a bank with no
// reserve account has no reserve to lodge into. See
// TestABankTheSchemeHasNotAnsweredForCanTakeCashAndCannotLodgeIt.
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
	in, err := s.mesh.Lodge(r.Context(), p.ID, ledger.AssetCode(req.Asset), ledger.Amount(req.Amount))
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

// handleListReserves reports every bank's reserve in every asset it operates
// in — one row per (participant, asset), because a reserve in one asset says
// nothing about a reserve in another and the two must not be added up.
func (s *Server) handleListReserves(w http.ResponseWriter, r *http.Request) {
	parts, err := s.network().ListBanks(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]reserveDTO, 0, len(parts))
	for _, p := range parts {
		rows, err := s.reserveRows(r, p)
		if err != nil {
			writeError(w, err)
			return
		}
		out = append(out, rows...)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetReserve reports one bank's reserves, one row per asset, for the
// same reason handleListReserves does.
func (s *Server) handleGetReserve(w http.ResponseWriter, r *http.Request) {
	p, err := s.network().GetBank(r.Context(), payment.ParticipantID(r.PathValue("pid")))
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := s.reserveRows(r, p)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// reserveRows reads one participant's reserve in each of its assets, in asset
// order so the response does not reshuffle between identical requests.
//
// # One rule: a row for every asset the settlement agent holds an account in
//
// Nothing else gets a row, and neither of the two ways an account can be missing
// is an error about the request. Both are the settlement agent saying it holds
// nothing here, and a reserve report has nothing to report.
//
// payment.ErrSettlementMemberNotFound is the agent holding no row for this bank
// at all — a bank that has founded itself and not yet joined, which admission
// being a conversation makes an ordinary state rather than a fault. The whole
// bank loses its rows together, and that is right: it has no reserves at all,
// not some. (The skip sits inside the per-asset loop only because that is where
// ReserveBalance is asked; payment.settlementAccountTx reads the member row
// before it looks at the asset, so this sentinel cannot come back for one asset
// and not another.)
//
// payment.ErrParticipantAssetNotFound is the agent holding a row without THIS
// asset in it, and that one really is per asset. It is not exotic and it is not
// broken: one acmt.007 asks for one currency, so a bank joining in euro and
// dollars has two applications answered in two commits, and between them the
// agent holds euro and not dollars while the bank's own row says both. Every
// two-asset admission passes through that window. No rate is quoted for it,
// because a rate here is a fact about when the reads were taken and not about
// the system — the window is as wide as one unit of work at one actor, and a
// probe can miss it entirely or find it repeatedly without either result saying
// anything.
//
// Which is why it is skipped rather than surfaced, and the change is deliberate:
// reporting it turned the LIST route into a 422 for every bank on it because one
// applicant was mid-conversation, which is worse than the founded-bank 500 this
// endpoint was fixed for. An empty list from a whole route is not a truer answer
// than a missing row.
//
// # What that costs, and which stuck bank it is actually about
//
// TWO shapes become indistinguishable from that window, and they are worth
// naming exactly, because the obvious candidate is neither of them.
//
// The first is an acmt.007 that never reached the settlement agent — a
// dead letter on the way out, so no account is opened in that asset and none
// ever will be. The agent holds a row without the asset, permanently, which is
// byte-identical to holding one without the asset for another millisecond. The
// difference is TIME and nothing here watches a clock.
//
// The second is an acmt.007 the settlement agent REFUSED, and it is the one that
// makes the recipe below a heuristic rather than a test.
// mesh.bank.receiveAdmissionRejection writes NOTHING — there is nothing to
// write, since nothing about a bank changes when it applies — so a refused asset
// leaves the bank's own row carrying an empty reference and the agent holding no
// account, which is the dead letter's state exactly. The difference is that this
// applicant was TOLD, and the telling survives only as a log line. Reaching it
// needs payment.OpenSettlementAccountTx to fail, so it is a store or a ledger
// giving way rather than a state the flow produces; measured by making the
// SECOND settlement account of a two-asset admission fail to write, the readings
// are reserves = [EUR 0], status Member, own USD settlement account empty — the
// same three the dead letter gives.
//
// mesh.Mesh.Admit's "partly admitted bank" is a DIFFERENT state and this
// endpoint reports it fully. There the acmt.007 arrived and the acknowledgement
// went missing, and the agent opens the account before it acknowledges
// (mesh.centralBank.receiveAdmission), so the agent's row has both assets and
// this reports two rows — with the second showing a reserve the bank cannot
// spend, since payment.DepositTx resolves through the BANK's record and that one
// has no reference. That is precisely what payment.RecordMembershipTx describes:
// a deposit in that asset fails while the operator console cheerfully reports
// the reserve. Measured, on a bank admitted in both assets whose own USD
// reference was then cleared: reserves = [EUR 0, USD 0], status Member, its own
// row carrying an empty USD settlement account.
//
// So the recipe for finding any of them is not "count the rows against the
// assets". For the two the agent answers nothing for it is a bank whose own row
// names an asset with NO settlement reference and which has no reserve row in
// it; for Admit's it is the same empty reference WITH a reserve row. What the
// first recipe cannot do is what nothing here can: it returns the refused
// applicant beside the dead-lettered one, because "nobody could tell it" and "it
// was told no" leave the same rows, and the only record of the refusal is in a
// log. A reconciliation that acts on that list has to expect a bank that is not
// stuck at all.
//
// Both comparisons need the bank's own assets, which GET /members and GET /me
// carry and which no screen in this repository renders today — so it is two API
// reads, not something the console shows. Turning any of them into "will not
// finish" needs a clock, and that is Task 19's reconciliation rather than this
// handler's.
func (s *Server) reserveRows(r *http.Request, p *payment.Bank) ([]reserveDTO, error) {
	codes := make([]string, 0, len(p.Assets))
	for code := range p.Assets {
		codes = append(codes, string(code))
	}
	sort.Strings(codes)

	out := make([]reserveDTO, 0, len(codes))
	for _, code := range codes {
		bal, err := s.network().ReserveBalance(r.Context(), p.ID, ledger.AssetCode(code))
		// No account, no row — of either kind. See the doc above.
		if errors.Is(err, payment.ErrSettlementMemberNotFound) ||
			errors.Is(err, payment.ErrParticipantAssetNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, reserveDTO{Participant: string(p.ID), Asset: code, Reserve: int64(bal)})
	}
	return out, nil
}
