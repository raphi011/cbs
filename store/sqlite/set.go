package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// Set is one process's handle on every database in the system: one per member
// bank, the clearing house's, and the central bank's. It is the implementation
// of payment.Stores.
//
// # One database per institution, and the directory is the system
//
// A bank's database is named by that bank's BIC — the file is AURODEFFXXX.db,
// the banks row's id is AURODEFFXXX, the BookID is AURODEFFXXX — so the set of
// banks IS the set of files, and it needs no counter, no registry and nothing to
// reconcile at startup. That agreement is by construction rather than by
// discipline: nothing allocates a bank's identifier, so nothing can allocate one
// twice or lose one. See payment.AsBank for the ruling and payment.Stores for
// what the set is for.
//
// The two institutions are files with fixed names, because there is exactly one
// of each and each exists before any bank does. Their names cannot collide with
// a bank's: a BIC is eight or eleven upper-case letters and digits, and neither
// "clearing-house" nor "central-bank" is one.
//
// # Every database is opened, and opening one is what makes it exist
//
// With a directory, OpenSet opens every *.db in it, so a restart finds the banks
// the last process left and Reset can reach all of them. Bank opens one that is
// not there yet, which is what founding a bank does — see payment.Stores on why
// asking for a bank's database cannot be gated on membership.
//
// With no directory every database is ephemeral and in-memory, each with a
// random name of its own, so N+2 stores in one test see exactly as little of
// each other as N+2 processes would. That is what makes the split measurable
// rather than merely intended.
//
// # Handles are cached, and they have to be
//
// Two asks for one bank must answer with stores that see the same rows. On a
// file that is merely wasteful to get wrong; on an ephemeral database it is
// fatal, because a second Open would mint a second random name and the bank
// would forget everything it had written. So the map is the point of this type,
// and the mutex is what makes it safe for the listeners, which serve requests on
// whichever goroutine net/http hands them and open banks on demand.
type Set struct {
	// dir is where the databases live, or "" for an ephemeral set.
	dir   string
	clock func() time.Time

	// csm and cb are opened by OpenSet and never change: there is one of each
	// and it exists before the first bank does.
	csm *ClearingHouseStore
	cb  *CentralBankStore

	mu     sync.Mutex
	banks  map[iso20022.BIC]*BankStore
	closed bool
}

// compile-time check that the set is the store set the domain declares.
var _ payment.Stores = (*Set)(nil)

// The two institutions' file names. See Set.
const (
	clearingHouseFile = "clearing-house.db"
	centralBankFile   = "central-bank.db"
)

// dbExt is the suffix a database file carries, and the thing OpenSet recognises
// a bank by.
const dbExt = ".db"

// OpenSet opens the set of databases under dir, creating the directory and the
// two institutions' databases if they are not there.
//
// An empty dir means every database in the set is ephemeral and in-memory, which
// is what a test suite wants and what `cmd/server` with no -database means. It
// is not one shared in-memory database: each institution gets its own, for the
// reason on Set.
//
// A file-backed set opens every bank it finds, so that a second process picks up
// exactly the banks the first one left and Reset can empty all of them. A bank
// whose database is corrupt or whose migrations fail therefore stops startup,
// which is the right outcome: a member the roster names and this process cannot
// open is a bank that would answer nothing and say nothing about why.
func OpenSet(ctx context.Context, dir string, clock func() time.Time) (*Set, error) {
	s := &Set{dir: dir, clock: clock, banks: map[iso20022.BIC]*BankStore{}}

	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("sqlite: store directory %s: %w", dir, err)
		}
	}

	var err error
	if s.csm, err = OpenClearingHouse(ctx, s.path(clearingHouseFile), clock); err != nil {
		return nil, err
	}
	if s.cb, err = OpenCentralBank(ctx, s.path(centralBankFile), clock); err != nil {
		_ = s.Close()
		return nil, err
	}

	bics, err := s.existingBanks()
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	for _, bic := range bics {
		if _, err := s.bank(ctx, bic); err != nil {
			_ = s.Close()
			return nil, err
		}
	}
	return s, nil
}

