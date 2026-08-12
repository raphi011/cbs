package iso20022

import (
	"errors"
	"testing"
)

func TestPacs004RoundTrip(t *testing.T) {
	env := assertGoldenRoundTrip(t, "pacs004.xml")

	doc, ok := env.Document.(*Pacs004)
	if !ok {
		t.Fatalf("Document is %T, want *Pacs004", env.Document)
	}
	tx := doc.PmtRtr.TxInf
	if len(tx) != 1 {
		t.Fatalf("TxInf has %d entries, want 1", len(tx))
	}
	if got := tx[0].OrgnlEndToEndId; got != "INV-2026-0042" {
		t.Fatalf("OrgnlEndToEndId = %q, want INV-2026-0042", got)
	}
	if got := *tx[0].RtrRsnInf.Rsn.Cd; got != ReturnReasonClosedAccountNumber {
		t.Fatalf("return reason = %q, want AC04", got)
	}
	orig, err := tx[0].OrgnlIntrBkSttlmAmt.Minor(2)
	if err != nil {
		t.Fatalf("Minor() error = %v", err)
	}
	returned, err := tx[0].RtrdIntrBkSttlmAmt.Minor(2)
	if err != nil {
		t.Fatalf("Minor() error = %v", err)
	}
	// This system's returns are always whole, so the two are equal here.
	if orig != returned {
		t.Fatalf("original %d and returned %d differ; this system returns whole payments", orig, returned)
	}
	// OrgnlTxRef is what a settlement agent with no payment row resolves a
	// return's accounts from — see the ruling above ReturnTransaction.
	ref := tx[0].OrgnlTxRef
	if ref == nil || ref.DbtrAgt == nil || ref.CdtrAgt == nil {
		t.Fatalf("OrgnlTxRef = %+v, want both agents", ref)
	}
	if got := ref.DbtrAgt.FinInstnId.BICFI; got != "AURTSESSXXX" {
		t.Errorf("OrgnlTxRef/DbtrAgt/FinInstnId/BICFI = %q, want the original payer's bank AURTSESSXXX", got)
	}
	if got := ref.CdtrAgt.FinInstnId.BICFI; got != "VERDITMMXXX" {
		t.Errorf("OrgnlTxRef/CdtrAgt/FinInstnId/BICFI = %q, want the returning bank VERDITMMXXX", got)
	}
}

// TestPacs004NamesTheReturningBank pins the EPC-mandatory, ISO-optional
// originator on the return reason — the same element pacs.002 carries, and the
// same reason: the message is sent TO the clearing house, so the header alone
// cannot say which institution decided to send the money back.
func TestPacs004NamesTheReturningBank(t *testing.T) {
	env := assertGoldenRoundTrip(t, "pacs004.xml")
	tx := env.Document.(*Pacs004).PmtRtr.TxInf

	orgtr := tx[0].RtrRsnInf.Orgtr
	if orgtr == nil {
		t.Fatal("the return reason names no originator")
	}
	if orgtr.Nm != "" {
		t.Fatalf("Orgtr/Nm = %q; the EPC guidelines allow a name only for a non-financial institution", orgtr.Nm)
	}
	if orgtr.Id == nil || orgtr.Id.OrgId == nil {
		t.Fatalf("Orgtr is not identified through Id/OrgId: %#v", orgtr)
	}
	if got := orgtr.Id.OrgId.AnyBIC; got != "VERDITMMXXX" {
		t.Fatalf("Orgtr/Id/OrgId/AnyBIC = %q, want the creditor's bank VERDITMMXXX", got)
	}
	if got := tx[0].RtrId; got == tx[0].OrgnlTxId {
		t.Fatalf("RtrId and OrgnlTxId are the same value %q; the return's own reference is not the "+
			"original payment's", got)
	}
}

func TestPacs004Validate(t *testing.T) {
	valid := func() *Pacs004 {
		env := assertGoldenRoundTrip(t, "pacs004.xml")
		return env.Document.(*Pacs004)
	}

	t.Run("no transactions is a missing element", func(t *testing.T) {
		d := valid()
		d.PmtRtr.TxInf = nil
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("no return id is a missing element", func(t *testing.T) {
		d := valid()
		d.PmtRtr.TxInf[0].RtrId = ""
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("nothing to refer back by is a missing element", func(t *testing.T) {
		d := valid()
		d.PmtRtr.TxInf[0].OrgnlEndToEndId = ""
		d.PmtRtr.TxInf[0].OrgnlTxId = ""
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a return reason with both arms is an invalid choice", func(t *testing.T) {
		d := valid()
		prtry := "made up"
		d.PmtRtr.TxInf[0].RtrRsnInf.Rsn.Prtry = &prtry
		if err := d.validate(); !errors.Is(err, ErrInvalidChoice) {
			t.Fatalf("validate() = %v, want it to wrap ErrInvalidChoice", err)
		}
	})
	t.Run("no return reason at all is a missing element", func(t *testing.T) {
		d := valid()
		d.PmtRtr.TxInf[0].RtrRsnInf = nil
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a return reason with no originator is a missing element", func(t *testing.T) {
		d := valid()
		d.PmtRtr.TxInf[0].RtrRsnInf.Orgtr = nil
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("no OrgnlTxRef at all is valid", func(t *testing.T) {
		d := valid()
		d.PmtRtr.TxInf[0].OrgnlTxRef = nil
		if err := d.validate(); err != nil {
			t.Fatalf("validate() = %v, want OrgnlTxRef to stay optional", err)
		}
	})
	t.Run("an OrgnlTxRef naming one agent is a missing element", func(t *testing.T) {
		d := valid()
		d.PmtRtr.TxInf[0].OrgnlTxRef.CdtrAgt = nil
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement — a reference naming one "+
				"agent is one a settlement agent cannot act on", err)
		}
	})

	// The three amounts are each validated, and each is pinned separately:
	// wiring one up and assuming the others follow is exactly the gap the
	// reviews of Tasks 8 and 9 found twice.
	t.Run("a malformed returned amount is an amount format error", func(t *testing.T) {
		d := valid()
		d.PmtRtr.TxInf[0].RtrdIntrBkSttlmAmt.Value = "three thousand"
		if err := d.validate(); !errors.Is(err, ErrAmountFormat) {
			t.Fatalf("validate() = %v, want it to wrap ErrAmountFormat", err)
		}
	})
	t.Run("a malformed original amount is an amount format error", func(t *testing.T) {
		d := valid()
		d.PmtRtr.TxInf[0].OrgnlIntrBkSttlmAmt.Value = "3000.000000"
		if err := d.validate(); !errors.Is(err, ErrAmountScale) {
			t.Fatalf("validate() = %v, want it to wrap ErrAmountScale", err)
		}
	})
	t.Run("a malformed group total is an amount format error", func(t *testing.T) {
		d := valid()
		d.PmtRtr.GrpHdr.TtlRtrdIntrBkSttlmAmt.Value = "-1.00"
		if err := d.validate(); !errors.Is(err, ErrAmountFormat) {
			t.Fatalf("validate() = %v, want it to wrap ErrAmountFormat", err)
		}
	})
	t.Run("an amount with no currency is a missing element", func(t *testing.T) {
		d := valid()
		d.PmtRtr.TxInf[0].RtrdIntrBkSttlmAmt.Ccy = ""
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
}
