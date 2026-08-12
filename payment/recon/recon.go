// Package recon is the reconciliation harness: the one view of this system that
// no institution in it may have.
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
	// needs: "AURODEFFXXX (EUR)" for one member's own, "the clearing house and the
	// settlement agent" for the two rows about one cut-off.
	Where string
	// What is the disagreement, as one sentence, with both figures in it. A
	// break that named only the difference would send its reader to the two
	// databases to find out which side was wrong.
	What string
}

func (b Break) String() string { return b.Where + ": " + b.What }

// Unreconciled is one member bank's clearing suspense that has not returned to
// zero, together with everything outstanding that could account for it.
type Unreconciled struct {
	Bank  iso20022.BIC
	Asset ledger.AssetCode
	// Suspense is the balance itself. Positive means the bank owes it onward.
	Suspense ledger.Amount

	// Unbooked is what the settlement agent has moved this bank's reserves by and
	// this bank has not booked: a cycle id per cut-off, a payment id per return.
	Unbooked []string
	// UnbookedMovement is what those references are worth, signed the way a
	// SettlementAdvice is: positive means this bank's reserve is due to rise.
	UnbookedMovement ledger.Amount

	// InFlight is every payment this bank's own copy has not carried to a terminal
	// state.
	InFlight []payment.PaymentID
}

// StaleDirectory is one member bank's copy of the routing directory disagreeing
// with the roster it was copied from.
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

// betweenCHAndRegistry is the Where of a finding about the pairing one
// admission leaves in two registers: the one the settlement agent allocated
// from, and the one the clearing house publishes.
const betweenCHAndRegistry = "the clearing house and the bank-code registry"

// ---------------------------------------------------------------------------
// Running one
// ---------------------------------------------------------------------------

// Check reconciles the whole deployment and fails the test on every break.
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
	snap.partiesAreMembers(rep)
	snap.addressesResolveToTheirIssuer(rep)
	snap.rosterAgreesWithTheRegistry(rep)
	snap.directoriesAgainstTheRoster(rep)
	return rep, nil
}

// ---------------------------------------------------------------------------
// The snapshot
// ---------------------------------------------------------------------------

