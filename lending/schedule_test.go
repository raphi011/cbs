package lending

import (
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// termLoan carries no rate: the rate is BuildSchedule's own argument now, not a
// facility field, so each call below states the rate it is generating at.
func termLoan(method AmortMethod, months int) Facility {
	return Facility{
		Kind: TermLoan, Name: "Loan", Asset: "EUR",
		Method: method, TermMonths: months,
	}
}

// €10,000 over 5 years at 6% is the textbook annuity: an instalment of €193.33,
// of which €50.00 is interest in the first month and almost none in the last.
func TestBuildSchedule_Annuity(t *testing.T) {
	const principal ledger.Amount = 1_000_000 // €10,000 in cents

	got := BuildSchedule(termLoan(Annuity, 60), principal, 60_000, day(2025, time.February, 15))

	if len(got) != 60 {
		t.Fatalf("instalments = %d, want 60", len(got))
	}

	assertAmount(t, "first instalment total", got[0].Total(), 19_333)
	assertAmount(t, "first instalment interest", got[0].Interest, 5_000)
	assertAmount(t, "first instalment principal", got[0].Principal, 14_333)
	assertEqual(t, "first seq", got[0].Seq, 1)
	assertDate(t, "first due date", got[0].DueDate, day(2025, time.February, 15))
	assertDate(t, "second due date", got[1].DueDate, day(2025, time.March, 15))
	assertDate(t, "last due date", got[59].DueDate, day(2030, time.January, 15))

	// The interest portion falls and the principal portion rises. That is what
	// an annuity IS, and it is the thing a reader is meant to see.
	if got[59].Interest >= got[0].Interest {
		t.Errorf("interest did not fall: first %d, last %d", got[0].Interest, got[59].Interest)
	}
	if got[59].Principal <= got[0].Principal {
		t.Errorf("principal did not rise: first %d, last %d", got[0].Principal, got[59].Principal)
	}

	// Every instalment is the same total except the last, which absorbs the
	// rounding residue.
	for i := 0; i < 59; i++ {
		assertAmount(t, "instalment total", got[i].Total(), 19_333)
	}

	assertScheduleRepaysExactly(t, got, principal)
	assertNothingPaidYet(t, got)
}

func TestBuildSchedule_EqualPrincipal(t *testing.T) {
	const principal ledger.Amount = 1_000_000

	got := BuildSchedule(termLoan(EqualPrincipal, 60), principal, 60_000, day(2025, time.February, 15))

	if len(got) != 60 {
		t.Fatalf("instalments = %d, want 60", len(got))
	}

	// 1_000_000 / 60 = 16_666 with 40 left over, which lands on the last one.
	for i := 0; i < 59; i++ {
		assertAmount(t, "principal portion", got[i].Principal, 16_666)
	}
	assertAmount(t, "last principal portion", got[59].Principal, 16_706)

	// Interest falls with the outstanding balance, so the TOTAL falls too —
	// the visible difference from an annuity.
	assertAmount(t, "first interest", got[0].Interest, 5_000)
	if got[59].Total() >= got[0].Total() {
		t.Errorf("instalment total did not fall: first %d, last %d", got[0].Total(), got[59].Total())
	}

	assertScheduleRepaysExactly(t, got, principal)
}

// A zero-rate loan is a real product (staff loans, interest-free credit), and
// the annuity formula divides by zero on it, so it takes its own path.
func TestBuildSchedule_ZeroRate(t *testing.T) {
	const principal ledger.Amount = 1_200_000

	for _, method := range []AmortMethod{Annuity, EqualPrincipal} {
		t.Run(method.String(), func(t *testing.T) {
			got := BuildSchedule(termLoan(method, 12), principal, 0, day(2025, time.February, 15))

			if len(got) != 12 {
				t.Fatalf("instalments = %d, want 12", len(got))
			}
			for _, i := range got {
				assertAmount(t, "interest on a zero-rate loan", i.Interest, 0)
			}
			assertAmount(t, "principal portion", got[0].Principal, 100_000)
			assertScheduleRepaysExactly(t, got, principal)
		})
	}
}

// A rate that does not divide by twelve into a whole number of millionths must
// not silently truncate the schedule's interest.
func TestBuildSchedule_AwkwardRate(t *testing.T) {
	const principal ledger.Amount = 1_000_000
	got := BuildSchedule(termLoan(Annuity, 12), principal, 33_750, day(2025, time.February, 15)) // 3.375%

	// 1_000_000 × 33_750 / 12 / 1_000_000 = 2812.5, rounded half up to 2813.
	// A monthly fraction pre-divided to 2812 millionths would give 2812.
	assertAmount(t, "first interest at 3.375%", got[0].Interest, 2_813)
	assertScheduleRepaysExactly(t, got, principal)
}

// BuildSchedule is pinned to the rate it is GIVEN, not to one read off the
// facility — which is what makes the schedule a plan pinned to the terms in
// force at activation rather than something derived from a facility row that can
// now be repriced out from under it.
//
// The same Facility value and the same principal at two rates produce two
// different Interest columns. Since a Facility no longer carries a rate at all,
// the only way the two could agree is if the argument were ignored.
func TestBuildScheduleUsesTheRateItIsGiven(t *testing.T) {
	const (
		principal ledger.Amount = 1_000_000 // €10,000 in cents
		cheap     interest.Rate = 60_000    // 6%
		dear      interest.Rate = 120_000   // 12%
	)
	f := termLoan(Annuity, 12)
	firstDue := day(2025, time.February, 15)

	atCheap := BuildSchedule(f, principal, cheap, firstDue)
	atDear := BuildSchedule(f, principal, dear, firstDue)

	if len(atCheap) != 12 || len(atDear) != 12 {
		t.Fatalf("instalments = %d and %d, want 12 each", len(atCheap), len(atDear))
	}

	// The first instalment's scheduled interest is outstanding × rate / 12,
	// stated from the definition rather than copied from a run:
	//
	//	1_000_000 × 60_000  / 12_000_000 =  5_000
	//	1_000_000 × 120_000 / 12_000_000 = 10_000
	assertAmount(t, "first interest at 6%", atCheap[0].Interest, 5_000)
	assertAmount(t, "first interest at 12%", atDear[0].Interest, 10_000)

	// And the whole column differs, not just its head: a schedule that took the
	// rate from somewhere else would produce two identical plans.
	var cheapInterest, dearInterest ledger.Amount
	for i := range atCheap {
		cheapInterest += atCheap[i].Interest
		dearInterest += atDear[i].Interest
	}
	if dearInterest <= cheapInterest {
		t.Errorf("total scheduled interest: 12%% gave %d, 6%% gave %d; the rate argument was not used",
			dearInterest, cheapInterest)
	}

	// Both still repay the principal to the cent, which is the property the
	// generator's every rounding decision exists for and which a rate change
	// must not disturb.
	assertScheduleRepaysExactly(t, atCheap, principal)
	assertScheduleRepaysExactly(t, atDear, principal)
}

func TestBuildSchedule_RevolvingLineHasNoSchedule(t *testing.T) {
	line := Facility{Kind: RevolvingLine, Asset: "EUR", TermMonths: 12}
	if got := BuildSchedule(line, 100_000, 180_000, day(2025, time.February, 15)); len(got) != 0 {
		t.Errorf("a revolving line generated %d instalments, want 0", len(got))
	}
}

func TestBuildSchedule_RejectsNonsense(t *testing.T) {
	for _, tt := range []struct {
		name      string
		months    int
		principal ledger.Amount
	}{
		{"zero term", 0, 1_000_000},
		{"negative term", -12, 1_000_000},
		{"zero principal", 60, 0},
		{"negative principal", 60, -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := termLoan(Annuity, tt.months)
			if got := BuildSchedule(f, tt.principal, 60_000, day(2025, time.February, 15)); len(got) != 0 {
				t.Errorf("generated %d instalments, want 0", len(got))
			}
		})
	}
}

