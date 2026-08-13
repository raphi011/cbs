// Package node is what the three institutions share: the environment each is
// handed, the journal each reports into, and the message rules none of them
// owns. It names no institution, so it carries no crossing.
package node

import (
	"log/slog"
	"time"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// Env is what an institution needs from the deployment it runs in and could get
// from any other: a clock, somewhere to log, somewhere to report, and the two
// addresses it dials.
type Env struct {
	Now       func() time.Time
	Log       *slog.Logger
	Journal   Journal
	NextMsgID func(from iso20022.BIC) string

	CentralBankBIC   iso20022.BIC
	ClearingHouseBIC iso20022.BIC
}

// MessageContext is the header every message an institution builds carries: who
// to, from whom, under which id, at what business date.
func (e Env) MessageContext(from, to iso20022.BIC) payment.MessageContext {
	return payment.MessageContext{From: from, To: to, MsgID: e.NextMsgID(from), Now: e.Now()}
}

// A Journal is where an institution says what it did. It is an interface
// because the report belongs to the deployment and an institution knows
// nothing about one.
type Journal interface {
	File(FileMoved)
	Outcome(TransactionOutcome)
}
