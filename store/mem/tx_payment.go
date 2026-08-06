package mem

import (
	"context"
	"time"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// The payment half of tx. Same type as the ledger and deposit halves, which is
// the whole point: one transaction spans all three layers, so a caller can post
// across every participant's book and the central bank's and record the
// network's own rows beside them as a single unit of work.
//
// SettleCycle used to be that caller and is not any more: a cut-off is three
// institutions' units of work now, and the settlement agent's reaches only its
// own book. The honest example is seed.builder.settle, which composes all three
// halves — legitimately, because the seed is not an institution.
//
// Almost every entity here is network-scoped — it belongs to no single bank —
// so the methods take no BookID and the rows are sequenced under
// ledger.NetworkBook. The exception is the settlement advice at the bottom: that
// is one member bank's own record of a cut-off it was told about, so it is keyed
// and sequenced by that bank's book like a ledger or deposit row.

// compile-time check that tx satisfies the payment interface too.
var _ payment.Tx = (*tx)(nil)

// ---------------------------------------------------------------------------
// The three rows admission writes
// ---------------------------------------------------------------------------
//
// One per institution: the bank's own record of itself, the settlement agent's
// record of the account it opened, the clearing house's record of where to
// route. They are three maps here and three tables in store/pg, which is what
// makes them three stores at Task 18 without touching a caller.
//
// The two below the bank are keyed by BIC. That is not a convenience: neither
// institution allocates or is ever told a ParticipantID, so a BIC is the only
// key either of them could have.

// PutBank stores a bank, dropping its live Ledger and Deposit handles.
//
// Those two fields are derived, not data: they are handles over this very
// store, and store/pg has no column to put a *ledger.Book in. Keeping them here
// would let code work in memory and fail on Postgres, so mem throws them away
// too and the Network rebinds them on the way out.
func (t *tx) PutBank(ctx context.Context, b payment.Bank) error {
	if err := t.write(); err != nil {
		return err
	}
	b.Ledger = nil
	b.Deposit = nil
	t.state.insertSeq(ledger.NetworkBook, kindBank, string(b.ID))
	t.state.banks[b.ID] = copyBank(b)
	return nil
}

func (t *tx) GetBank(ctx context.Context, id payment.ParticipantID) (payment.Bank, error) {
	b, ok := t.state.banks[id]
	if !ok {
		return payment.Bank{}, payment.ErrParticipantNotFound
	}
	return copyBank(b), nil
}

func (t *tx) ListBanks(ctx context.Context) ([]payment.Bank, error) {
	out := make([]payment.Bank, 0, len(t.state.banks))
	for _, b := range t.state.banks {
		out = append(out, copyBank(b))
	}
	sortRows(t.state, out, ledger.NetworkBook, kindBank, func(b payment.Bank) (time.Time, string) {
		return b.CreatedAt, string(b.ID)
	})
	return out, nil
}

// PutSettlementMember stores the central bank's own record of one member.
//
// The map assignment REPLACES the row, accounts and all, which is what makes an
// upsert drop an account for an asset the member no longer holds — the same
// rule PutBank follows for Bank.Assets, and the one store/pg gets by deleting
// its child rows before it rewrites them.
func (t *tx) PutSettlementMember(ctx context.Context, m payment.SettlementMember) error {
	if err := t.write(); err != nil {
		return err
	}
	t.state.insertSeq(ledger.NetworkBook, kindSettlementMember, string(m.BIC))
	t.state.settlementMembers[m.BIC] = copySettlementMember(m)
	return nil
}

func (t *tx) GetSettlementMember(ctx context.Context, bic iso20022.BIC) (payment.SettlementMember, error) {
	m, ok := t.state.settlementMembers[bic]
	if !ok {
		return payment.SettlementMember{}, payment.ErrSettlementMemberNotFound
	}
	return copySettlementMember(m), nil
}

func (t *tx) ListSettlementMembers(ctx context.Context) ([]payment.SettlementMember, error) {
	out := make([]payment.SettlementMember, 0, len(t.state.settlementMembers))
	for _, m := range t.state.settlementMembers {
		out = append(out, copySettlementMember(m))
	}
	sortRows(t.state, out, ledger.NetworkBook, kindSettlementMember, func(m payment.SettlementMember) (time.Time, string) {
		return m.OpenedAt, string(m.BIC)
	})
	return out, nil
}

// PutRosterEntry stores the clearing house's routing row for one member.
func (t *tx) PutRosterEntry(ctx context.Context, e payment.RosterEntry) error {
	if err := t.write(); err != nil {
		return err
	}
	t.state.insertSeq(ledger.NetworkBook, kindRosterEntry, string(e.BIC))
	t.state.rosterEntries[e.BIC] = copyRosterEntry(e)
	return nil
}

func (t *tx) GetRosterEntry(ctx context.Context, bic iso20022.BIC) (payment.RosterEntry, error) {
	e, ok := t.state.rosterEntries[bic]
	if !ok {
		return payment.RosterEntry{}, payment.ErrRosterEntryNotFound
	}
	return copyRosterEntry(e), nil
}

func (t *tx) ListRosterEntries(ctx context.Context) ([]payment.RosterEntry, error) {
	out := make([]payment.RosterEntry, 0, len(t.state.rosterEntries))
	for _, e := range t.state.rosterEntries {
		out = append(out, copyRosterEntry(e))
	}
	sortRows(t.state, out, ledger.NetworkBook, kindRosterEntry, func(e payment.RosterEntry) (time.Time, string) {
		return e.AdmittedAt, string(e.BIC)
	})
	return out, nil
}

// ---------------------------------------------------------------------------
// Payments
// ---------------------------------------------------------------------------

// PutPayment stores a payment and claims its end-to-end id.
//
// An empty end-to-end id is not an identity, so empty ids are never indexed —
// the same rule PutTransaction applies to an empty idempotency key. The index
// is maintained here rather than by the caller so that a rolled-back payment
// cannot leave its id claimed.
func (t *tx) PutPayment(ctx context.Context, p payment.Payment) error {
	if err := t.write(); err != nil {
		return err
	}
	// A Put is an upsert, and an upsert may change or clear the reference. The
	// old claim goes with it, for the same reason PutTransaction releases a
	// replaced idempotency key: an index that only ever grows keeps resolving a
	// reference the payment no longer carries, and store/pg — where the
	// reference is a column of the row itself — does not.
	if prev, ok := t.state.payments[p.ID]; ok && prev.EndToEndID != p.EndToEndID {
		delete(t.state.endToEnd, prev.EndToEndID)
	}
	t.state.insertSeq(ledger.NetworkBook, kindPayment, string(p.ID))
	t.state.payments[p.ID] = copyPayment(p)
	if p.EndToEndID != "" {
		t.state.endToEnd[p.EndToEndID] = p.ID
	}
	return nil
}

func (t *tx) GetPayment(ctx context.Context, id payment.PaymentID) (payment.Payment, error) {
	p, ok := t.state.payments[id]
	if !ok {
		return payment.Payment{}, payment.ErrPaymentNotFound
	}
	return copyPayment(p), nil
}

func (t *tx) GetPaymentByEndToEndID(ctx context.Context, endToEndID string) (payment.Payment, error) {
	if endToEndID == "" {
		return payment.Payment{}, payment.ErrPaymentNotFound
	}
	id, ok := t.state.endToEnd[endToEndID]
	if !ok {
		return payment.Payment{}, payment.ErrPaymentNotFound
	}
	return t.GetPayment(ctx, id)
}

func (t *tx) ListPayments(ctx context.Context) ([]payment.Payment, error) {
	out := make([]payment.Payment, 0, len(t.state.payments))
	for _, p := range t.state.payments {
		out = append(out, copyPayment(p))
	}
	sortRows(t.state, out, ledger.NetworkBook, kindPayment, func(p payment.Payment) (time.Time, string) {
		return p.CreatedAt, string(p.ID)
	})
	return out, nil
}

// ---------------------------------------------------------------------------
// Mandates
// ---------------------------------------------------------------------------

func (t *tx) PutMandate(ctx context.Context, m payment.Mandate) error {
	if err := t.write(); err != nil {
		return err
	}
	t.state.insertSeq(ledger.NetworkBook, kindMandate, string(m.ID))
	t.state.mandates[m.ID] = m
	return nil
}

func (t *tx) GetMandate(ctx context.Context, id payment.MandateID) (payment.Mandate, error) {
	m, ok := t.state.mandates[id]
	if !ok {
		return payment.Mandate{}, payment.ErrMandateNotFound
	}
	return m, nil
}

func (t *tx) ListMandates(ctx context.Context) ([]payment.Mandate, error) {
	out := make([]payment.Mandate, 0, len(t.state.mandates))
	for _, m := range t.state.mandates {
		out = append(out, m)
	}
	sortRows(t.state, out, ledger.NetworkBook, kindMandate, func(m payment.Mandate) (time.Time, string) {
		return m.CreatedAt, string(m.ID)
	})
	return out, nil
}

// ---------------------------------------------------------------------------
// Clearing cycles
// ---------------------------------------------------------------------------

func (t *tx) PutCycle(ctx context.Context, c payment.ClearingCycle) error {
	if err := t.write(); err != nil {
		return err
	}
	t.state.insertSeq(ledger.NetworkBook, kindCycle, string(c.ID))
	t.state.cycles[c.ID] = copyCycle(c)
	return nil
}

func (t *tx) GetCycle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error) {
	c, ok := t.state.cycles[id]
	if !ok {
		return payment.ClearingCycle{}, payment.ErrCycleNotFound
	}
	return copyCycle(c), nil
}

