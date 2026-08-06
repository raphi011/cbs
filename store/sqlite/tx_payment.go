package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/raphi011/cbs/iso20022"
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
// The three rows admission writes
// ---------------------------------------------------------------------------
//
// One table per institution, plus a child table wherever a row holds something
// per asset. They are separate tables and not one wide one because each has a
// single writer and each moves into a different database at Task 18 — see the
// schema, which is where that argument is written down in full.

// PutBank stores a bank and the set of internal accounts it holds per asset.
// Its Ledger and Deposit fields are simply not written: they are live handles
// over this very store, not data, and there is no column that could hold a
// *ledger.Book. The Network rebinds them on the way out; storetest's
// BankRoundTripsAndDropsLiveHandles is what says a bank must come back with them
// dropped and with Status intact.
//
// The child rows are deleted and rewritten rather than upserted. An upsert
// alone would leave behind a row for an asset the bank no longer holds, and a
// stale row here is not a cosmetic problem: settlement would resolve an account
// the bank has given up.
func (t *tx) PutBank(ctx context.Context, b payment.Bank) error {
	if err := t.write(); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO banks
			(id, name, bic, book_id, customer_subledger, product_id, status, admission_ref, created_at, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, `+nextRowSeq("banks")+`)
		ON CONFLICT (id) DO UPDATE SET
			name               = EXCLUDED.name,
			bic                = EXCLUDED.bic,
			book_id            = EXCLUDED.book_id,
			customer_subledger = EXCLUDED.customer_subledger,
			product_id         = EXCLUDED.product_id,
			status             = EXCLUDED.status,
			admission_ref      = EXCLUDED.admission_ref,
			created_at         = EXCLUDED.created_at`,
		string(b.ID), b.Name, string(b.BIC), string(b.BookID), string(b.CustomerSubledger),
		string(b.ProductID), string(b.Status), b.AdmissionRef, nullTime{b.CreatedAt})
	if err != nil {
		return fmt.Errorf("sqlite: put bank %s: %w", b.ID, err)
	}

	if _, err := t.tx.ExecContext(ctx, "DELETE FROM bank_assets WHERE bank_id = ?", string(b.ID)); err != nil {
		return fmt.Errorf("sqlite: put bank %s: %w", b.ID, err)
	}
	// Sorted, because `seq` is an insertion order and Go's map iteration order is
	// deliberately random: inserting straight from the map gave a bank's asset
	// rows an arbitrary sequence, different on every write of the same data.
	// Nothing reads that order today — the rows fold back into a map — but `seq`
	// means real, load-bearing ordering everywhere else in this schema, and a
	// column that means one thing here and nothing there is how the next reader
	// gets it wrong.
	for _, asset := range slices.Sorted(maps.Keys(b.Assets)) {
		accts := b.Assets[asset]
		if _, err := t.tx.ExecContext(ctx, `
			INSERT INTO bank_assets (bank_id, asset, suspense, reserve, unclaimed, returns_receivable, settlement, seq)
			VALUES (?, ?, ?, ?, ?, ?, ?, `+nextRowSeq("bank_assets")+`)`,
			string(b.ID), string(asset),
			string(accts.Suspense), string(accts.Reserve), string(accts.Unclaimed), string(accts.ReturnsReceivable), string(accts.Settlement)); err != nil {
			return fmt.Errorf("sqlite: put bank %s asset %s: %w", b.ID, asset, err)
		}
	}
	return nil
}

const bankColumns = `id, name, bic, book_id, customer_subledger, product_id, status, admission_ref, created_at`

func scanBank(row interface{ Scan(...any) error }) (payment.Bank, error) {
	var (
		b         payment.Bank
		createdAt nullTime
	)
	err := row.Scan(&b.ID, &b.Name, &b.BIC, &b.BookID, &b.CustomerSubledger, &b.ProductID,
		&b.Status, &b.AdmissionRef, &createdAt)
	if err != nil {
		return payment.Bank{}, err
	}
	b.CreatedAt = createdAt.Time
	return b, nil
}

// bankAssets reads the internal accounts of one bank, or of every bank when id
// is empty.
//
// Listing takes the second form deliberately: one query keyed by bank id,
// folded into the records afterwards, rather than a query per row. A join
// would work too, but it would flatten the bank row once per asset and
// have to be de-duplicated on the way back — the same shape the cycle and
// settlement readers use, and not worth it for a child table this small.
func (t *tx) bankAssets(ctx context.Context, id payment.ParticipantID) (map[payment.ParticipantID]map[ledger.AssetCode]payment.BankAccounts, error) {
	query := "SELECT bank_id, asset, suspense, reserve, unclaimed, returns_receivable, settlement FROM bank_assets"
	args := []any{}
	if id != "" {
		query += " WHERE bank_id = ?"
		args = append(args, string(id))
	}
	query += " ORDER BY bank_id, seq"

	rows, err := t.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: bank assets: %w", err)
	}
	defer rows.Close()

	out := make(map[payment.ParticipantID]map[ledger.AssetCode]payment.BankAccounts)
	for rows.Next() {
		var (
			id    payment.ParticipantID
			asset ledger.AssetCode
			accts payment.BankAccounts
		)
		if err := rows.Scan(&id, &asset, &accts.Suspense, &accts.Reserve, &accts.Unclaimed, &accts.ReturnsReceivable, &accts.Settlement); err != nil {
			return nil, fmt.Errorf("sqlite: bank assets: %w", err)
		}
		if out[id] == nil {
			out[id] = make(map[ledger.AssetCode]payment.BankAccounts)
		}
		out[id][asset] = accts
	}
	return out, rows.Err()
}

func (t *tx) GetBank(ctx context.Context, id payment.ParticipantID) (payment.Bank, error) {
	b, err := scanBank(t.tx.QueryRowContext(ctx,
		"SELECT "+bankColumns+" FROM banks WHERE id = ?", string(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return payment.Bank{}, payment.ErrParticipantNotFound
	}
	if err != nil {
		return payment.Bank{}, fmt.Errorf("sqlite: get bank %s: %w", id, err)
	}
	assets, err := t.bankAssets(ctx, id)
	if err != nil {
		return payment.Bank{}, err
	}
	b.Assets = assets[id]
	return b, nil
}

func (t *tx) ListBanks(ctx context.Context) ([]payment.Bank, error) {
	rows, err := t.tx.QueryContext(ctx,
		"SELECT "+bankColumns+" FROM banks ORDER BY created_at ASC NULLS FIRST, seq")
	if err != nil {
		return nil, fmt.Errorf("sqlite: list banks: %w", err)
	}
	defer rows.Close()

	out := make([]payment.Bank, 0)
	for rows.Next() {
		b, err := scanBank(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list banks: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// One extra query for every bank's assets, rather than one per bank.
	assets, err := t.bankAssets(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Assets = assets[out[i].ID]
	}
	return out, nil
}

// PutSettlementMember stores the central bank's own record of one member and
// the account it holds for that member per asset.
//
// The child rows are deleted and rewritten for the reason PutBank's are: an
// account left behind for an asset the member has given up is one a cut-off
// would still post to.
func (t *tx) PutSettlementMember(ctx context.Context, m payment.SettlementMember) error {
	if err := t.write(); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO settlement_members (bic, name, opened_at, seq)
		VALUES (?, ?, ?, `+nextRowSeq("settlement_members")+`)
		ON CONFLICT (bic) DO UPDATE SET
			name      = EXCLUDED.name,
			opened_at = EXCLUDED.opened_at`,
		string(m.BIC), m.Name, nullTime{m.OpenedAt})
	if err != nil {
		return fmt.Errorf("sqlite: put settlement member %s: %w", m.BIC, err)
	}

	if _, err := t.tx.ExecContext(ctx, "DELETE FROM settlement_member_accounts WHERE bic = ?", string(m.BIC)); err != nil {
		return fmt.Errorf("sqlite: put settlement member %s: %w", m.BIC, err)
	}
	// Sorted for the reason bank_assets is sorted: `seq` is an insertion order
	// and map iteration is random, so an unsorted insert gives the same data a
	// different sequence on every write.
	for _, asset := range slices.Sorted(maps.Keys(m.Accounts)) {
		if _, err := t.tx.ExecContext(ctx, `
			INSERT INTO settlement_member_accounts (bic, asset, account, seq)
			VALUES (?, ?, ?, `+nextRowSeq("settlement_member_accounts")+`)`,
			string(m.BIC), string(asset), string(m.Accounts[asset])); err != nil {
			return fmt.Errorf("sqlite: put settlement member %s asset %s: %w", m.BIC, asset, err)
		}
	}
	return nil
}

