package bank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/ledger"
)

// A bank checking its own books, on that bank's own listener and nowhere else.
//
// Every route here reads one database — this bank's — and the statements it was
// sent. That is what makes them a bank's acts rather than a supervisor's: the
// instrument that holds every institution's books against each other is
// payment/recon, which opens all N+2 databases precisely because no institution
// may, and which is therefore a test harness and not a route.
//
// So there is nowhere on these four to name another bank, and no version of them
// belongs on the clearing house's port or the settlement agent's. The clearing
// house has no ledger at all; the settlement agent holds neither of the two
// accounts aged here and holds its own reserve register rather than a member's
// claim on it. See payment.Network.ReconcileTx, which refuses both by name.
func (s *surface) registerReconciliationRoutes(mux *api.Router) {
	// A POST, and it is the one act among the four. Reconcile appends
	// reconciliation.run and one reconciliation.break per finding to this bank's
	// own audit log, in the same unit of work as the read — which is what makes a
	// finding a claim about a consistent snapshot rather than about several reads
	// taken while a cut-off committed underneath them. A GET that writes would be
	// a lie about the method, and a cache in front of it would swallow the run.
	//
	// It answers 200 and not 201. Nothing is created: there is no findings table,
	// deliberately, because a finding is a pure function of the books at a moment
	// and a stored one is a cache that can disagree with them. What comes back is
	// the report, and it names no resource to go and fetch.
	mux.HandleFunc("POST /reconciliation", api.Handle(http.StatusOK, s.handleReconcile))
	// The stored camt.053s, read back. This is where a break naming a reference
	// sends its reader.
	mux.HandleFunc("GET /settlement-advices", api.Handle(http.StatusOK, s.handleListSettlementAdvices))
	// The two in-transit accounts, aged. Reads, and they judge nothing that
	// running them again would change.
	mux.HandleFunc("GET /clearing-suspense/ageing", api.Handle(http.StatusOK, s.handleAgeClearingSuspense))
	mux.HandleFunc("GET /unclaimed-balances/ageing", api.Handle(http.StatusOK, s.handleAgeUnclaimedBalances))
}

// assetParam reads the asset a report is about off the query string.
//
// It is required rather than defaulted and rather than optional. A bank holds one
// reserve, one clearing suspense and one unclaimed-balances account PER ASSET,
// and a report that ran over all of them at once would add euros to dollars —
// which is why payment.BankAccounts is resolved per asset and why nothing in the
// domain will take the question without one. Defaulting it to EUR would answer
// confidently about an asset the caller did not ask about.
func assetParam(r *http.Request) (ledger.AssetCode, error) {
	code := r.URL.Query().Get("asset")
	if code == "" {
		return "", api.BadRequest("asset is required: a bank holds one reserve and one clearing suspense per asset, and a report over all of them at once would add one money to another")
	}
	return ledger.AssetCode(code), nil
}

// handleReconcile runs this bank's own reconciliation for one asset.
//
// What comes back has two kinds of finding in it and only one is a defect. A
// BREAK is something this bank's own books say cannot be true — a mirror leg
// posted against the wrong figure, a reserve moved with no statement behind it, a
// statement claiming a posting that is not there. A POSITION is money in flight,
// with how long it has been: a clearing suspense has no closed-form identity from
// inside one bank, because the mirror leg is netted and names no payment, so it
// is reported with an age and never as a break.
func (s *surface) handleReconcile(r *http.Request) (api.ReconciliationDTO, error) {
	asset, err := assetParam(r)
	if err != nil {
		return api.ReconciliationDTO{}, err
	}
	rec, err := s.network().Reconcile(r.Context(), asset)
	if err != nil {
		return api.ReconciliationDTO{}, err
	}
	return api.ToReconciliationDTO(rec), nil
}

// handleListSettlementAdvices answers every statement this bank has been sent,
// oldest first, in every asset.
//
// No asset filter, unlike the three routes beside it, and the difference is not
// an inconsistency: those three read ONE account each and an account belongs to
// one asset, where this reads a bank's whole correspondence and every row on it
// carries its own asset. A client wanting one asset's statements has the field to
// filter on; a client wanting all of them cannot assemble the list from a route
// that demands an asset it does not know the bank operates in.
func (s *surface) handleListSettlementAdvices(r *http.Request) ([]api.SettlementAdviceDTO, error) {
	advices, err := s.network().ListSettlementAdvices(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]api.SettlementAdviceDTO, 0, len(advices))
	for _, a := range advices {
		out = append(out, api.ToSettlementAdviceDTO(a))
	}
	return out, nil
}

// handleAgeClearingSuspense decomposes this bank's clearing suspense by age.
//
// It ages and does not judge: no rulebook puts a clock on a clearing suspense —
// what discharges it is a conversation — so no lot here carries a deadline and
// none is ever overdue, however old it is. What the report gives an operator is
// the age; what counts as too long is their judgement.
func (s *surface) handleAgeClearingSuspense(r *http.Request) (api.AgeingReportDTO, error) {
	asset, err := assetParam(r)
	if err != nil {
		return api.AgeingReportDTO{}, err
	}
	rep, err := s.network().AgeClearingSuspense(r.Context(), asset)
	if err != nil {
		return api.AgeingReportDTO{}, err
	}
	return api.ToAgeingReportDTO(rep), nil
}

// handleAgeUnclaimedBalances decomposes this bank's unclaimed balances by age
// and says what may be done about each part.
//
// The answer here is per payment and exact, where the suspense's is FIFO lots,
// because every credit into this account is one payment's diverted leg and
// carries its id. Each lot therefore comes back with a deadline or with the
// reason this bank cannot clear it — a pull whose return is the other bank's to
// send, a refund whose payer has already been sent it back once. See
// payment.AgeUnclaimedBalances.
func (s *surface) handleAgeUnclaimedBalances(r *http.Request) (api.AgeingReportDTO, error) {
	asset, err := assetParam(r)
	if err != nil {
		return api.AgeingReportDTO{}, err
	}
	rep, err := s.network().AgeUnclaimedBalances(r.Context(), asset)
	if err != nil {
		return api.AgeingReportDTO{}, err
	}
	return api.ToAgeingReportDTO(rep), nil
}
