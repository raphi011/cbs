package centralbank

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/node"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/testenv"
)

const (
	agentBIC  = "CBSEDEFFXXX"
	memberBIC = "AURODEFFXXX"
	csmBIC    = "CSMXFRPPXXX"
)

var testTime = time.Date(2024, 3, 4, 10, 0, 0, 0, time.UTC)

// silentJournal takes what the settlement agent reports and says nothing about
// it. What went on the wire is read off the host's queues instead.
type silentJournal struct{}

func (silentJournal) File(node.FileMoved)             {}
func (silentJournal) Outcome(node.TransactionOutcome) {}

// refusing is a settlement agent whose lodgement act fails with one error. It
// keeps what it is asked to record and is otherwise unreachable: the two tests
// below drive that act alone, and every file this agent addresses goes through
// the message log on the way out.
type refusing struct {
	ops
	err      error
	recorded []payment.Message
}

func (o *refusing) ReceiveLodgement(context.Context, payment.LodgementInstruction) (payment.LodgementReceipt, error) {
	return payment.LodgementReceipt{}, o.err
}

func (o *refusing) RecordMessage(_ context.Context, m payment.Message) error {
	o.recorded = append(o.recorded, m)
	return nil
}

// assertRecorded checks one row of the agent's own message log.
func assertRecorded(t *testing.T, m payment.Message, dir payment.MessageDirection,
	other iso20022.BIC, msgDef string,
) {
	t.Helper()
	if m.Direction != dir {
		t.Errorf("the file was recorded as %s, want %s", m.Direction, dir)
	}
	if m.Counterparty != other {
		t.Errorf("the file was recorded against %s, want %s", m.Counterparty, other)
	}
	if m.MsgDefIdr != msgDef {
		t.Errorf("the file was recorded as a %s, want a %s", m.MsgDefIdr, msgDef)
	}
	if len(m.Payload) == 0 {
		t.Error("the file was recorded with no payload; the log keeps the bytes")
	}
}

// agent is the settlement agent over a real host with the member and the
// clearing house enrolled, and whatever view of the domain the test hands it.
func agent(t *testing.T, view ops) *CentralBank {
	t.Helper()

	host := ebics.NewServer(testenv.NewSet(t, func() time.Time { return testTime }).CentralBankEBICS())
	host.Enrol(memberBIC)
	host.Enrol(csmBIC)

	seq := 0
	env := node.Env{
		Now:     func() time.Time { return testTime },
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Journal: silentJournal{},
		NextMsgID: func(from iso20022.BIC) string {
			seq++
			return fmt.Sprintf("%s-%d", from, seq)
		},
		CentralBankBIC:   agentBIC,
		ClearingHouseBIC: csmBIC,
	}
	return &CentralBank{env: env, ops: view, bic: agentBIC, host: host}
}

// queued is everything waiting for one subscriber, as the documents it carries.
func queued(t *testing.T, c *CentralBank, to iso20022.BIC) []iso20022.Envelope {
	t.Helper()

	files, err := c.host.Download(context.Background(), ebics.SubscriberID(to), ebics.BTD)
	if err != nil && ebics.CodeOf(err) != ebics.NoDownloadDataAvailable {
		t.Fatalf("Download for %s: %v", to, err)
	}
	out := make([]iso20022.Envelope, 0, len(files))
	for _, f := range files {
		env, err := iso20022.Unmarshal(f.Payload)
		if err != nil {
			t.Fatalf("a queued file does not parse: %v", err)
		}
		out = append(out, env)
	}
	return out
}

// lodgement is the camt.050 a member uploads to ask for a reserve credit.
func lodgement(t *testing.T, ref string) (iso20022.AppHdr, *iso20022.Camt050) {
	t.Helper()

	env, err := payment.LodgementMessage(payment.LodgementInstruction{
		BIC:     memberBIC,
		Agent:   agentBIC,
		Account: "acct_reserve_aurora",
		Asset:   "EUR",
		Amount:  40_000,
		Ref:     ref,
	}, payment.MessageContext{From: memberBIC, To: agentBIC, MsgID: ref, Now: testTime})
	if err != nil {
		t.Fatalf("LodgementMessage: %v", err)
	}
	return env.AppHdr, env.Document.(*iso20022.Camt050)
}

