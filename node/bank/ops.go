package bank

import (
	"context"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// ops is a member bank's view NARROWED: what a bank's own acts may reach.
type ops interface {
	// The submitting bank's half. It sends NOTHING: the instruction joins this
	// bank's hub and travels at the next cut-off, which is what a bulk network is.
	// See Bank.Submit.
	TakeInstruction(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error)

	// The file that cut-off then builds: one scheme's batch as the pacs.008 or
	// pacs.003 that carries it to the clearing house.
	InstructionMessage(ctx context.Context, ps []payment.Payment, mc payment.MessageContext) (iso20022.Envelope, error)

	// The receiving bank's half.
	CreditTransferRequest(ctx context.Context, doc *iso20022.Pacs008) ([]payment.InboundTransaction, error)
	DirectDebitRequest(ctx context.Context, doc *iso20022.Pacs003) ([]payment.InboundTransaction, error)
	AcceptInbound(ctx context.Context, id payment.PaymentID, req payment.InitiatePaymentRequest) error
	ReceiveUnapplied(ctx context.Context, id payment.PaymentID, req payment.InitiatePaymentRequest) (payment.Payment, error)

	// ResolveIdentifier is the on-us check, and the one method here called from a
	// door rather than from a handler: an instruction naming a payee this same
	// bank holds is not a payment this transport carries, and the bank that would
	// submit it is the only institution that can say so.
	ResolveIdentifier(ctx context.Context, ident deposit.Identifier) (payment.PartyRef, error)

	// This bank's own copy of a payment, which is the only one it has and the only
	// one it may read.
	GetPayment(ctx context.Context, id payment.PaymentID) (payment.Payment, error)

	// This bank's own row, which is the handle its end-of-day batches hang off:
	// payment.Bank.RunEndOfDay is on the row rather than on the network, because
	// accrual is a bank's own act over its own deposit and lending layers.
	GetBank(ctx context.Context, id payment.ParticipantID) (*payment.Bank, error)

	// The bank's half of a rejection: record it on this bank's own copy, and give
	// the payer their money back if this bank is the one holding it.
	RejectAtBank(ctx context.Context, id payment.PaymentID, code iso20022.StatusReason, reason string) (payment.Payment, error)

	// The bank's half of an acceptance, and the one of the three that posts
	// nothing.
	AcceptAtBank(ctx context.Context, id payment.PaymentID) (payment.Payment, error)

	// The returning bank's message. It takes no context because it reads no store
	// — the amount's scale comes from the scheme registry, a map in memory, and
	// everything it puts in OrgnlTxRef is on the payment it is handed.
	ReturnMessage(p payment.Payment, reason iso20022.ReturnReason, text string, mc payment.MessageContext) (iso20022.Envelope, error)

	// The two halves of a return that are a BANK's, one for each moment a bank
	// acts in one.
	PostReturnLeg(ctx context.Context, id payment.PaymentID, reason string) (payment.Payment, error)
	ReverseReturnLeg(ctx context.Context, id payment.PaymentID, reason string) error

	// The mirror leg. A member books it from the statement the settlement agent
	// sent; nothing else in this deployment may post in that book, and nothing on
	// this interface lets this bank post in anybody else's.
	PostSettlementAdvice(ctx context.Context, m payment.AdvisedMovement) (payment.SettlementAdvice, error)

	// The bank's half of settlement: record it on this bank's own copy, and — if
	// this bank holds the payee — release the payment out of its own clearing
	// suspense into its own customer's account.
	SettleAtBank(ctx context.Context, id payment.PaymentID) (payment.Payment, error)

	// The bank that ASKED for a return learning that it went through.
	CompleteReturn(ctx context.Context, id payment.PaymentID) (payment.Payment, error)

	// This bank's own copy of the scheme's routing directory, replaced with the
	// roster it is handed.
	RefreshDirectory(ctx context.Context, published []payment.RosterEntry) ([]payment.DirectoryEntry, error)

	// The scheme registry, which is what says whether an instruction names a
	// scheme this bank has joined and which side of one submits.
	Scheme(id payment.SchemeID) (payment.Scheme, bool)

	// This bank's own copy of the routing directory, which is where it asks
	// whether the two agents an instruction names are members. See Bank.member.
	ListDirectory(ctx context.Context) ([]payment.DirectoryEntry, error)

	// The bank's own liquidity management, and the second thing on this interface
	// whose subject is this bank rather than a payment.
	LodgeReserves(ctx context.Context, asset ledger.AssetCode,
		amount ledger.Amount, mc payment.MessageContext) (payment.LodgementInstruction, iso20022.Envelope, error)
}

// The bank's type satisfies the bank's interface, and this assertion is what
// keeps that true: a method added above that a bank does not have fails the
// build here rather than at the handler that wanted it.
var _ ops = (*payment.BankNetwork)(nil)