// settlementMemberAccounts reads the accounts of one member, or of every member
// when bic is empty — the same one-query-then-fold shape as bankAssets.
func (t *tx) settlementMemberAccounts(ctx context.Context, bic iso20022.BIC) (map[iso20022.BIC]map[ledger.AssetCode]ledger.AccountID, error) {
	query := "SELECT bic, asset, account FROM settlement_member_accounts"
	args := []any{}
	if bic != "" {
		query += " WHERE bic = ?"
		args = append(args, string(bic))
	}
	query += " ORDER BY bic, seq"

	rows, err := t.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: settlement member accounts: %w", err)
	}
	defer rows.Close()

	out := make(map[iso20022.BIC]map[ledger.AssetCode]ledger.AccountID)
	for rows.Next() {
		var (
			memberBIC iso20022.BIC
			asset     ledger.AssetCode
			account   ledger.AccountID
		)
		if err := rows.Scan(&memberBIC, &asset, &account); err != nil {
			return nil, fmt.Errorf("sqlite: settlement member accounts: %w", err)
		}
		if out[memberBIC] == nil {
			out[memberBIC] = make(map[ledger.AssetCode]ledger.AccountID)
		}
		out[memberBIC][asset] = account
	}
	return out, rows.Err()
}

func scanSettlementMember(row interface{ Scan(...any) error }) (payment.SettlementMember, error) {
	var (
		m        payment.SettlementMember
		openedAt nullTime
	)
	if err := row.Scan(&m.BIC, &m.Name, &openedAt); err != nil {
		return payment.SettlementMember{}, err
	}
	m.OpenedAt = openedAt.Time
	return m, nil
}

