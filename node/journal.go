package node

import (
	"errors"

	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// FileMoved is one file that left one institution for another: put on a
// connection, or put in a subscriber's download queue.
type FileMoved struct {
	From      iso20022.BIC
	To        iso20022.BIC
	OrderType ebics.OrderType
	OrderID   ebics.OrderID
}

// TransactionOutcome is one institution's decision about one payment: what it
// said, and what it said it in.
type TransactionOutcome struct {
	DecidedBy iso20022.BIC
	Payment   payment.PaymentID
	Status    iso20022.TransactionStatus
	Code      iso20022.StatusReason
	Text      string
}

// Problem is a file an institution could not process.
type Problem struct {
	Institution iso20022.BIC
	OrderID     ebics.OrderID
	Detail      string
}

// JoinProblemDetails renders a set of problems as one error, for the doors that
// reach a phase of the day out of turn and have a caller to tell.
func JoinProblemDetails(ps []Problem) error {
	var errs []error
	for _, p := range ps {
		errs = append(errs, errors.New(p.Detail))
	}
	return errors.Join(errs...)
}
