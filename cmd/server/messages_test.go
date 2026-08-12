package main

import (
	"testing"
	"time"

	"github.com/raphi011/cbs/iso20022"
)

// The message fixtures every other suite in this package builds from, and the
// one test that holds them to being valid.

// testTime is the instant this package's fixtures start on.
var testTime = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

// testConfig names the two institutions every fixture here plays. The two URLs
// are filled in by whichever fixture starts the listeners, because a host's
// address is not known until it is bound.
var testConfig = Config{CentralBankBIC: "CBSEDEFFXXX", ClearingHouseBIC: "CSMXFRPPXXX"}

// testEnvelope is a minimal but VALID pacs.002.
func testEnvelope(from, to iso20022.BIC, id string) iso20022.Envelope {
	return iso20022.Envelope{
		AppHdr: iso20022.AppHdr{
			Fr:        iso20022.NewAgent(from),
			To:        iso20022.NewAgent(to),
			BizMsgIdr: id,
			MsgDefIdr: "pacs.002.001.10",
			CreDt:     iso20022.ISODateTime{Time: testTime},
		},
		Document: &iso20022.Pacs002{
			FIToFIPmtStsRpt: iso20022.FIToFIPaymentStatusReport{
				GrpHdr: iso20022.StatusGroupHeader{
					MsgId:   id,
					CreDtTm: iso20022.ISODateTime{Time: testTime},
				},
				OrgnlGrpInfAndSts: iso20022.OriginalGroupHeader{
					OrgnlMsgId:   "orig-" + id,
					OrgnlMsgNmId: "pacs.008.001.08",
					GrpSts:       iso20022.GroupStatusAccepted,
				},
				TxInfAndSts: []iso20022.PaymentTransactionStatus{{
					StsId:     id,
					OrgnlTxId: "tx-" + id,
					TxSts:     iso20022.TransactionStatusAccepted,
				}},
			},
		},
	}
}

// The helper the other suites depend on has to be checked by a test of its own.
func TestTestEnvelopeMarshals(t *testing.T) {
	if _, err := iso20022.Marshal(testEnvelope("AAAADEFFXXX", "BBBBDEFFXXX", "x")); err != nil {
		t.Fatalf("the test envelope does not marshal: %v", err)
	}
}
