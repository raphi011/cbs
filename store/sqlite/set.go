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
		// A file that is not a BIC is not a bank.
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

// bank is the cached open.
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
func (s *Set) ClearingHouseEBICS() ebics.Store { return s.csm.EBICS() }
func (s *Set) CentralBankEBICS() ebics.Store   { return s.cb.EBICS() }

// Reset empties every database in the set.
func (s *Set) Reset(ctx context.Context) error {
	for _, st := range s.all() {
		if err := st.Reset(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Close closes every database in the set, joining whatever they answer.
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
