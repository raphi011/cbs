// Package recon is the reconciliation harness: the one view of this system that
// no institution in it may have.
//
// It opens all N+2 databases at once — every member bank's, the clearing house's
// and the settlement agent's — and asks the questions that can only be asked
// from outside all of them. Does a bank's Reserve at Central Bank agree with the
// central bank's reserve liability for that bank? Has every bank's clearing
// suspense returned to zero, and if it has not, is there something in flight to
// account for it? Do the clearing house's cycles and the settlement agent's
// settlements describe the same cut-off? Do three institutions agree about the
// same payment, the same member, the same admission?
//
// Every one of those spans two databases, so no act in the domain can make it
// and none should be able to: an institution that could read another's books to
// check its own would not need messages, and this whole sub-project is about the
// messages. The harness is what a supervisor or an auditor would be — somebody
// with the right to see every ledger and no power to write in any of them.
//
// # It is the successor to the book recorder
//
// mesh/books_test.go's recordingStores watches which ledger book each unit of
// work reached and fails a test when one reaches somebody else's. Its whole
// subject is a CROSSING, and the store split made most crossings impossible
// rather than merely wrong. What the split cannot see is the defect class that
// replaces the crossing: two institutions' books that no longer agree, with no
// crossing anywhere and no message misdelivered. A payment settled at the
// central bank and never booked by the member, a return whose reserves reversed
// at one end, a cut-off discharged twice — none of those touches a book it
// should not, and every one of them is money in the wrong place.
//
// The spec called that class invisible to everything this sub-project had. This
// is the instrument for it.
//
// # It reports two kinds of finding and they are not the same kind
//
// A BREAK is something that cannot be true: two books disagree and there is
// nothing in this system that could make them agree again. It is a defect.
//
// An UNRECONCILED POSITION is a bank's clearing suspense that has not returned
// to zero with something outstanding to explain it — a payment still in flight,
// or a reserve movement the central bank has made and this bank has not booked.
// It is REAL, it is modelled on purpose, and it is not a defect: the interval
// between the settlement agent's commit and a member's is what the Settlement
// Finality Directive is about. Check fails a test on the first kind and reports
// the second, because a harness that treated an unreconciled position as a break
// would be a harness nobody could run against a network with a payment in it.
//
// # And ONE finding that is a report and can never be a break
//
// A member bank's routing directory is a COPY of the clearing house's roster,
// pulled by that member and used to derive where its payments go. A copy that is
// behind the roster is LEGAL BY CONSTRUCTION — it is the behaviour the whole
// subscription model was chosen for, and it is why a bank admitted this morning
// cannot be paid by a bank that pulled yesterday. So this harness says "Aurora's
// directory is two entries behind" and passes.
//
// A reconciliation that FAILED on a stale copy would assert the opposite of the
// design, and it would be an easy thing to write: the roster and the copy are two
// tables holding one pairing, which is exactly the shape of every break above.
// The difference is that the other pairs have no path back to agreement — nothing
// in the system could make a bank's reserve and the agent's liability differ
// legitimately — while this one has a path and the path is a request somebody
// makes. This is the most likely way the design gets quietly undone later, which
// is why it is written here and not only in a case.
//
// What IS a break is the pairing the copy comes FROM: the roster against the
// settlement agent's registry, and every account's address against the registry
// that issued its bank code. Those two cannot legitimately disagree.
//
// # Where the figures come from
//
// Nothing here goes through a payment.Network. A Network is one institution's
// handle and every method on it is an act that institution may perform; reading
// four institutions' books is not an act, so the harness reads through
// payment.Stores — the composition root's handle, and the only thing in this
// repository that holds more than one institution's database. See
// payment.Networks.Stores, whose doc says the same thing from the other side.
//
// # It is test-only by convention and not by compiler
//
// It is an ordinary package because the suites that want it are in three
// packages (mesh, seed and payment's own callers) and a _test.go file cannot be
// imported. Nothing in cmd/ or api/ may call it and nothing does: an institution
// running this would be an institution reading everybody's books. store/storetest
// and store/testenv are here for the same reason and under the same rule.
package recon

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// ---------------------------------------------------------------------------
// What a run finds
// ---------------------------------------------------------------------------

// Break is two books that disagree with nothing in this system able to make them
// agree again.
type Break struct {
	// Where names the books the disagreement is between, in the words a reader
	// needs: "AURODEFFXXX (EUR)" for one member's own, "the clearing house and
	// the settlement agent" for the two rows about one cut-off. It is prose
	// rather than an identifier because a break is read by a person deciding
	// which institution to go and ask.
	Where string
	// What is the disagreement, as one sentence, with both figures in it. A
	// break that named only the difference would send its reader to the two
	// databases to find out which side was wrong.
	What string
}

func (b Break) String() string { return b.Where + ": " + b.What }

