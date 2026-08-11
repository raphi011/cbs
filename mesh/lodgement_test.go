package mesh

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// lodgementFails is a settlement agent whose domain act refuses everything with
// one error, and is otherwise the real one.
//
// It embeds settlementOps rather than implementing it, so a method added to that
// interface reaches these tests without touching them — and so the ONE method
// under test is the only one that is not the real implementation. Everything
// receiveLodgement does before and after the act is therefore genuine: the reader,
// the message id, the send.
type lodgementFails struct {
	settlementOps
	err error
}

func (o lodgementFails) ReceiveLodgement(context.Context, payment.LodgementInstruction) (payment.LodgementReceipt, error) {
	return payment.LodgementReceipt{}, o.err
}

// lodgementFor runs the lodging bank's own half for real and hands back the
// camt.050 it would have sent.
//
// The leg is COMMITTED by the time this returns, which is the premise both tests
// below rest on and the reason neither can be written against a hand-built
// message: payment.LodgeReservesTx posts Debit Reserve / Credit Vault Cash before
// the request exists, so what the agent's answer is about is money that has
// already moved in the member's book.
func lodgementFor(t *testing.T, h *meshHarness, amount ledger.Amount, msgID string) (iso20022.AppHdr, *iso20022.Camt050) {
	t.Helper()
	ctx := context.Background()

	// A fresh deposit, so there is unlodged cash in the vault to move. The
	// fixture has already lodged its own funding.
	if err := h.bank(h.debtor.BIC).Deposit(ctx, h.debtor.ID, h.debtorAcct.ID, amount, "cash in"); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	_, env, err := h.bank(h.debtor.BIC).LodgeReserves(ctx, "EUR", amount, payment.MessageContext{
		From:  h.debtorBIC,
		To:    h.cfg.CentralBankBIC,
		MsgID: msgID,
		Now:   testTime,
	})
	if err != nil {
		t.Fatalf("LodgeReserves: %v", err)
	}
	return env.AppHdr, env.Document.(*iso20022.Camt050)
}

// TestAStoreFailureAtTheAgentIsNotARefusal is the money test on this branch's
// newest conversation, and the defect it pins was live until a whole-branch
// review found it.
//
// receiveLodgement answered EVERY non-duplicate error with a refusing camt.025.
// That is safe for a request whose sender has posted nothing, and a lodging
// member has already committed its leg. So a store failure at the agent — a retry budget
// exhausted under contention, a cancelled context, an I/O error — came back to
// the member as a JUDGEMENT about its request, and the member's reserve mirror
// stayed raised against an agent whose book never moved. Nothing retries and
// nothing can unwind it, because the amount is not on the receipt.
//
// What is asserted is the discrimination and not the mirror: both outcomes leave
// the mirror overstated, and saying otherwise would be the overclaim this
// package's log keeps recording. The difference this test is about is that a dead
// letter is a visible break an operator can re-drive, and a refusal is a break
// dressed as a completed conversation.
//
// The error is deliberately one no table classifies. A sentinel would prove
// nothing here — the question is what happens to an error the agent has never
// heard of, which is what a store failure is.
func TestAStoreFailureAtTheAgentIsNotARefusal(t *testing.T) {
	h := newMeshHarness(t)
	hdr, doc := lodgementFor(t, h, 40_000, "lodge-store-failure")

	broken := errors.New("store: the retry budget ran out")
	cb := &centralBank{
		m:   h.mesh,
		ops: lodgementFails{settlementOps: h.cb(), err: broken},
		bic: h.cfg.CentralBankBIC,
	}

	mark := h.messagesSeen()
	err := cb.receiveLodgement(context.Background(), h.debtorBIC, hdr, doc)
	if err == nil {
		t.Fatal("a store failure at the settlement agent was ANSWERED rather than dead-lettered; " +
			"the member has posted its leg and is now being told the lodgement did not happen")
	}
	if !errors.Is(err, broken) {
		t.Errorf("the dead letter does not carry the cause: %v", err)
	}
	// The tap records a message when an actor is HANDED it, not when it is sent,
	// so the count is only a count of what went on the wire after a drain. The
	// camt.050 was never sent — this test calls the handler directly — so a drain
	// here settles the receipt and nothing else.
	h.drain(t)
	if sent := h.messagesFrom(mark); len(sent) != 0 {
		t.Errorf("a store failure put %d messages on the wire, want none; "+
			"there is nothing true to tell the member", len(sent))
	}
}

// TestALodgementRefusalIsAJudgement is the other side of the same discrimination,
// and it is what stops the fix for the test above from being "never answer".
//
// Every sentinel in lodgementRefusals is a judgement the agent made about the
// request — a member it holds no account for, an asset it holds none in for that
// member, an account number that is not the one it holds, an amount that is not
// an amount. Each of those the member can act on, and refusing to a counterparty
// is completed work rather than a defect.
//
// It also pins the LIST against payment.ReceiveLodgementTx's "What it refuses"
// section. The two are a pair maintained by hand, and a sentinel added there and
// not here would silently become a dead letter.
//
// Each is wrapped before it is returned, because that is how the domain returns
// them — with the BIC and the account the refusal is about, which is the prose the
// receipt carries as its Desc. An identity check would pass here and fail in
// production.
func TestALodgementRefusalIsAJudgement(t *testing.T) {
	for _, sentinel := range lodgementRefusals {
		t.Run(sentinel.Error(), func(t *testing.T) {
			h := newMeshHarness(t)
			hdr, doc := lodgementFor(t, h, 40_000, "lodge-refused")

			refusal := fmt.Errorf("payment: %s lodges EUR: %w", h.debtorBIC, sentinel)
			cb := &centralBank{
				m:   h.mesh,
				ops: lodgementFails{settlementOps: h.cb(), err: refusal},
				bic: h.cfg.CentralBankBIC,
			}

			if err := cb.receiveLodgement(context.Background(), h.debtorBIC, hdr, doc); err != nil {
				t.Fatalf("a judgement about the request became a dead letter: %v", err)
			}
			h.drain(t)

			env, err := iso20022.Unmarshal(h.lastMessageOfTypeTo(t, h.debtorBIC, "camt.025.001.05"))
			if err != nil {
				t.Fatalf("the receipt does not parse: %v", err)
			}
			receipt := env.Document.(*iso20022.Camt025)
			if got := receipt.Rct.RctDtls[0].ReqHdlg[0].StsCd; got != string(iso20022.TransactionStatusRejected) {
				t.Errorf("the receipt reports %q, want %q", got, iso20022.TransactionStatusRejected)
			}
		})
	}
}
