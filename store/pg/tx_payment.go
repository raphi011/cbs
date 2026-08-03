package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// The payment half of tx — the same type again, which is what lets one
// BEGIN … COMMIT post across every participant's book and the central bank's and
// record the network's own rows beside them.
//
// SettleCycle used to be the example of that and is not any more: a cut-off is
// three institutions' units of work now, and the settlement agent's touches only
// its own book. The honest example is seed.builder.settle, which is the one
// caller that legitimately plays all three at once — because the seed is not an
// institution — and which needs exactly this to do it.
//
// Almost every entity here is network-scoped: a payment belongs to no single
// bank, so unlike the ledger and deposit tables these are keyed by id alone and
// take no BookID. The exception is the settlement advice at the bottom — a
// member bank's own record of a cut-off it was told about — which carries a
// book_id like a ledger or deposit row, because it belongs to that bank.

// compile-time check that tx satisfies the payment interface too.
var _ payment.Tx = (*tx)(nil)

// ---------------------------------------------------------------------------
// Participants
// ---------------------------------------------------------------------------

// PutParticipant stores a participant and the set of internal accounts it
// holds per asset. Its Ledger and Deposit fields are simply not written: they
// are live handles over this very store, not data, and there is no column that
// could hold a *ledger.Book. store/mem nils them for the same reason, and the
// Network rebinds them on the way out.
//
// The child rows are deleted and rewritten rather than upserted. An upsert
// alone would leave behind a row for an asset the participant no longer holds,
// and a stale row here is not a cosmetic problem: settlement would resolve an
// account the bank has given up.
func (t *tx) PutParticipant(ctx context.Context, p payment.Participant) error {
	if err := t.write(); err != nil {
		return err
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO participants
			(id, name, bic, book_id, customer_subledger, product_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE SET
			name               = EXCLUDED.name,
			bic                = EXCLUDED.bic,
			book_id            = EXCLUDED.book_id,
			customer_subledger = EXCLUDED.customer_subledger,
			product_id         = EXCLUDED.product_id,
			created_at         = EXCLUDED.created_at`,
		string(p.ID), p.Name, string(p.BIC), string(p.BookID), string(p.CustomerSubledger),
		string(p.ProductID), nullTime(p.CreatedAt))
	if err != nil {
		return fmt.Errorf("pg: put participant %s: %w", p.ID, err)
	}

	if _, err := t.tx.Exec(ctx, "DELETE FROM participant_assets WHERE participant_id = $1", string(p.ID)); err != nil {
		return fmt.Errorf("pg: put participant %s: %w", p.ID, err)
	}
	// Sorted, because `seq` is a BIGSERIAL and Go's map iteration order is
	// deliberately random: inserting straight from the map gave a participant's
	// asset rows an arbitrary sequence, different on every write of the same
	// data. Nothing reads that order today — the rows fold back into a map — but
	// `seq` means real, load-bearing ordering everywhere else in this schema,
	// and store/mem has no equivalent randomness to diverge from.
	for _, asset := range slices.Sorted(maps.Keys(p.Assets)) {
		accts := p.Assets[asset]
		if _, err := t.tx.Exec(ctx, `
			INSERT INTO participant_assets (participant_id, asset, suspense, reserve, unclaimed, returns_receivable, settlement)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			string(p.ID), string(asset),
			string(accts.Suspense), string(accts.Reserve), string(accts.Unclaimed), string(accts.ReturnsReceivable), string(accts.Settlement)); err != nil {
			return fmt.Errorf("pg: put participant %s asset %s: %w", p.ID, asset, err)
		}
	}
	return nil
}

const participantColumns = `id, name, bic, book_id, customer_subledger, product_id, created_at`

func scanParticipant(row pgx.Row) (payment.Participant, error) {
	var (
		p         payment.Participant
		createdAt *time.Time
	)
	err := row.Scan(&p.ID, &p.Name, &p.BIC, &p.BookID, &p.CustomerSubledger, &p.ProductID, &createdAt)
	if err != nil {
		return payment.Participant{}, err
	}
	p.CreatedAt = readTime(createdAt)
	return p, nil
}