// GetOpenCycle returns the open cycle for a scheme. The domain keeps at most
// one open per scheme; the listing is scanned in order so that if that
// invariant were ever broken the earliest would win rather than a random one.
func (t *tx) GetOpenCycle(ctx context.Context, scheme payment.SchemeID) (payment.ClearingCycle, error) {
	cycles, err := t.ListCycles(ctx)
	if err != nil {
		return payment.ClearingCycle{}, err
	}
	for _, c := range cycles {
		if c.Scheme == scheme && c.Status == payment.CycleOpen {
			return c, nil
		}
	}
	return payment.ClearingCycle{}, payment.ErrCycleNotFound
}

func (t *tx) ListCycles(ctx context.Context) ([]payment.ClearingCycle, error) {
	out := make([]payment.ClearingCycle, 0, len(t.state.cycles))
	for _, c := range t.state.cycles {
		out = append(out, copyCycle(c))
	}
	sortRows(t.state, out, ledger.NetworkBook, kindCycle, func(c payment.ClearingCycle) (time.Time, string) {
		return c.OpenedAt, string(c.ID)
	})
	return out, nil
}

// ---------------------------------------------------------------------------
// Settlements
// ---------------------------------------------------------------------------

func (t *tx) PutSettlement(ctx context.Context, s payment.Settlement) error {
	if err := t.write(); err != nil {
		return err
	}
	t.state.insertSeq(ledger.NetworkBook, kindSettlement, string(s.ID))
	t.state.settlements[s.ID] = copySettlement(s)
	return nil
}

