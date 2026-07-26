package mem

import (
	"context"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// The payment half of tx. Same type as the ledger and deposit halves, which is
// the whole point: one transaction spans all three layers, so SettleCycle can
// post across every participant's book and the central bank's and record the
// settlement itself as a single unit of work.
//
// Every entity here is network-scoped — it belongs to no single bank — so the
// methods take no BookID and the rows are sequenced under ledger.NetworkBook.

// compile-time check that tx satisfies the payment interface too.
var _ payment.Tx = (*tx)(nil)

// ---------------------------------------------------------------------------
// Participants
// ---------------------------------------------------------------------------

// PutParticipant stores a participant, dropping its live Ledger and Deposit
// handles.
//
// Those two fields are derived, not data: they are handles over this very
// store, and store/pg has no column to put a *ledger.Book in. Keeping them here
// would let code work in memory and fail on Postgres, so mem throws them away
// too and the Network rebinds them on the way out.
func (t *tx) PutParticipant(ctx context.Context, p payment.Participant) error {
	if err := t.write(); err != nil {
		return err
	}
	p.Ledger = nil
	p.Deposit = nil
	t.state.insertSeq(ledger.NetworkBook, kindParticipant, string(p.ID))
	t.state.participants[p.ID] = p
	return nil
}

func (t *tx) GetParticipant(ctx context.Context, id payment.ParticipantID) (payment.Participant, error) {
	p, ok := t.state.participants[id]
	if !ok {
		return payment.Participant{}, payment.ErrParticipantNotFound
	}
	return p, nil
}

func (t *tx) ListParticipants(ctx context.Context) ([]payment.Participant, error) {
	out := make([]payment.Participant, 0, len(t.state.participants))
	for _, p := range t.state.participants {
		out = append(out, p)
	}
	sortRows(t.state, out, ledger.NetworkBook, kindParticipant, func(p payment.Participant) (time.Time, string) {
		return p.CreatedAt, string(p.ID)
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
// Copying
// ---------------------------------------------------------------------------
//
// Payments, cycles and settlements carry maps and slices, so they are copied in
// both directions: neither the store nor its caller may end up holding a
// reference into the other's data.

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
