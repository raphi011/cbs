package main

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
type lodgementFails struct {
	settlementOps
	err error
}

func (o lodgementFails) ReceiveLodgement(context.Context, payment.LodgementInstruction) (payment.LodgementReceipt, error) {
	return payment.LodgementReceipt{}, o.err
}

// lodgementFor runs the lodging bank's own half for real and hands back the
// camt.050 it would have sent.
func lodgementFor(t *testing.T, h *harness, amount ledger.Amount, msgID string) (iso20022.AppHdr, *iso20022.Camt050) {
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
func TestAStoreFailureAtTheAgentIsNotARefusal(t *testing.T) {
	h := newHarness(t)
	hdr, doc := lodgementFor(t, h, 40_000, "lodge-store-failure")

	broken := errors.New("store: the retry budget ran out")
	cb := &CentralBank{
		d:    h.dep,
		net:  h.cb(),
		ops:  lodgementFails{settlementOps: h.cb(), err: broken},
		bic:  h.cfg.CentralBankBIC,
		host: h.dep.CentralBank().host,
	}

	mark := h.messagesSeen()
	err := cb.receiveLodgement(context.Background(), h.debtorBIC, hdr, doc)
	if err == nil {
		t.Fatal("a store failure at the settlement agent was ANSWERED rather than reported; " +
			"the member has posted its leg and is now being told the lodgement did not happen")
	}
	if !errors.Is(err, broken) {
		t.Errorf("the reported problem does not carry the cause: %v", err)
	}
	// The tap records a file when it CROSSES, so a receipt sitting in a queue is
	// not on it until somebody collects.
	h.work(t)
	if sent := h.messagesFrom(mark); len(sent) != 0 {
		t.Errorf("a store failure put %d messages on the wire, want none; "+
			"there is nothing true to tell the member", len(sent))
	}
}

// TestALodgementRefusalIsAJudgement is the other side of the same
// discrimination, and it is what stops the fix for the test above from being
// "never answer".
func TestALodgementRefusalIsAJudgement(t *testing.T) {
	for _, sentinel := range lodgementRefusals {
		t.Run(sentinel.Error(), func(t *testing.T) {
			h := newHarness(t)
			hdr, doc := lodgementFor(t, h, 40_000, "lodge-refused")

			refusal := fmt.Errorf("payment: %s lodges EUR: %w", h.debtorBIC, sentinel)
			cb := &CentralBank{
				d:    h.dep,
				net:  h.cb(),
				ops:  lodgementFails{settlementOps: h.cb(), err: refusal},
				bic:  h.cfg.CentralBankBIC,
				host: h.dep.CentralBank().host,
			}

			if err := cb.receiveLodgement(context.Background(), h.debtorBIC, hdr, doc); err != nil {
				t.Fatalf("a judgement about the request became a reported problem: %v", err)
			}
			h.work(t)

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
