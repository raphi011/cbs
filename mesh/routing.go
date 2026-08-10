package mesh

import (
	"fmt"
	"time"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// What every actor in this package needs and no actor owns: the clock, the
// message identifiers, the send, the two rules that name a party, and the answer
// to a message that would not parse.

// now is the clock every header this mesh writes is stamped from, and it is the
// payment network's own.
//
// A mesh with a clock of its own would be a second answer to what time it is,
// and under the frozen clock these tests run on the two would be years apart: a
// pacs.008 dated 2026 carrying a payment booked in 2025. The package doc says
// the actors share one clock; this is that sentence's implementation.
//
// It is only ever called from a handler or from a door, both of which exist only
// on a mesh that has a network.
func (m *Mesh) now() time.Time { return m.clearingHouse.Now() }

// nextMsgID mints the identifier a message travels under: the sender's BIC and a
// number nobody else in this mesh will use.
//
// Per-mesh and not per-sender, which is a simplification worth naming. A real
// BizMsgIdr is unique within the sender and nowhere else, so two banks
// legitimately emit the same one; a receiver that assumed otherwise would
// deduplicate one bank's message against another's. Here one counter serves
// every actor, which is strictly stronger than the standard requires and cannot
// therefore hide a bug the standard would allow. What it does buy is that a
// message id is unique under a FROZEN clock, which a timestamp-derived one would
// not be — and this package's tests run on one.
func (m *Mesh) nextMsgID(from iso20022.BIC) string {
	return fmt.Sprintf("%s-%d", from, m.msgSeq.Add(1))
}

// send marshals an envelope and hands the BYTES to the transport.
//
// The split is where the domain stops. Structs never cross an actor boundary —
// if two actors exchanged a *Pacs008 the message format would be decoration on a
// function call, malformed input would stop being a reachable failure mode, and
// the FF01 path would be untestable — so this is the only place a document
// becomes bytes, and wire.Bus.Send is the only place bytes become a message
// waiting in an inbox.
func (m *Mesh) send(from, to iso20022.BIC, env iso20022.Envelope) error {
	raw, err := iso20022.Marshal(env)
	if err != nil {
		return fmt.Errorf("mesh: marshalling for %s: %w", to, err)
	}
	return m.bus.Send(from, to, raw)
}

// sendRaw puts bytes on the wire that this system's marshaller never blessed.
//
// Nothing in production calls it — send is the only door in — but without it
// every message in this package would be one iso20022.Marshal had approved, and
// FF01 would be a code with no path to it. See
// TestAMalformedEnvelopeIsAnsweredWithFF01.
func (m *Mesh) sendRaw(from, to iso20022.BIC, raw []byte) error {
	return m.bus.Send(from, to, raw)
}

// submitterOf is the party whose bank hands a payment to the clearing house.
//
// One rule, two directions, and every actor in this package that has to name the
// instructing agent uses it: Mesh.Submit to choose whose goroutine does the
// submitting half, and csm.receiveStatus to choose whose inbox the answer goes
// back to. Written once because the two must agree — an answer addressed to a
// bank that did not submit is a message nobody was waiting for.
//
// It takes the two AGENTS rather than a Payment, because Submit has only a
// request and a request is not yet a payment. What all four callers want is an
// address to send to. See payment.PartyRef.
func submitterOf(scheme payment.Scheme, debtorAgent, creditorAgent iso20022.BIC) iso20022.BIC {
	if scheme.Direction() == payment.Pull {
		return creditorAgent
	}
	return debtorAgent
}

// returnerOf is the party whose bank sends a settled payment back: submitterOf's
// counterpart in both senses, the other party and the other role.
//
// The rule itself is payment.ReturnerOf, and that is where its reasoning lives.
// It moved out of this package when the domain acquired a second use for it —
// payment.PostReturnLegTx decides whether a bank may REFUSE its leg by asking
// whether that bank is the returner — and two copies would have been free to
// disagree about who the returner is. This stays as a delegation so that the
// call sites here, which read as mesh-local rules beside submitterOf, do not
// have to.
func returnerOf(scheme payment.Scheme, debtorAgent, creditorAgent iso20022.BIC) iso20022.BIC {
	return payment.ReturnerOf(scheme, debtorAgent, creditorAgent)
}

// returnMsgDef is the pacs.004's message name, which two actors here dispatch a
// pacs.002 by.
//
// A status report says which message definition it is ABOUT — OrgnlMsgNmId,
// written by payment.StatusMessage and read back by payment.ReadStatus — and
// that element is load-bearing in this mesh rather than decorative. The clearing
// house is answered by the settlement agent about a CYCLE it instructed and
// about a PAYMENT whose return it forwarded, and the two answers are the same
// message definition arriving from the same BIC; reading one as the other would
// look a cycle id up as a payment. A bank is answered about the instruction it
// submitted and about the return it asked for, and only the first is ever a
// reason to give a payer their money back.
//
// Taken from the codec rather than written out as a literal, so that it cannot
// drift from the identifier iso20022 actually puts on the wire.
var returnMsgDef = iso20022.Pacs004{}.MessageDefinitionIdentifier()

// isAbout reports whether a status answers a message of the given definition.
//
// It parses the document a second time, which is cheap and deliberate:
// payment.ReadStatus is pure, and the alternative is threading the parse
// through a dispatch that exists precisely to decide WHICH handler should do
// the reading.
func isAbout(doc *iso20022.Pacs002, msgDef string) bool {
	orig, _ := payment.ReadStatus(doc)
	return orig.MsgDefIdr == msgDef
}

// notProvided is what a message says where a reference is genuinely unavailable.
//
// It is the EPC's convention, already used by payment for a credit transfer with
// no end-to-end reference, and it is here for the one case that has no reference
// at all: a pacs.002 answering a file that did not parse. That report cannot
// quote the original's message id, its message name or the transaction's
// references, because the bytes carrying them were unreadable — and this
// package's pacs.002 makes all four mandatory (iso20022.OriginalGroupHeader and
// PaymentTransactionStatus, the latter needing at least one back-reference).
//
// A real network would not have the problem: an unreadable file is answered with
// admi.002 MessageReject, which refers back by nothing and carries a reason on
// its own. This system has no admi.002, so the FF01 goes in the message it does
// have, with the unavailable references named as unavailable rather than
// invented. The substitution is recorded here rather than hidden in the one
// function that makes it.
const notProvided = "NOTPROVIDED"

// answerUnreadable tells whoever sent a message that it could not be parsed.
//
// This is why the transport carries the sender beside the bytes. A message whose
// header is unreadable cannot say who it is from, so a receiver that had only
// the bytes could DETECT a malformed file and not answer it — which would make
// FF01, the one rejection every real receiver must be able to send, the one this
// system could not.
//
// The answer is a pacs.002 and not a dead letter, and the handler that calls
// this returns nil: a message it answered is a message it dealt with. What
// cannot be dealt with is the answer failing to send, which comes back as the
// dead letter it is.
func (m *Mesh) answerUnreadable(self, sender iso20022.BIC, cause error) error {
	env, err := payment.StatusMessage(
		payment.OriginalMessage{MsgID: notProvided, MsgDefIdr: notProvided},
		[]payment.TransactionStatusReport{{
			EndToEndID: notProvided,
			Status:     iso20022.TransactionStatusRejected,
			Code:       iso20022.StatusReasonInvalidFileFormat,
			Text:       cause.Error(),
		}},
		payment.MessageContext{From: self, To: sender, MsgID: m.nextMsgID(self), Now: m.now()},
	)
	if err != nil {
		return fmt.Errorf("mesh: %s could not build the FF01 for %s: %w", self, sender, err)
	}
	return m.send(self, sender, env)
}
