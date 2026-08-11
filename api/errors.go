package api

import (
	"errors"
	"net/http"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
)

// errorStatus maps a domain sentinel error to an HTTP status code. Unknown
// errors fall through to 500. The categories are:
//
//   - 404 Not Found: an entity referenced by ID does not exist.
//   - 409 Conflict: a duplicate or already-applied operation.
//   - 422 Unprocessable Entity: a well-formed request that violates business
//     state (insufficient funds, frozen account, invalid state transition).
//   - 400 Bad Request: malformed input (unbalanced/empty transaction, non-
//     positive amounts, text carrying a control character), and everything a
//     handler refuses before the core is ever called — a body that will not
//     decode, an enum naming nothing, a required parameter that is absent — each
//     of which arrives here as a BadRequest. A control character in the request
//     target is refused earlier still, by the middleware (see
//     screenRequestTarget).
func errorStatus(err error) int {
	var refused badRequest
	switch {
	case errors.As(err, &refused):
		return http.StatusBadRequest

	case errors.Is(err, ledger.ErrLedgerNotFound),
		errors.Is(err, ledger.ErrSubledgerNotFound),
		errors.Is(err, ledger.ErrAccountNotFound),
		errors.Is(err, ledger.ErrTransactionNotFound),
		errors.Is(err, deposit.ErrAccountNotFound),
		errors.Is(err, deposit.ErrHoldNotFound),
		errors.Is(err, deposit.ErrSnapshotNotFound),
		errors.Is(err, deposit.ErrIdentifierNotFound),
		errors.Is(err, payment.ErrParticipantNotFound),
		errors.Is(err, payment.ErrPaymentNotFound),
		errors.Is(err, payment.ErrMandateNotFound),
		errors.Is(err, payment.ErrCycleNotFound),
		errors.Is(err, payment.ErrSettlementNotFound),
		errors.Is(err, payment.ErrSchemeNotFound),
		errors.Is(err, payment.ErrAccountNotInParticipant),
		errors.Is(err, lending.ErrFacilityNotFound),
		errors.Is(err, product.ErrProductNotFound),
		// A product with no published version in force is a price that does not
		// exist yet, which is a missing resource rather than a refused one.
		errors.Is(err, product.ErrVersionNotFound):
		return http.StatusNotFound

	case errors.Is(err, ledger.ErrDuplicateIdempotencyKey),
		errors.Is(err, ledger.ErrTransactionAlreadyReversed),
		errors.Is(err, payment.ErrDuplicateEndToEndID),
		errors.Is(err, payment.ErrCycleAlreadyOpen),
		// A billing cycle already on the schedule is the already-applied
		// category exactly: the request is valid and the state already
		// reflects it. 409 is what tells a retrying proxy that its first
		// attempt landed, where 422 would read as "this line cannot be
		// billed".
		errors.Is(err, lending.ErrCycleAlreadyBilled),
		// An identifier another account at this bank already holds is the same
		// already-applied shape: the request is well formed, and what stops it is
		// that the address is spoken for.
		errors.Is(err, deposit.ErrIdentifierTaken),
		// An address that resolves to more than one account is a conflict IN THE
		// DATA, not a malformed request: the answer exists and is contested, which
		// is what tells a client the situation needs a human rather than a retry.
		errors.Is(err, deposit.ErrIdentifierAmbiguous):
		return http.StatusConflict

	case errors.Is(err, ledger.ErrInsufficientBalance),
		errors.Is(err, deposit.ErrInsufficientAvailable),
		errors.Is(err, deposit.ErrAccountFrozen),
		// Dormancy is an ordinary account state with an ordinary rule — credits
		// revive it, debits wait for a reactivation — so a refused debit is a
		// business-state violation like a frozen one, not malformed input.
		errors.Is(err, deposit.ErrAccountDormant),
		errors.Is(err, deposit.ErrAccountClosed),
		errors.Is(err, deposit.ErrAccountNotEmpty),
		errors.Is(err, deposit.ErrHoldNotActive),
		errors.Is(err, deposit.ErrInvalidStatusTransition),
		errors.Is(err, payment.ErrCycleNotOpen),
		errors.Is(err, payment.ErrCycleNotClosed),
		errors.Is(err, payment.ErrInvalidStateTransition),
		errors.Is(err, payment.ErrMandateRevoked),
		errors.Is(err, payment.ErrMandateMismatch),
		errors.Is(err, payment.ErrMandateExceeded),
		errors.Is(err, payment.ErrMandateRequired),
		errors.Is(err, payment.ErrSchemeUnsupportedReturn),
		errors.Is(err, payment.ErrAssetMismatch),
		// The bank exists and the asset exists; this bank simply holds no
		// accounts in it. 404 would read as "participant not found", and 422
		// matches the sibling underfunded-member failure.
		//
		// Two routes answer with it. POST /participants/{pid}/deposits, where the
		// funded account is in an asset its bank does not operate in; and
		// POST /payments, where the scheme's asset is — Mesh.Submit runs the
		// submitting bank's half on the caller's goroutine, so Bank.AccountsFor's
		// refusal comes back as a status code rather than as a message. Measured
		// on a bank admitted in dollars only, paying in a euro scheme: 422,
		// "participant does not hold accounts in this asset: EUR in Bank A".
		//
		// It does not reach a settlement route, because there is none: settling is
		// performed on instruction (mesh.centralBank), so that refusal goes back to
		// the clearing house as a pacs.002. Nor the reserve routes, which report a
		// missing account as a missing row (see api/centralbank.reserveRows).
		errors.Is(err, payment.ErrParticipantAssetNotFound),
		// A bank the settlement agent holds no account for is a bank that has
		// founded itself and not yet joined — a legitimate state since admission
		// became a conversation, and a refusal about that bank's membership
		// rather than about anything in the request. 422 for the same reason the
		// asset case above is: the request is well formed and the state refuses
		// it. Unlike that one it reaches a caller from a single route —
		// POST /participants/{pid}/deposits, whose customer account is fine and
		// whose bank has nowhere to place the cash on reserve.
		//
		// From nowhere else, and the reason is worth stating accurately because
		// the tidy version of it is false. The other sites that raise it are the
		// settlement ones — SettleCycleTx and SettleReturnTx, through
		// payment.settlementAccountTx — and in the running system those are
		// INSTRUCTED, so their refusal leaves as a pacs.002 and never as a status
		// code. But they are not only reached that way: seed.builder calls
		// Network.Deposit, SettleReturnTx and SettleCycleTx directly, and
		// seed.Populate runs inside POST /admin/reset (see cmd/server's Deployment.Reset), whose
		// error is written by this same function — so a seed that could produce
		// this sentinel would produce a 422 from the reset route too. It cannot,
		// and that is a property of the seed rather than of the code's shape:
		// seed.builder provisions each bank through package provision and stops
		// rather than building further if any of the four acts fails, so every
		// reserve the scenario funds or settles already has an account behind it. The reserve routes are the remaining readers,
		// and they report a missing account as a missing row.
		errors.Is(err, payment.ErrSettlementMemberNotFound),
		errors.Is(err, lending.ErrFacilityClosed),
		errors.Is(err, lending.ErrFacilityNotEmpty),
		errors.Is(err, lending.ErrLimitExceeded),
		errors.Is(err, lending.ErrAlreadyDisbursed),
		errors.Is(err, lending.ErrNothingOutstanding),
		// Its mirror: the bank owes this facility nothing to refund. Same
		// category and so the same status — a well-formed request against state
		// that has nothing for it to do.
		errors.Is(err, lending.ErrNoRefundOutstanding),
		// ErrWrongFacilityKind is 422 rather than 400 deliberately: the request
		// is well formed and the field values are valid, but this facility is
		// the wrong product for the operation — the same category as
		// ErrCycleNotOpen.
		errors.Is(err, lending.ErrWrongFacilityKind),
		// The catalogue's four refusals are the same category: each request is
		// well formed and its field values are valid, and the state is what
		// refuses it. A retired product is on the books but off sale, a
		// published version is frozen by design, a backdated publication is
		// refused outright because its blast radius is every account on the
		// product, and a terms row with no product cannot resolve a price.
		errors.Is(err, product.ErrProductRetired),
		errors.Is(err, product.ErrKindMismatch),
		errors.Is(err, product.ErrVersionPublished),
		errors.Is(err, product.ErrRetroactivePublish),
		errors.Is(err, deposit.ErrProductRequired),
		// Two accounts at one bank in two currencies. Both are real, the request
		// is well formed, and what refuses it is that the amount would have to
		// mean two different numbers — converting is a second operation with a
		// price in it, and this bank has no desk that quotes one. Same category
		// as payment.ErrAssetMismatch, which is the same rule across two books.
		errors.Is(err, deposit.ErrAssetMismatch),
		// The account exists and the request is well formed; it simply carries no
		// address in the kind the scheme routes on, or the address it quoted
		// belongs to a different account, or it quoted none and the account has
		// several the scheme could route on so initiation will not pick one. All
		// three are business-state refusals, the same category as a frozen
		// account or an unbalanced state transition — and the third stays 422
		// rather than joining ErrIdentifierAmbiguous at 409 because nothing in
		// the data is contested: the caller can fix it by naming an address.
		errors.Is(err, payment.ErrUnaddressableAccount),
		errors.Is(err, payment.ErrIdentifierMismatch),
		errors.Is(err, payment.ErrAmbiguousAddress),
		// The request is well-formed JSON that named no counterparty (or an
		// invalid one) — the same category as an unaddressable account: nothing
		// on the submission path looks the other side up, so the instruction
		// must carry it.
		errors.Is(err, payment.ErrCounterpartyNotNamed),
		// Its sibling, and now the narrow case: the counterparty's BIC is derived
		// from their address, so it is only ever asserted for an address no
		// directory here covers. Same category and same 422 — the caller can fix it
		// by supplying one.
		errors.Is(err, payment.ErrCounterpartyAgentNotNamed),
		// The address is well formed, its check digits verify, and this bank's copy
		// of the routing directory has no entry for the code inside it. Same
		// category and the same 422, and the status is the whole of what can
		// honestly be said: the bank cannot tell "no such member" from "my copy is
		// behind", so a 404 claiming the payee's bank does not exist would be
		// asserting something a subscriber has no way to know. The remedy is a
		// refresh, or giving up, and only the caller can choose. See
		// payment.ErrBankCodeUnknown.
		errors.Is(err, payment.ErrBankCodeUnknown),
		// And the clearing house's version of the same shape, from the one route
		// that reads the roster to decide which bank an instruction belongs to: an
		// address under a code no member is published under. Same category, same
		// 422, and NOT a 404 — an address nobody holds is a well-formed field this
		// system will not act on, not a missing resource. See
		// payment.Network.RosterAgentFor.
		errors.Is(err, payment.ErrRosterEntryNotFound),
		// A malformed BIC is well-formed JSON naming a field that is not a
		// structurally valid ISO 9362 code — the same category as an
		// unaddressable account, not a decoding failure.
		errors.Is(err, iso20022.ErrBICFormat),
		// The same category one identifier along: a bank code that is not the
		// width its country allocates, a country nothing here issues in, or an
		// address whose check digits do not verify. Every one of them is a
		// present, well-typed field failing a business rule, and every one is
		// fixable by the caller. They reach here from the deposit routes — a caller
		// supplying an address, or a bank minting one under its allocation.
		//
		// Listed as a group rather than one by one because the group IS the
		// category — "this is not a well-formed address or allocation" — and a
		// caller can do nothing different about any of them. The message says
		// which.
		errors.Is(err, iban.ErrUnknownCountry),
		errors.Is(err, iban.ErrLength),
		errors.Is(err, iban.ErrCharacter),
		errors.Is(err, iban.ErrCheckDigits),
		errors.Is(err, iban.ErrNationalCheck),
		errors.Is(err, iban.ErrBankCodeWidth),
		errors.Is(err, iban.ErrSerialTooLarge),
		// A caller supplying an address the BANK issues. Well-formed, and refused
		// because it is not the caller's to say — see deposit.ErrIBANIsIssued.
		errors.Is(err, deposit.ErrIBANIsIssued),
		// A bank asked to open an account before any registry allocated it a bank
		// code. A real state, and 422 rather than 409: nothing is contested, the
		// bank simply has no addresses to give out yet.
		errors.Is(err, deposit.ErrNoIssuer),
		// An on-us payment is well formed, names two real accounts and is a
		// perfectly legitimate thing to want; what refuses it is that this route
		// does not carry it. Both parties bank at one institution, so nothing
		// clears — see payment.ErrOnUsPayment. The same category as an unaddressable
		// account, and the reason it is mapped here rather than in the handler
		// (as mesh.ErrAddressTaken is): two handlers submit through Mesh.Submit,
		// and a rule written twice is a rule that can differ by route.
		errors.Is(err, payment.ErrOnUsPayment),
		// A payment to or from a bank the scheme has not admitted is the same
		// category again: the request is well formed, both accounts are real, and
		// what refuses it is that this route does not carry it — a founded bank
		// has a licence and a book and no place in a clearing scheme. It is
		// mapped here rather than in the handler for payment.ErrOnUsPayment's stated
		// reason, and it arrives here from Mesh.Submit; the clearing house's own
		// copy of the refusal is asked after the 202 has been answered and is
		// reported as a rejected payment, not as a status. See
		// payment.ErrBankNotAdmitted.
		errors.Is(err, payment.ErrBankNotAdmitted):
		return http.StatusUnprocessableEntity

	case errors.Is(err, ledger.ErrEmptyTransaction),
		errors.Is(err, ledger.ErrUnbalancedTransaction),
		errors.Is(err, ledger.ErrInvalidAmount),
		errors.Is(err, ledger.ErrInvalidText),
		// A leg that names a control account and no subsidiary, or a plain account
		// and one. 400 for ErrUnbalancedTransaction's reason: the request is
		// wrong about what it is posting to, and no account's state would let
		// it through on a retry.
		errors.Is(err, ledger.ErrSubsidiaryRequired),
		errors.Is(err, ledger.ErrSubsidiaryNotAllowed),
		// Assets are a fixed list in code, so an unknown code is a bad field
		// value like an unparseable account type — not a missing resource.
		errors.Is(err, ledger.ErrAssetNotFound),
		errors.Is(err, deposit.ErrInvalidAmount),
		errors.Is(err, deposit.ErrInvalidRate),
		// A transfer naming one account twice. 400 and not 422 because nothing
		// about either account's state refuses it: the request contradicts
		// itself, exactly as an unbalanced transaction does.
		errors.Is(err, deposit.ErrSameAccount),
		errors.Is(err, product.ErrInvalidRate),
		errors.Is(err, product.ErrNameRequired),
		errors.Is(err, payment.ErrInvalidPaymentAmount),
		errors.Is(err, lending.ErrInvalidAmount),
		errors.Is(err, lending.ErrInvalidRate),
		errors.Is(err, lending.ErrInvalidTerm):
		return http.StatusBadRequest

	// product.ErrHashMismatch is deliberately NOT mapped, so it falls through to
	// 500. A published version whose content no longer matches its hash means
	// stored data was edited behind the system's back: the caller did nothing
	// wrong, and a 4xx would tell them to fix their request.
	default:
		return http.StatusInternalServerError
	}
}
