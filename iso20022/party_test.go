package iso20022

import (
	"errors"
	"testing"
)

func TestBICValidate(t *testing.T) {
	tests := []struct {
		name string
		bic  BIC
		ok   bool
	}{
		{"eight character", "AURTSESS", true},
		{"eleven character with branch", "AURTSESSXXX", true},
		{"numeric location code", "NORDNOK1XXX", true},
		{"numeric branch code", "VERDITMM001", true},
		{"seven characters", "AURTSES", false},
		{"nine characters", "AURTSESSX", false},
		{"ten characters", "AURTSESSXX", false},
		{"lowercase", "aurtsess", false},
		{"digit in institution code", "AUR1SESS", false},
		{"digit in country code", "AURTS1SS", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.bic.Validate()
			if tt.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tt.ok {
				if err == nil {
					t.Fatalf("Validate() = nil, want an error")
				}
				if !errors.Is(err, ErrBICFormat) {
					t.Fatalf("Validate() = %v, want it to wrap ErrBICFormat", err)
				}
			}
		})
	}
}

func TestIBANCompact(t *testing.T) {
	tests := []struct {
		name string
		in   IBAN
		want IBAN
	}{
		{"seed value with hyphens", "SE89-AURORA-1001", "SE89AURORA1001"},
		{"seed value for Banca Verde", "IT60-VERDE-2002", "IT60VERDE2002"},
		{"displayed in groups of four", "SE89 AURO RA10 01", "SE89AURORA1001"},
		{"already compact", "NO93NORD3001", "NO93NORD3001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.Compact(); got != tt.want {
				t.Fatalf("Compact() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestIBANValidateAcceptsTheSeedsReadableIdentifiers is the claim sub-project 5
// made and this package must not quietly break: the seed's IBANs carry no valid
// mod-97 check digit, and they must still produce schema-valid documents.
func TestIBANValidateAcceptsTheSeedsReadableIdentifiers(t *testing.T) {
	for _, v := range []IBAN{"SE89-AURORA-1001", "IT60-VERDE-2002", "NO93-NORD-3001"} {
		if err := v.Validate(); err != nil {
			t.Fatalf("Validate(%q) = %v, want nil", v, err)
		}
	}
}

func TestIBANValidateRejects(t *testing.T) {
	tests := []struct {
		name string
		in   IBAN
	}{
		{"lowercase country code", "se89AURORA1001"},
		{"no check digits", "SEXXAURORA1001"},
		{"country code only", "SE89"},
		{"too long", "SE89" + "A123456789012345678901234567890X"},
		{"punctuation that is not a separator", "SE89/AURORA/1001"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Validate()
			if err == nil {
				t.Fatalf("Validate(%q) = nil, want an error", tt.in)
			}
			if !errors.Is(err, ErrIBANPattern) {
				t.Fatalf("Validate(%q) = %v, want it to wrap ErrIBANPattern", tt.in, err)
			}
		})
	}
}
