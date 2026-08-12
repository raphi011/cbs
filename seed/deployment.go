package seed

import (
	"context"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// A Deployment is the running system this scenario is built into: the acts the
// builder cannot perform for itself.
//
// Everything else the seed does it does directly, one institution's half at a
// time, through payment.Networks. What is left here is what needs a table no
// single institution owns — a bank's place in the network, the roster the
// clearing house publishes, the address of the agent that settles — and what
// needs a FILE, which is the last two.
//
// # Nothing here waits, and the last two are why that had to be arranged
//
// Every act below is synchronous and complete when it returns, which is what
// lets a fixed scenario be built without a business day running through it. For
// three of them that is free: they compose both halves of a conversation
// directly and no file exists.
//
// Submit and CarryToClearing are the pair that does not get it free. A
// submission joins its bank's hub and travels at that bank's next cut-off, so
// the two are one act split in two — and the seed calls the second itself,
// rather than waiting for a day, precisely so the dataset does not depend on
// when somebody advanced a clock. What it buys by uploading for real is the one
// thing a composed half cannot leave behind: the receiving bank's SHARE of the
// file, which is what the clearing house releases when the cycle settles.
//
// It is an interface because the deployment that satisfies it is a composition
// root and not a library: cmd/server holds the institutions, and a package it
// imports cannot name them. So the seed declares what it needs and is handed
// something that has it, which is the same shape api/bank's Institution takes
// one layer up.
//
// A nil pointer inside a non-nil interface is NOT caught by Populate's guard,
// and there is no defensive check for it here. The one field in this process
// that holds a Deployment refuses a nil at construction, so the value reaching
// this is either absent — which the guard does catch — or real.
type Deployment interface {
	// AddBank gives a provisioned bank its place in the network. A bank with rows
	// and no place can neither send nor be sent to.
	AddBank(context.Context, *payment.Bank) error

	// RefreshDirectory is one bank subscribing: it replaces that bank's copy of
	// the scheme's routing directory with the roster the clearing house
	// publishes. Two databases, and no bank may open the other's.
	RefreshDirectory(context.Context, iso20022.BIC) ([]payment.DirectoryEntry, error)

	// CentralBankBIC names the settlement agent, which has no roster row to be
	// read from: it is not a member of the scheme it settles. A settlement
	// instruction names it at one end of every leg.
	CentralBankBIC() iso20022.BIC

	// Submit hands a customer's instruction to the bank the scheme's direction
	// makes the submitter, which runs its own half and puts the payment in its
	// hub. Nothing has left that bank when this returns.
	//
	// The seed uses it for the payments it wants IN FLIGHT and composes the
	// halves itself for the ones it settles. Which door a payment came through is
	// invisible afterwards — the same rows, in the same three databases — and the
	// difference is the file, which only this one produces.
	Submit(context.Context, payment.InitiatePaymentRequest) (payment.Payment, error)

	// CarryToClearing reaches every member's cut-off, works the uploaded files
	// through the clearing house and collects each submitting bank's answers. It
	// settles nothing.
	//
	// One call carries every hub, so the seed makes it once with everything it
	// wants in flight already submitted. The payments come out Accepted, in the
	// open cycle for their scheme, with the receiving banks' shares held against
	// it — which is what the first business day releases.
	CarryToClearing(context.Context) error
}
