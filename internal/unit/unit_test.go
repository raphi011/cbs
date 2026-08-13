package unit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/raphi011/cbs/internal/unit"
)

// tx stands in for a store's transaction: what it is does not matter here, only
// that a unit of work hands one to the act and that the act answers with a value.
type tx struct{ open bool }

var errRolledBack = errors.New("rolled back")

// commits runs fn against an open transaction. fails opens none, which is what a
// store does when it cannot begin or when the unit does not commit.
func commits(ctx context.Context, fn func(context.Context, tx) error) error {
	return fn(ctx, tx{open: true})
}

func fails(ctx context.Context, fn func(context.Context, tx) error) error {
	if err := fn(ctx, tx{open: true}); err != nil {
		return err
	}
	return errRolledBack
}

func TestRunAnswersWithTheActsValue(t *testing.T) {
	got, err := unit.Run(context.Background(), commits, func(ctx context.Context, tx tx) (string, error) {
		if !tx.open {
			t.Error("the act was handed a transaction that is not open")
		}
		return "settled", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "settled" {
		t.Errorf("the act's value came back as %q, want %q", got, "settled")
	}
}

// A unit that does not commit kept nothing, so the value it was building is not
// the caller's to see — whether the act failed or the commit did.
func TestAUnitThatDoesNotCommitYieldsTheZeroValue(t *testing.T) {
	t.Run("the act fails", func(t *testing.T) {
		want := errors.New("refused")
		got, err := unit.Run(context.Background(), commits, func(ctx context.Context, tx tx) (*string, error) {
			half := "half-built"
			return &half, want
		})
		if !errors.Is(err, want) {
			t.Fatalf("error: %v, want %v", err, want)
		}
		if got != nil {
			t.Errorf("a failed act handed back %q", *got)
		}
	})

	t.Run("the commit fails", func(t *testing.T) {
		got, err := unit.Run(context.Background(), fails, func(ctx context.Context, tx tx) (string, error) {
			return "settled", nil
		})
		if !errors.Is(err, errRolledBack) {
			t.Fatalf("error: %v, want %v", err, errRolledBack)
		}
		if got != "" {
			t.Errorf("a rolled-back unit handed back %q", got)
		}
	})
}

func TestRun2AnswersWithBothValues(t *testing.T) {
	a, b, err := unit.Run2(context.Background(), commits, func(ctx context.Context, tx tx) (string, int, error) {
		return "eur", 2, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a != "eur" || b != 2 {
		t.Errorf("the act's values came back as %q, %d; want %q, %d", a, b, "eur", 2)
	}

	a, b, err = unit.Run2(context.Background(), fails, func(ctx context.Context, tx tx) (string, int, error) {
		return "eur", 2, nil
	})
	if !errors.Is(err, errRolledBack) {
		t.Fatalf("error: %v, want %v", err, errRolledBack)
	}
	if a != "" || b != 0 {
		t.Errorf("a rolled-back unit handed back %q, %d", a, b)
	}
}
