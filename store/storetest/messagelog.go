package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// The message log is ONE body run three times, because messages is one table in
// all three schemas and an institution's traffic is the same shape whichever
// institution it is. The three suites each hand it their own unit of work.

// A messageLog is one institution's store narrowed to its message log. The
// three suites build one apiece, which is what lets this file name no
// institution.
type messageLog struct {
	update func(*testing.T, func(context.Context, payment.MessageLogTx) error)
	view   func(*testing.T, func(context.Context, payment.MessageLogTx) error)
}

// bankMessageLog and the two beside it narrow one institution's store.
func bankMessageLog(s payment.BankStore) messageLog {
	return messageLog{
		update: func(t *testing.T, fn func(context.Context, payment.MessageLogTx) error) {
			t.Helper()
			updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error { return fn(ctx, tx) })
		},
		view: func(t *testing.T, fn func(context.Context, payment.MessageLogTx) error) {
			t.Helper()
			viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error { return fn(ctx, tx) })
		},
	}
}

func clearingHouseMessageLog(s payment.ClearingHouseStore) messageLog {
	return messageLog{
		update: func(t *testing.T, fn func(context.Context, payment.MessageLogTx) error) {
			t.Helper()
			updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error { return fn(ctx, tx) })
		},
		view: func(t *testing.T, fn func(context.Context, payment.MessageLogTx) error) {
			t.Helper()
			viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error { return fn(ctx, tx) })
		},
	}
}

func centralBankMessageLog(s payment.CentralBankStore) messageLog {
	return messageLog{
		update: func(t *testing.T, fn func(context.Context, payment.MessageLogTx) error) {
			t.Helper()
			updateCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error { return fn(ctx, tx) })
		},
		view: func(t *testing.T, fn func(context.Context, payment.MessageLogTx) error) {
			t.Helper()
			viewCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error { return fn(ctx, tx) })
		},
	}
}

// sentTo and receivedFrom are the two halves of a crossing, as the institution
// that is logging them sees it.
func sentTo(to iso20022.BIC, msgID string, payments ...payment.PaymentID) payment.Message {
	return payment.Message{
		Direction:    payment.MessageSent,
		Counterparty: to,
		MsgDefIdr:    "pacs.008.001.08",
		MsgID:        msgID,
		OrderID:      "A001",
		At:           early,
		Payload:      []byte("<Document>" + msgID + "</Document>"),
		Payments:     payments,
	}
}

func receivedFrom(from iso20022.BIC, msgID string, payments ...payment.PaymentID) payment.Message {
	m := sentTo(from, msgID, payments...)
	m.Direction = payment.MessageReceived
	return m
}

