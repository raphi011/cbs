package iso20022

import (
	"errors"
	"testing"
)

// TestPacs002RoundTripCarriesAPartialRejection is the point of this message:
// a bulk can be accepted and rejected at once, and the group status and the
// per-transaction statuses say different things on purpose.
func TestPacs002RoundTripCarriesAPartialRejection(t *testing.T) {
	env := assertGoldenRoundTrip(t, "pacs002.xml")

	doc, ok := env.Document.(*Pacs002)
	if !ok {
		t.Fatalf("Document is %T, want *Pacs002", env.Document)
	}
	rpt := doc.FIToFIPmtStsRpt

	if got := rpt.OrgnlGrpInfAndSts.GrpSts; got != GroupStatusPartiallyAccepted {
		t.Fatalf("GrpSts = %q, want PART", got)
	}
	if got := rpt.OrgnlGrpInfAndSts.OrgnlMsgNmId; got != "pacs.008.001.08" {
		t.Fatalf("OrgnlMsgNmId = %q, want pacs.008.001.08", got)
	}
	if len(rpt.TxInfAndSts) != 2 {
		t.Fatalf("TxInfAndSts has %d entries, want 2", len(rpt.TxInfAndSts))
	}

	accepted := rpt.TxInfAndSts[0]
	if accepted.TxSts != TransactionStatusAccepted {
		t.Fatalf("first TxSts = %q, want ACCP", accepted.TxSts)
	}
	if accepted.StsRsnInf != nil {
		t.Fatalf("an accepted transaction carries a status reason: %#v", accepted.StsRsnInf)
	}

	rejected := rpt.TxInfAndSts[1]
	if rejected.TxSts != TransactionStatusRejected {
		t.Fatalf("second TxSts = %q, want RJCT", rejected.TxSts)
	}
	if rejected.StsRsnInf == nil {
		t.Fatal("a rejected transaction carries no status reason")
	}
	if got := *rejected.StsRsnInf.Rsn.Cd; got != StatusReasonInsufficientFunds {
		t.Fatalf("reason = %q, want AM04", got)
	}
}

// TestPacs002CarriesItsOwnStatusIdentifiers pins the two EPC-mandatory,
// ISO-optional elements that make a status attributable: the reference the
// issuer gives to THIS status, and the identity of the party that issued it.
func TestPacs002CarriesItsOwnStatusIdentifiers(t *testing.T) {
	env := assertGoldenRoundTrip(t, "pacs002.xml")
	rpt := env.Document.(*Pacs002).FIToFIPmtStsRpt

	for i, want := range []string{"CSMBFRPPXXX-STS-000123-01", "CSMBFRPPXXX-STS-000123-02"} {
		if got := rpt.TxInfAndSts[i].StsId; got != want {
			t.Fatalf("TxInfAndSts[%d].StsId = %q, want %q", i, got, want)
		}
		if got := rpt.TxInfAndSts[i].OrgnlTxId; got == rpt.TxInfAndSts[i].StsId {
			t.Fatalf("TxInfAndSts[%d]: StsId and OrgnlTxId are the same value %q; "+
				"the status's own reference is not the original payment's", i, got)
		}
	}

	orgtr := rpt.TxInfAndSts[1].StsRsnInf.Orgtr
	if orgtr == nil {
		t.Fatal("the rejected transaction's status reason names no originator")
	}
	if orgtr.Nm != "" {
		t.Fatalf("Orgtr/Nm = %q; the EPC guidelines allow a name only for a CSM with no BIC", orgtr.Nm)
	}
	if orgtr.Id == nil || orgtr.Id.OrgId == nil {
		t.Fatalf("Orgtr is not identified through Id/OrgId: %#v", orgtr)
	}
	if got := orgtr.Id.OrgId.AnyBIC; got != "CSMBFRPPXXX" {
		t.Fatalf("Orgtr/Id/OrgId/AnyBIC = %q, want the clearing house CSMBFRPPXXX", got)
	}
}

func TestPacs002Validate(t *testing.T) {
	valid := func() *Pacs002 {
		env := assertGoldenRoundTrip(t, "pacs002.xml")
		return env.Document.(*Pacs002)
	}

	t.Run("no original message id is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFIPmtStsRpt.OrgnlGrpInfAndSts.OrgnlMsgId = ""
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a transaction with no status is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFIPmtStsRpt.TxInfAndSts[0].TxSts = ""
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a transaction with no status identification is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFIPmtStsRpt.TxInfAndSts[0].StsId = ""
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a status reason with no originator is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFIPmtStsRpt.TxInfAndSts[1].StsRsnInf.Orgtr = nil
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("an originator identified in no way at all is a missing element", func(t *testing.T) {
		d := valid()
		d.FIToFIPmtStsRpt.TxInfAndSts[1].StsRsnInf.Orgtr = &PartyIdentification{}
		if err := d.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("an originator claiming both arms of Party38Choice is an invalid choice", func(t *testing.T) {
		d := valid()
		orgtr := d.FIToFIPmtStsRpt.TxInfAndSts[1].StsRsnInf.Orgtr
		orgtr.Id.PrvtId = &PersonIdentification{
			Othr: GenericPersonIdentification{
				Id:      "FR12ZZZ123456",
				SchmeNm: PersonIdentificationScheme{Prtry: "SEPA"},
			},
		}
		if err := d.validate(); !errors.Is(err, ErrInvalidChoice) {
			t.Fatalf("validate() = %v, want it to wrap ErrInvalidChoice", err)
		}
	})
	t.Run("a status reason with both arms is an invalid choice", func(t *testing.T) {
		d := valid()
		prtry := "made up"
		d.FIToFIPmtStsRpt.TxInfAndSts[1].StsRsnInf.Rsn.Prtry = &prtry
		if err := d.validate(); !errors.Is(err, ErrInvalidChoice) {
			t.Fatalf("validate() = %v, want it to wrap ErrInvalidChoice", err)
		}
	})
	t.Run("a status reason with neither arm is an invalid choice", func(t *testing.T) {
		d := valid()
		d.FIToFIPmtStsRpt.TxInfAndSts[1].StsRsnInf.Rsn.Cd = nil
		if err := d.validate(); !errors.Is(err, ErrInvalidChoice) {
			t.Fatalf("validate() = %v, want it to wrap ErrInvalidChoice", err)
		}
	})
}
