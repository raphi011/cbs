package product_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
	. "github.com/raphi011/cbs/product"
	"github.com/raphi011/cbs/store/testenv"
)

// mutableClock lets a test move time forward, which the forward-only
// publication rule makes necessary: "before today" is a question about now.
type mutableClock struct{ at time.Time }

func (c *mutableClock) now() time.Time  { return c.at }
func (c *mutableClock) set(t time.Time) { c.at = t }

func newTestCatalogue(t *testing.T, clock func() time.Time) *Catalogue {
	t.Helper()
	s := testenv.New(t, clock)
	book := ledger.NewBook(s.Ledger(), "bank", clock)
	return NewCatalogue(s.Product(), book, "bank", clock)
}

func TestCreateDraftPublish(t *testing.T) {
	ctx := context.Background()
	clock := &mutableClock{at: day(10)}
	c := newTestCatalogue(t, clock.now)

	p, err := c.CreateProduct(ctx, "Basic Current Account", CurrentAccount)
	if err != nil {
		t.Fatalf("CreateProduct: %v", err)
	}
	if p.ID == "" {
		t.Fatal("CreateProduct returned no ID")
	}
	if p.Retired {
		t.Error("a new product is retired")
	}

	// A draft prices nothing: it is invisible to resolution until published.
	drafted, err := c.DraftVersion(ctx, p.ID, day(20), OverdraftPricing{Rate: 120_000, DayCount: 0})
	if err != nil {
		t.Fatalf("DraftVersion: %v", err)
	}
	if drafted.Published() {
		t.Error("a draft is published")
	}
	if drafted.Hash != "" {
		t.Error("a draft carries a hash; the hash is computed at publication")
	}
	if _, err := c.VersionInForce(ctx, p.ID, day(25)); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("a draft priced a day: %v", err)
	}

	// A draft may be edited — that is what it is for.
	if _, err := c.DraftVersion(ctx, p.ID, day(20), OverdraftPricing{Rate: 149_000}); err != nil {
		t.Fatalf("redrafting the same day: %v", err)
	}

	pub, err := c.PublishVersion(ctx, p.ID, day(20))
	if err != nil {
		t.Fatalf("PublishVersion: %v", err)
	}
	if !pub.Published() {
		t.Error("PublishVersion returned an unpublished row")
	}
	if pub.Hash != pub.ComputeHash() {
		t.Error("the published hash does not verify")
	}
	if int64(pub.Overdraft.Rate) != 149_000 {
		t.Errorf("published rate = %d, want the redrafted 149000", pub.Overdraft.Rate)
	}

	// And a published version is frozen.
	if _, err := c.DraftVersion(ctx, p.ID, day(20), OverdraftPricing{Rate: 1}); !errors.Is(err, ErrVersionPublished) {
		t.Errorf("editing a published version: %v, want ErrVersionPublished", err)
	}
	if _, err := c.PublishVersion(ctx, p.ID, day(20)); !errors.Is(err, ErrVersionPublished) {
		t.Errorf("republishing: %v, want ErrVersionPublished", err)
	}

	clock.set(day(21))
	got, err := c.VersionInForce(ctx, p.ID, day(21))
	if err != nil {
		t.Fatalf("VersionInForce: %v", err)
	}
	if int64(got.Overdraft.Rate) != 149_000 {
		t.Errorf("in force = %d, want 149000", got.Overdraft.Rate)
	}
}

// Publication is forward-only. A version effective in the past would reprice
// every account bound to the product retroactively, moving interest already
// charged, with the audit log as the only control. Retroactivity is a
// per-account overlay instead — see the design doc.
func TestPublishRefusesThePast(t *testing.T) {
	ctx := context.Background()
	clock := &mutableClock{at: day(10)}
	c := newTestCatalogue(t, clock.now)

	p, err := c.CreateProduct(ctx, "Basic", CurrentAccount)
	if err != nil {
		t.Fatalf("CreateProduct: %v", err)
	}
	if _, err := c.DraftVersion(ctx, p.ID, day(5), OverdraftPricing{Rate: 120_000}); err != nil {
		t.Fatalf("DraftVersion: %v", err)
	}

	if _, err := c.PublishVersion(ctx, p.ID, day(5)); !errors.Is(err, ErrRetroactivePublish) {
		t.Fatalf("publishing effective in the past: %v, want ErrRetroactivePublish", err)
	}

	// Today is fine — a version effective today prices today onwards, and
	// today has not been accrued yet.
	if _, err := c.DraftVersion(ctx, p.ID, day(10), OverdraftPricing{Rate: 120_000}); err != nil {
		t.Fatalf("DraftVersion: %v", err)
	}
	if _, err := c.PublishVersion(ctx, p.ID, day(10)); err != nil {
		t.Fatalf("publishing effective today: %v", err)
	}
}