// Unreconciled is one member bank's clearing suspense that has not returned to
// zero, together with everything outstanding that could account for it.
//
// It is the state the README calls the unreconciled position, and reporting it
// is the whole reason this type exists beside Break. A suspense balance on its
// own says nothing: the same figure is a bank mid-payment and a bank that has
// lost money, and the difference is entirely in whether anything is still owed
// to it. So the two lists travel with the figure.
//
// A bank with a non-zero suspense and BOTH lists empty is not one of these. It
// is a Break, and it is the most serious one this harness can report: money has
// left a customer, nothing is coming, and no institution in the system is in a
// position to notice.
type Unreconciled struct {
	Bank  iso20022.BIC
	Asset ledger.AssetCode
	// Suspense is the balance itself. Positive means the bank owes it onward.
	Suspense ledger.Amount

	// Unbooked is what the settlement agent has moved this bank's reserves by
	// and this bank has not booked: a cycle id per cut-off, a payment id per
	// return. It is derived by holding the agent's settlement register against
	// this bank's own advice rows, which is a comparison neither of them can
	// make — see SettlementAdvice, whose absence is the only detector there is.
	Unbooked []string
	// UnbookedMovement is what those references are worth, signed the way a
	// SettlementAdvice is: positive means this bank's reserve is due to rise.
	UnbookedMovement ledger.Amount

	// InFlight is every payment this bank's own copy has not carried to a
	// terminal state. Whose copy is the point: a bank's row and the clearing
	// house's legitimately disagree, and what a bank's suspense answers to is
	// what that bank was told.
	InFlight []payment.PaymentID
}

// StaleDirectory is one member bank's copy of the routing directory disagreeing
// with the roster it was copied from.
//
// It is a REPORT and never a break, and the distinction is the design rather than
// a tolerance. A member pulls a snapshot and routes from it, so between two pulls
// its copy is behind whatever the clearing house has published since — which is
// what makes a bank admitted this morning unpayable by a bank that pulled
// yesterday, and is the behaviour the subscription model exists to have. See the
// package doc.
//
// Missing is the ordinary direction and Extra is the interesting one. A copy is
// INCOMPLETE because allocations are never reassigned, so an entry the roster no
// longer publishes means a member left the scheme and this bank has not pulled
// since — still legal, still not a break, and still worth reporting, because a
// bank routing to an ex-member is a payment the clearing house will refuse.
type StaleDirectory struct {
	Bank iso20022.BIC
	// RefreshedAt is the instant the whole copy carries, or the zero time if this
	// bank has never pulled one at all. Every row of one snapshot shares it.
	RefreshedAt time.Time
	// Missing is published and not copied; Extra is copied and no longer
	// published. Both are BICs, in address order.
	Missing []iso20022.BIC
	Extra   []iso20022.BIC
}

func (d StaleDirectory) String() string {
	when := "never pulled"
	if !d.RefreshedAt.IsZero() {
		when = "pulled " + d.RefreshedAt.Format(time.RFC3339)
	}
	return fmt.Sprintf("%s (%s): %d published entries not copied %v, %d copied entries no longer published %v",
		d.Bank, when, len(d.Missing), d.Missing, len(d.Extra), d.Extra)
}

// Report is everything one run found.
type Report struct {
	Breaks       []Break
	Unreconciled []Unreconciled
	// Stale is every member whose routing directory disagrees with the roster. It
	// does not make a run fail; see StaleDirectory.
	Stale []StaleDirectory
}

// Reconciled reports whether the network's books agree. Unreconciled positions
// do not make it false; see the package doc for why the two are different kinds
// of finding.
func (r *Report) Reconciled() bool { return len(r.Breaks) == 0 }

func (r *Report) breakf(where, format string, args ...any) {
	r.Breaks = append(r.Breaks, Break{Where: where, What: fmt.Sprintf(format, args...)})
}

// at is the Where of a finding about one member's own books.
func at(bic iso20022.BIC, asset ledger.AssetCode) string {
	return fmt.Sprintf("%s (%s)", bic, asset)
}

// betweenCHAndAgent is the Where of a finding about the two rows one cut-off
// leaves in two institutions.
const betweenCHAndAgent = "the clearing house and the settlement agent"

// betweenCHAndRegistry is the Where of a finding about the pairing one admission
// leaves in two registers: the one the settlement agent allocated from, and the
// one the clearing house publishes. Separate from the constant above because the
// settlement agent keeps two registers and a reader has to know which.
const betweenCHAndRegistry = "the clearing house and the bank-code registry"

// ---------------------------------------------------------------------------
// Running one
// ---------------------------------------------------------------------------

// Check reconciles the whole deployment and fails the test on every break.
//
// It returns the report so that a test whose subject IS an unreconciled position
// — a payment left mid-flight on purpose, a bank that was told and could not
// book — can assert on it. A test that only wants "the books agree" ignores the
// return value.
func Check(tb testing.TB, nets *payment.Networks) *Report {
	tb.Helper()
	rep, err := Reconcile(context.Background(), nets)
	if err != nil {
		tb.Fatalf("recon: reading the deployment's books: %v", err)
	}
	for _, b := range rep.Breaks {
		tb.Errorf("reconciliation break — %s", b)
	}
	return rep
}

// Reconcile takes one snapshot of every institution's books and reports what it
// finds.
//
// The snapshot is taken store by store and NOT under one transaction, because
// there is no such thing: five databases is five units of work and no reader can
// span them. That is the honest shape of the question rather than a limitation
// of this code — a real supervisor reading five banks' books reads them one at a
// time too — but it means a run against a network that is still moving can
// report a difference that closed a moment later. Every caller in this
// repository drains the mesh first, and a caller that does not should expect to
// see the transport rather than the ledgers.
func Reconcile(ctx context.Context, nets *payment.Networks) (*Report, error) {
	snap, err := take(ctx, nets)
	if err != nil {
		return nil, err
	}
	rep := &Report{}
	snap.reservesMirror(rep)
	snap.suspenseIsExplained(rep)
	snap.cyclesAndSettlementsAgree(rep)
	snap.partiesHoldTheirCopy(rep)
	snap.admissionWroteItsThreeRows(rep)
	snap.addressesResolveToTheirIssuer(rep)
	snap.rosterAgreesWithTheRegistry(rep)
	snap.directoriesAgainstTheRoster(rep)
	return rep, nil
}

