package centralbank

import (
	"context"

	"github.com/raphi011/cbs/payment"
)

// ops is the settlement agent's view NARROWED: what a settlement handler may
// reach.
type ops interface {
	// SettleCycle takes the LEGS as well as the cycle id, which is the shape
	// SettleReturn below has and for the same reason: everything this institution
	// acts on comes off the message.
	SettleCycle(ctx context.Context, id payment.CycleID, legs []payment.SettlementLeg) (payment.Settlement, []payment.SettlementStatement, error)

	// SettleReturn takes the INSTRUCTION rather than a payment id: everything it
	// acts on came off the pacs.004 (payment.ReadReturn), because a settlement
	// agent holds no payment rows and never saw the payment clear.
	SettleReturn(ctx context.Context, in payment.ReturnInstruction) ([]payment.SettlementStatement, error)

	// ReceiveLodgement is the third: a member has asked this institution to credit
	// the member's own reserve account, and this institution posts Debit
	// Settlement Assets / Credit Reserve: <member> in its own book.
	ReceiveLodgement(ctx context.Context, in payment.LodgementInstruction) (payment.LodgementReceipt, error)

	// This institution's own record of a file it sent or received. It is the same
	// act at all three institutions; see node.Record.
	RecordMessage(ctx context.Context, m payment.Message) error
}

// The settlement agent's type satisfies the settlement agent's interface, and
// this assertion is what keeps that true.
var _ ops = (*payment.CentralBankNetwork)(nil)