// adviceKey is what identifies one settlement advice within a bank: the
// reference the statement carried and the asset it was in.
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
	// order the register lists them.
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
	// holds which bank code, in which country.
	allocations map[iban.Issuer]payment.BankCodeAllocation

	// The clearing house's.
	roster   map[iso20022.BIC]payment.RosterEntry
	cycles   []payment.ClearingCycle
	payments []payment.Payment

	// assetOf resolves a scheme to the asset it settles in.
	assetOf func(payment.SchemeID) (ledger.AssetCode, bool)

	// schemeOf is the same registry read one step earlier, for the one question an
	// asset cannot answer: which of a payment's two banks INSTRUCTED it.
	schemeOf func(payment.SchemeID) (payment.Scheme, bool)
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
		schemeOf: ch.Scheme,
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
		if err := store.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
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

	if err := stores.CentralBank().View(ctx, func(ctx context.Context, tx payment.CentralBankTx) error {
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

	if err := stores.ClearingHouse().View(ctx, func(ctx context.Context, tx payment.CsmTx) error {
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
// positive.
func balanceOf(ctx context.Context, tx ledger.Tx, book ledger.BookID, id ledger.AccountID) (ledger.Amount, error) {
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
// in one asset that the bank holds no advice row against, and what they come
// to.
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
func (s *snapshot) reservesMirror(rep *Report) {
	for _, bic := range s.order {
		view := s.banks[bic]
		for _, asset := range assetsOf(view.row) {
			own := view.reserve[asset]
			pending, refs := s.unbooked(bic, asset)

			held, isMember := s.reserves[bic][asset]
			if !isMember {
				// A founded bank has a Reserve account and no account at the agent to
				// mirror, which is correct and quiet — until something has been put in it,
				// which nothing can do: a lodgement is refused a bank the agent holds no
				// account for.
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

// suspenseIsExplained asks every bank's clearing suspense the only question
// that can be asked of it: if it is not zero, what is still owed?
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
			// Unless there was nothing to discharge.
			if payment.NetsToNothing(c) {
				continue
			}
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
		receiver, uncredited := receiverOf(s, p)
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
			// A settled payment whose RECEIVING bank has not moved off Initiated.
			if p.Status == payment.Settled && own.Status == payment.Initiated && bic == receiver {
				rep.breakf(string(bic), "payment %s settled at the clearing house and this bank still holds it as Initiated; "+
					"it was never handed the instruction, so %s", p.ID, uncredited)
			}
		}
	}
}

// receiverOf is the bank a released file is addressed to, and what it was never
// able to do with the instruction it did not get.
func receiverOf(s *snapshot, p payment.Payment) (iso20022.BIC, string) {
	if scheme, ok := s.schemeOf(p.Scheme); ok && scheme.Direction() == payment.Pull {
		return p.DebtorDetails.Agent,
			"its payer was never debited and the collection settled against money it never took"
	}
	return p.CreditorDetails.Agent,
		"its payee is uncredited and the amount is stranded in clearing suspense"
}

// admissionWroteItsThreeRows holds one admission's three rows against each
// other: the bank's own record of itself, the settlement agent's account, and
// the clearing house's routing entry.
func (s *snapshot) admissionWroteItsThreeRows(rep *Report) {
	for _, bic := range s.order {
		view := s.banks[bic]
		member, hasAccount := s.members[bic]
		entry, routed := s.roster[bic]

		// What this bank's own row says it has been admitted in: the assets it
		// holds a settlement reference for. None of them, and the scheme has not
		// answered it — which is a whole state and not a broken one.
		var recorded []ledger.AssetCode
		for _, asset := range assetsOf(view.row) {
			if view.row.Assets[asset].Settlement != "" {
				recorded = append(recorded, asset)
			}
		}

		if len(recorded) == 0 {
			if hasAccount {
				rep.breakf(string(bic),
					"this bank's own row records no settlement account and the settlement agent holds accounts for it in %v",
					slices.Sorted(maps.Keys(member.Accounts)))
			}
			if routed {
				rep.breakf(string(bic),
					"this bank's own row records no settlement account and the clearing house routes payments to it")
			}
			continue
		}

		if !hasAccount {
			rep.breakf(string(bic),
				"this bank's own row settles %v through the settlement agent and the agent holds no account for it",
				recorded)
		}
		if !routed {
			rep.breakf(string(bic),
				"this bank's own row settles %v through the settlement agent and the clearing house does not route to it",
				recorded)
		}
		// And the NUMBERS, which is the third row read rather than counted. The two
		// arms above ask whether the settlement agent holds anything for this bank;
		// this asks whether it is the same account.
		if hasAccount {
			for _, asset := range recorded {
				own := view.row.Assets[asset].Settlement
				held, opened := member.Accounts[asset]
				switch {
				case !opened:
					rep.breakf(string(bic),
						"this bank settles %s through %s and the settlement agent has opened it no account in that asset",
						asset, own)
				case own != held:
					rep.breakf(string(bic),
						"this bank settles %s through %s and the settlement agent holds %s for it; a lodgement would credit one account and a cut-off would move the other",
						asset, own, held)
				}
			}
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

// partiesAreMembers holds every payment the clearing house has taken against
// the roster it took it under.
func (s *snapshot) partiesAreMembers(rep *Report) {
	for _, p := range s.payments {
		switch p.Status {
		case payment.Accepted, payment.Cleared, payment.Settled, payment.Returned:
		default:
			continue
		}
		parties := []iso20022.BIC{p.DebtorDetails.Agent, p.CreditorDetails.Agent}
		if parties[0] == parties[1] {
			parties = parties[:1]
		}
		for _, bic := range parties {
			if _, routed := s.roster[bic]; !routed {
				rep.breakf("the clearing house",
					"payment %s is %s and names %s as a party, an address this scheme's roster does not carry; a cut-off holding it can be netted and not settled",
					p.ID, p.Status, bic)
			}
		}
	}
}

// addressesResolveToTheirIssuer holds every customer address in the deployment
// against the registry that issued the bank code inside it.
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