// path is the file a named database lives in, or "" when the set is ephemeral —
// which is what Open reads as "in memory, under a name of your own".
func (s *Set) path(name string) string {
	if s.dir == "" {
		return ""
	}
	return filepath.Join(s.dir, name)
}

// existingBanks is every bank database already in the directory: the set of
// banks, read off the file system, which is the only place it is written down.
func (s *Set) existingBanks() ([]iso20022.BIC, error) {
	if s.dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("sqlite: reading store directory %s: %w", s.dir, err)
	}
	var bics []iso20022.BIC
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, dbExt) {
			continue
		}
		if name == clearingHouseFile || name == centralBankFile {
			continue
		}
		bic := iso20022.BIC(strings.TrimSuffix(name, dbExt))
		// A file that is not a BIC is not a bank. It is left alone rather than
		// refused: WAL and shared-memory sidecars sit beside every database this
		// package opens, and a directory a human has put something else in is
		// not a reason to refuse to start.
		if bic.Validate() != nil {
			continue
		}
		bics = append(bics, bic)
	}
	return bics, nil
}

// Bank is payment.Stores.Bank: one member bank's database, opened on the first
// ask.
func (s *Set) Bank(ctx context.Context, bic iso20022.BIC) (payment.BankStore, error) {
	return s.bank(ctx, bic)
}

// Banks is every bank this set holds a database for, ascending by address.
//
// The candidates come from the open handles rather than from the directory, and
// the two agree by construction: OpenSet opens every database it finds and Bank
// adds the one it creates. Reading the directory again would answer about a
// moment this process has already moved past — a bank founded a millisecond ago
// is in the map and may not have been flushed anywhere a listing would see.
//
// # A database is not a bank until something founds one in it
//
// So each candidate is ASKED, and this is the one method on the set that reads a
// row. The set of banks is the set of databases, with one exception this process
// produces itself: Reset empties every database and deletes none, so a reset
// system holds N bank databases with no bank in any of them.
//
// Leaving them in the answer is not a small inaccuracy. api's GET /members reads
// each listed bank's own row and got ErrParticipantNotFound; cmd/server's plan
// would bind a listener per phantom, and fail startup naming a bank nobody has
// heard of. Both are the same wrong claim — that a file is a bank — and
// TestAReadmittedBankCanBePaidThroughAfterAReset asserts the right one: a
// reset with no reseed leaves no members.
//
// The alternative was for Reset to CLOSE and delete what it empties, and it was
// rejected because a running process cannot survive it. cmd/server binds each
// bank's surface once, at startup, over that bank's store handle; a reset that
// replaced the handle would leave every bank listener serving a database nobody
// writes to any more, and the reseed founds the same addresses straight back into
// new files. Truncating in place keeps the handle valid, which is why a reseeded
// system is still servable on the ports it started with.
//
// It costs one keyed read per open bank per call, and there are four callers:
// cmd/server's boot plan, the seed's idempotency probe, api's GET /members, and
// payment's audit fixture. None of them is on a payment's path.
func (s *Set) Banks(ctx context.Context) ([]iso20022.BIC, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("sqlite: the store set is closed")
	}
	candidates := make(map[iso20022.BIC]*BankStore, len(s.banks))
	for bic, st := range s.banks {
		candidates[bic] = st
	}
	s.mu.Unlock()

	out := make([]iso20022.BIC, 0, len(candidates))
	for bic, st := range candidates {
		founded, err := st.holdsABank(ctx, bic)
		if err != nil {
			return nil, err
		}
		if founded {
			out = append(out, bic)
		}
	}
	slices.Sort(out)
	return out, nil
}

// holdsABank is whether this database has had a bank founded in it — the read
// Banks makes of each of its candidates.
//
// It asks for the row keyed by the address the file is named after rather than
// listing the table, because a bank's database holds exactly its own row and a
// listing would be the same read with a slice around it.
func (s *BankStore) holdsABank(ctx context.Context, bic iso20022.BIC) (bool, error) {
	var found bool
	err := s.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
		_, err := tx.GetBank(ctx, payment.ParticipantID(bic))
		switch {
		case errors.Is(err, payment.ErrParticipantNotFound):
			return nil
		case err != nil:
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("sqlite: asking %s's database whether a bank was founded in it: %w", bic, err)
	}
	return found, nil
}

