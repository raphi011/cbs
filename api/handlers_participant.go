package api

import (
	"errors"
	"net/http"
	"sort"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
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
// and it can open NO customer account yet: an account is opened with an address,
// an address is minted under a bank code, and the code is what the registry has
// not answered with. Nor can it LODGE anything on reserve, since only the central
// bank can credit an account in the central bank's book and none is held for this
// bank yet — 422 from POST /lodgements, naming the reserve account it cannot
// name. It is in no routing
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
// the write fails. So a refusal leaves no row, no actor and nothing to clean up.
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
	p, err := s.mesh.Admit(r.Context(), req.Name, iso20022.BIC(req.BIC), iban.Country(req.Country), assets)
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
		// The advice is all this adds; the sentence about the address comes from the
		// error alone.
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

// handleListParticipants answers every bank this deployment holds a database
// for, each read out of its own database, ascending by address.
//
// # No institution is asked, because none of them knows
//
// It is the SETTLEMENT AGENT's read and not the clearing house's: the csm shape
// has no banks table, and the roster is not a substitute — a bank founded and
// not yet admitted has no roster entry and is precisely the bank this listing
// exists to show, since participantDTO.Status is what tells "Founded" from
// "Member" apart.
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
// that are not this listener's. So does POST /members beside it, which founds a
// bank and writes that bank's database whole. Both are the operator's acts over a
// deployment rather than the settlement agent's over its own books; the router
// comment in surface.go is where that exception is written down, and this is one
// of the two routes it covers.
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
// has no reserve to lodge into. Taking cash in is not refused the same way — see
// TestABankTheSchemeHasNotAnsweredForCanNeitherOpenAnAccountNorLodge.
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
// account per asset, written by the agent when it answered an acmt.007 — so
// every row this builds is a reserve this institution actually keeps.
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
// An acmt.007 that never reached the settlement agent — a dead letter on the way
// out — leaves the agent holding a row without that asset, permanently. An
// acmt.007 the agent REFUSED leaves exactly the same rows:
// mesh.bank.receiveAdmissionRejection writes nothing, since nothing about a bank
// changes when it applies, so the bank's own row carries an empty reference and
// the agent holds no account. The two were already indistinguishable from each
// other — "nobody could tell it" and "it was told no" leave identical state and
// the refusal survives only as a log line — and both now look, from this
// endpoint, like a member admitted in one asset, which is also what a two-asset
// admission looks like between its two commits. The difference is TIME and
// nothing here watches a clock.
//
// mesh.Mesh.Admit's "partly admitted bank" is a DIFFERENT state and this endpoint
// still reports it fully. There the acmt.007 arrived and the acknowledgement went
// missing, and the agent opens the account before it acknowledges
// (mesh.centralBank.receiveAdmission), so its row has both assets and this
// reports two rows — the second showing a reserve the bank cannot spend, since
// payment.DepositTx resolves through the BANK's record and that one has no
// reference. That is precisely what payment.RecordMembershipTx describes: a
// deposit in that asset fails while the operator console cheerfully reports the
// reserve.
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
