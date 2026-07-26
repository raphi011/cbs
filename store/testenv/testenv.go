// Package testenv picks the store every test suite in this repository runs
// against.
//
// It defaults to store/mem, so `go test ./...` needs no setup at all. Setting
// TEST_DATABASE_URL switches the same suites onto store/pg, which is the only
// way the cross-layer atomicity tests mean anything: under a single process-wide
// mutex, "these writes commit or roll back together" is true whether or not the
// code arranged for it.
//
// It is an ordinary package rather than a set of _test.go files because the
// domain suites (ledger, deposit, payment, api, seed) all import it, and a
// _test.go file in store/pg would be unimportable.
package testenv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/mem"
	"github.com/raphi011/cbs/store/pg"
)

// Store is the shape both implementations share: a ledger.Store that can also
// present itself as a deposit.Store and a payment.Store over the same state and
// the same unit of work. Go allows a type one Update method, which is why the
// two narrower views are adapters rather than more interfaces on this one.
type Store interface {
	ledger.Store
	Deposit() deposit.Store
	Payment() payment.Store
}

// compile-time checks that both implementations really are interchangeable here.
var (
	_ Store = (*mem.Store)(nil)
	_ Store = (*pg.Store)(nil)
)

// DSN returns the Postgres connection string the suites should use, or "" for
// the in-memory store.
func DSN() string { return os.Getenv("TEST_DATABASE_URL") }

// New returns the store the suites run against: store/mem by default, store/pg
// in its own fresh schema when TEST_DATABASE_URL is set.
func New(t *testing.T, clock func() time.Time) Store {
	t.Helper()
	if dsn := DSN(); dsn != "" {
		return OpenInFreshSchema(t, dsn, clock)
	}
	return mem.New(clock)
}

// schemaPrefix is unique per process, so that packages tested in parallel — each
// its own OS process, each with its own counter — cannot land on the same schema
// name, and so that a schema left behind by a crashed run is never reused.
var schemaPrefix = func() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("testenv: " + err.Error())
	}
	return "test_" + hex.EncodeToString(b[:])
}()

var schemaCounter atomic.Int64

// OpenInFreshSchema opens a Postgres store in a schema of its own and migrates
// it, dropping the schema when the test ends.
//
// A schema per test rather than a TRUNCATE between tests: truncation makes every
// test depend on the order tables are emptied in, and it makes tests that share
// a database unable to run in parallel. A private schema has neither problem,
// and it costs one CREATE SCHEMA and one DROP SCHEMA.
func OpenInFreshSchema(t *testing.T, dsn string, clock func() time.Time) *pg.Store {
	t.Helper()
	ctx := context.Background()
	schema := fmt.Sprintf("%s_%d", schemaPrefix, schemaCounter.Add(1))

	store, err := pg.OpenSchema(ctx, dsn, schema, clock)
	if err != nil {
		t.Fatalf("testenv: open %s: %v", schema, err)
	}
	t.Cleanup(func() {
		// Close is idempotent, which matters because storetest closes the store
		// it was handed as well.
		if err := store.Close(); err != nil {
			t.Errorf("testenv: close: %v", err)
		}
		dropSchema(t, dsn, schema)
	})
	return store
}

// dropSchema removes a test schema on a throwaway connection, because the
// store's pool is closed by the time it runs.
func dropSchema(t *testing.T, dsn, schema string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Errorf("testenv: connect to drop %s: %v", schema, err)
		return
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
		t.Errorf("testenv: drop schema %s: %v", schema, err)
	}
}