// ---------------------------------------------------------------------------
// The snapshot
// ---------------------------------------------------------------------------

// adviceKey is what identifies one settlement advice within a bank: the
// reference the statement carried and the asset it was in. Both, because a bank
// operating in two assets settles each separately and a cut-off and a return can
// be advised in the same asset under different references.
type adviceKey struct {
	reference string
	asset     ledger.AssetCode
}

// bankView is one member bank's own books, as that bank holds them.
type bankView struct {
	row      payment.Bank
	suspense map[ledger.AssetCode]ledger.Amount
	reserve  map[ledger.AssetCode]ledger.Amount
	advices  map[adviceKey]payment.SettlementAdvice
	payments map[payment.PaymentID]payment.Payment
	// addresses is every IBAN this bank has issued to a customer account, in the
	// order the register lists them. It is what the registry's allocations are
	// held against: an account addressed under somebody else's code is a payment
	// routable to the wrong institution, and no institution can see it.
	addresses []string
	// directory is this bank's own copy of the scheme's routing directory, which
	// is the one table here that is ALLOWED to disagree with its source. See
	// StaleDirectory.
	directory []payment.DirectoryEntry
}

// snapshot is every institution's books at one moment, as far as that is a thing
// that exists. See Reconcile.
type snapshot struct {
	// order is every address this deployment holds a database for, ascending, so
	// that two runs report their findings in the same order. payment.Stores.Banks
	// is where the ordering contract is stated.
	order []iso20022.BIC
	banks map[iso20022.BIC]*bankView

	// The settlement agent's.
	members     map[iso20022.BIC]payment.SettlementMember
	settlements []payment.Settlement
	// reserves is what the CENTRAL BANK's own book says each member holds, which
	// is the other half of every mirror comparison and is not derivable from
	// anything a member keeps.
	reserves map[iso20022.BIC]map[ledger.AssetCode]ledger.Amount

	// allocations is the settlement agent's SECOND register: which institution
	// holds which bank code, in which country. It is the ISSUER's own record and
	// no bank ever reads it, which is exactly why holding it against what banks
	// have issued is this harness's and nobody else's.
	allocations map[iban.Issuer]payment.BankCodeAllocation

	// The clearing house's.
	roster   map[iso20022.BIC]payment.RosterEntry
	cycles   []payment.ClearingCycle
	payments []payment.Payment

	// assetOf resolves a scheme to the asset it settles in. It is a function
	// rather than a map because schemes are code and not rows — see
	// payment.Networks.schemes — so the registry is the only place to ask, and
	// the clearing house's network is the one this harness asks through.
	assetOf func(payment.SchemeID) (ledger.AssetCode, bool)
}

func take(ctx context.Context, nets *payment.Networks) (*snapshot, error) {
	stores := nets.Stores()
	ch := nets.ClearingHouse()

	snap := &snapshot{
		banks:       map[iso20022.BIC]*bankView{},
		members:     map[iso20022.BIC]payment.SettlementMember{},
		reserves:    map[iso20022.BIC]map[ledger.AssetCode]ledger.Amount{},
		allocations: map[iban.Issuer]payment.BankCodeAllocation{},
		roster:      map[iso20022.BIC]payment.RosterEntry{},
		assetOf: func(id payment.SchemeID) (ledger.AssetCode, bool) {
			sc, ok := ch.Scheme(id)
			if !ok {
				return "", false
			}
			return sc.Asset(), true
		},
	}

	bics, err := stores.Banks(ctx)
	if err != nil {
		return nil, fmt.Errorf("recon: which banks this deployment holds: %w", err)
	}
	snap.order = bics
	for _, bic := range bics {
		store, err := stores.Bank(ctx, bic)
		if err != nil {
			return nil, fmt.Errorf("recon: opening %s's store: %w", bic, err)
		}
		view := &bankView{
			suspense: map[ledger.AssetCode]ledger.Amount{},
			reserve:  map[ledger.AssetCode]ledger.Amount{},
			advices:  map[adviceKey]payment.SettlementAdvice{},
			payments: map[payment.PaymentID]payment.Payment{},
		}
		if err := store.View(ctx, func(ctx context.Context, tx payment.Tx) error {
			row, err := tx.GetBank(ctx, payment.ParticipantID(bic))
			if err != nil {
				return err
			}
			view.row = row
			for asset, accts := range row.Assets {
				if view.suspense[asset], err = balanceOf(ctx, tx, row.BookID, accts.Suspense); err != nil {
					return err
				}
				if view.reserve[asset], err = balanceOf(ctx, tx, row.BookID, accts.Reserve); err != nil {
					return err
				}
			}
			payments, err := tx.ListPayments(ctx)
			if err != nil {
				return err
			}
			for _, p := range payments {
				view.payments[p.ID] = p
			}
			advices, err := tx.ListSettlementAdvices(ctx, row.BookID)
			if err != nil {
				return err
			}
			for _, a := range advices {
				view.advices[adviceKey{reference: a.Reference, asset: a.Asset}] = a
			}
			accounts, err := tx.ListDepositAccounts(ctx, row.BookID)
			if err != nil {
				return err
			}
			for _, a := range accounts {
				for _, id := range a.Identifiers {
					if id.Scheme == deposit.IdentifierIBAN {
						view.addresses = append(view.addresses, id.Value)
					}
				}
			}
			view.directory, err = tx.ListDirectoryEntries(ctx)
			return err
		}); err != nil {
			return nil, fmt.Errorf("recon: reading %s's own books: %w", bic, err)
		}
		snap.banks[bic] = view
	}

	if err := stores.CentralBank().View(ctx, func(ctx context.Context, tx payment.Tx) error {
		members, err := tx.ListSettlementMembers(ctx)
		if err != nil {
			return err
		}
		for _, m := range members {
			snap.members[m.BIC] = m
			held := map[ledger.AssetCode]ledger.Amount{}
			for asset, acct := range m.Accounts {
				if held[asset], err = balanceOf(ctx, tx, payment.CentralBankBook, acct); err != nil {
					return err
				}
			}
			snap.reserves[m.BIC] = held
		}
		allocations, err := tx.ListBankCodes(ctx)
		if err != nil {
			return err
		}
		for _, a := range allocations {
			snap.allocations[a.Issuer] = a
		}
		snap.settlements, err = tx.ListSettlements(ctx)
		return err
	}); err != nil {
		return nil, fmt.Errorf("recon: reading the settlement agent's books: %w", err)
	}

	if err := stores.ClearingHouse().View(ctx, func(ctx context.Context, tx payment.Tx) error {
		entries, err := tx.ListRosterEntries(ctx)
		if err != nil {
			return err
		}
		for _, e := range entries {
			snap.roster[e.BIC] = e
		}
		if snap.cycles, err = tx.ListCycles(ctx); err != nil {
			return err
		}
		snap.payments, err = tx.ListPayments(ctx)
		return err
	}); err != nil {
		return nil, fmt.Errorf("recon: reading the clearing house's books: %w", err)
	}

	return snap, nil
}

