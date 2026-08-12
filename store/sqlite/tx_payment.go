package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// The payment half of tx — the same type again, which is what lets one
// BEGIN … COMMIT post across every participant's book and the central bank's and
// record the network's own rows beside them.
//
// The honest example is seed.builder.settle, which is the one caller that
// legitimately plays all three institutions at once — because the seed is not an
// institution — and which needs exactly this to do it.
//
// Almost every entity here is network-scoped: a payment belongs to no single
// bank, so unlike the ledger and deposit tables these are keyed by id alone and
// take no BookID. The exception is the settlement advice at the bottom — a
// member bank's own record of a cut-off it was told about — which carries a
// book_id like a ledger or deposit row, because it belongs to that bank.

// compile-time checks that tx satisfies all three institutions' interfaces. It
// is one value and one body of SQL; which of the three a caller may name is
// decided by the store it was handed. See sqlite.BankStore.
var (
	_ payment.BankTx        = (*tx)(nil)
	_ payment.CsmTx         = (*tx)(nil)
	_ payment.CentralBankTx = (*tx)(nil)
)

// ---------------------------------------------------------------------------
// The three rows admission writes
// ---------------------------------------------------------------------------
//
// One table per institution, plus a child table wherever a row holds something
// per asset. They are separate tables and not one wide one because each has a
// single writer and each lives in a different database — see the schema, which
// is where that argument is written down in full.

// PutBank stores a bank and the set of internal accounts it holds per asset.
// Its Ledger and Deposit fields are simply not written: they are live handles
// over this very store, not data, and there is no column that could hold a
// *ledger.Book. The Network rebinds them on the way out; storetest's
// BankRoundTripsAndDropsLiveHandles is what says a bank must come back with them
// dropped and with Status intact.
//
// The child rows are deleted and rewritten rather than upserted. An upsert alone
// would leave behind a row for an asset the bank no longer holds, and a stale row
// here is not cosmetic: settlement would resolve an account the bank has given
// up.
func (t *tx) PutBank(ctx context.Context, b payment.Bank) error {
	if err := t.inShape("banks"); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	// bic and book_id are NOT written, and there are no columns for them. A bank's
	// id IS its BIC and IS its book (see payment.AsBank and the banks statement in
	// the schema), so the two fields on the record are derivations of the primary
	// key rather than data — scanBank fills them back in.
	//
	// A record whose BIC or BookID disagrees with its id is silently normalised
	// rather than refused, which is the same stance PutBank has always taken on
	// the live handles it drops: this is a serialiser, and the place that refuses
	// an inconsistent bank is FoundBankTx, which is the only thing that mints one.
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO banks
			(id, name, customer_subledger, product_id, admission_ref, country, bank_code, created_at, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, `+nextRowSeq("banks")+`)
		ON CONFLICT (id) DO UPDATE SET
			name               = EXCLUDED.name,
			customer_subledger = EXCLUDED.customer_subledger,
			product_id         = EXCLUDED.product_id,
			admission_ref      = EXCLUDED.admission_ref,
			country            = EXCLUDED.country,
			bank_code          = EXCLUDED.bank_code,
			created_at         = EXCLUDED.created_at`,
		string(b.ID), b.Name, string(b.CustomerSubledger),
		string(b.ProductID), b.AdmissionRef,
		string(b.Issuer.Country), string(b.Issuer.BankCode), nullTime{b.CreatedAt})
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
			INSERT INTO bank_assets (bank_id, asset, suspense, reserve, unclaimed, returns_receivable, vault_cash, settlement, seq)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, `+nextRowSeq("bank_assets")+`)`,
			string(b.ID), string(asset),
			string(accts.Suspense), string(accts.Reserve), string(accts.Unclaimed), string(accts.ReturnsReceivable),
			string(accts.VaultCash), string(accts.Settlement)); err != nil {
			return fmt.Errorf("sqlite: put bank %s asset %s: %w", b.ID, asset, err)
		}
	}
	return nil
}

const bankColumns = `id, name, customer_subledger, product_id, admission_ref, country, bank_code, created_at`