func (t *tx) GetSettlement(ctx context.Context, id payment.SettlementID) (payment.Settlement, error) {
	s, ok := t.state.settlements[id]
	if !ok {
		return payment.Settlement{}, payment.ErrSettlementNotFound
	}
	return copySettlement(s), nil
}

func (t *tx) ListSettlements(ctx context.Context) ([]payment.Settlement, error) {
	out := make([]payment.Settlement, 0, len(t.state.settlements))
	for _, s := range t.state.settlements {
		out = append(out, copySettlement(s))
	}
	sortRows(t.state, out, ledger.NetworkBook, kindSettlement, func(s payment.Settlement) (time.Time, string) {
		return s.SettledAt, string(s.ID)
	})
	return out, nil
}

// ---------------------------------------------------------------------------
// Settlement advices
// ---------------------------------------------------------------------------
//
// The one payment-layer table that IS book-scoped. An advice is a member bank's
// own record of a reserve movement it was told about, so the book is part of
// its identity and its sequence is taken under that book rather than under
// NetworkBook.

// PutSettlementAdvice stores one bank's advice for one reference in one asset.
//
// SettlementAdvice is all scalars — an ID, a reference string, an asset code,
// two amounts, a status, a transaction ID and two instants — so struct
// assignment IS the deep copy the store contract asks for: there is no map or
// slice for a caller to keep a reference into, unlike the banks,
// payments, cycles and settlements copied at the bottom of this file.
func (t *tx) PutSettlementAdvice(ctx context.Context, book ledger.BookID, a payment.SettlementAdvice) error {
	if err := t.write(); err != nil {
		return err
	}
	// The book ARGUMENT is the scope, and a.Book is the row's record of it. They
	// are forced to agree here because store/pg cannot do otherwise — its INSERT
	// writes book_id from the argument and its SELECT reads a.Book back out of
	// that column — so a caller passing an advice whose Book disagrees must read
	// the same answer from both stores.
	a.Book = book
	k := adviceKey{book: book, reference: a.Reference, asset: a.Asset}
	t.state.insertSeq(book, kindAdvice, adviceSeqID(k))
	t.state.settlementAdvices[k] = a
	return nil
}