// balanceOf is one account's book balance, in the sign its own type says is
// positive. It reads the account first because the sign convention lives on
// AccountType and nowhere else (ledger.AccountType.NormalBalance), so a caller
// that assumed one would be right about a reserve and wrong about a suspense.
func balanceOf(ctx context.Context, tx payment.Tx, book ledger.BookID, id ledger.AccountID) (ledger.Amount, error) {
	acct, err := tx.GetAccount(ctx, book, id)
	if err != nil {
		return 0, fmt.Errorf("account %s in %s: %w", id, book, err)
	}
	return tx.BookBalance(ctx, book, id.Total(), acct.Type.NormalBalance())
}

// assetsOf is the assets one bank operates in, sorted, so that findings come out
// in the same order on every run. Bank.Assets is a map and map iteration is
// randomised.
func assetsOf(b payment.Bank) []ledger.AssetCode {
	return slices.Sorted(maps.Keys(b.Assets))
}

// ---------------------------------------------------------------------------
// What the settlement agent has moved and a bank has not booked
// ---------------------------------------------------------------------------

// unbooked is every reserve movement the settlement agent has made for one bank
// in one asset that the bank holds no advice row against, and what they come to.
//
// It is the harness's central derivation and the one thing here that no single
// institution could compute. The agent knows what it moved and not who booked
// it; the bank knows what it booked and not what else it was sent. Holding the
// two together is the whole job.
//
// There are two sources because reserves move for two reasons, and the second is
// invisible in the agent's own register:
//
//   - A CUT-OFF leaves a settlements row with one position per member, so the
//     movement is read straight off it, keyed by the cycle id the statement
//     carried. A position of zero is skipped, because the agent sends no
//     statement for one (payment.settlementLegsTx) and there is nothing for a
//     bank to book.
//   - A RETURN leaves the agent NOTHING — see settlements in
//     store/sqlite/schema/centralbank/0001_init.sql, which says so in as many
//     words: a cut-off writes a Settlement and a return writes no row at all,
//     because the only durable trace it needs is the idempotency key on the
//     reversal. So the movement is derived from the CLEARING HOUSE's copy of the
//     returned payment, and its direction is domain knowledge rather than a
//     lookup: a return sends the money back to the payer, so the DEBTOR's bank
//     gains and the CREDITOR's bank loses, on a pull exactly as on a push. The
//     debtor is the payer under both schemes; only who initiates differs.
//
// A payment whose two agents are one institution moves no reserves at all, and
// is skipped for that reason: SettleReturn moves reserves BETWEEN two members
// and there are none. See payment.PostReturnLegTx, which answers such a return
// out of its own book.
func (s *snapshot) unbooked(bic iso20022.BIC, asset ledger.AssetCode) (ledger.Amount, []string) {
	view := s.banks[bic]
	var movement ledger.Amount
	var refs []string

	for _, st := range s.settlements {
		if st.Asset != asset {
			continue
		}
		net, ok := st.NetPositions[bic]
		if !ok || net == 0 {
			continue
		}
		if _, booked := view.advices[adviceKey{reference: string(st.CycleID), asset: asset}]; booked {
			continue
		}
		movement += net
		refs = append(refs, string(st.CycleID))
	}

	for _, p := range s.payments {
		if p.Status != payment.Returned {
			continue
		}
		if a, ok := s.assetOf(p.Scheme); !ok || a != asset {
			continue
		}
		debtor, creditor := p.DebtorDetails.Agent, p.CreditorDetails.Agent
		if debtor == creditor {
			continue
		}
		var reversal ledger.Amount
		switch bic {
		case debtor:
			reversal = p.Amount
		case creditor:
			reversal = -p.Amount
		default:
			continue
		}
		if _, booked := view.advices[adviceKey{reference: string(p.ID), asset: asset}]; booked {
			continue
		}
		movement += reversal
		refs = append(refs, string(p.ID))
	}

	slices.Sort(refs)
	return movement, refs
}

