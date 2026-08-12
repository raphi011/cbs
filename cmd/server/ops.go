package main

import (
	"context"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// The three narrowed views, one per kind of actor.
type bankOps interface {
	// The submitting bank's half. It sends NOTHING: the instruction joins this
	// bank's hub and travels at the next cut-off, which is what a bulk network is.
	// See Bank.submit.
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

	// The bank's own liquidity management, and the second thing on this interface
	// whose subject is this bank rather than a payment.
	LodgeReserves(ctx context.Context, asset ledger.AssetCode,
		amount ledger.Amount, mc payment.MessageContext) (payment.LodgementInstruction, iso20022.Envelope, error)
}

// csmOps is the clearing house's view: what a CSM handler may reach.
type csmOps interface {
	// The clearing house's own copy of an instruction it is carrying, written as
	// it routes one.
	RecordRelayedCreditTransfer(ctx context.Context, doc *iso20022.Pacs008) ([]payment.Payment, error)
	RecordRelayedDirectDebit(ctx context.Context, doc *iso20022.Pacs003) ([]payment.Payment, error)

	AcceptAtCSM(ctx context.Context, id payment.PaymentID) (payment.Payment, error)
	RejectAtCSM(ctx context.Context, id payment.PaymentID, code iso20022.StatusReason, reason string) (payment.Payment, error)

	// The two the clearing house needs to keep its OWN copies honest once the
	// decision is somebody else's to make.
	SettleAtCSM(ctx context.Context, id payment.CycleID) ([]payment.Payment, error)
	CompleteReturn(ctx context.Context, id payment.PaymentID) (payment.Payment, error)

	// Scheme is what says which bank a status goes back to: the payer's for a
	// push, the payee's for a pull.
	Scheme(id payment.SchemeID) (payment.Scheme, bool)

	// The cut-off, and what it has to say afterwards.
	CloseCycle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error)
	GetCycle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error)
	GetPayment(ctx context.Context, id payment.PaymentID) (payment.Payment, error)

	// The two ends of a clearing day, and the pair that makes the cut-off an event
	// on a calendar rather than a route somebody remembers to call.
	ListCycles(ctx context.Context) ([]payment.ClearingCycle, error)
	OpenCycle(ctx context.Context, scheme payment.SchemeID) (payment.ClearingCycle, error)

	// ListSchemes is which cycles there are to open. The scheme registry is a map
	// in memory rather than a table, which is why this takes no context.
	ListSchemes() []payment.Scheme

	// GetRosterEntryByBIC is the one lookup on any of these three interfaces that
	// crosses nothing.
	GetRosterEntryByBIC(ctx context.Context, bic iso20022.BIC) (payment.RosterEntry, error)

	// ListRosterEntries is the roster PUBLISHED: every member, as the directory a
	// subscriber pulls.
	ListRosterEntries(ctx context.Context) ([]payment.RosterEntry, error)

	// WHAT THIS INSTITUTION HAS TAKEN IN AND NOT YET HANDED OVER, which is the
	// only state in this package that is an obligation rather than a record. See
	// payment.HeldFile and payment.HeldReturn.
	HoldFile(ctx context.Context, f payment.HeldFile) error
	ListHeldFiles(ctx context.Context, id payment.CycleID) ([]payment.HeldFile, error)
	DropHeldFile(ctx context.Context, id payment.CycleID, seq int64) error

	HoldReturn(ctx context.Context, r payment.HeldReturn) error
	GetHeldReturn(ctx context.Context, id payment.PaymentID) (payment.HeldReturn, error)
	DropHeldReturn(ctx context.Context, id payment.PaymentID) error
}

// settlementOps is the central bank's view: what a settlement handler may
// reach.
type settlementOps interface {
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
}

// One institution's type satisfies one interface, and these assertions are what
// keep that true: a method added to an interface above that its institution
// does not have fails the build here rather than at the handler that wanted it,
// and a method moved to the wrong institution in payment fails here too, naming
// both.
var (
	_ bankOps       = (*payment.BankNetwork)(nil)
	_ csmOps        = (*payment.ClearingHouseNetwork)(nil)
	_ settlementOps = (*payment.CentralBankNetwork)(nil)
)
