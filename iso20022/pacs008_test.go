package iso20022

import (
	"errors"
	"testing"
)

func TestPacs008RoundTrip(t *testing.T) {
	env := assertGoldenRoundTrip(t, "pacs008.xml")

	doc, ok := env.Document.(*Pacs008)
	if !ok {
		t.Fatalf("Document is %T, want *Pacs008", env.Document)
	}
	tx := doc.FIToFICstmrCdtTrf.CdtTrfTxInf
	if len(tx) != 1 {
		t.Fatalf("CdtTrfTxInf has %d entries, want 1", len(tx))
	}
	if got := tx[0].PmtId.EndToEndId; got != "INV-2026-0042" {
		t.Fatalf("EndToEndId = %q, want INV-2026-0042", got)
	}
	if got := *tx[0].DbtrAcct.Id.IBAN; got != "SE89AURORA1001" {
		t.Fatalf("debtor IBAN = %q, want SE89AURORA1001", got)
	}
	if got := tx[0].CdtrAgt.FinInstnId.BICFI; got != "VERDITMMXXX" {
		t.Fatalf("creditor agent = %q, want VERDITMMXXX", got)
	}
	minor, err := tx[0].IntrBkSttlmAmt.Minor(2)
	if err != nil {
		t.Fatalf("Minor() error = %v", err)
	}
	if minor != 300000 {
		t.Fatalf("amount = %d minor units, want 300000", minor)
	}
}

func TestPacs008Validate(t *testing.T) {
	valid := func() *Pacs008 {
		env := assertGoldenRoundTrip(t, "pacs008.xml")
		return env.Document.(*Pacs008)
	}

	t.Run("no transactions is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFICstmrCdtTrf.CdtTrfTxInf = nil
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a transaction with no end-to-end id is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFICstmrCdtTrf.CdtTrfTxInf[0].PmtId.EndToEndId = ""
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("an account with both arms of the choice is an invalid choice", func(t *testing.T) {
		d := valid()
		d.FIToFICstmrCdtTrf.CdtTrfTxInf[0].DbtrAcct.Id.Othr = &GenericAccountIdentification{Id: "x"}
		if err := d.validate(); !errors.Is(err, ErrInvalidChoice) {
			t.Fatalf("validate() = %v, want it to wrap ErrInvalidChoice", err)
		}
	})
	t.Run("a malformed creditor agent BIC is a format error", func(t *testing.T) {
		d := valid()
		d.FIToFICstmrCdtTrf.CdtTrfTxInf[0].CdtrAgt.FinInstnId.BICFI = "VERDITMMX"
		if err := d.validate(); !errors.Is(err, ErrBICFormat) {
			t.Fatalf("validate() = %v, want it to wrap ErrBICFormat", err)
		}
	})
}