func (t *tx) GetSettlementAdvice(ctx context.Context, book ledger.BookID, reference string, asset ledger.AssetCode) (payment.SettlementAdvice, error) {
	a, ok := t.state.settlementAdvices[adviceKey{book: book, reference: reference, asset: asset}]
	if !ok {
		return payment.SettlementAdvice{}, payment.ErrSettlementAdviceNotFound
	}
	return a, nil
}

func (t *tx) ListSettlementAdvices(ctx context.Context, book ledger.BookID) ([]payment.SettlementAdvice, error) {
	out := make([]payment.SettlementAdvice, 0)
	for k, a := range t.state.settlementAdvices {
		if k.book == book {
			out = append(out, a)
		}
	}
	sortRows(t.state, out, book, kindAdvice, func(a payment.SettlementAdvice) (time.Time, string) {
		return a.AdvisedAt, adviceSeqID(adviceKey{book: book, reference: a.Reference, asset: a.Asset})
	})
	return out, nil
}

// adviceSeqID renders an advice's composite key as the string rowSeq is keyed
// by. The book is already the sequence's own scope, so only the two remaining
// parts go in.
func adviceSeqID(k adviceKey) string {
	return k.reference + ":" + string(k.asset)
}

// ---------------------------------------------------------------------------
// Copying
// ---------------------------------------------------------------------------
//
// Banks, settlement members, roster entries, payments, cycles and settlements
// carry maps and slices, so they are copied in both directions: neither the
// store nor its caller may end up holding a reference into the other's data. A
// stored row a caller can still mutate in place is not stored.

// copyBank copies the per-asset account map, which is the one reference a Bank
// carries.
//
// An empty map becomes nil, because store/pg cannot tell the two apart: the
// accounts live in a child table, and no rows is no rows. A store that
// answered "empty map" where the other answers nil would be a difference the
// conformance suite exists to prevent.
func copyBank(b payment.Bank) payment.Bank {
	cp := b
	cp.Assets = nil
	if len(b.Assets) > 0 {
		cp.Assets = make(map[ledger.AssetCode]payment.BankAccounts, len(b.Assets))
		for k, v := range b.Assets {
			cp.Assets[k] = v
		}
	}
	return cp
}

// copySettlementMember copies the per-asset account map, on the same empty-is-
// nil rule as copyBank and for the same reason: in store/pg those accounts are
// a child table, so a member with no accounts comes back with no map.
func copySettlementMember(m payment.SettlementMember) payment.SettlementMember {
	cp := m
	cp.Accounts = nil
	if len(m.Accounts) > 0 {
		cp.Accounts = make(map[ledger.AssetCode]ledger.AccountID, len(m.Accounts))
		for k, v := range m.Accounts {
			cp.Accounts[k] = v
		}
	}
	return cp
}

// copyRosterEntry copies the asset slice. Empty becomes nil here too: the
// assets are a child table in store/pg, and a roster entry listing no asset
// reads back with no slice from both stores.
func copyRosterEntry(e payment.RosterEntry) payment.RosterEntry {
	cp := e
	cp.Assets = nil
	if len(e.Assets) > 0 {
		cp.Assets = make([]ledger.AssetCode, len(e.Assets))
		copy(cp.Assets, e.Assets)
	}
	return cp
}

func copyPayment(p payment.Payment) payment.Payment {
	cp := p
	if p.Metadata != nil {
		cp.Metadata = make(map[string]string, len(p.Metadata))
		for k, v := range p.Metadata {
			cp.Metadata[k] = v
		}
	}
	return cp
}

func copyCycle(c payment.ClearingCycle) payment.ClearingCycle {
	cp := c
	if c.PaymentIDs != nil {
		cp.PaymentIDs = make([]payment.PaymentID, len(c.PaymentIDs))
		copy(cp.PaymentIDs, c.PaymentIDs)
	}
	cp.NetPositions = copyPositions(c.NetPositions)
	return cp
}

func copySettlement(s payment.Settlement) payment.Settlement {
	cp := s
	cp.NetPositions = copyPositions(s.NetPositions)
	return cp
}

func copyPositions(in map[payment.ParticipantID]ledger.Amount) map[payment.ParticipantID]ledger.Amount {
	if in == nil {
		return nil
	}
	out := make(map[payment.ParticipantID]ledger.Amount, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