// TestAStoreFailureAtTheAgentIsNotARefusal is the money test on this
// conversation, and the defect it pins was live until a whole-branch review
// found it.
func TestAStoreFailureAtTheAgentIsNotARefusal(t *testing.T) {
	broken := errors.New("store: the retry budget ran out")
	view := &refusing{err: broken}
	c := agent(t, view)
	hdr, doc := lodgement(t, "lodge-store-failure")

	err := c.receiveLodgement(context.Background(), memberBIC, hdr, doc)
	if err == nil {
		t.Fatal("a store failure at the settlement agent was ANSWERED rather than reported; " +
			"the member has posted its leg and is now being told the lodgement did not happen")
	}
	if !errors.Is(err, broken) {
		t.Errorf("the reported problem does not carry the cause: %v", err)
	}
	if sent := queued(t, c, memberBIC); len(sent) != 0 {
		t.Errorf("a store failure queued %d files for the member, want none; "+
			"there is nothing true to tell it", len(sent))
	}
}

// TestALodgementRefusalIsAJudgement is the other side of the same
// discrimination, and it is what stops the fix for the test above from being
// "never answer".
func TestALodgementRefusalIsAJudgement(t *testing.T) {
	for _, sentinel := range lodgementRefusals {
		t.Run(sentinel.Error(), func(t *testing.T) {
			refusal := fmt.Errorf("payment: %s lodges EUR: %w", memberBIC, sentinel)
			view := &refusing{err: refusal}
			c := agent(t, view)
			hdr, doc := lodgement(t, "lodge-refused")

			if err := c.receiveLodgement(context.Background(), memberBIC, hdr, doc); err != nil {
				t.Fatalf("a judgement about the request became a reported problem: %v", err)
			}
			sent := queued(t, c, memberBIC)
			if len(sent) != 1 {
				t.Fatalf("a refusal queued %d files for the member, want the camt.025 that says so", len(sent))
			}
			receipt, ok := sent[0].Document.(*iso20022.Camt025)
			if !ok {
				t.Fatalf("the member was queued a %T, want a camt.025", sent[0].Document)
			}
			if got := receipt.Rct.RctDtls[0].ReqHdlg[0].StsCd; got != string(iso20022.TransactionStatusRejected) {
				t.Errorf("the receipt reports %q, want %q", got, iso20022.TransactionStatusRejected)
			}
			// And the agent kept its own record of what it addressed, which is the
			// half of a crossing this institution is the one to write down.
			if len(view.recorded) != 1 {
				t.Fatalf("the agent recorded %d files, want the one it addressed", len(view.recorded))
			}
			rec := view.recorded[0]
			assertRecorded(t, rec, payment.MessageSent, memberBIC, iso20022.Camt025{}.MessageDefinitionIdentifier())
		})
	}
}

// TestTheSettlementAgentCannotAnswerYesWithAReason: a pacs.002 carries
// StsRsnInf only for a rejection, so a cause passed beside SettlementCompleted
// sets a code and a text the builder then silently drops — a message saying
// everything is fine, with the reason it was not deleted on the way out.
func TestTheSettlementAgentCannotAnswerYesWithAReason(t *testing.T) {
	c := agent(t, &refusing{})

	err := c.answer(context.Background(), csmBIC,
		payment.OriginalMessage{MsgID: node.NotProvided, MsgDefIdr: node.NotProvided},
		node.NotProvided, "cyc_x",
		iso20022.TransactionStatusSettlementCompleted,
		payment.ErrCycleNotFound)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}

	sent := queued(t, c, csmBIC)
	if len(sent) != 1 {
		t.Fatalf("the answer queued %d files for the clearing house, want one", len(sent))
	}
	status := sent[0].Document.(*iso20022.Pacs002)
	tx := status.FIToFIPmtStsRpt.TxInfAndSts[0]
	if tx.TxSts != iso20022.TransactionStatusRejected {
		t.Fatalf("an answer built with a cause reports %v, want RJCT — the reason is dropped on any other status", tx.TxSts)
	}
	if tx.StsRsnInf == nil || tx.StsRsnInf.Rsn.Cd == nil {
		t.Fatalf("the rejection carries no reason code: %#v", tx.StsRsnInf)
	}
}