// participantAssets reads the internal accounts of one participant, or of
// every participant when id is empty.
//
// Listing takes the second form deliberately: one query keyed by participant
// id, folded into the records afterwards, rather than a query per row. A join
// would work too, but it would flatten the participant row once per asset and
// have to be de-duplicated on the way back — the same shape the cycle and
// settlement readers use, and not worth it for a child table this small.
func (t *tx) participantAssets(ctx context.Context, id payment.ParticipantID) (map[payment.ParticipantID]map[ledger.AssetCode]payment.ParticipantAccounts, error) {
	query := "SELECT participant_id, asset, suspense, reserve, unclaimed, returns_receivable, settlement FROM participant_assets"
	args := []any{}
	if id != "" {
		query += " WHERE participant_id = $1"
		args = append(args, string(id))
	}
	query += " ORDER BY participant_id, seq"

	rows, err := t.tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("pg: participant assets: %w", err)
	}
	defer rows.Close()

	out := make(map[payment.ParticipantID]map[ledger.AssetCode]payment.ParticipantAccounts)
	for rows.Next() {
		var (
			pid   payment.ParticipantID
			asset ledger.AssetCode
			accts payment.ParticipantAccounts
		)
		if err := rows.Scan(&pid, &asset, &accts.Suspense, &accts.Reserve, &accts.Unclaimed, &accts.ReturnsReceivable, &accts.Settlement); err != nil {
			return nil, fmt.Errorf("pg: participant assets: %w", err)
		}
		if out[pid] == nil {
			out[pid] = make(map[ledger.AssetCode]payment.ParticipantAccounts)
		}
		out[pid][asset] = accts
	}
	return out, rows.Err()
}

