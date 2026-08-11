package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/raphi011/cbs/iso20022"
)

// What this file holds is what is left of the transport's tests once the
// transport moved: the fixtures every other suite here builds messages from,
// and the three claims that are the MESH's rather than the wire's — that the
// two configured institutions become actors, that a configuration it could not
// route is refused, and that an actor with no behaviour yet refuses what it is
// sent instead of eating it.
//
// Everything about queues, Drain, Stop and dead letters is in mesh/wire, tested
// against raw bytes. See wire's own suite.

func drainCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// testTime is the instant every test envelope is stamped with. Fixed, because a
// message whose bytes depend on the clock cannot be compared.
var testTime = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

// testConfig names the two institutions. Their actors exist in every test mesh
// alongside A, B and C.
var testConfig = MeshConfig{CentralBankBIC: "CBSEDEFFXXX", ClearingHouseBIC: "CSMXFRPPXXX"}

// newTestMesh builds a mesh of three actors with no-op handlers and NO
// payment.Network. Nothing that uses it needs one: what it exercises is
// routing and registration, and a test that needed a store to run would be
// testing the wrong thing.
func newTestMesh(t *testing.T) *Mesh {
	t.Helper()
	m, err := NewMesh(nil, testConfig, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, bic := range []iso20022.BIC{"AAAADEFFXXX", "BBBBDEFFXXX", "CCCCDEFFXXX"} {
		if err := m.bus.AddActor(bic, string(bic), func(context.Context, iso20022.BIC, []byte) error { return nil }); err != nil {
			t.Fatalf("AddActor %s: %v", bic, err)
		}
	}
	return m
}

// testEnvelope is a minimal but VALID pacs.002. Valid matters: send marshals
// before it routes, so an envelope that failed validation would never reach a
// queue and every test using it would be asserting on the marshaller instead of
// on what it meant to. TestTestEnvelopeMarshals holds the helper to it.
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

// testRejection is testEnvelope's rejecting twin: the same valid pacs.002,
// saying no.
//
// The two are not interchangeable, and which one a test wants is a question
// about behaviour rather than about fixtures. An acceptance is a message a
// payer's bank has nothing to do about, so a handler that received one and did
// nothing is indistinguishable from one that was never run. A rejection is work.
func testRejection(from, to iso20022.BIC, id string) iso20022.Envelope {
	env := testEnvelope(from, to, id)
	doc := env.Document.(*iso20022.Pacs002)
	doc.FIToFIPmtStsRpt.OrgnlGrpInfAndSts.GrpSts = iso20022.GroupStatusRejected
	code := iso20022.StatusReasonNotSpecifiedAgentGenerated
	doc.FIToFIPmtStsRpt.TxInfAndSts[0].TxSts = iso20022.TransactionStatusRejected
	doc.FIToFIPmtStsRpt.TxInfAndSts[0].StsRsnInf = &iso20022.StatusReasonInformation{
		Orgtr: &iso20022.PartyIdentification{
			Id: &iso20022.PartyChoice{OrgId: &iso20022.OrganisationIdentification{AnyBIC: from}},
		},
		Rsn: iso20022.StatusReasonChoice{Cd: &code},
	}
	return env
}

// The helper the other suites depend on has to be checked by a test of its own.
// If testEnvelope stopped being valid, send would fail at the marshaller and
// several tests would go on passing for the wrong reason — an assertion about a
// refusal would be satisfied by the marshaller's refusal rather than the mesh's.
func TestTestEnvelopeMarshals(t *testing.T) {
	if _, err := iso20022.Marshal(testEnvelope("AAAADEFFXXX", "BBBBDEFFXXX", "x")); err != nil {
		t.Fatalf("the test envelope does not marshal: %v", err)
	}
}

// An actor with no behaviour yet REFUSES what it is sent: a message to one must
// not look like a message that was dealt with.
//
// The clearing house is the one addressed here because newTestMesh builds its
// mesh over NO network, and a clearing house with no payments to clear keeps the
// placeholder — see unhandled. On a mesh that has one, every actor has a real
// handler.
func TestAMessageToAnActorWithNoHandlerIsADeadLetter(t *testing.T) {
	m := newTestMesh(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	to := testConfig.ClearingHouseBIC
	if err := m.send("AAAADEFFXXX", to, testEnvelope("AAAADEFFXXX", to, "x")); err != nil {
		t.Fatalf("send: %v", err)
	}
	err := m.Drain(drainCtx(t))
	if err == nil {
		t.Fatal("Drain reported a clean mesh; the placeholder handler swallowed the message")
	}
	if !strings.Contains(err.Error(), string(to)) {
		t.Errorf("dead letter %q does not name the actor that produced it", err)
	}
}

// The two configured institutions get actors of their own, so that a message
// addressed to the clearing house or the central bank routes like any other.
func TestNewCreatesTheTwoInstitutions(t *testing.T) {
	m := newTestMesh(t)
	for _, bic := range []iso20022.BIC{testConfig.CentralBankBIC, testConfig.ClearingHouseBIC} {
		if !m.bus.Has(bic) {
			t.Errorf("no actor for %s", bic)
		}
	}
}

func TestNewRefusesAConfigItCannotRoute(t *testing.T) {
	cases := map[string]MeshConfig{
		"no clearing house":  {CentralBankBIC: "CBSEDEFFXXX"},
		"no central bank":    {ClearingHouseBIC: "CSMXFRPPXXX"},
		"malformed BIC":      {CentralBankBIC: "cbse", ClearingHouseBIC: "CSMXFRPPXXX"},
		"one BIC, two roles": {CentralBankBIC: "CBSEDEFFXXX", ClearingHouseBIC: "CBSEDEFFXXX"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewMesh(nil, cfg, slog.New(slog.DiscardHandler)); err == nil {
				t.Fatalf("NewMesh accepted %+v", cfg)
			}
		})
	}
}
