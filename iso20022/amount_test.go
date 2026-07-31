package iso20022

import (
	"errors"
	"testing"
)

func TestNewAmount(t *testing.T) {
	tests := []struct {
		name  string
		minor int64
		scale uint8
		want  string
	}{
		{"euro", 300000, 2, "3000.00"},
		{"euro with cents", 123456, 2, "1234.56"},
		{"euro under one unit", 5, 2, "0.05"},
		{"euro zero", 0, 2, "0.00"},
		{"bitcoin", 100000000, 8, "1.00000000"},
		{"bitcoin one satoshi", 1, 8, "0.00000001"},
		{"scale zero", 42, 0, "42"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewAmount(tt.minor, tt.scale, "EUR")
			if err != nil {
				t.Fatalf("NewAmount() error = %v", err)
			}
			if got.Value != tt.want {
				t.Fatalf("Value = %q, want %q", got.Value, tt.want)
			}
			if got.Ccy != "EUR" {
				t.Fatalf("Ccy = %q, want EUR", got.Ccy)
			}
		})
	}
}

func TestNewAmountRejectsNegative(t *testing.T) {
	_, err := NewAmount(-1, 2, "EUR")
	if !errors.Is(err, ErrAmountFormat) {
		t.Fatalf("NewAmount(-1) = %v, want it to wrap ErrAmountFormat", err)
	}
}

func TestAmountMinor(t *testing.T) {
	tests := []struct {
		name  string
		value string
		scale uint8
		want  int64
	}{
		{"exact", "1234.56", 2, 123456},
		{"short fraction is padded", "1234.5", 2, 123450},
		{"no fraction is padded", "1234", 2, 123400},
		{"zero", "0.00", 2, 0},
		{"bitcoin", "0.00000001", 8, 1},
		{"scale zero", "42", 0, 42},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ActiveCurrencyAndAmount{Ccy: "EUR", Value: tt.value}.Minor(tt.scale)
			if err != nil {
				t.Fatalf("Minor() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Minor() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestAmountMinorRefusesToRound is the load-bearing case. A euro message
// carrying 0.005 does not mean half a cent that should become one cent or zero
// — it means something this system cannot represent, and picking an answer
// would put a rounding error into a ledger that is otherwise exact.
func TestAmountMinorRefusesToRound(t *testing.T) {
	for _, v := range []string{"1234.567", "0.005", "1.000000001"} {
		_, err := ActiveCurrencyAndAmount{Ccy: "EUR", Value: v}.Minor(2)
		if !errors.Is(err, ErrAmountScale) {
			t.Fatalf("Minor(%q, scale 2) = %v, want it to wrap ErrAmountScale", v, err)
		}
	}
}

func TestAmountMinorRejectsMalformed(t *testing.T) {
	for _, v := range []string{"", "abc", "-1.00", "1.2.3", "1,00", " 1.00"} {
		_, err := ActiveCurrencyAndAmount{Ccy: "EUR", Value: v}.Minor(2)
		if !errors.Is(err, ErrAmountFormat) {
			t.Fatalf("Minor(%q) = %v, want it to wrap ErrAmountFormat", v, err)
		}
	}
}

func TestAmountRoundTrips(t *testing.T) {
	for _, minor := range []int64{0, 1, 5, 99, 100, 123456, 999999999} {
		a, err := NewAmount(minor, 2, "EUR")
		if err != nil {
			t.Fatalf("NewAmount(%d) error = %v", minor, err)
		}
		back, err := a.Minor(2)
		if err != nil {
			t.Fatalf("Minor() error = %v", err)
		}
		if back != minor {
			t.Fatalf("round trip of %d = %d", minor, back)
		}
	}
}
