package mesh

import (
	"context"
	"errors"
	"fmt"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// csm is the clearing house as an actor.
//
// It sits between two banks that never address each other. Everything a bank
// learns about the far side arrives through here, and everything this type does
// falls out of that: it ROUTES an instruction onward, and it CLEARS the answer —
// taking the payment into a cycle, or rejecting it — and passes that answer back
// to the bank that started it.
//
// The routing is where the two flows differ and the clearing is where they do
// not. A credit transfer goes to the agent named as the creditor's, a collection
// to the agent named as the debtor's, because a push travels towards the money's
// destination and a pull towards its source. What comes back is a pacs.002
// either way, and this actor treats it the same way either way.
//
// # It also sits between the banks and the settlement agent
//
// Task 12 gave it a second counterparty. Reaching a cut-off nets
// the batch and then INSTRUCTS the central bank to discharge the positions, in a
// pacs.009, because moving reserves is not something a clearing house may do.
// The answer to that instruction comes back here rather than to any bank, and
// this actor turns it into per-payment news — which it can and the central bank
// cannot, because it is the one that knows which payments are in the batch. See
// closeCycle and receiveSettlementStatus.
//
// # And it carries returns, HOLDING one end of the conversation
//
// Task 13 gave it the return, and this note called it the one where this actor
// does least: a pacs.004 handed straight to the central bank, the answer passed
// back to the bank that asked, nothing cleared and nothing netted. The first
// two clauses are still true and the "does least" is not. A return is a
// conversation with THREE participants and two of them never address each
// other, so the message that makes the second bank move its customer's money
// has to be carried by the institution that has seen both ends. This actor
// holds the pacs.004 until the settlement agent has said ACSC and only then
// relays it onward. See relayReturn, which is where that state lives and where
// its failure mode is recorded, and receiveReturnStatus, which releases it.
//
// It still clears nothing and nets nothing: a return in this system is final the
// moment the settlement agent posts it, and no cycle is touched.
//
// # And it admits, which is the one flow where it DECIDES rather than carries
//
// Task 17 gave it its first subject that is not a payment. An
// application for a settlement account is relayed to the settlement agent and
// the acknowledgement comes back, and out of that answer this actor writes its
// OWN row: the routing entry, which is what makes a bank reachable at all. It is
// the only row this institution owns, and the only place in this package where a
// clearing house writes something from a message it did not originate.
//
// It carries NOTHING between the two hops, unlike the return, and relayAdmission
// says why the two flows differ.
//
// It is also where the domain's refusal of a duplicate address is made — before
// the relay, so a second institution never gets an account opened for it — and
// that refusal is keyed on the ADMISSION rather than on the address. See
// relayAdmission, and payment.ErrBICAlreadyAdmitted.
//
// It holds a csmOps: nothing about clearing moves money, and that is what makes
// clearing and settlement different jobs. What that still does NOT amount to is
// a compile-time ban on posting. It used to be one method short of a way in —
// GetParticipant handed back live ledger and deposit handles bound to the bank
// it named — and Task 17 replaced it with GetRosterEntry, which hands back an
// address. What has not changed is that these interfaces narrow by method and
// never by book, so the ban stays the recorder's:
// TestTheCSMTouchesOnlyTheNetworkBook is what holds this actor to it, and
// TestTheCSMStillTouchesOnlyTheNetworkBookWhenItSettles extends that over the
// cut-off and the settlement conversation.
type csm struct {
	m   *Mesh
	ops csmOps
	bic iso20022.BIC

	// held is the returns this actor has relayed to the settlement agent and has
	// not yet heard about, keyed by the payment each names.
	//
	// It is the only state any actor in this package keeps between messages, and
	// it is deliberate rather than convenient: see relayReturn for why the
	// message cannot be relayed onward before finality, and for what is lost if
	// this map is. Admission, which is the other flow this actor relays, keeps
	// nothing at all — see relayAdmission for what makes the difference.
	//
	// No lock. Only relayReturn and receiveReturnStatus touch it and both are
	// reached only from handle, which runs on this actor's own goroutine and
	// nobody else's — the property mesh.actor's doc states and the reason a
	// handler may touch its actor's state without one. The three methods on this
	// type that DO run on a caller's goroutine (closeCycle, settle, reject) are
	// the ones that must never read it, and none does.
	held map[payment.PaymentID]heldReturn
}

// heldReturn is one pacs.004 in flight: the document itself, and who sent it.
//
// The document rather than the bytes, because relaying is rebuilding the
// envelope around an unchanged document — see relay — and re-parsing what this
// actor has already parsed would be a second answer to what the message says.
//
// from is the RETURNING bank, and it is what the second hop is routed against:
// the other bank is whichever of OrgnlTxRef's two agents this is not. Kept here
// rather than re-derived, because it is a fact about the transport that this
// actor observed, and re-deriving it would mean asking the store which bank
// ought to have sent a message it can see the sender of.
type heldReturn struct {
	doc  *iso20022.Pacs004
	from iso20022.BIC
}

// handle dispatches on the message that arrived. See bank.handle, which has the
// same shape and the same reason for taking the sender as an argument.
func (c *csm) handle(ctx context.Context, from iso20022.BIC, raw []byte) error {
	env, err := iso20022.Unmarshal(raw)
	if err != nil {
		return c.m.answerUnreadable(c.bic, from, err)
	}
	switch doc := env.Document.(type) {
	case *iso20022.Pacs008:
		return c.relayCreditTransfer(ctx, from, env, doc)
	case *iso20022.Pacs003:
		return c.relayDirectDebit(ctx, from, env, doc)
	case *iso20022.Pacs004:
		return c.relayReturn(from, env, doc)
	case *iso20022.Acmt007:
		return c.relayAdmission(ctx, from, env, doc)
	case *iso20022.Acmt010:
		return c.receiveAdmissionStatus(ctx, from, env, doc)
	case *iso20022.Acmt011:
		return c.relayAdmissionRejection(from, env, doc)
	case *iso20022.Pacs002:
		// Three kinds of status arrive here, and it takes two questions to tell
		// them apart.
		//
		// The SENDER separates a member bank's from the settlement agent's. A
		// bank's pacs.002 answers an instruction about one customer payment;
		// the central bank's answers something this actor asked IT for. By BIC
		// rather than by trying a lookup and seeing what happens, because the
		// central bank is not a member of this network: it holds no participant
		// row, it never submits and it is never a payment's agent, so the only
		// messages it ever sends this actor are answers to what this actor
		// asked it.
		//
		// What it was asked separates the settlement agent's two. A settlement
		// answer is about a whole CYCLE and its OrgnlTxId is a cycle id; a
		// return answer is about one PAYMENT. Reading either as the other would
		// look an identifier up in the wrong table. The message says which —
		// OrgnlMsgNmId, the definition it is answering — so this is read off the
		// wire rather than guessed at by trying both. See returnMsgDef.
		switch {
		case from != c.m.cfg.CentralBankBIC:
			return c.receiveStatus(ctx, from, doc)
		case isAbout(doc, returnMsgDef):
			return c.receiveReturnStatus(ctx, from, doc)
		default:
			return c.receiveSettlementStatus(ctx, from, doc)
		}
	default:
		return fmt.Errorf("mesh: %s has no handler for %s", c.bic, env.AppHdr.MsgDefIdr)
	}
}

// relayCreditTransfer hands a credit transfer on to the CREDITOR's agent: the
// bank that holds the payee, because a push travels towards the money's
// destination — and records this institution's own copy of it.
//
// # The record comes AFTER the routing, and the order is decided rather than
// incidental
//
// A message this actor cannot route never becomes a payment here. Both refusals
// above the relay are of that kind — a bulk file, and the RC01 for an agent this
// mesh has no address for — and neither leaves a row behind saying the clearing
// house is carrying something that never left the building. Recording first
// would mean rejecting that row a moment later to say the same thing.
//
// It is safe to record after the send because an actor handles its inbox
// SERIALLY (Mesh.run: pop, handle, repeat). The receiving bank's pacs.002 lands
// in this actor's inbox and cannot be popped until this handler returns, so the
// row is always there before anything asks for it. That is a property of the
// transport, and it is named here because it is what makes the order a choice.
//
// What it costs is one seam: a relay that succeeded and a record that failed
// leaves the message in flight with no row to answer against, and the bank's
// answer then dead-letters. The error is returned rather than swallowed, so it
// is one dead letter here and one there.
//
// c.relay itself still reads and writes NO store — see its doc, which makes a
// property of that. The write is this function's.
func (c *csm) relayCreditTransfer(ctx context.Context, from iso20022.BIC, env iso20022.Envelope, doc *iso20022.Pacs008) error {
	body := doc.FIToFICstmrCdtTrf
	ref := body.CdtTrfTxInf[0].PmtId
	orig := payment.OriginalMessage{
		MsgID:     body.GrpHdr.MsgId,
		MsgDefIdr: env.AppHdr.MsgDefIdr,
		CreDtTm:   body.GrpHdr.CreDtTm.Time,
	}
	if n := len(body.CdtTrfTxInf); n != 1 {
		return c.refuseBulk(from, orig, ref, "CdtTrfTxInf", n)
	}
	if err := c.relay(from, env, doc, orig, ref, body.CdtTrfTxInf[0].CdtrAgt.FinInstnId.BICFI); err != nil {
		return err
	}
	if _, err := c.ops.RecordRelayedCreditTransfer(ctx, doc); err != nil {
		return fmt.Errorf("mesh: %s relayed %s and could not record it: %w", c.bic, ref.TxId, err)
	}
	return nil
}

// relayDirectDebit hands a collection on to the DEBTOR's agent: the bank that
// holds the payer, because a pull travels towards the money's source.
//
// One element different from relayCreditTransfer, and it is the whole direction
// of the payment. Routing a pacs.003 by CdtrAgt would send the collection back
// to the bank that sent it, which would answer its own instruction — and the
// resolution inside DirectDebitRequest would succeed while it did, because both
// parties resolve by address whoever is asking.
// It records its own copy after the relay, for relayCreditTransfer's reasons.
func (c *csm) relayDirectDebit(ctx context.Context, from iso20022.BIC, env iso20022.Envelope, doc *iso20022.Pacs003) error {
	body := doc.FIToFICstmrDrctDbt
	ref := body.DrctDbtTxInf[0].PmtId
	orig := payment.OriginalMessage{
		MsgID:     body.GrpHdr.MsgId,
		MsgDefIdr: env.AppHdr.MsgDefIdr,
		CreDtTm:   body.GrpHdr.CreDtTm.Time,
	}
	if n := len(body.DrctDbtTxInf); n != 1 {
		return c.refuseBulk(from, orig, ref, "DrctDbtTxInf", n)
	}
	if err := c.relay(from, env, doc, orig, ref, body.DrctDbtTxInf[0].DbtrAgt.FinInstnId.BICFI); err != nil {
		return err
	}
	if _, err := c.ops.RecordRelayedDirectDebit(ctx, doc); err != nil {
		return fmt.Errorf("mesh: %s relayed %s and could not record it: %w", c.bic, ref.TxId, err)
	}
	return nil
}

// relayReturn hands a return on to the SETTLEMENT AGENT, and keeps a copy for
// the bank that has not heard about it yet.
//
// # The first hop, which is unchanged
//
// Not to a bank, and that is the whole routing decision of the way OUT. A
// return moves central-bank reserves back — payment.SettleReturnTx reverses the
// movement between the two banks' settlement accounts — and moving reserves is
// the settlement agent's act, as it is at a cut-off. So the destination is a
// fact about the MESSAGE DEFINITION rather than about anything inside the
// message, and this hop reads no element to route by.
//
// It still goes THROUGH the clearing house rather than from the bank to the
// central bank directly. A member bank in this mesh addresses the clearing
// house and nothing else — that is the shape every other flow has, and the
// routing table lives in one actor for the same reason RC01 can only be said
// here.
//
// A bulk return is NOT refused here, unlike a bulk pacs.008 or pacs.003, and
// the difference is what refuseBulk is actually about: several transactions in
// an instruction mean several counterparty agents and therefore several
// destinations, and this actor has one routing decision per message to make.
// Every return in one message has the same first destination, so routing has no
// objection to make. The settlement agent has one, and makes it — see
// centralBank.receiveReturn, which refuses anything but a single return whole.
//
// # The second hop, which is why this actor now keeps state
//
// This doc used to say a pacs.004 "names no parties at all" and that this hop
// "reads no element to route by and no store either". Neither survives. A
// pacs.004 carries OrgnlTxRef since Task 16b, so it names both agents; and the
// message has a SECOND destination — the bank that did not ask for the return
// and has to post the leg the returning bank does not hold. That hop is routed
// by the agents in OrgnlTxRef: the other bank is whichever of the two this
// actor did not receive the message from.
//
// It cannot be sent now. The returning bank may still be refused — the
// settlement agent answers RJCT when the creditor's bank cannot cover the
// reserve reversal, or when the message names nothing it can resolve — and a
// bank that had already posted its customer leg against a refused return would
// have moved a customer's money for nothing, with no message in the flow that
// would ever tell it. So the message waits here until an ACSC arrives, and
// csm.receiveReturnStatus is what releases it.
//
// # Where this state lives, and what is lost when it is lost
//
// In memory, in one map on this actor, and that is a decision rather than an
// oversight. This process is the clearing house; the map is its record of the
// conversations it is in the middle of, and it does not survive a restart.
//
// What a restart costs is exact and worth writing down rather than discovering:
// a return relayed but not yet answered is gone, so when the ACSC arrives
// — or if it arrived while nobody was listening — the second bank is never sent
// the pacs.004 and never posts its leg. The reserves have moved and are final,
// the returning bank's customer has been debited or credited, and the payment
// stays Settled for ever with half a return standing in one book. That is the
// same shape Task 15 carried into main for a cycle left Cleared after its
// settlement, and it has the same remedy: none from inside this flow, and Task
// 19's reconciliation to make it visible.
//
// The alternative was to relay immediately and have the other bank tolerate a
// return that is later refused. It was rejected because there is no such
// tolerating. That bank posts after finality and cannot refuse, so "tolerate"
// means unwinding a leg it already posted — which needs a second message this
// flow does not have (the settlement agent answers the bank that ASKED, and
// only that one), and until it arrived the payer would be holding a refund the
// network had refused. Holding trades a durability gap for a correctness one,
// and the correctness one is on a customer's account.
//
// Making it durable is a row, and the row belongs to the clearing house's own
// store — which sub-project 8 gives it and which does not exist yet. Writing
// one into the shared payment store today would be this actor recording a fact
// about a payment on a row it is not supposed to hold at all.
func (c *csm) relayReturn(from iso20022.BIC, env iso20022.Envelope, doc *iso20022.Pacs004) error {
	body := doc.PmtRtr
	orig := payment.OriginalMessage{
		MsgID:     body.GrpHdr.MsgId,
		MsgDefIdr: env.AppHdr.MsgDefIdr,
		CreDtTm:   body.GrpHdr.CreDtTm.Time,
	}
	// Unmarshal refuses a pacs.004 with no transactions (iso20022's
	// PaymentReturn.validate), so there is always a first one to refer back by.
	first := body.TxInf[0]
	ref := iso20022.PaymentIdentification{
		EndToEndId: first.OrgnlEndToEndId,
		TxId:       first.OrgnlTxId,
	}
	// Held under the payment the answer will name, which is the only thing the
	// two messages have in common. A return that names no payment is relayed and
	// NOT held: the answer to it names none either, so nothing could ever match
	// it and an entry under the empty key would sit here for ever. That message
	// dies at the settlement agent's answer instead, which is where
	// TestAReturnThatNamesNoPaymentCannotBeAnswered pins it.
	if first.OrgnlTxId != "" {
		c.held[payment.PaymentID(first.OrgnlTxId)] = heldReturn{doc: doc, from: from}
	}
	return c.relay(from, env, doc, orig, ref, c.m.cfg.CentralBankBIC)
}

// releaseReturn sends the held pacs.004 on to the bank that did not ask for the
// return, now that the settlement agent has made it final.
//
// # It is routed by the message and not by the store
//
// The two agents are on OrgnlTxRef and the returning bank is the one this actor
// took the message FROM, so the recipient is a subtraction rather than a lookup.
// receiveReturnStatus does read the store to address the ANSWER — it has only a
// payment id to go on there — but the message being relayed carries its own
// parties, and relaying it by anything else would be this actor asserting
// something the document already says.
//
// A payment between two accounts at ONE bank has both agents equal, so the
// subtraction leaves that same bank and the message goes back to it. That is
// correct and not a degenerate case: that bank holds both legs and
// payment.PostReturnLegTx posts them one call at a time, so the second call is
// exactly what this message is for.
//
// # A held return with no agents cannot be relayed
//
// It also cannot happen: payment.ReadReturn refuses a pacs.004 whose OrgnlTxRef
// names no agents, and the settlement agent answers that RJCT, so an ACSC
// implies the document is readable. The guard is here anyway because the cost
// of being wrong is a nil dereference in an actor's goroutine, which takes the
// mesh down; an error is a dead letter, which is a test failure.
func (c *csm) releaseReturn(held heldReturn, id payment.PaymentID) error {
	ref := held.doc.PmtRtr.TxInf[0].OrgnlTxRef
	if ref == nil || ref.DbtrAgt == nil || ref.CdtrAgt == nil {
		return fmt.Errorf("mesh: %s settled the return of %s and the message names no agents to relay it to", c.bic, id)
	}
	to := ref.DbtrAgt.FinInstnId.BICFI
	if to == held.from {
		to = ref.CdtrAgt.FinInstnId.BICFI
	}
	return c.m.send(c.bic, to, iso20022.Envelope{
		AppHdr: iso20022.AppHdr{
			Fr:        iso20022.NewAgent(c.bic),
			To:        iso20022.NewAgent(to),
			BizMsgIdr: c.m.nextMsgID(c.bic),
			MsgDefIdr: returnMsgDef,
			CreDt:     iso20022.ISODateTime{Time: c.m.now()},
		},
		// The document travels unchanged, for relay's reason: it is what the
		// returning bank said and is not this actor's to rewrite. Its GrpHdr/MsgId
		// therefore stays that bank's, which is what makes the two copies of this
		// return one message rather than two.
		Document: held.doc,
	})
}

// relayAdmission hands a bank's application for a settlement account on to the
// SETTLEMENT AGENT — or refuses it, which is the one thing this actor decides in
// the whole flow.
//
// # The destination is the message definition's, exactly as a return's is
//
// Not a bank, and no element is read to route by. An account at the settlement
// agent is the settlement agent's to open, so a bank asking for one has one
// possible destination whoever it is. The bank addresses this actor and nothing
// else, which is the shape every flow here has.
//
// # It HOLDS NOTHING, and the contrast with csm.held is the point
//
// A reader who knows the return will expect a hold here, because that flow keeps
// the whole pacs.004 until the settlement agent has said the reserves moved, and
// csm.held is the only state any actor in this package keeps between messages.
// The absence needs a reason rather than silence, and the reason is what the two
// answers carry.
//
// A pacs.002 about a return says one thing — settled, or not — so the message it
// releases has to have been kept. An acmt.010 carries the applicant's ADDRESS,
// every account the servicer opened and the admission's process id, which is
// every field of the routing entry this actor writes. There is nothing about the
// request left to remember, so nothing is remembered.
//
// That is exact and it was briefly not. A roster entry used to carry the
// member's legal NAME as well, and an acmt.010 names nobody — it identifies the
// owner with an OrganisationIdentification29, which has no name element — so for
// one round this actor kept the applicant's name across the relay to fill it.
// The field went instead, because nothing read it: routing is an address, and
// payment.RosterEntry records the whole of that reversal.
//
// # It refuses two different things, and only one of them is about the roster
//
// WHOSE application this is comes first and reads no store: an acmt.007 names
// its applicant in the document and its sender in the header, and this compares
// them. Without it a bank could apply on an address it does not hold, and the
// settlement agent would open an account for that address without ever being
// able to tell who asked — an account servicer sees one request about one BIC.
// See payment.ErrNotThisBanksAdmission, which is the same comparison made by the
// BANK one hop later about the answer, and
// TestTheClearingHouseRefusesAnApplicationOnAnotherBanksAddress.
//
// # The refusal lives here, one institution before the account is opened
//
// The roster is the DOMAIN's truth about who holds an address and the mesh's
// actor map is the TRANSPORT's, and this is where the domain's answer is given.
// It is keyed on the ADMISSION and not on the address, and that distinction is
// the whole of why it works: Mesh.Admit claims the address at the mesh before
// anything is written or sent, so an impostor never gets a message onto the
// wire, and what CAN arrive on a BIC already in the roster is the same bank
// asking again. A refusal keyed on "is this BIC in the roster" would refuse that
// and never fire on the case it exists for. See payment.ErrBICAlreadyAdmitted
// and payment.RosterEntry's AdmissionRef.
//
// Which requests reach this actor on an address already in the roster is worth
// being exact about, because the obvious answer is wrong for this transport. A
// two-asset bank's SECOND acmt.007 is not one of them: Mesh.Admit pushes both
// applications onto this actor's queue before the settlement agent has answered
// either, and a queue is popped in order, so both are relayed while the roster
// still holds nothing. That is a property of a lossless in-process queue and not
// of the design — reorder or delay one message and the second request meets the
// entry the first produced — which is why the refusal has to be keyed on the
// reference here as well as at payment.AdmitMemberTx, where the second
// acknowledgement really does meet the entry every time. What reaches it today
// is an operator re-driving, and any request a real counterparty composed.
// TestTheClearingHouseRefusesASecondInstitutionOnAnAdmittedAddress injects one
// of each and is what holds both arms.
//
// It refuses BEFORE it relays, so a refused applicant gets no account opened for
// it in another institution's book — which the settlement agent could not undo,
// and could not have refused either: an account servicer asked twice for one
// address by two institutions cannot tell them apart.
//
// # It does not reuse relay, and the reason is the same one the answer has
//
// csm.relay turns an unroutable destination into RC01 in a pacs.002, which is a
// status report about a payment transaction; this is not one. So the envelope is
// built here, as releaseReturn's is, and a destination this mesh cannot reach
// becomes an acmt.011 like every other refusal on this path. Nothing in this
// system can provoke it — the settlement agent is one of the two configured
// actors and always has an inbox — which is why it is an arm and not a flow.
func (c *csm) relayAdmission(ctx context.Context, from iso20022.BIC, env iso20022.Envelope, doc *iso20022.Acmt007) error {
	in, err := payment.ReadAdmissionRequest(doc)
	if err != nil {
		// Unreadable, and unanswerable for the same reason: what this actor would
		// address and correlate a refusal by are the elements this reader refuses
		// on. See centralBank.refuseAdmission, which makes the same distinction
		// one hop on.
		return fmt.Errorf("mesh: %s could not read the admission request %s sent it: %w", c.bic, from, err)
	}
	// WHOSE application this is. An acmt.007 names its applicant in the document
	// and the sender in the header, and nothing made them agree: a bank could
	// apply on another address, and what it would get is an account opened in the
	// settlement agent's book for a BIC it does not hold and a routing entry
	// written from the answer. payment.RecordMembershipTx makes exactly this
	// comparison one hop later (ErrNotThisBanksAdmission) — that one is a bank
	// refusing a message about somebody else, and this is the clearing house
	// refusing an applicant who is somebody else.
	//
	// It is answered rather than dropped, and this is the one refusal on this
	// path whose acmt.011 goes somewhere other than the applicant: the sender is
	// who asked, the sender has an actor because it sent, and the applicant it
	// named never asked for anything.
	if from != in.BIC {
		return c.refuseAdmission(from, doc, in, fmt.Errorf("%w: %s applied on behalf of %s",
			payment.ErrNotThisBanksAdmission, from, in.BIC))
	}
	entry, err := c.ops.GetRosterEntryByBIC(ctx, in.BIC)
	switch {
	case err == nil && entry.AdmissionRef != in.Ref:
		return c.refuseAdmission(from, doc, in, fmt.Errorf("%w: %s is admitted under %q and this request quotes %q",
			payment.ErrBICAlreadyAdmitted, in.BIC, entry.AdmissionRef, in.Ref))
	case err != nil && !errors.Is(err, payment.ErrRosterEntryNotFound):
		return fmt.Errorf("mesh: %s cannot tell whether %s is already admitted: %w", c.bic, in.BIC, err)
	}

	to := c.m.cfg.CentralBankBIC
	relayed := iso20022.Envelope{
		AppHdr: iso20022.AppHdr{
			Fr:        iso20022.NewAgent(c.bic),
			To:        iso20022.NewAgent(to),
			BizMsgIdr: c.m.nextMsgID(c.bic),
			MsgDefIdr: env.AppHdr.MsgDefIdr,
			CreDt:     iso20022.ISODateTime{Time: c.m.now()},
		},
		// Unchanged, for relay's reason: the document is what the applicant said
		// and is not this actor's to rewrite. Its Refs/MsgId therefore stays the
		// bank's, which is what the settlement agent quotes back as RjctdReqId
		// when it refuses.
		Document: doc,
	}
	if err := c.m.send(c.bic, to, relayed); err != nil {
		if errors.Is(err, ErrUnknownBIC) {
			return c.refuseAdmission(from, doc, in, err)
		}
		return err
	}
	return nil
}

// receiveAdmissionStatus is the clearing house writing its own routing entry
// from the settlement agent's acknowledgement, and then forwarding it to the
// bank the message is about.
//
// # The entry is written BEFORE the acknowledgement is forwarded
//
// A bank told it is a member is one this clearing house can already route to.
// The other order leaves an interval in which the bank has recorded its
// membership and a payment addressed to it comes back RC01 — the answer this
// actor gives for a BIC in no routing table — which is the settlement path's
// statement-before-answer rule in a new place and load-bearing for the same
// reason: what is sent second is what makes somebody else act on the first.
//
// # Everything the entry is made of is on the message
//
// The address, the accounts and the admission reference, which is why this
// handler reads no store of its own and holds nothing between messages. See
// relayAdmission, where the contrast with the return's hold is set out.
//
// # A refused acknowledgement stops here
//
// payment.ErrBICAlreadyAdmitted is unreachable from this arm — the refusal is
// made against the REQUEST, one message earlier, so nothing that gets this far
// can be a second institution's — and any other failure of the act is this
// system disagreeing with itself. Either way there is nobody to tell: the bank
// is waiting for an acknowledgement it will not now get, and the settlement
// agent has already opened the account. It becomes a dead letter, and the
// admission is one an operator re-drives.
func (c *csm) receiveAdmissionStatus(ctx context.Context, from iso20022.BIC, env iso20022.Envelope, doc *iso20022.Acmt010) error {
	ack, err := payment.ReadAdmissionAcknowledgement(doc)
	if err != nil {
		return fmt.Errorf("mesh: %s could not read the admission acknowledgement %s sent it: %w", c.bic, from, err)
	}
	if _, err := c.ops.AdmitMember(ctx, ack); err != nil {
		return fmt.Errorf("mesh: %s cannot route to %s: %w", c.bic, ack.BIC, err)
	}
	return c.forwardAdmission(env, doc, ack.BIC)
}

// relayAdmissionRejection carries the settlement agent's refusal back to the
// bank that applied.
//
// The recipient is on the message: an acmt.011 names the applicant in OrgId,
// which is the same element the acknowledgement names it in, so this hop reads
// no store — releaseReturn's property, arrived at the same way.
//
// A refusal this actor made ITSELF does not come through here. That one is
// answered straight to the sender inside relayAdmission, because the sender is
// the applicant and there is nothing to carry.
func (c *csm) relayAdmissionRejection(from iso20022.BIC, env iso20022.Envelope, doc *iso20022.Acmt011) error {
	to := doc.AcctReqRjctn.OrgId.AnyBIC
	if to == "" {
		return fmt.Errorf("mesh: %s got a refused admission from %s naming no applicant", c.bic, from)
	}
	return c.forwardAdmission(env, doc, to)
}

// forwardAdmission passes an admission message on to the bank it is about, with
// the header replaced and the document unchanged.
//
// It is csm.relay's body for the account-management family, and it is separate
// for the reason relayAdmission gives: relay answers an unroutable destination
// with a pacs.002, and a status report is about a payment transaction. Here an
// unroutable destination is a bank the settlement agent has answered about and
// this mesh cannot reach, which is nobody's to be told — so it is a dead letter.
func (c *csm) forwardAdmission(env iso20022.Envelope, doc iso20022.Document, to iso20022.BIC) error {
	return c.m.send(c.bic, to, iso20022.Envelope{
		AppHdr: iso20022.AppHdr{
			Fr:        iso20022.NewAgent(c.bic),
			To:        iso20022.NewAgent(to),
			BizMsgIdr: c.m.nextMsgID(c.bic),
			MsgDefIdr: env.AppHdr.MsgDefIdr,
			CreDt:     iso20022.ISODateTime{Time: c.m.now()},
		},
		Document: doc,
	})
}

// refuseAdmission answers an application this clearing house will not relay,
// back to the bank that sent it.
//
// The acmt.011 is built rather than relayed, because this actor DECIDED the
// refusal: the settlement agent has not seen the request and must not appear to
// have refused it. That is the same distinction csm.forwardDecision makes on the
// return path, made here by which function builds the message rather than by a
// field, because this family has no originator element to set.
func (c *csm) refuseAdmission(to iso20022.BIC, doc *iso20022.Acmt007, in payment.AdmissionRequest, cause error) error {
	env, err := payment.AdmissionRejectionMessage(in, doc.AcctOpngReq.Refs.MsgId, cause.Error(), payment.MessageContext{
		From:  c.bic,
		To:    to,
		MsgID: c.m.nextMsgID(c.bic),
		Now:   c.m.now(),
	})
	if err != nil {
		return errors.Join(fmt.Errorf("mesh: %s could not build its acmt.011 for %s: %w", c.bic, to, err), cause)
	}
	// The cause is not returned once it has been answered, for bank.answer's
	// reason: a refusal the applicant was told about is completed work.
	return c.m.send(c.bic, to, env)
}

// refuseBulk is this system's one-payment-per-message limit, stated to the
// sender.
//
// A bulk file names several counterparty agents and therefore several
// destinations, and this clearing house has one routing decision per message to
// make. Refused rather than split: sending the first of five would drop four.
func (c *csm) refuseBulk(from iso20022.BIC, orig payment.OriginalMessage, ref iso20022.PaymentIdentification, element string, n int) error {
	return c.answer(from, orig, ref, iso20022.StatusReasonNotSpecifiedAgentGenerated,
		fmt.Sprintf("this clearing house routes one payment per message; %s carries %d", element, n))
}

// relay forwards an instruction to the agent the message named, whichever
// direction it runs in.
//
// It reads no store, and that is a property worth keeping rather than an
// accident of how little there is to do. The address it routes by came out of
// the message — CdtrAgt for a push, DbtrAgt for a pull — so a clearing house
// that looked the payment up to decide where to send it would be one that could
// not route a message about a payment it does not hold. Which is every message,
// in a real network.
//
// A RETURN's FIRST hop is routed by neither element, because its destination
// follows from the message DEFINITION: the settlement agent, whoever the parties
// are. Its caller passes that address in rather than reading one out (see
// relayReturn), which keeps this function's property intact — the store is
// untouched either way.
//
// Its SECOND hop does read an element, and it does not come through here. A
// pacs.004 carries both agents in OrgnlTxRef since Task 16b, and the bank that
// did not ask for the return is whichever of them the message did not arrive
// from — a subtraction this function has no argument for, and one that happens
// long after the first hop rather than in place of it. See releaseReturn, which
// builds its own envelope for that reason and keeps the same no-store property.
//
// The DOCUMENT travels unchanged and only the header is replaced. That is what
// relaying is: the header says who is handing this to whom and is this hop's,
// the document is what the submitting bank said and is not the clearing house's
// to rewrite. Its GrpHdr/MsgId therefore stays that bank's, which is what the
// receiving bank quotes back as OrgnlMsgId and what lets the answer be matched
// to the original all the way home.
func (c *csm) relay(from iso20022.BIC, env iso20022.Envelope, doc iso20022.Document,
	orig payment.OriginalMessage, ref iso20022.PaymentIdentification, to iso20022.BIC) error {

	relayed := iso20022.Envelope{
		AppHdr: iso20022.AppHdr{
			Fr:        iso20022.NewAgent(c.bic),
			To:        iso20022.NewAgent(to),
			BizMsgIdr: c.m.nextMsgID(c.bic),
			MsgDefIdr: env.AppHdr.MsgDefIdr,
			CreDt:     iso20022.ISODateTime{Time: c.m.now()},
		},
		Document: doc,
	}
	err := c.m.send(c.bic, to, relayed)
	if errors.Is(err, ErrUnknownBIC) {
		// RC01, and it is this actor that says it because it is the only one
		// holding the routing table. A bank cannot know that a BIC is unroutable;
		// it can only know that its message was not answered.
		return c.answer(from, orig, ref, iso20022.StatusReasonBankIdentifierIncorrect, err.Error())
	}
	return err
}

// receiveStatus is the clearing house acting on what the payee's bank said, and
// then telling the payer's bank.
//
// This is where a payment becomes Accepted or Rejected, and both are the
// clearing house's act rather than either bank's:
//
//   - ACCP: the payment goes into the open cycle for its scheme. Which cycle a
//     payment clears in is not a bank's decision, so ErrCycleNotOpen is not a
//     bank's refusal either — it comes back as TM01, "invalid cut-off time",
//     which is exactly what "there is no window open" means.
//   - RJCT: the payment is rejected with the code the payee's bank chose, and
//     dropped from any cycle it had reached.
//
// # Who is told, and why it can be two banks
//
// The answer goes to the bank that SUBMITTED, looked up from the payment rather
// than taken from the message's sender — the sender is the bank that just
// decided, and the submitter is a third party neither of them addressed. Which
// bank submitted is the scheme's direction and nothing else: the payer's bank
// pushed, or the payee's bank pulled. See submitterOf.
//
// A REJECTION can need a second recipient, and only on a pull. By the time the
// clearing house refuses a collection, the payer's bank has already posted the
// debtor leg — that is what accepting a collection means — and that bank is not
// the one that submitted. A rejection that never reached it would leave the
// payer's money sitting in a clearing suspense against a payment this network
// records as rejected: nobody would ever settle it and nobody would ever give it
// back. So the payer's bank is told as well, and the condition is exactly the
// money: a posted debtor leg, at a bank that is not the submitter.
//
// On a push the two are the same bank and there is one message, unchanged from
// Task 10. On a pull the payer's bank refused ITSELF — AM04, which its funds
// check makes before the leg is posted — there is no leg to give back, so there
// is one message again, and TestARejectedCollectionIsAnsweredToThePayeesBank
// counts it.
//
// A refusal that never reached the payer's bank at all — an agent this mesh
// cannot route to, a bulk file — does not arrive here in the first place: both
// are answered by c.answer, from relay and refuseBulk, before any payment has
// been looked up.
//
// # This handler does two writes, and the second may not happen
//
// Rejecting is the clearing house's half; reversing the payer's debit is the
// payer's bank's, and it happens later, in another actor, in another unit of
// work. RejectAtCSMTx's doc names that seam and says the mesh is where it stops
// being hidden: if the reversal fails there is nobody to answer, so it becomes a
// dead letter and Drain returns it. The system fails a test rather than quietly
// telling a payer their money is back.
func (c *csm) receiveStatus(ctx context.Context, from iso20022.BIC, doc *iso20022.Pacs002) error {
	orig, reports := payment.ReadStatus(doc)
	for _, r := range reports {
		if r.TxID == "" {
			// A status that names no transaction. Nothing in this flow produces
			// one for the clearing house — a bank's answer always quotes the
			// payment it is about — so it is a message this actor cannot act on
			// and has nobody to ask about.
			return fmt.Errorf("mesh: %s got a status from %s naming no transaction", c.bic, from)
		}
		id := payment.PaymentID(r.TxID)

		// What the ANSWERING BANK said, kept before this actor decides anything,
		// because clear rewrites r on the path where the clearing house turns an
		// acceptance into its own rejection. An ACCP from the payer's bank is what
		// says its debtor leg is posted; see tell, which used to read the leg id
		// off a column this institution's schema does not have.
		payerAccepted := r.Status == iso20022.TransactionStatusAccepted

		var decided payment.Payment
		var err error
		if r.Status == iso20022.TransactionStatusRejected {
			decided, err = c.ops.RejectAtCSM(ctx, id, r.Code, r.Text)
		} else {
			decided, r, err = c.clear(ctx, id, r)
		}
		if err != nil {
			// Already answered. The queue redelivers, and a second copy of an
			// acceptance arrives at a payment the clearing house has already
			// taken into a cycle — or a second rejection at one already rejected.
			// Either way this sentinel says the state machine forbids the move,
			// which is a statement about THIS system and not about the sender's
			// message: payment's reasonTable gives it the empty code for exactly
			// that reason, and ReasonFor would turn it into MS03 and reject a
			// payment that was in fact accepted. Dead letter, and no pacs.002.
			if errors.Is(err, payment.ErrInvalidStateTransition) {
				return fmt.Errorf("mesh: %s was told about %s again: %w", c.bic, id, err)
			}
			return err
		}

		if err := c.tell(ctx, decided, orig, r, payerAccepted); err != nil {
			return err
		}
	}
	return nil
}

// tell sends the decision to every bank that has to have it. See the note on
// receiveStatus for which banks those are and why a rejected collection has two.
//
// # The MONEY message goes first, and neither is skipped because the other
// failed
//
// The two messages are not the same kind of news. The submitter's is the answer
// to a question it asked. The payer's bank's — sent only when a rejection
// leaves it holding a posted debtor leg it did not submit, which is a rejected
// collection and nothing else — is what makes it give a customer their money
// back. Sending the informational one first and returning on its error left the
// refund unattempted, and that is reachable: the send is answered with
// ErrUnknownBIC during Reset's ForgetBanks/JoinRoster window. A payer whose money
// is in a suspense against a payment this network records as Rejected has nobody
// left to notice.
//
// So the refund message is attempted first and both are attempted regardless,
// with the failures joined. Two banks that cannot be addressed produce two
// errors and one dead letter carrying both, which is more than the caller could
// have learnt before.
//
// # How it knows the payer's bank is holding money, now that it cannot see
//
// It read p.DebtorLegTx, the id of the transaction the payer's bank posted. That
// column is the BANK's and is not in this institution's schema — the clearing
// house posts nothing and holds no book of accounts, so csm/0001_init.sql has no
// leg columns at all, and on this actor's copy the field is empty for every
// payment. Read as it stood, no rejected collection would ever reach the payer's
// bank and every rejected pull would strand a customer's money.
//
// What replaces it is the fact the column was standing in for: HAS THE PAYER'S
// BANK ACCEPTED THIS COLLECTION. A payer's bank posts the debtor leg when it
// accepts one and not before — that is what accepting one means, and its own
// funds check (AM04) runs first — so the two are the same question, and the
// second is one this actor can answer about itself.
//
// It is passed in rather than derived here, because the two callers WITNESS it
// differently and neither witness is available to the other:
//
//   - receiveStatus has the payer's bank's own pacs.002 in hand. ACCP means the
//     leg is posted and this actor is the one refusing (see clear, the only path
//     that turns an acceptance into a rejection); RJCT means the bank refused
//     itself, before posting.
//   - reject has no message at all — an operator provoked it — and reads its own
//     copy's status instead. Accepted means the payer's bank has already answered
//     and posted; Initiated means it has not.
//
// The status on this actor's copy cannot serve the first case: clear rejects
// after AcceptAtCSM has rolled back, so the copy still says Initiated while the
// bank's leg is posted. That asymmetry is why this is an argument.
//
// The direction is the other conjunct, and it is what says the payer's bank is
// not the submitter — so on a push this is one bank and one message, unchanged.
// What no longer has to hold at all is a THIRD thing, that the address is in the
// roster, because nothing here is looked up any more.
func (c *csm) tell(ctx context.Context, p payment.Payment, orig payment.OriginalMessage, r payment.TransactionStatusReport, payerAccepted bool) error {
	scheme, ok := c.ops.Scheme(p.Scheme)
	if !ok {
		return fmt.Errorf("mesh: %s decided %s and holds no %q scheme to say who submitted it: %w",
			c.bic, p.ID, p.Scheme, payment.ErrSchemeNotFound)
	}
	submitter := submitterOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent)

	var errs []error
	// Both addresses below are read off the PAYMENT. They used to be read out of
	// this institution's roster, keyed by a participant id that had to be turned
	// into a BIC by reading the named bank's own row first — a clearing house
	// reaching into a member's database, twice, on the answer to every payment.
	// A payment names both agents by BIC and always has; see payment.PartyRef.
	//
	// The payer's bank, when it is holding money against a payment that has just
	// been rejected and is not the bank waiting for the answer. Only a pull
	// reaches this — on a push the two are one bank and one message.
	var refunded iso20022.BIC
	if r.Status == iso20022.TransactionStatusRejected &&
		payerAccepted &&
		p.DebtorDetails.Agent != submitter {
		refunded = p.DebtorDetails.Agent
		if err := c.forward(refunded, orig, r); err != nil {
			errs = append(errs, err)
		}
	}

	// The bank that submitted, which is always told. The comparison against
	// `refunded` is what stops a push, where the two are one bank, being sent the
	// same status twice — a duplicate pacs.002 would be a second acceptance at a
	// bank that already has one.
	if submitter != refunded {
		if err := c.forward(submitter, orig, r); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// clear takes an accepted payment into the open cycle for its scheme, and turns
// a refusal to do so into the rejection the submitting bank will be sent.
//
// The report comes back changed on that path, and that is the point: what the
// answering bank said was ACCP, and what the submitting bank must be told is
// RJCT with the clearing house's own reason. Relaying the acceptance while
// rejecting the payment would tell the bank that instructed this the opposite of
// what happened.
func (c *csm) clear(ctx context.Context, id payment.PaymentID, r payment.TransactionStatusReport) (payment.Payment, payment.TransactionStatusReport, error) {
	p, err := c.ops.AcceptAtCSM(ctx, id)
	if err == nil {
		return p, r, nil
	}
	if errors.Is(err, payment.ErrInvalidStateTransition) {
		return payment.Payment{}, r, err
	}
	// A separate unit of work, because the first one rolled back. The clearing
	// house could not clear it, so the clearing house rejects it, with the code
	// its own refusal maps to — TM01 for no open cycle.
	code := payment.ReasonFor(err)
	rejected, rerr := c.ops.RejectAtCSM(ctx, id, code, err.Error())
	if rerr != nil {
		return payment.Payment{}, r, errors.Join(err, rerr)
	}
	r.Status = iso20022.TransactionStatusRejected
	r.Code = code
	r.Text = err.Error()
	return rejected, r, nil
}

// forward sends the status on to one bank. Which bank, and how many of them, is
// tell's decision.
//
// It builds a NEW pacs.002 rather than relaying the one that arrived, and the
// difference is in one element: the originator. A status report says who
// DECIDED, and what this message reports is the clearing house's decision — this
// payment is in a cycle, or this payment is rejected and out of one. The
// answering bank's own decision travelled in its own pacs.002, to this actor,
// carrying its own originator. Each hop states what that hop decided, which is
// why relaying the bytes would be wrong here and right for an instruction.
//
// The ORIGINAL message it refers back to is unchanged: that is the submitting
// bank's pacs.008 or pacs.003, which is what every hop is about and the only
// thing that bank can match on.
//
// decidedBy is the one hop where the claim above is not true, and it is passed
// in rather than inferred. See forwardDecision.
func (c *csm) forward(to iso20022.BIC, orig payment.OriginalMessage, r payment.TransactionStatusReport) error {
	return c.forwardDecision(to, "", orig, r)
}

// forwardDecision is forward for a status this actor is CARRYING rather than
// making: it names the institution that decided, so the pacs.002's Orgtr does
// not say the clearing house.
//
// There is exactly one such hop — the settlement agent's answer about a return,
// passed back to the bank that asked for one — and it is worth a second entry
// point rather than a bool. csm.receiveReturnStatus's own doc says this actor
// decides nothing there, and iso20022.StatusReasonInformation says Orgtr exists
// so that a receiver does not blame the relay for the refusal. Stamping this
// actor would have a returning bank investigating a clearing house that never
// looked at its request.
func (c *csm) forwardDecision(to, decidedBy iso20022.BIC, orig payment.OriginalMessage, r payment.TransactionStatusReport) error {
	env, err := payment.StatusMessage(orig, []payment.TransactionStatusReport{r}, payment.MessageContext{
		From:      c.bic,
		To:        to,
		MsgID:     c.m.nextMsgID(c.bic),
		Now:       c.m.now(),
		DecidedBy: decidedBy,
	})
	if err != nil {
		return fmt.Errorf("mesh: %s could not build its pacs.002 for %s: %w", c.bic, to, err)
	}
	return c.m.send(c.bic, to, env)
}

// reject is the clearing house declining a payment because an operator said so.
//
// It is receiveStatus's rejection arm without the message that would have
// provoked it: the same RejectAtCSM, the same tell, the same banks told. Written
// as its own three statements rather than by faking a pacs.002 and feeding it to
// the handler, because a message this actor never received is not a thing to
// invent — and because what a caller from outside the mesh needs back is the
// payment, which a handler has no way to return.
//
// It runs synchronously on the CALLER's goroutine, and sends after the unit of
// work has committed, for the reasons Mesh.Submit and closeCycle both set out. A
// pacs.002 enqueued from inside the rejection would be one the payer's bank
// could act on against a rejection the store then rolled back — and the act in
// question is handing money back to a customer.
//
// tell is what fans it out, so the answer goes exactly where a counterparty's
// rejection would have gone: to the bank that submitted the payment, and — on a
// pull whose debtor leg has already posted — to the payer's bank as well, which
// is the one holding the money. Two banks, one decision, and the same code path
// that has been carrying that since Task 11.
func (c *csm) reject(ctx context.Context, id payment.PaymentID, code iso20022.StatusReason, text string) (payment.Payment, error) {
	// Everything below is the clearing house's work, and is recorded as the
	// clearing house's. See withActor.
	ctx = withActor(ctx, c.bic)

	// Read BEFORE rejecting, because what tell needs is the status this payment
	// was at when the operator refused it, and RejectAtCSM overwrites exactly
	// that. Accepted means the payer's bank has already answered the collection
	// and posted its debtor leg, which is the money tell has to make sure reaches
	// somebody; Initiated means it has not. See tell, which took the leg's
	// transaction id off the payment until that column turned out to be the
	// bank's and not this institution's.
	//
	// Two reads of one row in two units of work, and a status that changed between
	// them would be another actor rejecting or accepting this payment
	// concurrently — in which case RejectAtCSM below fails the transition and
	// there is nothing to tell anybody.
	before, err := c.ops.GetPayment(ctx, id)
	if err != nil {
		return payment.Payment{}, err
	}
	payerAccepted := before.Status == payment.Accepted

	p, err := c.ops.RejectAtCSM(ctx, id, code, text)
	if err != nil {
		return payment.Payment{}, err
	}
	r := payment.TransactionStatusReport{
		EndToEndID: endToEndOf(p),
		TxID:       string(p.ID),
		Status:     iso20022.TransactionStatusRejected,
		Code:       code,
		Text:       text,
	}
	// The rejection is a fact and the send may still fail, so the payment comes
	// back BESIDE the error rather than being swallowed — closeCycle's shape, and
	// the same half-happened outcome: a payment that is Rejected and whose payer
	// has not been given their money back.
	if err := c.tell(ctx, p, payment.OriginalMessage{MsgID: notProvided, MsgDefIdr: notProvided}, r, payerAccepted); err != nil {
		return p, fmt.Errorf("mesh: %s rejected %s and could not say so: %w", c.bic, p.ID, err)
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// The cut-off, and the settlement it instructs
// ---------------------------------------------------------------------------

// closeCycle is the clearing house reaching a cut-off: it nets the batch, and
// then asks the central bank to discharge it.
//
// Two steps, and the seam between them is the point of this task. Netting is
// the clearing house's own act and moves nothing — CloseCycleTx posts NOTHING
// at all, it transitions each payment to Cleared and writes the positions onto
// the cycle. Discharging those positions moves central-bank reserves, which no
// clearing house may do, so the second step is a MESSAGE to another institution
// and not a call. Before the mesh the two were separate console buttons on
// separate operators, with nothing between them; the pacs.009 is what was
// missing.
//
// It runs synchronously on the CALLER's goroutine, for Mesh.Submit's reason: an
// operator reaching a cut-off has an error to be told about, then and there. And
// like Submit, the send is OUTSIDE the unit of work — a settlement instruction
// enqueued from inside CloseCycleTx would be one the central bank could act on
// against a cut-off the store then rolled back.
//
// The two failure modes are not the same, and the signature keeps them apart. A
// refused cut-off netted nothing and is the caller's answer. A cycle that closed
// and whose instruction could not be sent is HALF-HAPPENED — the payments are
// Cleared and no settlement agent has been told — so the closed cycle comes back
// beside the error rather than being swallowed. It is the same seam
// RejectAtCSMTx documents, and the console is where it shows: a cycle that is
// Closed and has no settlement.
func (c *csm) closeCycle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error) {
	// Everything below is the clearing house's work, and is recorded as the
	// clearing house's. See withActor.
	ctx = withActor(ctx, c.bic)

	closed, err := c.ops.CloseCycle(ctx, id)
	if err != nil {
		return payment.ClearingCycle{}, err
	}
	if err := c.instructSettlement(ctx, closed); err != nil {
		return closed, fmt.Errorf("mesh: %s closed %s and could not instruct settlement: %w", c.bic, closed.ID, err)
	}
	return closed, nil
}

// settle is the clearing house sending a settlement instruction AGAIN, for a
// cycle the central bank refused.
//
// It is the way out of the only state in this system that had none. A cut-off
// whose net payer is short of reserves comes back RJCT/AM04, and
// receiveSettlementStatus tells nobody — correctly, because nothing about any
// payment changed. What is left is a cycle that is Closed with no settlement, a
// batch of payments that are Cleared, every payer debited into their own bank's
// clearing suspense and every payee unpaid. Every other route is shut by a
// guard that is right: CloseCycleTx wants an open cycle, RejectAtCSMTx takes
// only an Initiated or Accepted payment, PostReturnLegTx wants a settled one.
// Until this method existed the operator's remedy — fund the short member and
// ask again — had nothing to ask.
//
// # It re-sends the instruction rather than settling
//
// The clearing house does not discharge positions and this does not start to.
// It rebuilds the same pacs.009 from the same stored net positions and hands it
// to the settlement agent, which decides again with the reserves as they are
// now. That is why the caller gets the CYCLE back rather than a settlement, and
// why the answer is 202 at api: whether it worked this time arrives later, at
// another actor, exactly as it does after a cut-off.
//
// # Double-settling is not reachable, and it is guarded in two places
//
// This refuses anything that is not Closed, so a cycle that has already settled
// is ErrCycleNotClosed here and no second instruction is built. Two calls that
// raced past that check would each send one, and the SECOND is refused at the
// settlement agent by SettleCycleTx's own CycleClosed guard — the same refusal a
// redelivered pacs.009 gets, dead-lettered rather than answered, because
// telling the clearing house RJCT about a cycle that in fact settled would be a
// lie. Behind both of those the central bank's posting carries the idempotency
// key "<cycle>:settle", so even a third arrangement that got past the state
// machine could not post the reserves twice.
// TestReSettlingASettledCycleIsRefused walks the first guard and
// TestASecondSettlementInstructionPostsNothing the other two.
func (c *csm) settle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error) {
	// Everything below is the clearing house's work, and is recorded as the
	// clearing house's. See withActor.
	ctx = withActor(ctx, c.bic)

	cycle, err := c.ops.GetCycle(ctx, id)
	if err != nil {
		return payment.ClearingCycle{}, err
	}
	if cycle.Status != payment.CycleClosed {
		return payment.ClearingCycle{}, fmt.Errorf("mesh: %s is %v, and an instruction to settle is only re-sent for a closed one: %w",
			id, cycle.Status, payment.ErrCycleNotClosed)
	}
	// The cycle is a fact and the send may still fail, so it comes back BESIDE
	// the error rather than being swallowed — closeCycle's shape, for the same
	// reason and with the same half-happened outcome.
	if err := c.instructSettlement(ctx, cycle); err != nil {
		return cycle, fmt.Errorf("mesh: %s could not re-instruct settlement of %s: %w", c.bic, cycle.ID, err)
	}
	return cycle, nil
}

// instructSettlement sends the closed cycle's net positions to the central bank
// as a pacs.009.
//
// A pacs.009 and not a pacs.008 because both parties to every leg are banks:
// this moves a bank's own money, not a customer's. payment.SettlementMessage's
// doc has the whole of that distinction, and the compiler holds it — Dbtr and
// Cdtr in this message are agents and a customer cannot be put in one.
//
// # One instruction, one cycle, one asset
//
// A cycle belongs to one scheme and a scheme settles in one asset, so the asset
// is read off the scheme once and every leg carries it. Two cycles in two assets
// are therefore two cut-offs and two instructions, which is what
// TestOneSettlementInstructionPerAsset measures. Nothing here would stop a
// FUTURE caller putting two cycles in one message — payment.ReadSettlement
// supports it deliberately — but this system does not, and the central bank
// refuses a message that does; see cycleOf.
//
// # A cycle that nets to nothing instructs nothing
//
// An empty cycle, or one whose members' positions all cancel, has no leg to
// send, and a pacs.009 with no transaction is not a message this codec will
// build. Silence is correct there and is not the same as a failure: there is
// nothing for a settlement agent to discharge. Every credit-transfer test in
// this package closes an untouched direct-debit cycle for exactly that reason,
// so the path is walked constantly rather than reasoned about.
func (c *csm) instructSettlement(ctx context.Context, closed payment.ClearingCycle) error {
	scheme, ok := c.ops.Scheme(closed.Scheme)
	if !ok {
		return fmt.Errorf("mesh: %s closed %s and holds no %q scheme to settle it in: %w",
			c.bic, closed.ID, closed.Scheme, payment.ErrSchemeNotFound)
	}
	legs, err := c.settlementLegs(ctx, closed, scheme.Asset())
	if err != nil {
		return err
	}
	if len(legs) == 0 {
		return nil
	}

	to := c.m.cfg.CentralBankBIC
	env, err := payment.SettlementMessage(legs, payment.MessageContext{
		From:  c.bic,
		To:    to,
		MsgID: c.m.nextMsgID(c.bic),
		Now:   c.m.now(),
	})
	if err != nil {
		return fmt.Errorf("mesh: %s could not build the settlement instruction for %s: %w", c.bic, closed.ID, err)
	}
	return c.m.send(c.bic, to, env)
}

// settlementLegs turns a cycle's net positions into the legs of an instruction:
// one per member with something to discharge, each against the CENTRAL BANK.
//
// Against the central bank and not against another member, because a
// multilateral net position has no counterparty among the banks. Three banks
// that each paid and were paid net out to one figure apiece, and the sum of
// those figures is zero — but there is no pairing of payer to payee left in
// them, which is precisely what netting destroys. Every position is therefore a
// claim on or an obligation to the settlement agent, and the message says so:
// a net payer's leg runs from that bank TO the central bank, a net receiver's
// from the central bank to it.
//
// The reference on every leg is the CYCLE, which is what the central bank reads
// to know what it is being asked to discharge. See payment.SettlementLeg.
//
// Members are visited in sorted BIC order rather than map order. The legs are the
// message's transactions, so map iteration would put a different byte sequence
// on the wire on every run for no reason — the same argument settlementLegsTx
// makes one layer down for the entries of the stored transaction, arrived at
// independently because these two orders are not each other's.
//
// A position of zero is left out. It nets to nothing, and a leg for it would be
// an IntrBkSttlmAmt of zero, which the codec refuses (ActiveCurrencyAndAmount
// requires a positive amount) and which would say a bank was instructed to move
// nothing.
// The rendering itself is payment.SettlementLegsOf. It moved there when the
// settlement agent stopped reading the cycle and started working from the
// instruction: the seed plays every institution and sends no messages, so it has
// to produce exactly what this actor would have put on the wire, and two
// renderings of one intent are two things that can drift.
func (c *csm) settlementLegs(ctx context.Context, closed payment.ClearingCycle, asset ledger.AssetCode) ([]payment.SettlementLeg, error) {
	return payment.SettlementLegsOf(closed, asset, c.m.cfg.CentralBankBIC), nil
}

// receiveSettlementStatus is the clearing house acting on what the CENTRAL BANK
// said about a settlement instruction.
//
// The two outcomes are not symmetrical, and the asymmetry is a fact about who is
// waiting for what.
//
// ACSC is news every bank in the batch has been waiting for since it submitted,
// and it is fanned out per payment — see tellSettled. Settlement is the point of
// finality: it is the moment a payee's bank has actually been paid, and before
// the mesh a submitting bank could learn it only by reading the return value of
// somebody else's call.
//
// RJCT is told to NOBODY, and that is deliberate rather than an omission. The
// batch failed whole and nothing was posted in any book: the central bank checks
// each net payer's reserve before it posts anything of its own, and it advises no
// member unless it settles, so no bank was told and no bank booked. That leaves
// every payment in the cycle exactly where the cut-off left it: Cleared, with the
// payer's money still in its own bank's clearing suspense. A bank told "rejected"
// would try to reverse a debtor leg that must not be reversed, and
// bank.receiveStatus refuses to: it checks this network's
// own record of the payment and finds it Cleared rather than Rejected, so the
// message would become a dead letter at every recipient. There is nothing
// truthful to tell a bank here, because nothing about its payments changed.
//
// What DID change is the cycle, and the cycle is where the failure is visible:
// it stays Closed with no settlement against it. That is the state the central
// bank's console reads — the reason is recoverable from the net positions and
// the reserves, which is what the console puts side by side. The code is logged
// here as well, because the code is the one thing that arrives on the wire and
// is nowhere in the store.
//
// What the operator does about it is fund the short member and ask the clearing
// house to instruct settlement again: POST /cycles/{cid}/settle, which is
// csm.settle. That route is the whole of the remedy, and this handler is
// deliberately not part of it — a clearing house that retried by itself would
// re-present a batch against reserves nobody had changed.
func (c *csm) receiveSettlementStatus(ctx context.Context, from iso20022.BIC, doc *iso20022.Pacs002) error {
	orig, reports := payment.ReadStatus(doc)
	for _, r := range reports {
		if r.TxID == "" {
			// A status naming no cycle. The central bank quotes the cycle it was
			// asked about even when it refuses, so this is a message this actor
			// cannot act on and has nobody to ask about.
			return fmt.Errorf("mesh: %s got a settlement status from %s naming no cycle", c.bic, from)
		}
		if r.Status != iso20022.TransactionStatusSettlementCompleted {
			c.m.log.Error("mesh: settlement refused",
				"clearing house", c.bic, "settlement agent", from,
				"cycle", r.TxID, "status", r.Status, "code", r.Code, "reason", r.Text)
			continue
		}
		if err := c.tellSettled(ctx, payment.CycleID(r.TxID), orig, r); err != nil {
			return err
		}
	}
	return nil
}

// tellSettled turns one settled CYCLE into per-PAYMENT advices, addressed to the
// banks that have something to do about each.
//
// # Two recipients, and they are not told the same thing for the same reason
//
// The SUBMITTER is waiting for the answer to its instruction: it sent a pacs.008
// or a pacs.003 naming one payment and has heard ACCP since, and ACSC is what
// closes it. That recipient is unchanged from Task 12, and it is chosen by the
// scheme's direction exactly as tell does — the payer's bank pushed, or the
// payee's bank pulled, and the other one never asked.
//
// The CREDITOR's bank has a LEG TO POST. Settlement moved the reserves; the
// payee is paid when its own bank releases the money out of its clearing
// suspense, and until Task 15 the central bank did that FOR it, in that bank's
// own book. This message is what replaces the reach.
//
// On a PULL they are the same institution and there is one message: the payee's
// bank submitted the collection and is also the creditor. On a PUSH they are two,
// and the second is new. Deduplicating rather than sending twice, because a bank
// that received the same advice twice would post nothing the second time — the
// ledger's idempotency key and PostCreditorLegTx's own Settled guard both see to
// that — but would still be told twice, and a system that says everything twice
// teaches nothing about who needs to hear what.
//
// # The central bank could not send these
//
// It is answering about a CYCLE, and settlementOps holds nothing that turns one
// into the payments inside it — SettleCycle answers with a Settlement and one
// statement per MEMBER, and neither GetCycle nor GetPayment is on that interface.
// It can act on a payment somebody else named to it, which is what a return is,
// and that is a different thing from being able to enumerate a batch. That was
// true before this task and it is why the fan-out lives here; what is new is that
// the message now causes a POSTING at the recipient rather than merely closing
// its instruction.
//
// The ORIGINAL message these refer back to is the SETTLEMENT INSTRUCTION, not
// the bank's own pacs.008, and that is a limitation worth naming. A bank matches
// on OrgnlTxId, which is the payment id and is right; but OrgnlMsgId names a
// message that bank never sent and has never seen. It is the honest one
// available — the clearing house does not keep each submitting bank's message id
// — and a real network would, so that every hop of a payment's answer quotes the
// instruction that started it.
func (c *csm) tellSettled(ctx context.Context, id payment.CycleID, orig payment.OriginalMessage, r payment.TransactionStatusReport) error {
	// This institution's OWN copies move first, in one unit of work, and what
	// comes back is what they say. Reading the cycle and then each payment is what
	// this used to do, and it read rows nothing had written Settled onto: the
	// payee's BANK wrote that, on the row all three institutions shared. See
	// payment.SettleAtCSMTx.
	//
	// A cycle whose copies could not all be marked tells nobody anything, which is
	// the same rule the ACSC fan-out already had for a payment it could not read.
	settled, err := c.ops.SettleAtCSM(ctx, id)
	if err != nil {
		return fmt.Errorf("mesh: %s was told %s settled and cannot record it: %w", c.bic, id, err)
	}
	for _, p := range settled {
		scheme, ok := c.ops.Scheme(p.Scheme)
		if !ok {
			return fmt.Errorf("mesh: %s was told %s settled and holds no %q scheme to say who submitted %s: %w",
				c.bic, id, p.Scheme, p.ID, payment.ErrSchemeNotFound)
		}
		// The submitter first, so a push's two messages go out in the order the
		// banks have reason to expect them: the answer to the instruction, then
		// the advice to act on. On a pull the two ids are equal and there is one.
		// Both recipients are addresses the payment already carries. They were
		// participant ids, and each was turned into a BIC by reading that bank's
		// own row through this institution's roster — a read into a database this
		// one does not hold, made once per recipient per payment in a settled
		// cycle. See payment.PartyRef.
		recipients := []iso20022.BIC{submitterOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent)}
		if creditor := p.CreditorDetails.Agent; creditor != recipients[0] {
			recipients = append(recipients, creditor)
		}
		for _, recipient := range recipients {
			if err := c.forward(recipient, orig, payment.TransactionStatusReport{
				EndToEndID: endToEndOf(p),
				TxID:       string(p.ID),
				Status:     r.Status,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// receiveReturnStatus is the clearing house acting on the settlement agent's
// answer about a RETURN: it tells the bank that asked, and on a yes it releases
// the message to the bank that did not.
//
// It is the mirror of tellSettled and it has grown into the same shape. A
// settlement answer is about a cycle and has to be turned into per-payment news
// by the only actor that knows what is in the batch; a return answer already
// names the one payment it is about, so this actor does not have to enumerate
// anything — but it does have to reach two banks with two different things, for
// the same reason tellSettled does: one is waiting for an answer and the other
// has a LEG TO POST.
//
// Addressing the answer is the part that needs the store. The clearing house
// keeps no record of who sent it which message — it relays and forgets, which is
// what lets it route a message about a payment it does not hold — so "who asked
// for this return" is recomputed from the payment the answer names, by the same
// rule that chose the bank in the first place. See returnerOf. Releasing the
// pacs.004 needs no such lookup: the message carries its own parties, and the
// bank to send it to is whichever agent is not the one it came from. See
// releaseReturn.
//
// # The two outcomes are no longer symmetrical
//
// This doc used to say both were forwarded unchanged in substance, and that was
// true when a refused return had cost nobody anything. Both are still forwarded
// — a refused RETURN is the answer to a question one bank asked and is owed,
// which is the asymmetry receiveSettlementStatus has and this one does not —
// but what they carry with them differs:
//
//   - ACSC: the answer goes to the bank that asked, and the held pacs.004 goes
//     to the other bank, which posts the leg the returner does not hold. The
//     ANSWER goes first, so the bank that asked hears the outcome before the
//     other bank starts moving money on it. Both are attempted regardless, and
//     the errors are joined, for the reason tell gives: a failed message to one
//     bank must not silently cancel the other, and the second is the one that
//     moves a customer's money.
//   - RJCT: only the answer goes, and the held message is DROPPED. That is the
//     whole point of holding it — a bank that had already posted its leg
//     against a return the settlement agent refused would have moved a
//     customer's money for nothing.
//
// The delete runs BEFORE the release, so a release that fails takes the message
// with it and no later answer can recover it. That is deliberate — an entry kept
// on failure would be retried by nothing and swept by nothing — and its cost is
// the same one csm.relayReturn records for a restart, reached a different way:
// the reserves are final, the returning bank's leg is posted, and the other
// bank's never will be. Unreachable in this transport for Mesh.send's three
// reasons, and Task 19's to notice.
//
// An entry is otherwise dropped only when an answer REACHES the delete, and two
// things can stop it. A return the settlement agent dead-letters is never
// answered at all — a redelivered pacs.004 is the reachable case — so the entry
// it overwrote stays. And an answer this handler cannot act on returns above the
// delete: a payment it cannot read, a scheme it does not hold, a participant it
// cannot address. Both leak one entry apiece, both are bounded by the number of
// returns a process carries, and both are recorded rather than swept: a sweep
// needs a clock and a policy, and this map needs a store first. See
// relayReturn.
func (c *csm) receiveReturnStatus(ctx context.Context, from iso20022.BIC, doc *iso20022.Pacs002) error {
	orig, reports := payment.ReadStatus(doc)
	for _, r := range reports {
		if r.TxID == "" {
			// A return status naming no payment. The settlement agent quotes
			// the payment it was asked about even when it refuses, so this is a
			// message this actor cannot act on and has nobody to ask about.
			return fmt.Errorf("mesh: %s got a return status from %s naming no payment", c.bic, from)
		}
		id := payment.PaymentID(r.TxID)
		p, err := c.ops.GetPayment(ctx, id)
		if err != nil {
			return fmt.Errorf("mesh: %s was told about the return of %s and cannot read it: %w", c.bic, r.TxID, err)
		}
		scheme, ok := c.ops.Scheme(p.Scheme)
		if !ok {
			return fmt.Errorf("mesh: %s was told about the return of %s and holds no %q scheme to say who asked: %w",
				c.bic, p.ID, p.Scheme, payment.ErrSchemeNotFound)
		}
		// The returner's address is on the payment, as every address on this actor
		// now is. It was a participant id turned into a BIC by reading that bank's
		// own row; see payment.PartyRef.
		returner := returnerOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent)
		// The SETTLEMENT AGENT decided this, not the clearing house, and the
		// message says so. It is the one hop in this system where the sender and
		// the originator are different institutions; see forwardDecision.
		errs := []error{c.forwardDecision(returner, from, orig, r)}

		held, holding := c.held[id]
		delete(c.held, id)
		if holding && r.Status == iso20022.TransactionStatusSettlementCompleted {
			errs = append(errs, c.releaseReturn(held, id))
		}
		if r.Status == iso20022.TransactionStatusSettlementCompleted {
			// This institution's own copy, which nothing else writes any more. The
			// return's two customer legs land in the two BANKS' databases, so what
			// used to move this row — the second of them — is not reachable from
			// here. See payment.CompleteReturnTx.
			//
			// After the release rather than before it, so the message that makes
			// the other bank post is not held up by this actor's bookkeeping; and
			// its error is joined rather than returned, for the reason the whole
			// block joins: a failure in one of these must not silently cancel the
			// others.
			if _, err := c.ops.CompleteReturn(ctx, id); err != nil {
				errs = append(errs, fmt.Errorf("mesh: %s could not record the return of %s: %w", c.bic, id, err))
			}
		}
		if err := errors.Join(errs...); err != nil {
			return err
		}
	}
	return nil
}

// endToEndOf is the payer's own reference for a payment, or the EPC's
// convention where there is none.
//
// It repeats payment's unexported helper of the same name rather than reaching
// for it, because these two references must agree: a bank matching the answer to
// its instruction compares what it sent with what came back, and a status
// quoting an empty string against a pacs.008 carrying NOTPROVIDED would not
// match. See mesh.notProvided for the whole of that convention.
func endToEndOf(p payment.Payment) string {
	if p.EndToEndID == "" {
		return notProvided
	}
	return p.EndToEndID
}

// answer rejects a message the clearing house could not carry out, back to
// whoever sent it. No payment changes state: these are refusals of the MESSAGE —
// a bulk file, an agent this mesh cannot route to — decided before any payment
// was looked up.
func (c *csm) answer(to iso20022.BIC, orig payment.OriginalMessage, ref iso20022.PaymentIdentification, code iso20022.StatusReason, text string) error {
	return c.forward(to, orig, payment.TransactionStatusReport{
		EndToEndID: ref.EndToEndId,
		TxID:       ref.TxId,
		Status:     iso20022.TransactionStatusRejected,
		Code:       code,
		Text:       text,
	})
}