// bank is the cached open. It validates the BIC first, because the value names a
// FILE here: an unvalidated one is a path this set would create on somebody
// else's behalf, and "which banks exist" is a question answered by the contents
// of a directory.
//
// A bank IS its book and that id is its BIC, so the file name and the BookID are
// the same string. See payment.AsBank for the ruling.
func (s *Set) bank(ctx context.Context, bic iso20022.BIC) (*BankStore, error) {
	if err := bic.Validate(); err != nil {
		return nil, fmt.Errorf("sqlite: no database for %q: %w", bic, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("sqlite: the store set is closed")
	}
	if st, ok := s.banks[bic]; ok {
		return st, nil
	}
	st, err := OpenBank(ctx, ledger.BookID(bic), s.path(string(bic)+dbExt), s.clock)
	if err != nil {
		return nil, err
	}
	s.banks[bic] = st
	return st, nil
}

// ClearingHouse and CentralBank are the two institutions' databases. Neither
// takes a context or returns an error, because both were opened by OpenSet and
// there is exactly one of each. See payment.Stores.
func (s *Set) ClearingHouse() payment.ClearingHouseStore { return s.csm }
func (s *Set) CentralBank() payment.CentralBankStore     { return s.cb }

// ClearingHouseEBICS and CentralBankEBICS are the two hosts' transport state:
// the download queues and the order log of each institution that is DIALLED.
//
// There is no bank equivalent and there cannot be one: EBICS is on the two store
// types that host a queue and on no third, so a member bank's store has no such
// method to call.
//
// They are on the set rather than on payment.Stores because the domain must not
// name the transport: a payment.Network that could reach a queue would be an
// institution able to read a file it is only carrying. The composition root
// holds both and is where the two meet.
func (s *Set) ClearingHouseEBICS() ebics.Store { return s.csm.EBICS() }
func (s *Set) CentralBankEBICS() ebics.Store   { return s.cb.EBICS() }

// Reset empties every database in the set.
//
// It empties them one at a time and in no particular order, and there is nothing
// to be gained by doing better: these are separate databases, so there is no
// transaction that could span them and no ordering in which a half-reset system
// is consistent. That is the same fact the business day models everywhere else —
// an interval between two institutions' units of work — and the reset route
// holds its own lock over the whole thing.
//
// The first failure stops it and is returned. A partially emptied system is not
// a state anything recovers from, and pressing on would only make it larger.
func (s *Set) Reset(ctx context.Context) error {
	for _, st := range s.all() {
		if err := st.Reset(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Close closes every database in the set, joining whatever they answer.
//
// It is idempotent, for Store.Close's reason, and it closes the banks it opened
// even when one of them fails: a leaked connection to a database whose close
// errored is still a leaked connection, and on an ephemeral set it is the thing
// keeping a dead database alive.
func (s *Set) Close() error {
	s.mu.Lock()
	s.closed = true
	banks := make([]*BankStore, 0, len(s.banks))
	for _, st := range s.banks {
		banks = append(banks, st)
	}
	s.banks = map[iso20022.BIC]*BankStore{}
	s.mu.Unlock()

	var errs []error
	for _, st := range banks {
		errs = append(errs, st.Close())
	}
	if s.csm != nil {
		errs = append(errs, s.csm.Close())
	}
	if s.cb != nil {
		errs = append(errs, s.cb.Close())
	}
	return errors.Join(errs...)
}

// all is every store currently open, the two institutions first. It takes the
// lock, so a caller iterating it is not holding one while it does I/O.
func (s *Set) all() []*store {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*store, 0, len(s.banks)+2)
	out = append(out, s.csm.store, s.cb.store)
	for _, st := range s.banks {
		out = append(out, st.store)
	}
	return out
}