// inFlight is every payment one bank's OWN copy has not carried to a terminal
// state, in one asset.
//
// Whose copy is load-bearing. The clearing house's row runs the state machine
// and a bank's row records what that bank was told, so the two legitimately
// disagree — a bank that answered an instruction is never sent the ACCP and its
// copy says Initiated while the network's says Accepted. What a bank's clearing
// suspense answers to is what THAT bank did, so this reads that bank's copy.
//
// Rejected counts as terminal because a bank's copy reaches it only when the
// reversal posted: a bank that cannot give the money back does not record the
// rejection either (payment.RejectAtBankTx, and
// TestAFailedRejectionAtABankLeavesThatBanksCopyUntouched).
func (v *bankView) inFlight(asset ledger.AssetCode, assetOf func(payment.SchemeID) (ledger.AssetCode, bool)) []payment.PaymentID {
	var out []payment.PaymentID
	for id, p := range v.payments {
		switch p.Status {
		case payment.Settled, payment.Rejected, payment.Returned:
			continue
		}
		if a, ok := assetOf(p.Scheme); !ok || a != asset {
			continue
		}
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// ---------------------------------------------------------------------------
// The checks
// ---------------------------------------------------------------------------

// reservesMirror holds every bank's Reserve at Central Bank against the central
// bank's own liability to that bank.
//
// It is the classic nostro/vostro check: the bank's asset and the central bank's
// liability are two records of one account in two databases, written by two
// institutions in two units of work. They can disagree, so it is worth asking
// whether they do.
//
// They are ALLOWED to disagree by exactly the movements the agent has made and
// the bank has not booked, and by nothing else. That is the equation:
//
//	the bank's own reserve  +  what it has been told and not booked
//	  =  the central bank's reserve for it
//
// Anything left over is a break. It is worth being precise about what a break
// here means, because the two directions are different failures: the bank
// showing MORE than the agent is a bank that has booked a movement the agent
// never made, and the bank showing less with nothing outstanding is a movement
// the agent made that has gone nowhere.
func (s *snapshot) reservesMirror(rep *Report) {
	for _, bic := range s.order {
		view := s.banks[bic]
		for _, asset := range assetsOf(view.row) {
			own := view.reserve[asset]
			pending, refs := s.unbooked(bic, asset)

			held, isMember := s.reserves[bic][asset]
			if !isMember {
				// A founded bank has a Reserve account and no account at the
				// agent to mirror, which is correct and quiet — until something
				// has been put in it, which nothing can do: a lodgement is
				// refused a bank the agent holds no account for.
				if own != 0 || len(refs) > 0 {
					rep.breakf(at(bic, asset),
						"this bank's own reserve stands at %d and the settlement agent holds no account for it in this asset at all",
						own)
				}
				continue
			}
			if own+pending == held {
				continue
			}
			rep.breakf(at(bic, asset),
				"the bank's own reserve says %d and the settlement agent's says %d; %d of movement is advised and unbooked (%s), which leaves %d nobody can account for",
				own, held, pending, refsOrNone(refs), held-own-pending)
		}
	}
}

// suspenseIsExplained asks every bank's clearing suspense the only question that
// can be asked of it: if it is not zero, what is still owed?
//
// A clearing suspense holds money that has left a customer and not yet settled
// between banks, so at rest it is zero and in motion it is not. There are
// exactly two things that can leave it non-zero legitimately — a payment this
// bank has not carried to a terminal state, and a reserve movement it has been
// told about and not booked — and a suspense balance with neither is the failure
// this whole harness exists to find: money that left a customer, that nothing is
// coming for, and that no institution in the system is in a position to notice.
//
// The absence of an advice row is the detector rather than a status on one,
// which is the ruling payment.AdviceAdvised carries at length: the row is
// written with the mirror leg in one unit of work, so a bank that was told and
// could not book looks exactly like a bank that was never told. Reading it from
// the agent's side is what makes the two distinguishable, and only somebody
// holding both databases can.
func (s *snapshot) suspenseIsExplained(rep *Report) {
	for _, bic := range s.order {
		view := s.banks[bic]
		for _, asset := range assetsOf(view.row) {
			suspense := view.suspense[asset]
			if suspense == 0 {
				continue
			}
			movement, refs := s.unbooked(bic, asset)
			flight := view.inFlight(asset, s.assetOf)
			if len(refs) == 0 && len(flight) == 0 {
				rep.breakf(at(bic, asset),
					"this bank's clearing suspense holds %d with nothing in flight and every advice booked; that money has left a customer and nothing in this system will settle it",
					suspense)
				continue
			}
			rep.Unreconciled = append(rep.Unreconciled, Unreconciled{
				Bank:             bic,
				Asset:            asset,
				Suspense:         suspense,
				Unbooked:         refs,
				UnbookedMovement: movement,
				InFlight:         flight,
			})
		}
	}
}

// cyclesAndSettlementsAgree holds the clearing house's cut-off against the
// settlement agent's discharge of it.
//
// These are two rows in two databases about one event and NEITHER points at the
// other in the way a reader expects. A cycle carries no settlement id — the
// agent allocates that inside its own unit of work and no message carries it
// back — and the agent holds no cycles table to join to. The one link is
// Settlement.CycleID, the agent's own row naming what it discharged, and that is
// what this walks in both directions.
//
// The asset comparison is here because the settlement's asset is a column of the
// agent's own (Settlement.Asset) and the cycle's is the clearing house's scheme.
// The two institutions each say what the cut-off was in, out of their own books,
// and the harness is what compares them.
func (s *snapshot) cyclesAndSettlementsAgree(rep *Report) {
	byCycle := map[payment.CycleID][]payment.Settlement{}
	for _, st := range s.settlements {
		byCycle[st.CycleID] = append(byCycle[st.CycleID], st)
	}
	cycles := map[payment.CycleID]payment.ClearingCycle{}
	for _, c := range s.cycles {
		cycles[c.ID] = c
	}

	for _, c := range s.cycles {
		found := byCycle[c.ID]
		if c.Status != payment.CycleSettled {
			if len(found) > 0 {
				rep.breakf(betweenCHAndAgent,
					"cycle %s is %s at the clearing house and the settlement agent has discharged it as settlement %s",
					c.ID, c.Status, found[0].ID)
			}
			continue
		}
		switch len(found) {
		case 0:
			rep.breakf(betweenCHAndAgent,
				"cycle %s is Settled at the clearing house and the settlement agent holds no settlement against it",
				c.ID)
			continue
		case 1:
		default:
			rep.breakf(betweenCHAndAgent,
				"cycle %s has been discharged %d times at the settlement agent (%s); a cut-off is settled whole or not at all",
				c.ID, len(found), settlementIDs(found))
			continue
		}
		st := found[0]

		if asset, ok := s.assetOf(c.Scheme); !ok {
			rep.breakf(betweenCHAndAgent,
				"cycle %s is under scheme %s, which no network in this deployment has registered, so what it settled in cannot be checked",
				c.ID, c.Scheme)
		} else if asset != st.Asset {
			rep.breakf(betweenCHAndAgent,
				"cycle %s clears scheme %s, which settles in %s, and the settlement agent recorded discharging it in %s",
				c.ID, c.Scheme, asset, st.Asset)
		}

		for bic, net := range sortedPositions(c.NetPositions) {
			if got := st.NetPositions[bic]; got != net {
				rep.breakf(betweenCHAndAgent,
					"cycle %s netted %s to %d and the settlement agent discharged %d for it",
					c.ID, bic, net, got)
			}
		}
		for bic, net := range sortedPositions(st.NetPositions) {
			if _, ok := c.NetPositions[bic]; !ok && net != 0 {
				rep.breakf(betweenCHAndAgent,
					"the settlement agent moved %d for %s under cycle %s, which the clearing house's cycle names no position for",
					net, bic, c.ID)
			}
		}
	}

	for _, st := range s.settlements {
		if _, ok := cycles[st.CycleID]; !ok {
			rep.breakf(betweenCHAndAgent,
				"settlement %s discharged cycle %s, which the clearing house holds no cycle for",
				st.ID, st.CycleID)
		}
		var sum ledger.Amount
		for _, net := range st.NetPositions {
			sum += net
		}
		if sum != 0 {
			rep.breakf("the settlement agent",
				"settlement %s moved net %d across its members; a netting sums to zero or somebody has been paid out of nothing",
				st.ID, sum)
		}
	}
}

// partiesHoldTheirCopy checks that the three institutions a payment passes
// through hold three rows about the same payment.
//
// A payment is three rows and they legitimately differ — the clearing house's
// has no legs, a bank's has no cycle, and their statuses run ahead of each
// other. What they may NOT differ about is the payment itself: the amount and
// the scheme are what was instructed, and a bank whose copy says a different
// figure from the clearing house's is a bank that will settle the wrong number.
//
// It walks only payments the clearing house has taken into a cycle, and that
// bound is the honest one rather than a convenience. A payment the receiving
// bank REFUSED never became a row at that bank — it answered and wrote nothing —
// so requiring two copies of every payment the clearing house holds would report
// every rejection as a break. From Cleared onwards both banks have acted.
func (s *snapshot) partiesHoldTheirCopy(rep *Report) {
	for _, p := range s.payments {
		switch p.Status {
		case payment.Cleared, payment.Settled, payment.Returned:
		default:
			continue
		}
		parties := []iso20022.BIC{p.DebtorDetails.Agent, p.CreditorDetails.Agent}
		if parties[0] == parties[1] {
			parties = parties[:1]
		}
		for _, bic := range parties {
			view, deployed := s.banks[bic]
			if !deployed {
				rep.breakf("the clearing house",
					"payment %s names %s as a party and this deployment holds no database for that address",
					p.ID, bic)
				continue
			}
			own, held := view.payments[p.ID]
			if !held {
				rep.breakf(string(bic),
					"the clearing house says this bank is a party to payment %s and has carried it to %s; this bank holds no copy of it",
					p.ID, p.Status)
				continue
			}
			if own.Amount != p.Amount {
				rep.breakf(string(bic),
					"payment %s is %d on this bank's copy and %d on the clearing house's",
					p.ID, own.Amount, p.Amount)
			}
			if own.Scheme != p.Scheme {
				rep.breakf(string(bic),
					"payment %s is under scheme %s on this bank's copy and %s on the clearing house's",
					p.ID, own.Scheme, p.Scheme)
			}
		}
	}
}

// admissionWroteItsThreeRows holds one admission's three rows against each
// other: the bank's own record of itself, the settlement agent's account, and
// the clearing house's routing entry.
//
// This is the reconciliation payment.BankStatus's doc asks for by name. There is
// no Applied state between Founded and Member because nothing would read it, and
// what "the request is out" is worth knowing for is a STUCK admission — which
// needs a walk of both ends rather than a column on one of them. This is that
// walk: a bank calling itself a Member that no agent holds an account for, or an
// agent holding an account for a bank the clearing house does not route to, is
// an admission that half happened.
//
// The per-asset comparison is the partly-admitted bank, which
// payment.RosterEntry.Assets records as the case its own reader exists for: one
// acmt.007 asks for one currency, so a two-asset admission commits twice and an
// agent that answers one and refuses the other leaves a Member with internal
// accounts in both assets and a settlement account in one.
func (s *snapshot) admissionWroteItsThreeRows(rep *Report) {
	for _, bic := range s.order {
		view := s.banks[bic]
		member, hasAccount := s.members[bic]
		entry, routed := s.roster[bic]

		switch view.row.Status {
		case payment.BankMember:
			if !hasAccount {
				rep.breakf(string(bic),
					"this bank's own row says it is a %s and the settlement agent holds no account for it",
					payment.BankMember)
			}
			if !routed {
				rep.breakf(string(bic),
					"this bank's own row says it is a %s and the clearing house does not route to it",
					payment.BankMember)
			}
			if hasAccount && routed {
				settled := slices.Sorted(maps.Keys(member.Accounts))
				admitted := slices.Sorted(slices.Values(entry.Assets))
				if !slices.Equal(settled, admitted) {
					rep.breakf(string(bic),
						"the clearing house routes this bank in %v and the settlement agent holds accounts for it in %v; a cut-off in the difference could be cleared and not settled",
						admitted, settled)
				}
			}
		case payment.BankFounded:
			if hasAccount {
				rep.breakf(string(bic),
					"this bank's own row says it is only %s and the settlement agent holds accounts for it in %v",
					payment.BankFounded, slices.Sorted(maps.Keys(member.Accounts)))
			}
			if routed {
				rep.breakf(string(bic),
					"this bank's own row says it is only %s and the clearing house routes payments to it",
					payment.BankFounded)
			}
		default:
			rep.breakf(string(bic), "this bank's own row carries the status %q, which is neither %s nor %s",
				view.row.Status, payment.BankFounded, payment.BankMember)
		}
	}

	for _, bic := range slices.Sorted(maps.Keys(s.members)) {
		if _, deployed := s.banks[bic]; !deployed {
			rep.breakf("the settlement agent",
				"a settlement account is held for %s, an address this deployment has no bank at", bic)
		}
	}
	for _, bic := range slices.Sorted(maps.Keys(s.roster)) {
		if _, deployed := s.banks[bic]; !deployed {
			rep.breakf("the clearing house",
				"payments are routed to %s, an address this deployment has no bank at", bic)
		}
	}
}

// addressesResolveToTheirIssuer holds every customer address in the deployment
// against the registry that issued the bank code inside it.
//
// This is the invariant the whole routing design rests on: THE BANK CODE IN AN
// IBAN IDENTIFIES THE BANK HOLDING THE ACCOUNT. A payer's bank derives a
// counterparty's agent from it and sends there; if an account at one bank carries
// another bank's code, the payment is routable to the wrong institution and the
// bank it reaches answers AC01 for an address that genuinely exists somewhere.
// That is a defect no institution can see — the issuing register is the settlement
// agent's, the account is a member's, and the two never meet.
//
// Two shapes of break, and they are different defects. An address under a code
// the registry has never allocated is an account nobody in this scheme can be
// paid at; an address under a code allocated to a DIFFERENT bank is the
// misrouting one, and it is the reason virtual IBANs are named as out of scope
// (see the design): a PSP issuing addresses under another institution's range
// would produce exactly this and would be legitimate.
//
// A malformed address is a break too, and a plain one: a register that minted a
// value its own package will not parse has stored something no payer can use.
func (s *snapshot) addressesResolveToTheirIssuer(rep *Report) {
	for _, bic := range s.order {
		for _, addr := range s.banks[bic].addresses {
			parsed, err := iban.Parse(addr)
			if err != nil {
				rep.breakf(string(bic), "this bank holds the address %q, which is not a valid IBAN: %v", addr, err)
				continue
			}
			code, err := parsed.BankCode()
			if err != nil {
				rep.breakf(string(bic), "no bank code can be read out of the address %q: %v", addr, err)
				continue
			}
			issuer := iban.Issuer{Country: parsed.Country(), BankCode: code}
			alloc, allocated := s.allocations[issuer]
			if !allocated {
				rep.breakf(string(bic),
					"the address %s carries the allocation %s %s, which no registry in this deployment has issued",
					addr, issuer.Country, issuer.BankCode)
				continue
			}
			if alloc.BIC != bic {
				rep.breakf(string(bic),
					"the address %s carries %s %s, which is allocated to %s; a payment quoting it would be routed there",
					addr, issuer.Country, issuer.BankCode, alloc.BIC)
			}
		}
	}
}

// rosterAgreesWithTheRegistry holds the clearing house's published pairing
// against the settlement agent's own record of what it allocated.
//
// The roster is what every member COPIES, so a roster that disagreed with the
// registry would put the wrong pairing into every subscriber's directory at once
// — and neither institution can check the other, which is the whole reason both
// tables exist. The clearing house learns the allocation from the acmt.010 that
// writes the row; a drift here means that message and the register it came from
// have parted company.
//
// A member with no allocation published is the other half, and it is a break
// rather than a state: a bank whose customers have addresses and whose code
// nobody publishes is a bank that can pay and cannot be paid, with nothing in the
// system able to say so.
func (s *snapshot) rosterAgreesWithTheRegistry(rep *Report) {
	for _, bic := range slices.Sorted(maps.Keys(s.roster)) {
		entry := s.roster[bic]
		if !entry.Issuer.Allocated() {
			rep.breakf("the clearing house",
				"%s is routed to with no allocation published; every member copying this row has nothing to route an address to it on", bic)
			continue
		}
		alloc, allocated := s.allocations[entry.Issuer]
		if !allocated {
			rep.breakf(betweenCHAndRegistry,
				"%s is published under %s %s, which the registry has never allocated",
				bic, entry.Issuer.Country, entry.Issuer.BankCode)
			continue
		}
		if alloc.BIC != bic {
			rep.breakf(betweenCHAndRegistry,
				"%s is published under %s %s, which the registry allocated to %s",
				bic, entry.Issuer.Country, entry.Issuer.BankCode, alloc.BIC)
		}
	}
	for _, issuer := range slices.SortedFunc(maps.Keys(s.allocations), func(a, b iban.Issuer) int {
		return cmp.Or(cmp.Compare(a.Country, b.Country), cmp.Compare(a.BankCode, b.BankCode))
	}) {
		alloc := s.allocations[issuer]
		if _, deployed := s.banks[alloc.BIC]; !deployed {
			rep.breakf("the settlement agent",
				"%s %s is allocated to %s, an address this deployment has no bank at",
				issuer.Country, issuer.BankCode, alloc.BIC)
		}
	}
}

// directoriesAgainstTheRoster REPORTS each member's copy against what the
// clearing house publishes, and never fails on the difference.
//
// See StaleDirectory and the package doc. A copy that is behind is the behaviour
// the subscription model was chosen for, so the finding is a line an operator
// reads — "two entries not copied, pulled three days ago" — and a run holding one
// still passes. Turning this into a break is the mistake this comment exists to
// stop; the pairings that CANNOT legitimately differ are the two checks above.
//
// It reports a bank that has never pulled at all, with an empty copy, exactly as
// it reports one behind by two entries. There is no third state and no threshold:
// how far behind is too far is a question about a deployment, and this harness
// answers questions about books.
func (s *snapshot) directoriesAgainstTheRoster(rep *Report) {
	for _, bic := range s.order {
		view := s.banks[bic]
		copied := map[iso20022.BIC]bool{}
		var refreshedAt time.Time
		for _, e := range view.directory {
			copied[e.BIC] = true
			if e.RefreshedAt.After(refreshedAt) {
				refreshedAt = e.RefreshedAt
			}
		}
		var missing, extra []iso20022.BIC
		for _, published := range slices.Sorted(maps.Keys(s.roster)) {
			if !copied[published] {
				missing = append(missing, published)
			}
		}
		for _, held := range slices.Sorted(maps.Keys(copied)) {
			if _, published := s.roster[held]; !published {
				extra = append(extra, held)
			}
		}
		if len(missing) == 0 && len(extra) == 0 {
			continue
		}
		rep.Stale = append(rep.Stale, StaleDirectory{
			Bank: bic, RefreshedAt: refreshedAt, Missing: missing, Extra: extra,
		})
	}
}

// ---------------------------------------------------------------------------
// Reporting helpers
// ---------------------------------------------------------------------------

// sortedPositions iterates a net-position map in address order, because map
// iteration is randomised and a break reported in a different order on every run
// is a break nobody can diff.
func sortedPositions(m map[iso20022.BIC]ledger.Amount) func(func(iso20022.BIC, ledger.Amount) bool) {
	keys := slices.SortedFunc(maps.Keys(m), func(a, b iso20022.BIC) int { return cmp.Compare(a, b) })
	return func(yield func(iso20022.BIC, ledger.Amount) bool) {
		for _, k := range keys {
			if !yield(k, m[k]) {
				return
			}
		}
	}
}

func settlementIDs(sts []payment.Settlement) string {
	ids := make([]string, 0, len(sts))
	for _, st := range sts {
		ids = append(ids, string(st.ID))
	}
	slices.Sort(ids)
	return joinOr(ids, "none")
}

func refsOrNone(refs []string) string { return joinOr(refs, "none") }

func joinOr(items []string, empty string) string {
	if len(items) == 0 {
		return empty
	}
	out := items[0]
	for _, s := range items[1:] {
		out += ", " + s
	}
	return out
}