// Adding a month to the 31st must not land in the month after next, which is
// what time.AddDate does on its own: 31 January + 1 month is 3 March.
func TestAddMonths_ClampsToTheEndOfTheMonth(t *testing.T) {
	for _, tt := range []struct {
		name string
		from time.Time
		n    int
		want time.Time
	}{
		{"31 Jan plus one is 28 Feb", day(2025, time.January, 31), 1, day(2025, time.February, 28)},
		{"31 Jan plus one in a leap year", day(2024, time.January, 31), 1, day(2024, time.February, 29)},
		{"31 Jan plus two is 31 Mar", day(2025, time.January, 31), 2, day(2025, time.March, 31)},
		{"30 Apr plus one is 30 May", day(2025, time.April, 30), 1, day(2025, time.May, 30)},
		{"15 Dec plus one crosses the year", day(2025, time.December, 15), 1, day(2026, time.January, 15)},
		{"plus zero is the same day", day(2025, time.January, 31), 0, day(2025, time.January, 31)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := AddMonths(tt.from, tt.n); !got.Equal(tt.want) {
				t.Errorf("AddMonths = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// A schedule that does not repay the principal to the cent is not a schedule.
// Every rounding decision in the generator exists to make this true.
func assertScheduleRepaysExactly(t *testing.T, got []Installment, principal ledger.Amount) {
	t.Helper()
	var total ledger.Amount
	for _, i := range got {
		total += i.Principal
	}
	if total != principal {
		t.Errorf("scheduled principal sums to %d, want %d", total, principal)
	}
	for _, i := range got {
		if i.Principal < 0 || i.Interest < 0 {
			t.Errorf("instalment %d has a negative portion: principal %d, interest %d", i.Seq, i.Principal, i.Interest)
		}
	}
	for i := range got {
		if got[i].Seq != i+1 {
			t.Errorf("instalment at index %d has Seq %d, want %d", i, got[i].Seq, i+1)
		}
	}
}

func assertNothingPaidYet(t *testing.T, got []Installment) {
	t.Helper()
	for _, i := range got {
		if i.PaidPrincipal != 0 || i.PaidInterest != 0 {
			t.Errorf("instalment %d was generated already paid", i.Seq)
		}
	}
}

func assertAmount(t *testing.T, label string, got, want ledger.Amount) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %d, want %d", label, got, want)
	}
}

func assertEqual[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", label, got, want)
	}
}

func assertDate(t *testing.T, label string, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Errorf("%s: got %v, want %v", label, got, want)
	}
}
