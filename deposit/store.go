package deposit

import (
	"context"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/product"
)

// Store owns the deposit layer's persistent state. Like ledger.Store it is
// declared here, by the consumer, and implemented by store/sqlite — so the
// store package imports the domain packages and never the reverse.
type Store interface {
	Update(ctx context.Context, fn func(context.Context, Tx) error) error
	View(ctx context.Context, fn func(context.Context, Tx) error) error
	Reset(ctx context.Context) error
	Close() error
}

// Tx embeds product.Tx — and, through it, ledger.Tx — so one concrete
// transaction spans all three layers. That is what makes CaptureHold (a hold
// write plus a GL posting) a single unit of work, and what lets OpenAccount
// validate the product an account is being opened from and write the account's
// first terms row without leaving it.
type Tx interface {
	product.Tx

	// The slot mapping, which the ledger keeps beside its own tables because
	// only a bank has one. Register opens an account's lines through it — see
	// Register.principalPosition — so a deposit unit of work has to carry it.
	ledger.SlotTx

	// Named with a Deposit prefix because ledger.Tx, embedded above, already has
	// PutAccount/GetAccount/ListAccounts for GL accounts. Go rejects an interface
	// carrying two methods of the same name and different signatures, so the
	// ledger keeps the bare names and deposit prefixes.
	PutDepositAccount(ctx context.Context, book ledger.BookID, a Account) error
	GetDepositAccount(ctx context.Context, book ledger.BookID, id AccountID) (Account, error)
	ListDepositAccounts(ctx context.Context, book ledger.BookID) ([]Account, error)

	// ListDepositAccountsByIdentifier returns every account in the book holding
	// this (scheme, value) pair, compared with Identifier.Matches — the scheme
	// exactly, the value under that scheme's own rule.
	//
	// It returns a slice rather than one account and a not-found sentinel
	// because "an address resolves to exactly one account" is a DOMAIN rule and
	// this layer holds none — the same division that keeps parent-existence out
	// of the schema. Register.ResolveIdentifier turns zero, one and more than
	// one into ErrIdentifierNotFound, the account, and ErrIdentifierAmbiguous.
	ListDepositAccountsByIdentifier(ctx context.Context, book ledger.BookID, ident Identifier) ([]Account, error)

	// NextAddressSerial issues the next account serial this book mints an IBAN
	// from: 1, 2, 3, …, dense and gap-free.
	//
	// A counter OF ITS OWN, and that is the whole reason it is a method rather
	// than a NextID call. NextID shares one counter per book across every prefix,
	// so an address drawn from it would carry whatever number the book happened
	// to be on — and the gaps between one account and the next would be a record
	// of unrelated work. See Register.mintAddressTx.
	//
	// Allocated in the caller's transaction and rolled back with it, like every
	// other counter here. A store implementing this outside the unit of work
	// would burn an address on a failed open.
	NextAddressSerial(ctx context.Context, book ledger.BookID) (uint64, error)

	PutHold(ctx context.Context, book ledger.BookID, h Hold) error
	GetHold(ctx context.Context, book ledger.BookID, id HoldID) (Hold, error)
	ListHoldsForAccount(ctx context.Context, book ledger.BookID, id AccountID) ([]Hold, error)
	// ActiveHoldTotal sums active, non-expired holds as at `now`.
	ActiveHoldTotal(ctx context.Context, book ledger.BookID, id AccountID, now time.Time) (ledger.Amount, error)

	PutSnapshot(ctx context.Context, book ledger.BookID, s Snapshot) error
	GetSnapshot(ctx context.Context, book ledger.BookID, id AccountID, dateKey string) (Snapshot, error)
	// ListSnapshotsForAccount returns an account's snapshots ordered by business
	// date. Not in the brief's set, but Register.ListSnapshots — a public method
	// that predates the store — has no other way to enumerate them.
	ListSnapshotsForAccount(ctx context.Context, book ledger.BookID, id AccountID) ([]Snapshot, error)

	// The account's effective-dated terms timeline. Three methods rather than
	// two, because the two callers want different things: accrual wants the
	// whole timeline in one read and resolves per day in Go, while balanceTx
	// wants exactly one row — the limit in force now — and should not pay for
	// history on a path that runs on every withdrawal check. A bounded lookup
	// as a store method is already this repo's idiom; ActiveHoldTotal and
	// ValueDateBalance are both aggregates with bounds, for the same reason.
	PutOverdraftTerms(ctx context.Context, book ledger.BookID, t OverdraftTerms) error
	ListOverdraftTermsForAccount(ctx context.Context, book ledger.BookID, id AccountID) ([]OverdraftTerms, error)
	GetOverdraftTermsAsOf(ctx context.Context, book ledger.BookID, id AccountID, day time.Time) (OverdraftTerms, error)
}