func TestRetireTakesAProductOffSaleWithoutUnpricingIt(t *testing.T) {
	ctx := context.Background()
	clock := &mutableClock{at: day(10)}
	c := newTestCatalogue(t, clock.now)

	p, err := c.CreateProduct(ctx, "Basic", CurrentAccount)
	if err != nil {
		t.Fatalf("CreateProduct: %v", err)
	}
	if _, err := c.DraftVersion(ctx, p.ID, day(10), OverdraftPricing{Rate: 120_000}); err != nil {
		t.Fatalf("DraftVersion: %v", err)
	}
	if _, err := c.PublishVersion(ctx, p.ID, day(10)); err != nil {
		t.Fatalf("PublishVersion: %v", err)
	}

	retired, err := c.RetireProduct(ctx, p.ID)
	if err != nil {
		t.Fatalf("RetireProduct: %v", err)
	}
	if !retired.Retired {
		t.Fatal("RetireProduct did not retire")
	}

	// The accounts already sold from it keep resolving, for as long as they
	// live. A bank that could not express that would keep dead products on sale.
	clock.set(day(400))
	if _, err := c.VersionInForce(ctx, p.ID, day(400)); err != nil {
		t.Fatalf("a retired product stopped pricing: %v", err)
	}
}

func TestUnknownProductAndBadPricingAreRefused(t *testing.T) {
	ctx := context.Background()
	c := newTestCatalogue(t, (&mutableClock{at: day(10)}).now)

	if _, err := c.DraftVersion(ctx, "prd_nope", day(10), OverdraftPricing{Rate: 1}); !errors.Is(err, ErrProductNotFound) {
		t.Errorf("drafting against an unknown product: %v", err)
	}
	if _, err := c.PublishVersion(ctx, "prd_nope", day(10)); !errors.Is(err, ErrProductNotFound) {
		t.Errorf("publishing an unknown product: %v", err)
	}

	p, err := c.CreateProduct(ctx, "Basic", CurrentAccount)
	if err != nil {
		t.Fatalf("CreateProduct: %v", err)
	}
	if _, err := c.DraftVersion(ctx, p.ID, day(10), OverdraftPricing{UnarrangedRate: 350_000}); !errors.Is(err, ErrInvalidRate) {
		t.Error("an unarranged rate with no arranged one was accepted")
	}
	if _, err := c.PublishVersion(ctx, p.ID, day(10)); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("publishing a day with no draft: %v, want ErrVersionNotFound", err)
	}
	if _, err := c.CreateProduct(ctx, "", CurrentAccount); err == nil {
		t.Error("an unnamed product was accepted")
	}
}

// Every catalogue write is in the audit log, under its own scope, because a
// published price is a fact an auditor asks who entered and when.
//
// The log is read through the store rather than through *ledger.Book, whose
// GetAuditLog deliberately narrows to ScopeLedger so a Book reports only the
// mutations it made. It is how lending/refund_test.go reads ScopeLending.
func TestCatalogueWritesAreAudited(t *testing.T) {
	ctx := context.Background()
	clock := &mutableClock{at: day(10)}
	c := newTestCatalogue(t, clock.now)

	p, err := c.CreateProduct(ctx, "Basic", CurrentAccount)
	if err != nil {
		t.Fatalf("CreateProduct: %v", err)
	}
	if _, err := c.DraftVersion(ctx, p.ID, day(10), OverdraftPricing{Rate: 120_000}); err != nil {
		t.Fatalf("DraftVersion: %v", err)
	}
	if _, err := c.PublishVersion(ctx, p.ID, day(10)); err != nil {
		t.Fatalf("PublishVersion: %v", err)
	}
	if _, err := c.RetireProduct(ctx, p.ID); err != nil {
		t.Fatalf("RetireProduct: %v", err)
	}

	var events []ledger.AuditEvent
	if err := c.Store().View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		events, err = tx.ListAudit(ctx, ledger.AuditFilter{BookID: "bank", Scope: ledger.ScopeProduct})
		return err
	}); err != nil {
		t.Fatalf("ListAudit: %v", err)
	}

	want := []string{
		ledger.EventProductCreated,
		ledger.EventProductVersionDrafted,
		ledger.EventProductVersionPublished,
		ledger.EventProductRetired,
	}
	if len(events) != len(want) {
		t.Fatalf("audit events = %d, want %d", len(events), len(want))
	}
	for i, w := range want {
		if events[i].Type != w {
			t.Errorf("event %d = %s, want %s", i, events[i].Type, w)
		}
		if events[i].EntityID != string(p.ID) {
			t.Errorf("event %d entity = %s, want %s", i, events[i].EntityID, p.ID)
		}
	}
}