// runMessageLog is the message log's whole suite.
func runMessageLog(t *testing.T, open func(*testing.T) messageLog) {
	t.Helper()

	t.Run("AMessageRoundTripsWithThePaymentsItCarried", func(t *testing.T) {
		log := open(t)

		sent := sentTo(verdeBIC, "MSG-1", "pay_1", "pay_2")
		log.update(t, func(ctx context.Context, tx payment.MessageLogTx) error {
			return tx.AppendMessage(ctx, sent)
		})

		var got []payment.Message
		log.view(t, func(ctx context.Context, tx payment.MessageLogTx) error {
			var err error
			got, err = tx.ListMessages(ctx, payment.MessageFilter{})
			return err
		})

		if len(got) != 1 {
			t.Fatalf("the log holds %d messages after one append, want 1", len(got))
		}
		assertEqual(t, "direction", string(got[0].Direction), string(payment.MessageSent))
		assertEqual(t, "counterparty", string(got[0].Counterparty), string(verdeBIC))
		assertEqual(t, "message definition", got[0].MsgDefIdr, "pacs.008.001.08")
		assertEqual(t, "message id", got[0].MsgID, "MSG-1")
		assertEqual(t, "order id", got[0].OrderID, "A001")
		assertEqual(t, "payload", string(got[0].Payload), string(sent.Payload))
		if !got[0].At.Equal(early) {
			t.Errorf("the message was seen at %v, want %v", got[0].At, early)
		}
		// The payments come back in DOCUMENT order, which is the order they were
		// recorded in and not the order their ids sort in.
		assertEqual(t, "payments carried", len(got[0].Payments), 2)
		assertEqual(t, "first payment", string(got[0].Payments[0]), "pay_1")
		assertEqual(t, "second payment", string(got[0].Payments[1]), "pay_2")
	})

	// The store allocates seq, so a caller cannot choose one and two appends
	// cannot collide. It is a total order over this institution's whole traffic.
	t.Run("SeqIsAllocatedByTheStoreAndOrdersTheWholeLog", func(t *testing.T) {
		log := open(t)

		log.update(t, func(ctx context.Context, tx payment.MessageLogTx) error {
			first := sentTo(verdeBIC, "MSG-1")
			first.Seq = 99 // whatever a caller sets is overwritten
			if err := tx.AppendMessage(ctx, first); err != nil {
				return err
			}
			return tx.AppendMessage(ctx, receivedFrom(verdeBIC, "MSG-2"))
		})

		var got []payment.Message
		log.view(t, func(ctx context.Context, tx payment.MessageLogTx) error {
			var err error
			got, err = tx.ListMessages(ctx, payment.MessageFilter{})
			return err
		})

		if len(got) != 2 {
			t.Fatalf("the log holds %d messages, want 2", len(got))
		}
		assertEqual(t, "first message id", got[0].MsgID, "MSG-1")
		assertEqual(t, "second message id", got[1].MsgID, "MSG-2")
		if got[0].Seq >= got[1].Seq {
			t.Errorf("seq went %d then %d; the log is ordered by the order it was written in", got[0].Seq, got[1].Seq)
		}
	})

	// The filter is what GET /messages and a payment's document list are built
	// out of.
	t.Run("TheFilterNarrowsByDirectionCounterpartyAndPayment", func(t *testing.T) {
		log := open(t)

		log.update(t, func(ctx context.Context, tx payment.MessageLogTx) error {
			if err := tx.AppendMessage(ctx, sentTo(verdeBIC, "MSG-1", "pay_1")); err != nil {
				return err
			}
			if err := tx.AppendMessage(ctx, receivedFrom(verdeBIC, "MSG-2", "pay_1", "pay_2")); err != nil {
				return err
			}
			return tx.AppendMessage(ctx, receivedFrom(auroraBIC, "MSG-3", "pay_3"))
		})

		list := func(f payment.MessageFilter) []payment.Message {
			t.Helper()
			var out []payment.Message
			log.view(t, func(ctx context.Context, tx payment.MessageLogTx) error {
				var err error
				out, err = tx.ListMessages(ctx, f)
				return err
			})
			return out
		}

		assertEqual(t, "sent", len(list(payment.MessageFilter{Direction: payment.MessageSent})), 1)
		assertEqual(t, "received", len(list(payment.MessageFilter{Direction: payment.MessageReceived})), 2)
		assertEqual(t, "one counterparty's traffic", len(list(payment.MessageFilter{Counterparty: verdeBIC})), 2)

		// The one the payment detail page asks: which files carried this payment.
		carried := list(payment.MessageFilter{PaymentID: "pay_1"})
		assertEqual(t, "files carrying pay_1", len(carried), 2)
		assertEqual(t, "the first of them", carried[0].MsgID, "MSG-1")
		assertEqual(t, "the second of them", carried[1].MsgID, "MSG-2")
		assertEqual(t, "files carrying an unknown payment",
			len(list(payment.MessageFilter{PaymentID: "pay_nope"})), 0)

		// Two predicates at once, which is what a viewer filtering a listing does.
		both := list(payment.MessageFilter{Direction: payment.MessageReceived, Counterparty: verdeBIC})
		assertEqual(t, "received from one counterparty", len(both), 1)
		assertEqual(t, "and it is the right one", both[0].MsgID, "MSG-2")

		// And one seq is one message, which is how the file itself is fetched.
		one := list(payment.MessageFilter{Seq: carried[1].Seq})
		assertEqual(t, "the message under one seq", len(one), 1)
		assertEqual(t, "and it is that message", one[0].MsgID, "MSG-2")
		assertEqual(t, "a seq nothing was written under",
			len(list(payment.MessageFilter{Seq: carried[1].Seq + 1000})), 0)
	})

	// Before and Limit page the log the way AuditFilter's do: a limit takes the
	// NEWEST matches and still hands them back oldest first.
	t.Run("BeforeAndLimitPageTheLog", func(t *testing.T) {
		log := open(t)

		log.update(t, func(ctx context.Context, tx payment.MessageLogTx) error {
			for _, id := range []string{"MSG-1", "MSG-2", "MSG-3"} {
				if err := tx.AppendMessage(ctx, sentTo(verdeBIC, id)); err != nil {
					return err
				}
			}
			return nil
		})

		var all, page []payment.Message
		log.view(t, func(ctx context.Context, tx payment.MessageLogTx) error {
			var err error
			if all, err = tx.ListMessages(ctx, payment.MessageFilter{}); err != nil {
				return err
			}
			page, err = tx.ListMessages(ctx, payment.MessageFilter{Limit: 2})
			return err
		})

		assertEqual(t, "the whole log", len(all), 3)
		assertEqual(t, "a page of two", len(page), 2)
		assertEqual(t, "the page holds the newest two, oldest first", page[0].MsgID, "MSG-2")
		assertEqual(t, "and ends at the newest", page[1].MsgID, "MSG-3")

		var before []payment.Message
		log.view(t, func(ctx context.Context, tx payment.MessageLogTx) error {
			var err error
			before, err = tx.ListMessages(ctx, payment.MessageFilter{Before: all[1].Seq})
			return err
		})
		assertEqual(t, "everything before the second message", len(before), 1)
		assertEqual(t, "which is the first", before[0].MsgID, "MSG-1")
	})

	// A message naming no payment is ordinary: a routing table, a statement and
	// an unreadable-file answer all carry none.
	t.Run("AMessageNamingNoPaymentIsOrdinary", func(t *testing.T) {
		log := open(t)

		log.update(t, func(ctx context.Context, tx payment.MessageLogTx) error {
			m := sentTo(verdeBIC, "MSG-1")
			m.MsgDefIdr = "camt.053.001.08"
			m.OrderID = "" // a published snapshot is minted no order id
			return tx.AppendMessage(ctx, m)
		})

		var got []payment.Message
		log.view(t, func(ctx context.Context, tx payment.MessageLogTx) error {
			var err error
			got, err = tx.ListMessages(ctx, payment.MessageFilter{})
			return err
		})
		assertEqual(t, "messages", len(got), 1)
		assertEqual(t, "order id", got[0].OrderID, "")
		assertEqual(t, "payments carried", len(got[0].Payments), 0)
	})

	// An index over a log that keeps every file forever must not carry the files
	// in it, and the size is what stands in for one.
	t.Run("AListingLeavesTheFilesUnreadAndStillAnswersTheirSize", func(t *testing.T) {
		log := open(t)

		sent := sentTo(verdeBIC, "MSG-1", "pay_1")
		log.update(t, func(ctx context.Context, tx payment.MessageLogTx) error {
			return tx.AppendMessage(ctx, sent)
		})

		var index, whole []payment.Message
		log.view(t, func(ctx context.Context, tx payment.MessageLogTx) error {
			var err error
			if index, err = tx.ListMessages(ctx, payment.MessageFilter{WithoutPayload: true}); err != nil {
				return err
			}
			whole, err = tx.ListMessages(ctx, payment.MessageFilter{})
			return err
		})

		assertEqual(t, "messages in the index", len(index), 1)
		if len(index[0].Payload) != 0 {
			t.Errorf("the index carried %d bytes of the file; it is an index", len(index[0].Payload))
		}
		assertEqual(t, "the size the index reports", index[0].PayloadSize, len(sent.Payload))
		// And it is the same listing otherwise: the payments still come with it.
		assertEqual(t, "payments carried", len(index[0].Payments), 1)
		assertEqual(t, "the file the document route reads", string(whole[0].Payload), string(sent.Payload))
		assertEqual(t, "and it reports the same size", whole[0].PayloadSize, len(sent.Payload))
	})

	// A log is a record: nothing deletes a row, and only Reset clears it. The
	// three Reset cases assert the clearing; this asserts the appending.
	t.Run("AppendingTwiceKeepsBoth", func(t *testing.T) {
		log := open(t)

		same := sentTo(verdeBIC, "MSG-1", "pay_1")
		log.update(t, func(ctx context.Context, tx payment.MessageLogTx) error {
			if err := tx.AppendMessage(ctx, same); err != nil {
				return err
			}
			return tx.AppendMessage(ctx, same)
		})

		var got []payment.Message
		log.view(t, func(ctx context.Context, tx payment.MessageLogTx) error {
			var err error
			got, err = tx.ListMessages(ctx, payment.MessageFilter{})
			return err
		})
		assertEqual(t, "two appends of one file", len(got), 2)
		if got[0].Seq == got[1].Seq {
			t.Errorf("both rows are seq %d; an append is never an upsert", got[0].Seq)
		}
	})

	// A message with no timestamp is one nobody stamped, and it comes back as the
	// zero time rather than as now.
	t.Run("AnUnstampedMessageComesBackUnstamped", func(t *testing.T) {
		log := open(t)

		log.update(t, func(ctx context.Context, tx payment.MessageLogTx) error {
			m := sentTo(verdeBIC, "MSG-1")
			m.At = time.Time{}
			return tx.AppendMessage(ctx, m)
		})

		var got []payment.Message
		log.view(t, func(ctx context.Context, tx payment.MessageLogTx) error {
			var err error
			got, err = tx.ListMessages(ctx, payment.MessageFilter{})
			return err
		})
		assertEqual(t, "messages", len(got), 1)
		if !got[0].At.IsZero() {
			t.Errorf("an unstamped message came back at %v, want the zero time", got[0].At)
		}
	})
}