// SnapshotDateKey is the business-date key a snapshot is stored under. A
// snapshot is identified by (account, date) at day granularity, so taking a
// second snapshot for the same business date overwrites the first.
//
// A store must derive the key of a snapshot passed to PutSnapshot with this
// function, so that GetSnapshot finds it: it is the value of the
// snapshots.date_key column, and the column the lookup compares on.
func SnapshotDateKey(date time.Time) string { return date.Format("2006-01-02") }

// Contract notes for implementers — storetest.RunDeposit pins all of these:
//
//   - GetDepositAccount returns ErrAccountNotFound, GetHold returns
//     ErrHoldNotFound and GetSnapshot returns ErrSnapshotNotFound when the row
//     is missing. Wrapping is fine; errors.Is is what the suite checks.
//   - Every method is scoped by book: the same AccountID in two books is two
//     different accounts, exactly as in ledger.Tx.
//   - ListDepositAccounts, ListHoldsForAccount: CreatedAt ascending, ties broken
//     by the row's monotonic insertion sequence — ORDER BY created_at, seq, the
//     same rule every ledger listing follows. Never break ties on the ID: IDs are
//     counter-derived strings, so "dep_10" sorts before "dep_8" and the list
//     reorders itself the moment a counter crosses a power of ten.
//     ListSnapshotsForAccount orders by business date ascending.
//   - ActiveHoldTotal counts only holds whose Status is HoldActive and whose
//     ExpiresAt is either zero (never expires) or not before now. It is an
//     aggregate like BookBalance: an unknown account is 0, not an error.
//   - PutHold and PutDepositAccount are upserts keyed by ID; PutSnapshot is an
//     upsert keyed by (account, SnapshotDateKey(s.Date)).
//   - Writes roll back with the surrounding Update, deposit rows and ledger rows
//     together — that is the whole point of Tx embedding ledger.Tx.
//   - PutOverdraftTerms is an upsert keyed by (account, TermsDayKey(t.EffectiveFrom)),
//     the same identity PutSnapshot has. Two rows entered for the same account
//     and the same effective DAY are the same row, and the later one wins —
//     which is what makes "the terms in force on day D" unique by construction
//     rather than by a validation rule.
//   - ListOverdraftTermsForAccount orders by effective day ASCENDING, ties
//     broken by the row's insertion sequence for form. Ascending is not a
//     convenience: deposit.termsAt binary-searches the slice it is handed.
//   - GetOverdraftTermsAsOf returns the row with the greatest effective day not
//     after `day`, or ErrTermsNotFound when the day precedes the account's
//     first row. It is book- and account-scoped like everything else here, and
//     unlike ActiveHoldTotal it is NOT an aggregate: an unknown account has no
//     terms, which is ErrTermsNotFound rather than a zero row that would read
//     as a real interest-free product.
//   - The store truncates nothing. Callers pass an already-DayStart-ed instant
//     and the store keys on deposit.TermsDayKey of it.
//   - PutDepositAccount writes Account.Identifiers as part of the aggregate and
//     REPLACES the stored set; both readers bring it back. Identifiers are not
//     separately writable, which is the condition under which the schema is
//     allowed a real FOREIGN KEY on them — see the exemption stated on
//     subledgers in store/sqlite/schema/bank/0001_init.sql.
//   - ListDepositAccountsByIdentifier matches with Identifier.Matches — the
//     scheme exactly, and the value under that scheme's comparison rule, which
//     for an IBAN means separators stripped and case folded on BOTH sides. A
//     store that compared raw values would leave an account stored as
//     DE20999000010000000001 unreachable from a customer who typed it
//     DE20 9990 0001 0000 0000 01, the way their statement prints it.
//     (ListDepositAccountsByIdentifierMatchesAnIBANThroughItsSeparators.) It is
//     book-scoped like everything else here, orders created_at then seq, and
//     returns an empty slice — never a sentinel — when nothing matches. It must
//     NOT enforce uniqueness: storetest/IdentifierUniquenessIsNotEnforced pins
//     that two accounts in one book may hold the same identifier, because the
//     rule against it lives in deposit.Register and a constraint here would fire
//     on a race the register lets through, as a constraint violation rather than
//     as ErrIdentifierTaken.
