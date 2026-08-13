package payment

import (
	"testing"
	"time"

	"github.com/raphi011/cbs/iso20022"
)

// Which payments a document names is a domain question, and the one case that
// is not obvious is the answer to a settlement instruction.
func TestWhichPaymentsADocumentNames(t *testing.T) {
	mc := MessageContext{From: "CSMXFRPPXXX", To: "CBSEDEFFXXX", MsgID: "MSG-1", Now: time.Now()}

	statusAbout := func(t *testing.T, orig OriginalMessage, id string) iso20022.Document {
		t.Helper()
		env, err := StatusMessage(orig, []TransactionStatusReport{{
			TxID:   id,
			Status: iso20022.TransactionStatusSettlementCompleted,
		}}, mc)
		if err != nil {
			t.Fatalf("StatusMessage: %v", err)
		}
		return env.Document
	}

	// A status about a payment file names the payment.
	about := OriginalMessage{MsgID: "MSG-0", MsgDefIdr: iso20022.Pacs008{}.MessageDefinitionIdentifier()}
	got := PaymentsIn(statusAbout(t, about, "pay_1"))
	if len(got) != 1 || got[0] != "pay_1" {
		t.Errorf("a status about a pacs.008 names %v, want [pay_1]", got)
	}

	// A status about a SETTLEMENT INSTRUCTION names the cut-off, and a cut-off is
	// not a payment. Recording it as one would put a cycle id in the join that
	// takes a payment to its documents.
	aboutSettlement := OriginalMessage{MsgID: "MSG-0", MsgDefIdr: iso20022.Pacs009{}.MessageDefinitionIdentifier()}
	if got := PaymentsIn(statusAbout(t, aboutSettlement, "cyc_1")); len(got) != 0 {
		t.Errorf("a status about a pacs.009 names %v, want nothing: a cut-off's positions net M payments", got)
	}

	// And a document that carries no payment at all names none rather than
	// failing: a statement is one account's movement.
	if got := PaymentsIn(&iso20022.Camt053{}); len(got) != 0 {
		t.Errorf("a camt.053 names %v, want nothing", got)
	}
}
