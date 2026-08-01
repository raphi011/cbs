package iso20022

import (
	"errors"
	"testing"
)

func TestPacs003RoundTrip(t *testing.T) {
	env := assertGoldenRoundTrip(t, "pacs003.xml")

	doc, ok := env.Document.(*Pacs003)
	if !ok {
		t.Fatalf("Document is %T, want *Pacs003", env.Document)
	}
	tx := doc.FIToFICstmrDrctDbt.DrctDbtTxInf
	if len(tx) != 1 {
		t.Fatalf("DrctDbtTxInf has %d entries, want 1", len(tx))
	}
	if got := tx[0].DrctDbtTx.MndtRltdInf.MndtId; got != "mnd-0001" {
		t.Fatalf("MndtId = %q, want mnd-0001", got)
	}
	if got := tx[0].DrctDbtTx.MndtRltdInf.DtOfSgntr.Format("2006-01-02"); got != "2026-01-15" {
		t.Fatalf("DtOfSgntr = %s, want 2026-01-15", got)
	}
	// The collection is a PULL: the creditor's agent sends it, so Fr is the
	// creditor's bank and the debtor's bank is the one being asked to pay.
	if got := env.AppHdr.Fr.FIId.FinInstnId.BICFI; got != "VERDITMMXXX" {
		t.Fatalf("Fr = %q, want the creditor's agent VERDITMMXXX", got)
	}
	if got := tx[0].DbtrAgt.FinInstnId.BICFI; got != "AURTSESSXXX" {
		t.Fatalf("DbtrAgt = %q, want AURTSESSXXX", got)
	}
	// AT-20 and AT-21: the EPC-mandatory local instrument and sequence type
	// that the ISO schema itself leaves optional.
	if tx[0].PmtTpInf == nil || tx[0].PmtTpInf.LclInstrm == nil || tx[0].PmtTpInf.LclInstrm.Cd == nil {
		t.Fatalf("LclInstrm/Cd missing, want %v", LocalInstrumentCore)
	}
	if got := *tx[0].PmtTpInf.LclInstrm.Cd; got != LocalInstrumentCore {
		t.Fatalf("LclInstrm/Cd = %q, want %q", got, LocalInstrumentCore)
	}
	if tx[0].PmtTpInf.SeqTp == nil {
		t.Fatalf("SeqTp missing, want %v", SequenceTypeRecurring)
	}
	if got := *tx[0].PmtTpInf.SeqTp; got != SequenceTypeRecurring {
		t.Fatalf("SeqTp = %q, want %q", got, SequenceTypeRecurring)
	}
	// AT-02: the Creditor Identifier the mandate was issued under.
	if got := tx[0].DrctDbtTx.CdtrSchmeId.Id.PrvtId.Othr.Id; got != "IT66ZZZVERDE0001" {
		t.Fatalf("CdtrSchmeId/Othr/Id = %q, want IT66ZZZVERDE0001", got)
	}
	if got := tx[0].DrctDbtTx.CdtrSchmeId.Id.PrvtId.Othr.SchmeNm.Prtry; got != "SEPA" {
		t.Fatalf("CdtrSchmeId/SchmeNm/Prtry = %q, want SEPA", got)
	}
}

func TestPacs003Validate(t *testing.T) {
	valid := func() *Pacs003 {
		env := assertGoldenRoundTrip(t, "pacs003.xml")
		return env.Document.(*Pacs003)
	}

	t.Run("no transactions is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFICstmrDrctDbt.DrctDbtTxInf = nil
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a collection with no mandate id is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFICstmrDrctDbt.DrctDbtTxInf[0].DrctDbtTx.MndtRltdInf.MndtId = ""
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a malformed debtor IBAN is a pattern error", func(t *testing.T) {
		d := valid()
		bad := IBAN("nope")
		d.FIToFICstmrDrctDbt.DrctDbtTxInf[0].DbtrAcct.Id.IBAN = &bad
		if err := d.validate(); !errors.Is(err, ErrIBANPattern) {
			t.Fatalf("validate() = %v, want it to wrap ErrIBANPattern", err)
		}
	})
	t.Run("a collection settlement amount with no currency is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFICstmrDrctDbt.DrctDbtTxInf[0].IntrBkSttlmAmt.Ccy = ""
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a malformed collection settlement amount is a format error", func(t *testing.T) {
		d := valid()
		d.FIToFICstmrDrctDbt.DrctDbtTxInf[0].IntrBkSttlmAmt.Value = "not-a-number"
		if err := d.validate(); !errors.Is(err, ErrAmountFormat) {
			t.Fatalf("validate() = %v, want it to wrap ErrAmountFormat", err)
		}
	})
	t.Run("a group header total settlement amount with no currency is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFICstmrDrctDbt.GrpHdr.TtlIntrBkSttlmAmt.Ccy = ""
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a malformed group header total settlement amount is a format error", func(t *testing.T) {
		d := valid()
		d.FIToFICstmrDrctDbt.GrpHdr.TtlIntrBkSttlmAmt.Value = "not-a-number"
		if err := d.validate(); !errors.Is(err, ErrAmountFormat) {
			t.Fatalf("validate() = %v, want it to wrap ErrAmountFormat", err)
		}
	})
	t.Run("a collection with no payment type information is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFICstmrDrctDbt.DrctDbtTxInf[0].PmtTpInf = nil
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a collection with no service level is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFICstmrDrctDbt.DrctDbtTxInf[0].PmtTpInf.SvcLvl = nil
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a collection with no local instrument is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFICstmrDrctDbt.DrctDbtTxInf[0].PmtTpInf.LclInstrm = nil
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a local instrument with both code and proprietary is an invalid choice", func(t *testing.T) {
		d := valid()
		prtry := "X"
		d.FIToFICstmrDrctDbt.DrctDbtTxInf[0].PmtTpInf.LclInstrm.Prtry = &prtry
		if err := d.validate(); !errors.Is(err, ErrInvalidChoice) {
			t.Fatalf("validate() = %v, want it to wrap ErrInvalidChoice", err)
		}
	})
	t.Run("a local instrument given only by proprietary identifier is a missing element", func(t *testing.T) {
		d := valid()
		prtry := "X"
		d.FIToFICstmrDrctDbt.DrctDbtTxInf[0].PmtTpInf.LclInstrm.Cd = nil
		d.FIToFICstmrDrctDbt.DrctDbtTxInf[0].PmtTpInf.LclInstrm.Prtry = &prtry
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a collection with no sequence type is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFICstmrDrctDbt.DrctDbtTxInf[0].PmtTpInf.SeqTp = nil
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a collection with no creditor identifier is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFICstmrDrctDbt.DrctDbtTxInf[0].DrctDbtTx.CdtrSchmeId.Id.PrvtId.Othr.Id = ""
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a collection with no creditor identifier scheme is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFICstmrDrctDbt.DrctDbtTxInf[0].DrctDbtTx.CdtrSchmeId.Id.PrvtId.Othr.SchmeNm.Prtry = ""
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a collection with no mandate signature date is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFICstmrDrctDbt.DrctDbtTxInf[0].DrctDbtTx.MndtRltdInf.DtOfSgntr = ISODate{}
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
}