func (t *tx) GetSettlementMember(ctx context.Context, bic iso20022.BIC) (payment.SettlementMember, error) {
	m, err := scanSettlementMember(t.tx.QueryRowContext(ctx,
		"SELECT bic, name, opened_at FROM settlement_members WHERE bic = ?", string(bic)))
	if errors.Is(err, sql.ErrNoRows) {
		return payment.SettlementMember{}, payment.ErrSettlementMemberNotFound
	}
	if err != nil {
		return payment.SettlementMember{}, fmt.Errorf("sqlite: get settlement member %s: %w", bic, err)
	}
	accounts, err := t.settlementMemberAccounts(ctx, bic)
	if err != nil {
		return payment.SettlementMember{}, err
	}
	m.Accounts = accounts[bic]
	return m, nil
}

func (t *tx) ListSettlementMembers(ctx context.Context) ([]payment.SettlementMember, error) {
	rows, err := t.tx.QueryContext(ctx,
		"SELECT bic, name, opened_at FROM settlement_members ORDER BY opened_at ASC NULLS FIRST, seq")
	if err != nil {
		return nil, fmt.Errorf("sqlite: list settlement members: %w", err)
	}
	defer rows.Close()

	out := make([]payment.SettlementMember, 0)
	for rows.Next() {
		m, err := scanSettlementMember(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list settlement members: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	accounts, err := t.settlementMemberAccounts(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Accounts = accounts[out[i].BIC]
	}
	return out, nil
}

// PutRosterEntry stores the clearing house's routing row for one member.
//
// The assets are a child table like the other two rows', but they are an
// ordered LIST rather than a map: the clearing house holds no account per
// asset, so there is nothing to key them by. They are written with an explicit
// position, which is what lets a caller's order survive and what keeps a
// repeated asset from being a row this store refuses and the Go type allows —
// see roster_entry_assets in the schema, and storetest's
// RosterEntryAssetsAreAnOrderedList.
func (t *tx) PutRosterEntry(ctx context.Context, e payment.RosterEntry) error {
	if err := t.write(); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO roster_entries (bic, admission_ref, admitted_at, seq)
		VALUES (?, ?, ?, `+nextRowSeq("roster_entries")+`)
		ON CONFLICT (bic) DO UPDATE SET
			admission_ref = EXCLUDED.admission_ref,
			admitted_at   = EXCLUDED.admitted_at`,
		string(e.BIC), e.AdmissionRef, nullTime{e.AdmittedAt})
	if err != nil {
		return fmt.Errorf("sqlite: put roster entry %s: %w", e.BIC, err)
	}

	if _, err := t.tx.ExecContext(ctx, "DELETE FROM roster_entry_assets WHERE bic = ?", string(e.BIC)); err != nil {
		return fmt.Errorf("sqlite: put roster entry %s: %w", e.BIC, err)
	}
	// NOT sorted, and this is the one child table here that is not. The other
	// two are maps, whose iteration order is random and therefore has to be
	// imposed; this is a slice the caller ordered, and the position column is
	// what carries that order back. Sorting here would silently reorder data,
	// and de-duplicating would silently drop it — both are decisions for
	// whoever reads the message the list came from.
	for i, asset := range e.Assets {
		if _, err := t.tx.ExecContext(ctx, `
			INSERT INTO roster_entry_assets (bic, position, asset) VALUES (?, ?, ?)`,
			string(e.BIC), i, string(asset)); err != nil {
			return fmt.Errorf("sqlite: put roster entry %s asset %s: %w", e.BIC, asset, err)
		}
	}
	return nil
}

// rosterEntryAssets reads the assets of one roster entry, or of every entry
// when bic is empty. Ordered by position within a BIC, which is the order the
// caller wrote — see PutRosterEntry.
func (t *tx) rosterEntryAssets(ctx context.Context, bic iso20022.BIC) (map[iso20022.BIC][]ledger.AssetCode, error) {
	query := "SELECT bic, asset FROM roster_entry_assets"
	args := []any{}
	if bic != "" {
		query += " WHERE bic = ?"
		args = append(args, string(bic))
	}
	query += " ORDER BY bic, position"

	rows, err := t.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: roster entry assets: %w", err)
	}
	defer rows.Close()

	out := make(map[iso20022.BIC][]ledger.AssetCode)
	for rows.Next() {
		var (
			entryBIC iso20022.BIC
			asset    ledger.AssetCode
		)
		if err := rows.Scan(&entryBIC, &asset); err != nil {
			return nil, fmt.Errorf("sqlite: roster entry assets: %w", err)
		}
		out[entryBIC] = append(out[entryBIC], asset)
	}
	return out, rows.Err()
}

func scanRosterEntry(row interface{ Scan(...any) error }) (payment.RosterEntry, error) {
	var (
		e          payment.RosterEntry
		admittedAt nullTime
	)
	if err := row.Scan(&e.BIC, &e.AdmissionRef, &admittedAt); err != nil {
		return payment.RosterEntry{}, err
	}
	e.AdmittedAt = admittedAt.Time
	return e, nil
}

func (t *tx) GetRosterEntry(ctx context.Context, bic iso20022.BIC) (payment.RosterEntry, error) {
	e, err := scanRosterEntry(t.tx.QueryRowContext(ctx,
		"SELECT bic, admission_ref, admitted_at FROM roster_entries WHERE bic = ?", string(bic)))
	if errors.Is(err, sql.ErrNoRows) {
		return payment.RosterEntry{}, payment.ErrRosterEntryNotFound
	}
	if err != nil {
		return payment.RosterEntry{}, fmt.Errorf("sqlite: get roster entry %s: %w", bic, err)
	}
	assets, err := t.rosterEntryAssets(ctx, bic)
	if err != nil {
		return payment.RosterEntry{}, err
	}
	e.Assets = assets[bic]
	return e, nil
}

func (t *tx) ListRosterEntries(ctx context.Context) ([]payment.RosterEntry, error) {
	rows, err := t.tx.QueryContext(ctx,
		"SELECT bic, admission_ref, admitted_at FROM roster_entries ORDER BY admitted_at ASC NULLS FIRST, seq")
	if err != nil {
		return nil, fmt.Errorf("sqlite: list roster entries: %w", err)
	}
	defer rows.Close()

	out := make([]payment.RosterEntry, 0)
	for rows.Next() {
		e, err := scanRosterEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list roster entries: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	assets, err := t.rosterEntryAssets(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Assets = assets[out[i].BIC]
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
	debtor_leg_tx, creditor_leg_tx, creditor_leg_account,
	return_clawback_tx, return_refund_tx`

func (t *tx) PutPayment(ctx context.Context, p payment.Payment) error {
	if err := t.write(); err != nil {
		return err
	}
	metadata, err := marshalStringMap(p.Metadata)
	if err != nil {
		return fmt.Errorf("sqlite: put payment %s: %w", p.ID, err)
	}
	_, err = t.tx.ExecContext(ctx, `
		INSERT INTO payments (`+paymentColumns+`, seq)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, `+nextRowSeq("payments")+`)
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
			creditor_leg_account       = EXCLUDED.creditor_leg_account,
			return_clawback_tx         = EXCLUDED.return_clawback_tx,
			return_refund_tx           = EXCLUDED.return_refund_tx`,
		string(p.ID), string(p.Scheme),
		string(p.Debtor.Participant), string(p.Debtor.Account), string(p.Debtor.Identifier.Scheme), p.Debtor.Identifier.Value,
		string(p.Creditor.Participant), string(p.Creditor.Account), string(p.Creditor.Identifier.Scheme), p.Creditor.Identifier.Value,
		string(p.DebtorDetails.Agent), p.DebtorDetails.Name, string(p.CreditorDetails.Agent), p.CreditorDetails.Name,
		int64(p.Amount), string(p.MandateID), p.EndToEndID, int64(p.Status), p.RejectReason, string(p.RejectCode), string(p.CycleID),
		nullTime{p.BookingDate}, nullTime{p.ValueDate}, p.Description, metadata, nullTime{p.CreatedAt},
		string(p.DebtorLegTx), string(p.CreditorLegTx), string(p.CreditorLegAccount),
		string(p.ReturnClawbackTx), string(p.ReturnRefundTx))
	if err != nil {
		return fmt.Errorf("sqlite: put payment %s: %w", p.ID, err)
	}
	return nil
}

func scanPayment(row interface{ Scan(...any) error }) (payment.Payment, error) {
	var (
		p                         payment.Payment
		status                    int64
		booking, value, createdAt nullTime
		metadata                  []byte
	)
	err := row.Scan(&p.ID, &p.Scheme,
		&p.Debtor.Participant, &p.Debtor.Account, &p.Debtor.Identifier.Scheme, &p.Debtor.Identifier.Value,
		&p.Creditor.Participant, &p.Creditor.Account, &p.Creditor.Identifier.Scheme, &p.Creditor.Identifier.Value,
		&p.DebtorDetails.Agent, &p.DebtorDetails.Name, &p.CreditorDetails.Agent, &p.CreditorDetails.Name,
		&p.Amount, &p.MandateID, &p.EndToEndID, &status, &p.RejectReason, &p.RejectCode, &p.CycleID,
		&booking, &value, &p.Description, &metadata, &createdAt,
		&p.DebtorLegTx, &p.CreditorLegTx, &p.CreditorLegAccount,
		&p.ReturnClawbackTx, &p.ReturnRefundTx)
	if err != nil {
		return payment.Payment{}, err
	}
	p.Status = payment.PaymentStatus(status)
	p.BookingDate = booking.Time
	p.ValueDate = value.Time
	p.CreatedAt = createdAt.Time
	if p.Metadata, err = unmarshalStringMap(metadata); err != nil {
		return payment.Payment{}, err
	}
	return p, nil
}

func (t *tx) GetPayment(ctx context.Context, id payment.PaymentID) (payment.Payment, error) {
	p, err := scanPayment(t.tx.QueryRowContext(ctx,
		"SELECT "+paymentColumns+" FROM payments WHERE id = ?", string(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return payment.Payment{}, payment.ErrPaymentNotFound
	}
	if err != nil {
		return payment.Payment{}, fmt.Errorf("sqlite: get payment %s: %w", id, err)
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
	p, err := scanPayment(t.tx.QueryRowContext(ctx,
		"SELECT "+paymentColumns+" FROM payments WHERE end_to_end_id = ? ORDER BY seq LIMIT 1", endToEndID))
	if errors.Is(err, sql.ErrNoRows) {
		return payment.Payment{}, payment.ErrPaymentNotFound
	}
	if err != nil {
		return payment.Payment{}, fmt.Errorf("sqlite: get payment by end-to-end id: %w", err)
	}
	return p, nil
}

func (t *tx) ListPayments(ctx context.Context) ([]payment.Payment, error) {
	rows, err := t.tx.QueryContext(ctx,
		"SELECT "+paymentColumns+" FROM payments ORDER BY created_at ASC NULLS FIRST, seq")
	if err != nil {
		return nil, fmt.Errorf("sqlite: list payments: %w", err)
	}
	defer rows.Close()

	out := make([]payment.Payment, 0)
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list payments: %w", err)
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
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO mandates (`+mandateColumns+`, seq)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?, `+nextRowSeq("mandates")+`)
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
		int64(m.MaxAmount), int64(m.Status), nullTime{m.CreatedAt})
	if err != nil {
		return fmt.Errorf("sqlite: put mandate %s: %w", m.ID, err)
	}
	return nil
}

func scanMandate(row interface{ Scan(...any) error }) (payment.Mandate, error) {
	var (
		m         payment.Mandate
		status    int64
		createdAt nullTime
	)
	err := row.Scan(&m.ID,
		&m.Debtor.Participant, &m.Debtor.Account, &m.Debtor.Identifier.Scheme, &m.Debtor.Identifier.Value,
		&m.Creditor.Participant, &m.Creditor.Account, &m.Creditor.Identifier.Scheme, &m.Creditor.Identifier.Value,
		&m.MaxAmount, &status, &createdAt)
	if err != nil {
		return payment.Mandate{}, err
	}
	m.Status = payment.MandateStatus(status)
	m.CreatedAt = createdAt.Time
	return m, nil
}

func (t *tx) GetMandate(ctx context.Context, id payment.MandateID) (payment.Mandate, error) {
	m, err := scanMandate(t.tx.QueryRowContext(ctx,
		"SELECT "+mandateColumns+" FROM mandates WHERE id = ?", string(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return payment.Mandate{}, payment.ErrMandateNotFound
	}
	if err != nil {
		return payment.Mandate{}, fmt.Errorf("sqlite: get mandate %s: %w", id, err)
	}
	return m, nil
}

func (t *tx) ListMandates(ctx context.Context) ([]payment.Mandate, error) {
	rows, err := t.tx.QueryContext(ctx,
		"SELECT "+mandateColumns+" FROM mandates ORDER BY created_at ASC NULLS FIRST, seq")
	if err != nil {
		return nil, fmt.Errorf("sqlite: list mandates: %w", err)
	}
	defer rows.Close()

	out := make([]payment.Mandate, 0)
	for rows.Next() {
		m, err := scanMandate(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list mandates: %w", err)
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
		return fmt.Errorf("sqlite: put cycle %s: %w", c.ID, err)
	}
	_, err = t.tx.ExecContext(ctx, `
		INSERT INTO cycles (id, scheme, status, net_positions, opened_at, closed_at, settlement_id, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, `+nextRowSeq("cycles")+`)
		ON CONFLICT (id) DO UPDATE SET
			scheme        = EXCLUDED.scheme,
			status        = EXCLUDED.status,
			net_positions = EXCLUDED.net_positions,
			opened_at     = EXCLUDED.opened_at,
			closed_at     = EXCLUDED.closed_at,
			settlement_id = EXCLUDED.settlement_id`,
		string(c.ID), string(c.Scheme), int64(c.Status), positions,
		nullTime{c.OpenedAt}, nullTime{c.ClosedAt}, string(c.SettlementID))
	if err != nil {
		return fmt.Errorf("sqlite: put cycle %s: %w", c.ID, err)
	}

	// PaymentIDs is an ordered slice, so it gets its own rows with an explicit
	// position; replacing the whole list keeps the upsert honest.
	if _, err := t.tx.ExecContext(ctx, "DELETE FROM cycle_payments WHERE cycle_id = ?", string(c.ID)); err != nil {
		return fmt.Errorf("sqlite: put cycle %s: %w", c.ID, err)
	}
	for i, id := range c.PaymentIDs {
		if _, err := t.tx.ExecContext(ctx,
			"INSERT INTO cycle_payments (cycle_id, position, payment_id) VALUES (?, ?, ?)",
			string(c.ID), i, string(id)); err != nil {
			return fmt.Errorf("sqlite: put cycle %s payment %d: %w", c.ID, i, err)
		}
	}
	return nil
}

func (t *tx) GetCycle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error) {
	out, err := t.queryCycles(ctx, "WHERE c.id = ?", "", string(id))
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
// payment.Store's documented answer rather than this query's accident.
//
// The scheme and the status are each bound twice rather than once. store/pg
// wrote $1 and $2 in both the outer predicate and the subquery; a positional
// placeholder cannot be reused, so the argument is passed again — the same
// query, spelled for a driver that counts rather than numbers.
func (t *tx) GetOpenCycle(ctx context.Context, scheme payment.SchemeID) (payment.ClearingCycle, error) {
	out, err := t.queryCycles(ctx,
		"WHERE c.scheme = ? AND c.status = ? AND c.id = (SELECT id FROM cycles WHERE scheme = ? AND status = ? ORDER BY opened_at ASC NULLS FIRST, seq LIMIT 1)",
		"", string(scheme), int64(payment.CycleOpen), string(scheme), int64(payment.CycleOpen))
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
	rows, err := t.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: query cycles: %w", err)
	}
	defer rows.Close()

	out := make([]payment.ClearingCycle, 0)
	index := make(map[payment.CycleID]int)
	for rows.Next() {
		var (
			c              payment.ClearingCycle
			status         int64
			positions      []byte
			opened, closed nullTime
			paymentID      sql.NullString
		)
		if err := rows.Scan(&c.ID, &c.Scheme, &status, &positions, &opened, &closed, &c.SettlementID, &paymentID); err != nil {
			return nil, fmt.Errorf("sqlite: query cycles: %w", err)
		}
		at, seen := index[c.ID]
		if !seen {
			c.Status = payment.CycleStatus(status)
			c.OpenedAt = opened.Time
			c.ClosedAt = closed.Time
			if c.NetPositions, err = unmarshalPositions(positions); err != nil {
				return nil, fmt.Errorf("sqlite: cycle %s net positions: %w", c.ID, err)
			}
			at = len(out)
			index[c.ID] = at
			out = append(out, c)
		}
		if paymentID.Valid {
			out[at].PaymentIDs = append(out[at].PaymentIDs, payment.PaymentID(paymentID.String))
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
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO settlements (id, cycle_id, settlement_tx, value_date, settled_at, seq)
		VALUES (?, ?, ?, ?, ?, `+nextRowSeq("settlements")+`)
		ON CONFLICT (id) DO UPDATE SET
			cycle_id      = EXCLUDED.cycle_id,
			settlement_tx = EXCLUDED.settlement_tx,
			value_date    = EXCLUDED.value_date,
			settled_at    = EXCLUDED.settled_at`,
		string(s.ID), string(s.CycleID), string(s.SettlementTx),
		nullTime{s.ValueDate}, nullTime{s.SettledAt})
	if err != nil {
		return fmt.Errorf("sqlite: put settlement %s: %w", s.ID, err)
	}

	if _, err := t.tx.ExecContext(ctx, "DELETE FROM settlement_positions WHERE settlement_id = ?", string(s.ID)); err != nil {
		return fmt.Errorf("sqlite: put settlement %s: %w", s.ID, err)
	}
	for participant, amount := range s.NetPositions {
		if _, err := t.tx.ExecContext(ctx,
			"INSERT INTO settlement_positions (settlement_id, participant_id, amount) VALUES (?, ?, ?)",
			string(s.ID), string(participant), int64(amount)); err != nil {
			return fmt.Errorf("sqlite: put settlement %s position %s: %w", s.ID, participant, err)
		}
	}
	return nil
}

func (t *tx) GetSettlement(ctx context.Context, id payment.SettlementID) (payment.Settlement, error) {
	out, err := t.querySettlements(ctx, "WHERE s.id = ?", "", string(id))
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
	rows, err := t.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: query settlements: %w", err)
	}
	defer rows.Close()

	out := make([]payment.Settlement, 0)
	index := make(map[payment.SettlementID]int)
	for rows.Next() {
		var (
			s             payment.Settlement
			value, at     nullTime
			participantID sql.NullString
			amount        sql.NullInt64
		)
		if err := rows.Scan(&s.ID, &s.CycleID, &s.SettlementTx, &value, &at, &participantID, &amount); err != nil {
			return nil, fmt.Errorf("sqlite: query settlements: %w", err)
		}
		pos, seen := index[s.ID]
		if !seen {
			s.ValueDate = value.Time
			s.SettledAt = at.Time
			s.NetPositions = make(map[payment.ParticipantID]ledger.Amount)
			pos = len(out)
			index[s.ID] = pos
			out = append(out, s)
		}
		if participantID.Valid {
			out[pos].NetPositions[payment.ParticipantID(participantID.String)] = ledger.Amount(amount.Int64)
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

const settlementAdviceColumns = `book_id, reference, asset, movement, closing_balance,
	status, mirror_tx, advised_at, posted_at`

func (t *tx) PutSettlementAdvice(ctx context.Context, book ledger.BookID, a payment.SettlementAdvice) error {
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO settlement_advices (`+settlementAdviceColumns+`, seq)
		VALUES (?,?,?,?,?,?,?,?,?, `+nextRowSeq("settlement_advices")+`)
		ON CONFLICT (book_id, reference, asset) DO UPDATE SET
			movement        = EXCLUDED.movement,
			closing_balance = EXCLUDED.closing_balance,
			status          = EXCLUDED.status,
			mirror_tx       = EXCLUDED.mirror_tx,
			advised_at      = EXCLUDED.advised_at,
			posted_at       = EXCLUDED.posted_at`,
		string(book), a.Reference, string(a.Asset), int64(a.Movement), int64(a.ClosingBalance),
		int64(a.Status), string(a.MirrorTx), nullTime{a.AdvisedAt}, nullTime{a.PostedAt})
	if err != nil {
		return fmt.Errorf("sqlite: put settlement advice %s/%s/%s: %w", book, a.Reference, a.Asset, err)
	}
	return nil
}

func scanSettlementAdvice(row interface{ Scan(...any) error }) (payment.SettlementAdvice, error) {
	var (
		a               payment.SettlementAdvice
		status          int64
		advised, posted nullTime
	)
	err := row.Scan(&a.Book, &a.Reference, &a.Asset, &a.Movement, &a.ClosingBalance,
		&status, &a.MirrorTx, &advised, &posted)
	if err != nil {
		return payment.SettlementAdvice{}, err
	}
	a.Status = payment.AdviceStatus(status)
	a.AdvisedAt = advised.Time
	a.PostedAt = posted.Time
	return a, nil
}

func (t *tx) GetSettlementAdvice(ctx context.Context, book ledger.BookID, reference string, asset ledger.AssetCode) (payment.SettlementAdvice, error) {
	a, err := scanSettlementAdvice(t.tx.QueryRowContext(ctx,
		"SELECT "+settlementAdviceColumns+" FROM settlement_advices WHERE book_id = ? AND reference = ? AND asset = ?",
		string(book), reference, string(asset)))
	if errors.Is(err, sql.ErrNoRows) {
		return payment.SettlementAdvice{}, payment.ErrSettlementAdviceNotFound
	}
	if err != nil {
		return payment.SettlementAdvice{}, fmt.Errorf("sqlite: get settlement advice %s/%s/%s: %w", book, reference, asset, err)
	}
	return a, nil
}

func (t *tx) ListSettlementAdvices(ctx context.Context, book ledger.BookID) ([]payment.SettlementAdvice, error) {
	rows, err := t.tx.QueryContext(ctx,
		"SELECT "+settlementAdviceColumns+" FROM settlement_advices WHERE book_id = ? "+
			"ORDER BY advised_at ASC NULLS FIRST, seq", string(book))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list settlement advices: %w", err)
	}
	defer rows.Close()

	out := make([]payment.SettlementAdvice, 0)
	for rows.Next() {
		a, err := scanSettlementAdvice(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list settlement advices: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Net positions
// ---------------------------------------------------------------------------

// marshalPositions encodes a net-position map for a json_valid column. A nil map
// is NULL and an empty one is {}: an open cycle carries an empty map and the API
// renders the two differently, so the distinction has to survive the round trip.
// A string and not a []byte, for jsonParam's reason.
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
	return string(raw), nil
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