func scanBank(row interface{ Scan(...any) error }) (payment.Bank, error) {
	var (
		b         payment.Bank
		createdAt nullTime
	)
	err := row.Scan(&b.ID, &b.Name, &b.CustomerSubledger, &b.ProductID,
		&b.AdmissionRef, &b.Issuer.Country, &b.Issuer.BankCode, &createdAt)
	if err != nil {
		return payment.Bank{}, err
	}
	// Both derived from the key, because both ARE the key: a bank is its own book
	// and its id is its address. See PutBank, which writes neither, and
	// storetest's BankRoundTripsAndDropsLiveHandles, which is what says a bank
	// must come back carrying them.
	b.BIC = iso20022.BIC(b.ID)
	b.BookID = ledger.BookID(b.ID)
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
	query := "SELECT bank_id, asset, suspense, reserve, unclaimed, returns_receivable, vault_cash, settlement FROM bank_assets"
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
		if err := rows.Scan(&id, &asset, &accts.Suspense, &accts.Reserve, &accts.Unclaimed, &accts.ReturnsReceivable,
			&accts.VaultCash, &accts.Settlement); err != nil {
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
	if err := t.inShape("banks"); err != nil {
		return payment.Bank{}, err
	}
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
	if err := t.inShape("banks"); err != nil {
		return nil, err
	}
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
	if err := t.inShape("settlement_members"); err != nil {
		return err
	}
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
	if err := t.inShape("settlement_members"); err != nil {
		return payment.SettlementMember{}, err
	}
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
	if err := t.inShape("settlement_members"); err != nil {
		return nil, err
	}
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

// NextBankCodeSerial counts one country's allocations, from a counter named
// after that country beside the book's shared "id" counter in the same table.
//
// One counter PER COUNTRY, and that is what makes the allocation rule below
// produce a readable code. A shared counter would still hand out unique numbers
// and the codes would have holes in them saying how much unrelated work the
// agent had done in between — the same argument NextAddressSerial makes about an
// account number, and for the same reason: a bank code is a number people quote.
func (t *tx) NextBankCodeSerial(ctx context.Context, book ledger.BookID, country iban.Country) (uint64, error) {
	if err := t.inShape("bank_codes"); err != nil {
		return 0, err
	}
	n, err := t.nextSeq(ctx, book, "bank_code:"+string(country))
	if err != nil {
		return 0, err
	}
	return uint64(n), nil
}

func (t *tx) PutBankCode(ctx context.Context, a payment.BankCodeAllocation) error {
	if err := t.inShape("bank_codes"); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	// An upsert like every other Put here, and the key it upserts on is
	// (country, code) — so re-writing an allocation to the SAME bank is a no-op
	// and moving one to another bank would silently succeed. What forbids that
	// is the act: payment's OpenSettlementAccountTx reads the row before it
	// writes and refuses a code held by anybody else (ErrBankCodeTaken). The
	// store holds the key; a code is never reassigned is a domain rule, and this
	// is the same division every other row in this file keeps.
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO bank_codes (country, code, bic, allocated_at, seq)
		VALUES (?, ?, ?, ?, `+nextRowSeq("bank_codes")+`)
		ON CONFLICT (country, code) DO UPDATE SET
			bic          = EXCLUDED.bic,
			allocated_at = EXCLUDED.allocated_at`,
		string(a.Issuer.Country), string(a.Issuer.BankCode), string(a.BIC), nullTime{a.AllocatedAt})
	if err != nil {
		return fmt.Errorf("sqlite: put bank code %s %s: %w", a.Issuer.Country, a.Issuer.BankCode, err)
	}
	return nil
}

func scanBankCode(row interface{ Scan(...any) error }) (payment.BankCodeAllocation, error) {
	var (
		a           payment.BankCodeAllocation
		allocatedAt nullTime
	)
	if err := row.Scan(&a.Issuer.Country, &a.Issuer.BankCode, &a.BIC, &allocatedAt); err != nil {
		return payment.BankCodeAllocation{}, err
	}
	a.AllocatedAt = allocatedAt.Time
	return a, nil
}

func (t *tx) GetBankCode(ctx context.Context, issuer iban.Issuer) (payment.BankCodeAllocation, error) {
	if err := t.inShape("bank_codes"); err != nil {
		return payment.BankCodeAllocation{}, err
	}
	a, err := scanBankCode(t.tx.QueryRowContext(ctx,
		"SELECT country, code, bic, allocated_at FROM bank_codes WHERE country = ? AND code = ?",
		string(issuer.Country), string(issuer.BankCode)))
	if errors.Is(err, sql.ErrNoRows) {
		return payment.BankCodeAllocation{}, payment.ErrBankCodeNotAllocated
	}
	if err != nil {
		return payment.BankCodeAllocation{}, fmt.Errorf("sqlite: get bank code %s %s: %w",
			issuer.Country, issuer.BankCode, err)
	}
	return a, nil
}

// GetBankCodeForBIC answers the other question this register is asked: what has
// this institution already been allocated in this country.
//
// There is no unique index behind it — the schema argues why at the bic column —
// so a second row would be invisible here rather than refused. It cannot exist:
// the act reads this before it allocates. LIMIT 1 on the seq is what makes the
// answer at least DETERMINISTIC if one ever did, rather than whichever row the
// query planner reached first.
func (t *tx) GetBankCodeForBIC(ctx context.Context, country iban.Country, bic iso20022.BIC) (payment.BankCodeAllocation, error) {
	if err := t.inShape("bank_codes"); err != nil {
		return payment.BankCodeAllocation{}, err
	}
	a, err := scanBankCode(t.tx.QueryRowContext(ctx, `
		SELECT country, code, bic, allocated_at FROM bank_codes
		 WHERE country = ? AND bic = ? ORDER BY seq LIMIT 1`, string(country), string(bic)))
	if errors.Is(err, sql.ErrNoRows) {
		return payment.BankCodeAllocation{}, payment.ErrBankCodeNotAllocated
	}
	if err != nil {
		return payment.BankCodeAllocation{}, fmt.Errorf("sqlite: get %s's bank code in %s: %w", bic, country, err)
	}
	return a, nil
}

func (t *tx) ListBankCodes(ctx context.Context) ([]payment.BankCodeAllocation, error) {
	if err := t.inShape("bank_codes"); err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(ctx,
		"SELECT country, code, bic, allocated_at FROM bank_codes ORDER BY allocated_at ASC NULLS FIRST, seq")
	if err != nil {
		return nil, fmt.Errorf("sqlite: list bank codes: %w", err)
	}
	defer rows.Close()

	out := make([]payment.BankCodeAllocation, 0)
	for rows.Next() {
		a, err := scanBankCode(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list bank codes: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
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
	if err := t.inShape("roster_entries"); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO roster_entries (bic, country, bank_code, admission_ref, admitted_at, seq)
		VALUES (?, ?, ?, ?, ?, `+nextRowSeq("roster_entries")+`)
		ON CONFLICT (bic) DO UPDATE SET
			country       = EXCLUDED.country,
			bank_code     = EXCLUDED.bank_code,
			admission_ref = EXCLUDED.admission_ref,
			admitted_at   = EXCLUDED.admitted_at`,
		string(e.BIC), string(e.Issuer.Country), string(e.Issuer.BankCode), e.AdmissionRef, nullTime{e.AdmittedAt})
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
	if err := row.Scan(&e.BIC, &e.Issuer.Country, &e.Issuer.BankCode, &e.AdmissionRef, &admittedAt); err != nil {
		return payment.RosterEntry{}, err
	}
	e.AdmittedAt = admittedAt.Time
	return e, nil
}

func (t *tx) GetRosterEntry(ctx context.Context, bic iso20022.BIC) (payment.RosterEntry, error) {
	if err := t.inShape("roster_entries"); err != nil {
		return payment.RosterEntry{}, err
	}
	e, err := scanRosterEntry(t.tx.QueryRowContext(ctx,
		"SELECT bic, country, bank_code, admission_ref, admitted_at FROM roster_entries WHERE bic = ?", string(bic)))
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

// GetRosterEntryByIssuer answers which member is published under one allocation.
//
// There is no index behind it and no UNIQUE either — the schema argues both at
// the bank_code column — so this is a scan of a table with one row per member.
// ORDER BY seq is what makes the answer deterministic if a duplicate ever
// existed, which the act that writes these rows is what prevents.
func (t *tx) GetRosterEntryByIssuer(ctx context.Context, issuer iban.Issuer) (payment.RosterEntry, error) {
	if err := t.inShape("roster_entries"); err != nil {
		return payment.RosterEntry{}, err
	}
	e, err := scanRosterEntry(t.tx.QueryRowContext(ctx, `
		SELECT bic, country, bank_code, admission_ref, admitted_at FROM roster_entries
		 WHERE country = ? AND bank_code = ? ORDER BY seq LIMIT 1`,
		string(issuer.Country), string(issuer.BankCode)))
	if errors.Is(err, sql.ErrNoRows) {
		return payment.RosterEntry{}, payment.ErrRosterEntryNotFound
	}
	if err != nil {
		return payment.RosterEntry{}, fmt.Errorf("sqlite: get roster entry for %s %s: %w",
			issuer.Country, issuer.BankCode, err)
	}
	assets, err := t.rosterEntryAssets(ctx, e.BIC)
	if err != nil {
		return payment.RosterEntry{}, err
	}
	e.Assets = assets[e.BIC]
	return e, nil
}

func (t *tx) ListRosterEntries(ctx context.Context) ([]payment.RosterEntry, error) {
	if err := t.inShape("roster_entries"); err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(ctx,
		"SELECT bic, country, bank_code, admission_ref, admitted_at FROM roster_entries ORDER BY admitted_at ASC NULLS FIRST, seq")
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
// A member bank's copy of the routing directory
// ---------------------------------------------------------------------------

// ReplaceRoutingDirectory takes delivery of a snapshot: every row goes, and the
// list replaces it.
//
// The DELETE is the method. Every other writer in this file upserts one row an
// institution decided about, and merging is right for those; a subscriber that
// merged would end up holding the union of every snapshot it ever pulled, and a
// member that had left the roster would still be routable from a directory
// nobody could correct. See routing_directory in the bank schema.
//
// seq restarts at 1 because the table is empty when the loop begins, so the
// stored order is the order the caller was given — the roster's own publication
// order — rather than an accumulation across refreshes.
func (t *tx) ReplaceRoutingDirectory(ctx context.Context, entries []payment.DirectoryEntry) error {
	if err := t.inShape("routing_directory"); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx, "DELETE FROM routing_directory"); err != nil {
		return fmt.Errorf("sqlite: replace routing directory: %w", err)
	}
	for _, e := range entries {
		_, err := t.tx.ExecContext(ctx, `
			INSERT INTO routing_directory (country, bank_code, bic, refreshed_at, seq)
			VALUES (?, ?, ?, ?, `+nextRowSeq("routing_directory")+`)`,
			string(e.Issuer.Country), string(e.Issuer.BankCode), string(e.BIC), nullTime{e.RefreshedAt})
		if err != nil {
			return fmt.Errorf("sqlite: replace routing directory at %s %s: %w",
				e.Issuer.Country, e.Issuer.BankCode, err)
		}
	}
	return nil
}

func scanDirectoryEntry(row interface{ Scan(...any) error }) (payment.DirectoryEntry, error) {
	var (
		e           payment.DirectoryEntry
		refreshedAt nullTime
	)
	if err := row.Scan(&e.Issuer.Country, &e.Issuer.BankCode, &e.BIC, &refreshedAt); err != nil {
		return payment.DirectoryEntry{}, err
	}
	e.RefreshedAt = refreshedAt.Time
	return e, nil
}

func (t *tx) GetDirectoryEntry(ctx context.Context, issuer iban.Issuer) (payment.DirectoryEntry, error) {
	if err := t.inShape("routing_directory"); err != nil {
		return payment.DirectoryEntry{}, err
	}
	e, err := scanDirectoryEntry(t.tx.QueryRowContext(ctx, `
		SELECT country, bank_code, bic, refreshed_at FROM routing_directory
		 WHERE country = ? AND bank_code = ?`, string(issuer.Country), string(issuer.BankCode)))
	if errors.Is(err, sql.ErrNoRows) {
		return payment.DirectoryEntry{}, payment.ErrBankCodeUnknown
	}
	if err != nil {
		return payment.DirectoryEntry{}, fmt.Errorf("sqlite: get directory entry %s %s: %w",
			issuer.Country, issuer.BankCode, err)
	}
	return e, nil
}

// ListDirectoryEntries answers in the order the snapshot arrived, which is the
// roster's, rather than in the (country, bank_code) order of the key. What a
// console shows is a directory as its publisher ordered it.
func (t *tx) ListDirectoryEntries(ctx context.Context) ([]payment.DirectoryEntry, error) {
	if err := t.inShape("routing_directory"); err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(ctx,
		"SELECT country, bank_code, bic, refreshed_at FROM routing_directory ORDER BY seq")
	if err != nil {
		return nil, fmt.Errorf("sqlite: list routing directory: %w", err)
	}
	defer rows.Close()

	out := make([]payment.DirectoryEntry, 0)
	for rows.Next() {
		e, err := scanDirectoryEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list routing directory: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Payments
// ---------------------------------------------------------------------------

// A payment row is not the same set of columns in every shape, and it is the one
// place in this store where two schemas hold one table differently.
//
// The bank keeps the LEGS — what it posted in its own book — and no cycle id,
// because a bank never sees a cut-off. The clearing house keeps the CYCLE id and
// no legs, because it posts nothing and holds no book of accounts. Each argument
// is written out inside the payments statement of the schema it belongs to; what
// is here is the mechanism that keeps the Go side agreeing with both.
//
// The column list, the bound values and the scan targets are built from one
// description in the same order, rather than being three parallel literals a
// reader has to line up by eye. Three literals is what this was, and a column
// added to the middle of one of them is a silently transposed row.
const paymentSharedColumns = `id, scheme,
	debtor_account, debtor_identifier_scheme, debtor_identifier_value,
	creditor_account, creditor_identifier_scheme, creditor_identifier_value,
	debtor_agent, debtor_name, creditor_agent, creditor_name,
	amount, mandate_id, end_to_end_id, status, reject_reason, reject_code,
	booking_date, value_date, description, metadata, created_at`

// The two per-shape tails. They go at the END of the list so that the shared
// prefix's positions never move.
const (
	paymentLegColumns = `debtor_leg_tx, creditor_leg_tx, creditor_leg_account,
	return_clawback_tx, return_refund_tx`
	paymentCycleColumn = `cycle_id`
)

// paymentColumns is the column list for this store's shape, in the order the
// values and the scan targets below use.
func (t *tx) paymentColumns() string {
	cols := paymentSharedColumns
	if t.store.shape.paymentLegs {
		cols += ", " + paymentLegColumns
	}
	if t.store.shape.paymentCycle {
		cols += ", " + paymentCycleColumn
	}
	return cols
}

// paymentValues is the bound values for this store's shape, in paymentColumns'
// order.
func (t *tx) paymentValues(p payment.Payment, metadata any) []any {
	vals := []any{
		string(p.ID), string(p.Scheme),
		string(p.Debtor.Account), string(p.Debtor.Identifier.Scheme), p.Debtor.Identifier.Value,
		string(p.Creditor.Account), string(p.Creditor.Identifier.Scheme), p.Creditor.Identifier.Value,
		string(p.DebtorDetails.Agent), p.DebtorDetails.Name, string(p.CreditorDetails.Agent), p.CreditorDetails.Name,
		int64(p.Amount), string(p.MandateID), p.EndToEndID, int64(p.Status), p.RejectReason, string(p.RejectCode),
		nullTime{p.BookingDate}, nullTime{p.ValueDate}, p.Description, metadata, nullTime{p.CreatedAt},
	}
	if t.store.shape.paymentLegs {
		vals = append(vals,
			string(p.DebtorLegTx), string(p.CreditorLegTx), string(p.CreditorLegAccount),
			string(p.ReturnClawbackTx), string(p.ReturnRefundTx))
	}
	if t.store.shape.paymentCycle {
		vals = append(vals, string(p.CycleID))
	}
	return vals
}

// paymentTargets is the scan targets for this store's shape, in the same order.
//
// A field this shape has no column for is left at its zero value, and that is
// the point rather than data loss: a bank's copy of a payment HAS no cycle, so
// reading one back with an empty CycleID is the truth about that row. Anything
// above the store that needed the other institution's half has to ask for it,
// which in this system means a message.
func (t *tx) paymentTargets(p *payment.Payment, status *int64, booking, value, createdAt *nullTime, metadata *[]byte) []any {
	targets := []any{
		&p.ID, &p.Scheme,
		&p.Debtor.Account, &p.Debtor.Identifier.Scheme, &p.Debtor.Identifier.Value,
		&p.Creditor.Account, &p.Creditor.Identifier.Scheme, &p.Creditor.Identifier.Value,
		&p.DebtorDetails.Agent, &p.DebtorDetails.Name, &p.CreditorDetails.Agent, &p.CreditorDetails.Name,
		&p.Amount, &p.MandateID, &p.EndToEndID, status, &p.RejectReason, &p.RejectCode,
		booking, value, &p.Description, metadata, createdAt,
	}
	if t.store.shape.paymentLegs {
		targets = append(targets,
			&p.DebtorLegTx, &p.CreditorLegTx, &p.CreditorLegAccount,
			&p.ReturnClawbackTx, &p.ReturnRefundTx)
	}
	if t.store.shape.paymentCycle {
		targets = append(targets, &p.CycleID)
	}
	return targets
}

// upsertSet is the ON CONFLICT assignment list for a column list: every column
// but the primary key, set from EXCLUDED.
func upsertSet(columns string, pk string) string {
	var b strings.Builder
	for _, c := range strings.Split(columns, ",") {
		c = strings.TrimSpace(c)
		if c == "" || c == pk {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(", ")
		}
		b.WriteString(c)
		b.WriteString(" = EXCLUDED.")
		b.WriteString(c)
	}
	return b.String()
}

func (t *tx) PutPayment(ctx context.Context, p payment.Payment) error {
	if err := t.inShape("payments"); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	metadata, err := marshalStringMap(p.Metadata)
	if err != nil {
		return fmt.Errorf("sqlite: put payment %s: %w", p.ID, err)
	}
	cols := t.paymentColumns()
	vals := t.paymentValues(p, metadata)
	_, err = t.tx.ExecContext(ctx, `
		INSERT INTO payments (`+cols+`, seq)
		VALUES (`+placeholders(len(vals))+`, `+nextRowSeq("payments")+`)
		ON CONFLICT (id) DO UPDATE SET `+upsertSet(cols, "id"), vals...)
	if err != nil {
		return fmt.Errorf("sqlite: put payment %s: %w", p.ID, err)
	}
	return nil
}

func (t *tx) scanPayment(row interface{ Scan(...any) error }) (payment.Payment, error) {
	var (
		p                         payment.Payment
		status                    int64
		booking, value, createdAt nullTime
		metadata                  []byte
	)
	if err := row.Scan(t.paymentTargets(&p, &status, &booking, &value, &createdAt, &metadata)...); err != nil {
		return payment.Payment{}, err
	}
	p.Status = payment.PaymentStatus(status)
	p.BookingDate = booking.Time
	p.ValueDate = value.Time
	p.CreatedAt = createdAt.Time
	var err error
	if p.Metadata, err = unmarshalStringMap(metadata); err != nil {
		return payment.Payment{}, err
	}
	return p, nil
}

func (t *tx) GetPayment(ctx context.Context, id payment.PaymentID) (payment.Payment, error) {
	if err := t.inShape("payments"); err != nil {
		return payment.Payment{}, err
	}
	p, err := t.scanPayment(t.tx.QueryRowContext(ctx,
		"SELECT "+t.paymentColumns()+" FROM payments WHERE id = ?", string(id)))
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
	if err := t.inShape("payments"); err != nil {
		return payment.Payment{}, err
	}
	if endToEndID == "" {
		return payment.Payment{}, payment.ErrPaymentNotFound
	}
	p, err := t.scanPayment(t.tx.QueryRowContext(ctx,
		"SELECT "+t.paymentColumns()+" FROM payments WHERE end_to_end_id = ? ORDER BY seq LIMIT 1", endToEndID))
	if errors.Is(err, sql.ErrNoRows) {
		return payment.Payment{}, payment.ErrPaymentNotFound
	}
	if err != nil {
		return payment.Payment{}, fmt.Errorf("sqlite: get payment by end-to-end id: %w", err)
	}
	return p, nil
}

func (t *tx) ListPayments(ctx context.Context) ([]payment.Payment, error) {
	if err := t.inShape("payments"); err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(ctx,
		"SELECT "+t.paymentColumns()+" FROM payments ORDER BY created_at ASC NULLS FIRST, seq")
	if err != nil {
		return nil, fmt.Errorf("sqlite: list payments: %w", err)
	}
	defer rows.Close()

	out := make([]payment.Payment, 0)
	for rows.Next() {
		p, err := t.scanPayment(rows)
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

const mandateColumns = `id, debtor_agent, debtor_account, debtor_identifier_scheme, debtor_identifier_value,
	creditor_account, creditor_identifier_scheme, creditor_identifier_value,
	asset, max_amount, status, created_at`

func (t *tx) PutMandate(ctx context.Context, m payment.Mandate) error {
	if err := t.inShape("mandates"); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	// The values and the placeholders are counted from ONE list, which is not
	// tidiness: a hand-written "?,?,?…" here was one short for a whole task and
	// nothing noticed, because the case that would have caught it wrote a mandate
	// through a path that never reached this statement. See paymentSharedColumns.
	vals := []any{
		string(m.ID),
		string(m.DebtorAgent), string(m.Debtor.Account), string(m.Debtor.Identifier.Scheme), m.Debtor.Identifier.Value,
		string(m.Creditor.Account), string(m.Creditor.Identifier.Scheme), m.Creditor.Identifier.Value,
		string(m.Asset), int64(m.MaxAmount), int64(m.Status), nullTime{m.CreatedAt},
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO mandates (`+mandateColumns+`, seq)
		VALUES (`+placeholders(len(vals))+`, `+nextRowSeq("mandates")+`)
		ON CONFLICT (id) DO UPDATE SET `+upsertSet(mandateColumns, "id"), vals...)
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
		&m.DebtorAgent, &m.Debtor.Account, &m.Debtor.Identifier.Scheme, &m.Debtor.Identifier.Value,
		&m.Creditor.Account, &m.Creditor.Identifier.Scheme, &m.Creditor.Identifier.Value,
		&m.Asset, &m.MaxAmount, &status, &createdAt)
	if err != nil {
		return payment.Mandate{}, err
	}
	m.Status = payment.MandateStatus(status)
	m.CreatedAt = createdAt.Time
	return m, nil
}

func (t *tx) GetMandate(ctx context.Context, id payment.MandateID) (payment.Mandate, error) {
	if err := t.inShape("mandates"); err != nil {
		return payment.Mandate{}, err
	}
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
	if err := t.inShape("mandates"); err != nil {
		return nil, err
	}
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

const cycleColumns = `c.id, c.scheme, c.status, c.net_positions, c.opened_at, c.closed_at`

func (t *tx) PutCycle(ctx context.Context, c payment.ClearingCycle) error {
	if err := t.inShape("cycles"); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	positions, err := marshalPositions(c.NetPositions)
	if err != nil {
		return fmt.Errorf("sqlite: put cycle %s: %w", c.ID, err)
	}
	_, err = t.tx.ExecContext(ctx, `
		INSERT INTO cycles (id, scheme, status, net_positions, opened_at, closed_at, seq)
		VALUES (?, ?, ?, ?, ?, ?, `+nextRowSeq("cycles")+`)
		ON CONFLICT (id) DO UPDATE SET
			scheme        = EXCLUDED.scheme,
			status        = EXCLUDED.status,
			net_positions = EXCLUDED.net_positions,
			opened_at     = EXCLUDED.opened_at,
			closed_at     = EXCLUDED.closed_at`,
		string(c.ID), string(c.Scheme), int64(c.Status), positions,
		nullTime{c.OpenedAt}, nullTime{c.ClosedAt})
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
	if err := t.inShape("cycles"); err != nil {
		return payment.ClearingCycle{}, err
	}
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
// The scheme and the status are each bound twice rather than once: a positional
// placeholder cannot be reused, so the argument is passed again for the
// subquery.
func (t *tx) GetOpenCycle(ctx context.Context, scheme payment.SchemeID) (payment.ClearingCycle, error) {
	if err := t.inShape("cycles"); err != nil {
		return payment.ClearingCycle{}, err
	}
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
	if err := t.inShape("cycles"); err != nil {
		return nil, err
	}
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
		if err := rows.Scan(&c.ID, &c.Scheme, &status, &positions, &opened, &closed, &paymentID); err != nil {
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
// What the clearing house is holding and has not handed over
// ---------------------------------------------------------------------------

// AddHeldFile appends one receiving bank's share of an uploaded file.
//
// It APPENDS where every other write in this file upserts, and the seq is why:
// a share has no key its caller knows, so the number is allocated here and the
// child rows are keyed by it. Allocating it in a SELECT rather than inside the
// INSERT's VALUES is what lets the same number reach the transactions — a
// subquery evaluated by the INSERT is not a value this side can read back
// without RETURNING.
//
// Both statements are the caller's unit of work, so a share and its positions
// commit together or not at all. That is what makes the foreign key on
// held_file_transactions writable: no caller can produce an orphan.
func (t *tx) AddHeldFile(ctx context.Context, f payment.HeldFile) error {
	if err := t.inShape("held_files"); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	var seq int64
	if err := t.tx.QueryRowContext(ctx, "SELECT "+nextRowSeq("held_files")).Scan(&seq); err != nil {
		return fmt.Errorf("sqlite: hold a file for %s in %s: %w", f.Destination, f.CycleID, err)
	}
	if _, err := t.tx.ExecContext(ctx,
		"INSERT INTO held_files (cycle_id, seq, destination, file) VALUES (?, ?, ?, ?)",
		string(f.CycleID), seq, string(f.Destination), f.File); err != nil {
		return fmt.Errorf("sqlite: hold a file for %s in %s: %w", f.Destination, f.CycleID, err)
	}
	for _, h := range f.Transactions {
		if _, err := t.tx.ExecContext(ctx,
			"INSERT INTO held_file_transactions (cycle_id, seq, position, payment_id) VALUES (?, ?, ?, ?)",
			string(f.CycleID), seq, int64(h.Position), string(h.PaymentID)); err != nil {
			return fmt.Errorf("sqlite: hold %s in the file for %s in %s: %w",
				h.PaymentID, f.Destination, f.CycleID, err)
		}
	}
	return nil
}

// ListHeldFiles reads one cut-off's shares in build order.
//
// TWO queries rather than a join, which is the opposite of queryCycles above and
// deliberate: the file column is a whole uploaded document, and a join would
// repeat it once per transaction in the share. The two statements are inside one
// transaction, so they see the same rows.
func (t *tx) ListHeldFiles(ctx context.Context, id payment.CycleID) ([]payment.HeldFile, error) {
	if err := t.inShape("held_files"); err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(ctx,
		"SELECT seq, destination, file FROM held_files WHERE cycle_id = ? ORDER BY seq", string(id))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list the files held for %s: %w", id, err)
	}
	defer rows.Close()

	out := make([]payment.HeldFile, 0)
	index := make(map[int64]int)
	for rows.Next() {
		f := payment.HeldFile{CycleID: id}
		if err := rows.Scan(&f.Seq, &f.Destination, &f.File); err != nil {
			return nil, fmt.Errorf("sqlite: list the files held for %s: %w", id, err)
		}
		index[f.Seq] = len(out)
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: list the files held for %s: %w", id, err)
	}

	txs, err := t.tx.QueryContext(ctx,
		"SELECT seq, position, payment_id FROM held_file_transactions WHERE cycle_id = ? ORDER BY seq, position",
		string(id))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list the transactions held for %s: %w", id, err)
	}
	defer txs.Close()

	for txs.Next() {
		var (
			seq int64
			h   payment.HeldTransaction
		)
		if err := txs.Scan(&seq, &h.Position, &h.PaymentID); err != nil {
			return nil, fmt.Errorf("sqlite: list the transactions held for %s: %w", id, err)
		}
		at, ok := index[seq]
		if !ok {
			// Unreachable while the foreign key holds, and reported rather than
			// skipped if it ever does not: a position with no share is a
			// transaction some bank is owed and nothing would hand over.
			return nil, fmt.Errorf("sqlite: %s is held in file %d of %s, which is not there", h.PaymentID, seq, id)
		}
		out[at].Transactions = append(out[at].Transactions, h)
	}
	return out, txs.Err()
}

// DeleteHeldFile discards one share of a cut-off. The positions go with it on
// the cascade.
func (t *tx) DeleteHeldFile(ctx context.Context, id payment.CycleID, seq int64) error {
	if err := t.inShape("held_files"); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx,
		"DELETE FROM held_files WHERE cycle_id = ? AND seq = ?", string(id), seq); err != nil {
		return fmt.Errorf("sqlite: release the file held as %d of %s: %w", seq, id, err)
	}
	return nil
}

func (t *tx) PutHeldReturn(ctx context.Context, r payment.HeldReturn) error {
	if err := t.inShape("held_returns"); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO held_returns (payment_id, returned_by, file)
		VALUES (?, ?, ?)
		ON CONFLICT (payment_id) DO UPDATE SET
			returned_by = EXCLUDED.returned_by,
			file        = EXCLUDED.file`,
		string(r.PaymentID), string(r.ReturnedBy), r.File)
	if err != nil {
		return fmt.Errorf("sqlite: hold the return of %s: %w", r.PaymentID, err)
	}
	return nil
}

func (t *tx) GetHeldReturn(ctx context.Context, id payment.PaymentID) (payment.HeldReturn, error) {
	if err := t.inShape("held_returns"); err != nil {
		return payment.HeldReturn{}, err
	}
	out := payment.HeldReturn{PaymentID: id}
	err := t.tx.QueryRowContext(ctx,
		"SELECT returned_by, file FROM held_returns WHERE payment_id = ?", string(id)).
		Scan(&out.ReturnedBy, &out.File)
	if errors.Is(err, sql.ErrNoRows) {
		return payment.HeldReturn{}, payment.ErrHeldReturnNotFound
	}
	if err != nil {
		return payment.HeldReturn{}, fmt.Errorf("sqlite: read the return held for %s: %w", id, err)
	}
	return out, nil
}

func (t *tx) DeleteHeldReturn(ctx context.Context, id payment.PaymentID) error {
	if err := t.inShape("held_returns"); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx, "DELETE FROM held_returns WHERE payment_id = ?", string(id)); err != nil {
		return fmt.Errorf("sqlite: drop the return held for %s: %w", id, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Settlements
// ---------------------------------------------------------------------------

func (t *tx) PutSettlement(ctx context.Context, s payment.Settlement) error {
	if err := t.inShape("settlements"); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO settlements (id, cycle_id, asset, settlement_tx, value_date, settled_at, seq)
		VALUES (?, ?, ?, ?, ?, ?, `+nextRowSeq("settlements")+`)
		ON CONFLICT (id) DO UPDATE SET
			cycle_id      = EXCLUDED.cycle_id,
			asset         = EXCLUDED.asset,
			settlement_tx = EXCLUDED.settlement_tx,
			value_date    = EXCLUDED.value_date,
			settled_at    = EXCLUDED.settled_at`,
		string(s.ID), string(s.CycleID), string(s.Asset), string(s.SettlementTx),
		nullTime{s.ValueDate}, nullTime{s.SettledAt})
	if err != nil {
		return fmt.Errorf("sqlite: put settlement %s: %w", s.ID, err)
	}

	if _, err := t.tx.ExecContext(ctx, "DELETE FROM settlement_positions WHERE settlement_id = ?", string(s.ID)); err != nil {
		return fmt.Errorf("sqlite: put settlement %s: %w", s.ID, err)
	}
	for participant, amount := range s.NetPositions {
		if _, err := t.tx.ExecContext(ctx,
			"INSERT INTO settlement_positions (settlement_id, bic, amount) VALUES (?, ?, ?)",
			string(s.ID), string(participant), int64(amount)); err != nil {
			return fmt.Errorf("sqlite: put settlement %s position %s: %w", s.ID, participant, err)
		}
	}
	return nil
}

func (t *tx) GetSettlement(ctx context.Context, id payment.SettlementID) (payment.Settlement, error) {
	if err := t.inShape("settlements"); err != nil {
		return payment.Settlement{}, err
	}
	out, err := t.querySettlements(ctx, "WHERE s.id = ?", "", string(id))
	if err != nil {
		return payment.Settlement{}, err
	}
	if len(out) == 0 {
		return payment.Settlement{}, payment.ErrSettlementNotFound
	}
	return out[0], nil
}

// GetSettlementByCycle answers "have I already discharged this cut-off". There
// is no unique index behind it — one cycle settles once because SettleCycleTx
// refuses a second, and adding one would be a second answer to that question by
// a layer that cannot say why. It orders so that a database which somehow held
// two would answer with the FIRST rather than with whichever the planner
// returned, because the first is the one whose posting stands.
func (t *tx) GetSettlementByCycle(ctx context.Context, id payment.CycleID) (payment.Settlement, error) {
	if err := t.inShape("settlements"); err != nil {
		return payment.Settlement{}, err
	}
	out, err := t.querySettlements(ctx, "WHERE s.cycle_id = ?", "s.seq", string(id))
	if err != nil {
		return payment.Settlement{}, err
	}
	if len(out) == 0 {
		return payment.Settlement{}, payment.ErrSettlementNotFound
	}
	return out[0], nil
}

func (t *tx) ListSettlements(ctx context.Context) ([]payment.Settlement, error) {
	if err := t.inShape("settlements"); err != nil {
		return nil, err
	}
	return t.querySettlements(ctx, "", "s.settled_at ASC NULLS FIRST, s.seq")
}

// querySettlements reads settlements joined to their net positions.
//
// The positions map is always non-nil for a settlement that exists: a row set
// cannot tell an empty map from an absent one, and every settlement the domain
// writes carries positions.
func (t *tx) querySettlements(ctx context.Context, where, order string, args ...any) ([]payment.Settlement, error) {
	query := "SELECT s.id, s.cycle_id, s.asset, s.settlement_tx, s.value_date, s.settled_at, sp.bic, sp.amount " +
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
			s         payment.Settlement
			value, at nullTime
			bic       sql.NullString
			amount    sql.NullInt64
		)
		if err := rows.Scan(&s.ID, &s.CycleID, &s.Asset, &s.SettlementTx, &value, &at, &bic, &amount); err != nil {
			return nil, fmt.Errorf("sqlite: query settlements: %w", err)
		}
		pos, seen := index[s.ID]
		if !seen {
			s.ValueDate = value.Time
			s.SettledAt = at.Time
			s.NetPositions = make(map[iso20022.BIC]ledger.Amount)
			pos = len(out)
			index[s.ID] = pos
			out = append(out, s)
		}
		if bic.Valid {
			out[pos].NetPositions[iso20022.BIC(bic.String)] = ledger.Amount(amount.Int64)
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
	if err := t.inShape("settlement_advices"); err != nil {
		return err
	}
	if err := t.own(book); err != nil {
		return err
	}
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
	if err := t.inShape("settlement_advices"); err != nil {
		return payment.SettlementAdvice{}, err
	}
	if err := t.own(book); err != nil {
		return payment.SettlementAdvice{}, err
	}
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
	if err := t.inShape("settlement_advices"); err != nil {
		return nil, err
	}
	if err := t.own(book); err != nil {
		return nil, err
	}
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
func marshalPositions(m map[iso20022.BIC]ledger.Amount) (any, error) {
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

func unmarshalPositions(raw []byte) (map[iso20022.BIC]ledger.Amount, error) {
	if raw == nil {
		return nil, nil
	}
	flat := make(map[string]int64)
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, err
	}
	out := make(map[iso20022.BIC]ledger.Amount, len(flat))
	for k, v := range flat {
		out[iso20022.BIC(k)] = ledger.Amount(v)
	}
	return out, nil
}
