package bank

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/node"
	"github.com/raphi011/cbs/payment"
)

const (
	memberBIC = "AURODEFFXXX"
	agentBIC  = "CBSEDEFFXXX"
)

var testTime = time.Date(2024, 3, 4, 10, 0, 0, 0, time.UTC)

// unrecordable is a bank whose message log refuses every write. Its one other
// reachable act is the settlement leg, which is what a collected statement
// makes this bank do; everything else on the interface is unreachable and nil.
type unrecordable struct {
	ops
	booked []payment.AdvisedMovement
}

func (o *unrecordable) RecordMessage(context.Context, payment.Message) error {
	return errors.New("store: the retry budget ran out")
}

func (o *unrecordable) PostSettlementAdvice(_ context.Context, m payment.AdvisedMovement) (payment.SettlementAdvice, error) {
	o.booked = append(o.booked, m)
	return payment.SettlementAdvice{}, nil
}

// statement is the camt.053 the settlement agent addresses to a member: one
// account, one movement, which is what a member is told about its own reserve.
func statement(t *testing.T) []byte {
	t.Helper()

	env, err := payment.StatementMessage(payment.SettlementStatement{
		Agent:          agentBIC,
		Account:        "acct_reserve_aurora",
		Asset:          "EUR",
		Reference:      "cyc_1",
		StatementRef:   "stmt_1",
		Movement:       -25_000,
		ClosingBalance: 75_000,
		ValueDate:      testTime,
	}, payment.MessageContext{From: agentBIC, To: memberBIC, MsgID: "stmt-1", Now: testTime})
	if err != nil {
		t.Fatalf("StatementMessage: %v", err)
	}
	raw, err := iso20022.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return raw
}

// TestAFailedRecordDoesNotCostACollectedFile: Download empties the queue as it
// hands a file over, so the file exists only in memory by the time it is
// worked. A log is a record of what happened rather than a gate on it.
func TestAFailedRecordDoesNotCostACollectedFile(t *testing.T) {
	view := &unrecordable{}
	b := &Bank{
		env: node.Env{
			Now:            func() time.Time { return testTime },
			Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
			CentralBankBIC: agentBIC,
		},
		ops: view,
		bic: memberBIC,
	}

	err := b.handle(context.Background(), agentBIC,
		ebics.File{OrderID: "A001", OrderType: ebics.C53, Payload: statement(t)})
	if err != nil {
		t.Fatalf("a file whose record failed was reported as a problem: %v", err)
	}
	if len(view.booked) != 1 {
		t.Fatalf("the bank booked %d settlement legs, want the one the statement advised; "+
			"the file is out of the queue and exists nowhere, so nothing will book it later", len(view.booked))
	}
	if got := view.booked[0].Reference; got != "cyc_1" {
		t.Errorf("the leg was booked against %q, want the cycle the statement named", got)
	}
}