func (t *tx) GetParticipant(ctx context.Context, id payment.ParticipantID) (payment.Participant, error) {
	p, err := scanParticipant(t.tx.QueryRow(ctx,
		"SELECT "+participantColumns+" FROM participants WHERE id = $1", string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.Participant{}, payment.ErrParticipantNotFound
	}
	if err != nil {
		return payment.Participant{}, fmt.Errorf("pg: get participant %s: %w", id, err)
	}
	assets, err := t.participantAssets(ctx, id)
	if err != nil {
		return payment.Participant{}, err
	}
	p.Assets = assets[id]
	return p, nil
}

func (t *tx) ListParticipants(ctx context.Context) ([]payment.Participant, error) {
	rows, err := t.tx.Query(ctx,
		"SELECT "+participantColumns+" FROM participants ORDER BY created_at ASC NULLS FIRST, seq")
	if err != nil {
		return nil, fmt.Errorf("pg: list participants: %w", err)
	}
	defer rows.Close()

	out := make([]payment.Participant, 0)
	for rows.Next() {
		p, err := scanParticipant(rows)
		if err != nil {
			return nil, fmt.Errorf("pg: list participants: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// One extra query for every participant's assets, rather than one per
	// participant.
	assets, err := t.participantAssets(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Assets = assets[out[i].ID]
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Payments
// ---------------------------------------------------------------------------

const paymentColumns = `id, scheme,
	debtor_participant, debtor_account, debtor_identifier_scheme, debtor_identifier_value,
	creditor_participant, creditor_account, creditor_identifier_scheme, creditor_identifier_value,
	debtor_agent, debtor_name, creditor_agent, creditor_name,
	amount, mandate_id, end_to_end_id, status, reject_reason, reject_code, cycle_id,
	booking_date, value_date, description, metadata, created_at,
	debtor_leg_tx, creditor_leg_tx, creditor_leg_account`

func (t *tx) PutPayment(ctx context.Context, p payment.Payment) error {
	if err := t.write(); err != nil {
		return err
	}
	metadata, err := marshalStringMap(p.Metadata)
	if err != nil {
		return fmt.Errorf("pg: put payment %s: %w", p.ID, err)
	}
	_, err = t.tx.Exec(ctx, `
		INSERT INTO payments (`+paymentColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29)
		ON CONFLICT (id) DO UPDATE SET
			scheme                     = EXCLUDED.scheme,
			debtor_participant         = EXCLUDED.debtor_participant,
			debtor_account             = EXCLUDED.debtor_account,
			debtor_identifier_scheme   = EXCLUDED.debtor_identifier_scheme,
			debtor_identifier_value    = EXCLUDED.debtor_identifier_value,
			creditor_participant       = EXCLUDED.creditor_participant,
			creditor_account           = EXCLUDED.creditor_account,
			creditor_identifier_scheme = EXCLUDED.creditor_identifier_scheme,
			creditor_identifier_value  = EXCLUDED.creditor_identifier_value,
			debtor_agent               = EXCLUDED.debtor_agent,
			debtor_name                = EXCLUDED.debtor_name,
			creditor_agent             = EXCLUDED.creditor_agent,
			creditor_name              = EXCLUDED.creditor_name,
			amount                     = EXCLUDED.amount,
			mandate_id                 = EXCLUDED.mandate_id,
			end_to_end_id              = EXCLUDED.end_to_end_id,
			status                     = EXCLUDED.status,
			reject_reason              = EXCLUDED.reject_reason,
			reject_code                = EXCLUDED.reject_code,
			cycle_id                   = EXCLUDED.cycle_id,
			booking_date               = EXCLUDED.booking_date,
			value_date                 = EXCLUDED.value_date,
			description                = EXCLUDED.description,
			metadata                   = EXCLUDED.metadata,
			created_at                 = EXCLUDED.created_at,
			debtor_leg_tx              = EXCLUDED.debtor_leg_tx,
			creditor_leg_tx            = EXCLUDED.creditor_leg_tx,
			creditor_leg_account       = EXCLUDED.creditor_leg_account`,
		string(p.ID), string(p.Scheme),
		string(p.Debtor.Participant), string(p.Debtor.Account), string(p.Debtor.Identifier.Scheme), p.Debtor.Identifier.Value,
		string(p.Creditor.Participant), string(p.Creditor.Account), string(p.Creditor.Identifier.Scheme), p.Creditor.Identifier.Value,
		string(p.DebtorDetails.Agent), p.DebtorDetails.Name, string(p.CreditorDetails.Agent), p.CreditorDetails.Name,
		p.Amount, string(p.MandateID), p.EndToEndID, int16(p.Status), p.RejectReason, string(p.RejectCode), string(p.CycleID),
		nullTime(p.BookingDate), nullTime(p.ValueDate), p.Description, metadata, nullTime(p.CreatedAt),
		string(p.DebtorLegTx), string(p.CreditorLegTx), string(p.CreditorLegAccount))
	if err != nil {
		return fmt.Errorf("pg: put payment %s: %w", p.ID, err)
	}
	return nil
}

func scanPayment(row pgx.Row) (payment.Payment, error) {
	var (
		p                         payment.Payment
		status                    int16
		booking, value, createdAt *time.Time
		metadata                  []byte
	)
	err := row.Scan(&p.ID, &p.Scheme,
		&p.Debtor.Participant, &p.Debtor.Account, &p.Debtor.Identifier.Scheme, &p.Debtor.Identifier.Value,
		&p.Creditor.Participant, &p.Creditor.Account, &p.Creditor.Identifier.Scheme, &p.Creditor.Identifier.Value,
		&p.DebtorDetails.Agent, &p.DebtorDetails.Name, &p.CreditorDetails.Agent, &p.CreditorDetails.Name,
		&p.Amount, &p.MandateID, &p.EndToEndID, &status, &p.RejectReason, &p.RejectCode, &p.CycleID,
		&booking, &value, &p.Description, &metadata, &createdAt,
		&p.DebtorLegTx, &p.CreditorLegTx, &p.CreditorLegAccount)
	if err != nil {
		return payment.Payment{}, err
	}
	p.Status = payment.PaymentStatus(status)
	p.BookingDate = readTime(booking)
	p.ValueDate = readTime(value)
	p.CreatedAt = readTime(createdAt)
	if p.Metadata, err = unmarshalStringMap(metadata); err != nil {
		return payment.Payment{}, err
	}
	return p, nil
}

func (t *tx) GetPayment(ctx context.Context, id payment.PaymentID) (payment.Payment, error) {
	p, err := scanPayment(t.tx.QueryRow(ctx,
		"SELECT "+paymentColumns+" FROM payments WHERE id = $1", string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.Payment{}, payment.ErrPaymentNotFound
	}
	if err != nil {
		return payment.Payment{}, fmt.Errorf("pg: get payment %s: %w", id, err)
	}
	return p, nil
}

// GetPaymentByEndToEndID matches exactly — no prefix, no case folding. An empty
// end-to-end id is never an identity, the same rule the ledger applies to an
// empty idempotency key, so it is not even looked up: two payments with no
// client reference must not deduplicate against each other.
func (t *tx) GetPaymentByEndToEndID(ctx context.Context, endToEndID string) (payment.Payment, error) {
	if endToEndID == "" {
		return payment.Payment{}, payment.ErrPaymentNotFound
	}
	p, err := scanPayment(t.tx.QueryRow(ctx,
		"SELECT "+paymentColumns+" FROM payments WHERE end_to_end_id = $1 ORDER BY seq LIMIT 1", endToEndID))
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.Payment{}, payment.ErrPaymentNotFound
	}
	if err != nil {
		return payment.Payment{}, fmt.Errorf("pg: get payment by end-to-end id: %w", err)
	}
	return p, nil
}

func (t *tx) ListPayments(ctx context.Context) ([]payment.Payment, error) {
	rows, err := t.tx.Query(ctx,
		"SELECT "+paymentColumns+" FROM payments ORDER BY created_at ASC NULLS FIRST, seq")
	if err != nil {
		return nil, fmt.Errorf("pg: list payments: %w", err)
	}
	defer rows.Close()

	out := make([]payment.Payment, 0)
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, fmt.Errorf("pg: list payments: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Mandates
// ---------------------------------------------------------------------------

const mandateColumns = `id, debtor_participant, debtor_account, debtor_identifier_scheme, debtor_identifier_value,
	creditor_participant, creditor_account, creditor_identifier_scheme, creditor_identifier_value,
	max_amount, status, created_at`

func (t *tx) PutMandate(ctx context.Context, m payment.Mandate) error {
	if err := t.write(); err != nil {
		return err
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO mandates (`+mandateColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (id) DO UPDATE SET
			debtor_participant         = EXCLUDED.debtor_participant,
			debtor_account             = EXCLUDED.debtor_account,
			debtor_identifier_scheme   = EXCLUDED.debtor_identifier_scheme,
			debtor_identifier_value    = EXCLUDED.debtor_identifier_value,
			creditor_participant       = EXCLUDED.creditor_participant,
			creditor_account           = EXCLUDED.creditor_account,
			creditor_identifier_scheme = EXCLUDED.creditor_identifier_scheme,
			creditor_identifier_value  = EXCLUDED.creditor_identifier_value,
			max_amount                 = EXCLUDED.max_amount,
			status                     = EXCLUDED.status,
			created_at                 = EXCLUDED.created_at`,
		string(m.ID),
		string(m.Debtor.Participant), string(m.Debtor.Account), string(m.Debtor.Identifier.Scheme), m.Debtor.Identifier.Value,
		string(m.Creditor.Participant), string(m.Creditor.Account), string(m.Creditor.Identifier.Scheme), m.Creditor.Identifier.Value,
		m.MaxAmount, int16(m.Status), nullTime(m.CreatedAt))
	if err != nil {
		return fmt.Errorf("pg: put mandate %s: %w", m.ID, err)
	}
	return nil
}

func scanMandate(row pgx.Row) (payment.Mandate, error) {
	var (
		m         payment.Mandate
		status    int16
		createdAt *time.Time
	)
	err := row.Scan(&m.ID,
		&m.Debtor.Participant, &m.Debtor.Account, &m.Debtor.Identifier.Scheme, &m.Debtor.Identifier.Value,
		&m.Creditor.Participant, &m.Creditor.Account, &m.Creditor.Identifier.Scheme, &m.Creditor.Identifier.Value,
		&m.MaxAmount, &status, &createdAt)
	if err != nil {
		return payment.Mandate{}, err
	}
	m.Status = payment.MandateStatus(status)
	m.CreatedAt = readTime(createdAt)
	return m, nil
}

func (t *tx) GetMandate(ctx context.Context, id payment.MandateID) (payment.Mandate, error) {
	m, err := scanMandate(t.tx.QueryRow(ctx,
		"SELECT "+mandateColumns+" FROM mandates WHERE id = $1", string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.Mandate{}, payment.ErrMandateNotFound
	}
	if err != nil {
		return payment.Mandate{}, fmt.Errorf("pg: get mandate %s: %w", id, err)
	}
	return m, nil
}

func (t *tx) ListMandates(ctx context.Context) ([]payment.Mandate, error) {
	rows, err := t.tx.Query(ctx,
		"SELECT "+mandateColumns+" FROM mandates ORDER BY created_at ASC NULLS FIRST, seq")
	if err != nil {
		return nil, fmt.Errorf("pg: list mandates: %w", err)
	}
	defer rows.Close()

	out := make([]payment.Mandate, 0)
	for rows.Next() {
		m, err := scanMandate(rows)
		if err != nil {
			return nil, fmt.Errorf("pg: list mandates: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Clearing cycles
// ---------------------------------------------------------------------------

const cycleColumns = `c.id, c.scheme, c.status, c.net_positions, c.opened_at, c.closed_at, c.settlement_id`

func (t *tx) PutCycle(ctx context.Context, c payment.ClearingCycle) error {
	if err := t.write(); err != nil {
		return err
	}
	positions, err := marshalPositions(c.NetPositions)
	if err != nil {
		return fmt.Errorf("pg: put cycle %s: %w", c.ID, err)
	}
	_, err = t.tx.Exec(ctx, `
		INSERT INTO cycles (id, scheme, status, net_positions, opened_at, closed_at, settlement_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE SET
			scheme        = EXCLUDED.scheme,
			status        = EXCLUDED.status,
			net_positions = EXCLUDED.net_positions,
			opened_at     = EXCLUDED.opened_at,
			closed_at     = EXCLUDED.closed_at,
			settlement_id = EXCLUDED.settlement_id`,
		string(c.ID), string(c.Scheme), int16(c.Status), positions,
		nullTime(c.OpenedAt), nullTime(c.ClosedAt), string(c.SettlementID))
	if err != nil {
		return fmt.Errorf("pg: put cycle %s: %w", c.ID, err)
	}

	// PaymentIDs is an ordered slice, so it gets its own rows with an explicit
	// position; replacing the whole list keeps the upsert honest.
	if _, err := t.tx.Exec(ctx, "DELETE FROM cycle_payments WHERE cycle_id = $1", string(c.ID)); err != nil {
		return fmt.Errorf("pg: put cycle %s: %w", c.ID, err)
	}
	for i, id := range c.PaymentIDs {
		if _, err := t.tx.Exec(ctx,
			"INSERT INTO cycle_payments (cycle_id, position, payment_id) VALUES ($1, $2, $3)",
			string(c.ID), i, string(id)); err != nil {
			return fmt.Errorf("pg: put cycle %s payment %d: %w", c.ID, i, err)
		}
	}
	return nil
}

func (t *tx) GetCycle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error) {
	out, err := t.queryCycles(ctx, "WHERE c.id = $1", "", string(id))
	if err != nil {
		return payment.ClearingCycle{}, err
	}
	if len(out) == 0 {
		return payment.ClearingCycle{}, payment.ErrCycleNotFound
	}
	return out[0], nil
}

// GetOpenCycle returns the open cycle for a scheme. The domain keeps at most one
// open per scheme; the earliest wins if that invariant is ever broken, which is
// what store/mem's ordered scan does too.
func (t *tx) GetOpenCycle(ctx context.Context, scheme payment.SchemeID) (payment.ClearingCycle, error) {
	out, err := t.queryCycles(ctx,
		"WHERE c.scheme = $1 AND c.status = $2 AND c.id = (SELECT id FROM cycles WHERE scheme = $1 AND status = $2 ORDER BY opened_at ASC NULLS FIRST, seq LIMIT 1)",
		"", string(scheme), int16(payment.CycleOpen))
	if err != nil {
		return payment.ClearingCycle{}, err
	}
	if len(out) == 0 {
		return payment.ClearingCycle{}, payment.ErrCycleNotFound
	}
	return out[0], nil
}

func (t *tx) ListCycles(ctx context.Context) ([]payment.ClearingCycle, error) {
	return t.queryCycles(ctx, "", "c.opened_at ASC NULLS FIRST, c.seq,")
}

// queryCycles reads cycles joined to their ordered payment ids and folds the
// flattened rows back into cycles.
func (t *tx) queryCycles(ctx context.Context, where, order string, args ...any) ([]payment.ClearingCycle, error) {
	query := "SELECT " + cycleColumns + ", cp.payment_id FROM cycles c " +
		"LEFT JOIN cycle_payments cp ON cp.cycle_id = c.id " + where +
		" ORDER BY " + order + " cp.position"
	rows, err := t.tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("pg: query cycles: %w", err)
	}
	defer rows.Close()

	out := make([]payment.ClearingCycle, 0)
	index := make(map[payment.CycleID]int)
	for rows.Next() {
		var (
			c              payment.ClearingCycle
			status         int16
			positions      []byte
			opened, closed *time.Time
			paymentID      *string
		)
		if err := rows.Scan(&c.ID, &c.Scheme, &status, &positions, &opened, &closed, &c.SettlementID, &paymentID); err != nil {
			return nil, fmt.Errorf("pg: query cycles: %w", err)
		}
		at, seen := index[c.ID]
		if !seen {
			c.Status = payment.CycleStatus(status)
			c.OpenedAt = readTime(opened)
			c.ClosedAt = readTime(closed)
			if c.NetPositions, err = unmarshalPositions(positions); err != nil {
				return nil, fmt.Errorf("pg: cycle %s net positions: %w", c.ID, err)
			}
			at = len(out)
			index[c.ID] = at
			out = append(out, c)
		}
		if paymentID != nil {
			out[at].PaymentIDs = append(out[at].PaymentIDs, payment.PaymentID(*paymentID))
		}
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Settlements
// ---------------------------------------------------------------------------

func (t *tx) PutSettlement(ctx context.Context, s payment.Settlement) error {
	if err := t.write(); err != nil {
		return err
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO settlements (id, cycle_id, settlement_tx, value_date, settled_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET
			cycle_id      = EXCLUDED.cycle_id,
			settlement_tx = EXCLUDED.settlement_tx,
			value_date    = EXCLUDED.value_date,
			settled_at    = EXCLUDED.settled_at`,
		string(s.ID), string(s.CycleID), string(s.SettlementTx),
		nullTime(s.ValueDate), nullTime(s.SettledAt))
	if err != nil {
		return fmt.Errorf("pg: put settlement %s: %w", s.ID, err)
	}

	if _, err := t.tx.Exec(ctx, "DELETE FROM settlement_positions WHERE settlement_id = $1", string(s.ID)); err != nil {
		return fmt.Errorf("pg: put settlement %s: %w", s.ID, err)
	}
	for participant, amount := range s.NetPositions {
		if _, err := t.tx.Exec(ctx,
			"INSERT INTO settlement_positions (settlement_id, participant_id, amount) VALUES ($1, $2, $3)",
			string(s.ID), string(participant), amount); err != nil {
			return fmt.Errorf("pg: put settlement %s position %s: %w", s.ID, participant, err)
		}
	}
	return nil
}

func (t *tx) GetSettlement(ctx context.Context, id payment.SettlementID) (payment.Settlement, error) {
	out, err := t.querySettlements(ctx, "WHERE s.id = $1", "", string(id))
	if err != nil {
		return payment.Settlement{}, err
	}
	if len(out) == 0 {
		return payment.Settlement{}, payment.ErrSettlementNotFound
	}
	return out[0], nil
}

func (t *tx) ListSettlements(ctx context.Context) ([]payment.Settlement, error) {
	return t.querySettlements(ctx, "", "s.settled_at ASC NULLS FIRST, s.seq")
}

// querySettlements reads settlements joined to their net positions.
//
// The positions map is always non-nil for a settlement that exists: a row set
// cannot tell an empty map from an absent one, and every settlement the domain
// writes carries positions.
func (t *tx) querySettlements(ctx context.Context, where, order string, args ...any) ([]payment.Settlement, error) {
	query := "SELECT s.id, s.cycle_id, s.settlement_tx, s.value_date, s.settled_at, sp.participant_id, sp.amount " +
		"FROM settlements s LEFT JOIN settlement_positions sp ON sp.settlement_id = s.id " + where
	if order != "" {
		query += " ORDER BY " + order
	}
	rows, err := t.tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("pg: query settlements: %w", err)
	}
	defer rows.Close()

	out := make([]payment.Settlement, 0)
	index := make(map[payment.SettlementID]int)
	for rows.Next() {
		var (
			s             payment.Settlement
			value, at     *time.Time
			participantID *string
			amount        *int64
		)
		if err := rows.Scan(&s.ID, &s.CycleID, &s.SettlementTx, &value, &at, &participantID, &amount); err != nil {
			return nil, fmt.Errorf("pg: query settlements: %w", err)
		}
		pos, seen := index[s.ID]
		if !seen {
			s.ValueDate = readTime(value)
			s.SettledAt = readTime(at)
			s.NetPositions = make(map[payment.ParticipantID]ledger.Amount)
			pos = len(out)
			index[s.ID] = pos
			out = append(out, s)
		}
		if participantID != nil {
			out[pos].NetPositions[payment.ParticipantID(*participantID)] = ledger.Amount(*amount)
		}
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Settlement advices
// ---------------------------------------------------------------------------
//
// The one payment-layer table that IS book-scoped, so these three are the only
// methods in this file that take a BookID — and the only ones that have to
// ensureBook, because settlement_advices.book_id is a real foreign key.

const settlementAdviceColumns = `book_id, cycle_id, asset, movement, closing_balance,
	status, mirror_tx, advised_at, posted_at`

func (t *tx) PutSettlementAdvice(ctx context.Context, book ledger.BookID, a payment.SettlementAdvice) error {
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO settlement_advices (`+settlementAdviceColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (book_id, cycle_id, asset) DO UPDATE SET
			movement        = EXCLUDED.movement,
			closing_balance = EXCLUDED.closing_balance,
			status          = EXCLUDED.status,
			mirror_tx       = EXCLUDED.mirror_tx,
			advised_at      = EXCLUDED.advised_at,
			posted_at       = EXCLUDED.posted_at`,
		string(book), string(a.CycleID), string(a.Asset), a.Movement, a.ClosingBalance,
		int16(a.Status), string(a.MirrorTx), nullTime(a.AdvisedAt), nullTime(a.PostedAt))
	if err != nil {
		return fmt.Errorf("pg: put settlement advice %s/%s/%s: %w", book, a.CycleID, a.Asset, err)
	}
	return nil
}

func scanSettlementAdvice(row pgx.Row) (payment.SettlementAdvice, error) {
	var (
		a               payment.SettlementAdvice
		status          int16
		advised, posted *time.Time
	)
	err := row.Scan(&a.Book, &a.CycleID, &a.Asset, &a.Movement, &a.ClosingBalance,
		&status, &a.MirrorTx, &advised, &posted)
	if err != nil {
		return payment.SettlementAdvice{}, err
	}
	a.Status = payment.AdviceStatus(status)
	a.AdvisedAt = readTime(advised)
	a.PostedAt = readTime(posted)
	return a, nil
}

func (t *tx) GetSettlementAdvice(ctx context.Context, book ledger.BookID, cycle payment.CycleID, asset ledger.AssetCode) (payment.SettlementAdvice, error) {
	a, err := scanSettlementAdvice(t.tx.QueryRow(ctx,
		"SELECT "+settlementAdviceColumns+" FROM settlement_advices WHERE book_id = $1 AND cycle_id = $2 AND asset = $3",
		string(book), string(cycle), string(asset)))
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.SettlementAdvice{}, payment.ErrSettlementAdviceNotFound
	}
	if err != nil {
		return payment.SettlementAdvice{}, fmt.Errorf("pg: get settlement advice %s/%s/%s: %w", book, cycle, asset, err)
	}
	return a, nil
}

func (t *tx) ListSettlementAdvices(ctx context.Context, book ledger.BookID) ([]payment.SettlementAdvice, error) {
	rows, err := t.tx.Query(ctx,
		"SELECT "+settlementAdviceColumns+" FROM settlement_advices WHERE book_id = $1 "+
			"ORDER BY advised_at ASC NULLS FIRST, seq", string(book))
	if err != nil {
		return nil, fmt.Errorf("pg: list settlement advices: %w", err)
	}
	defer rows.Close()

	out := make([]payment.SettlementAdvice, 0)
	for rows.Next() {
		a, err := scanSettlementAdvice(rows)
		if err != nil {
			return nil, fmt.Errorf("pg: list settlement advices: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Net positions
// ---------------------------------------------------------------------------

// marshalPositions encodes a net-position map for a JSONB column. A nil map is
// NULL and an empty one is {}: an open cycle carries an empty map and the API
// renders the two differently, so the distinction has to survive the round trip.
func marshalPositions(m map[payment.ParticipantID]ledger.Amount) (any, error) {
	if m == nil {
		return nil, nil
	}
	flat := make(map[string]int64, len(m))
	for k, v := range m {
		flat[string(k)] = int64(v)
	}
	raw, err := json.Marshal(flat)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func unmarshalPositions(raw []byte) (map[payment.ParticipantID]ledger.Amount, error) {
	if raw == nil {
		return nil, nil
	}
	flat := make(map[string]int64)
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, err
	}
	out := make(map[payment.ParticipantID]ledger.Amount, len(flat))
	for k, v := range flat {
		out[payment.ParticipantID(k)] = ledger.Amount(v)
	}
	return out, nil
}
