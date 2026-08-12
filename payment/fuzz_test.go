package payment

import (
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
)

// fuzzInput is one payment as the fuzzer describes it: the fields a credit
// transfer's message actually reads, and nothing else.
type fuzzInput struct {
	EndToEndID   string
	DebtorName   string
	CreditorName string
	DebtorIBAN   string
	CreditorIBAN string
	Amount       int64
	Remittance   string
	Now          time.Time
}

// buildCreditTransfer drives the SAME conversion CreditTransferMessage drives.
func buildCreditTransfer(in fuzzInput) (iso20022.Envelope, error) {
	p := Payment{
		ID:          "pay_fuzz",
		Scheme:      SchemeSEPACT,
		Amount:      ledger.Amount(in.Amount),
		EndToEndID:  in.EndToEndID,
		Description: in.Remittance,
		ValueDate:   in.Now,
	}
	debtor := messageParty{
		BIC:        "AURODEFFXXX",
		Name:       in.DebtorName,
		Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: in.DebtorIBAN},
	}
	creditor := messageParty{
		BIC:        "VERDITMMXXX",
		Name:       in.CreditorName,
		Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: in.CreditorIBAN},
	}
	mc := MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "FUZZ-1", Now: in.Now}
	return creditTransfer([]outbound{{payment: p, debtor: debtor, creditor: creditor, asset: "EUR"}}, mc)
}

// FuzzTranslate drives the translation boundary: a payment becomes a message,
// the message becomes bytes, the bytes become a message again.
func FuzzTranslate(f *testing.F) {
	f.Add("e2e-1", "Aurora Customer", "Verde Customer", "DE89370400440532013000", "IT60X0542811101000000123456", int64(1000), "invoice 42")
	f.Add("", "A", "B", "DE89370400440532013000", "IT60X0542811101000000123456", int64(1), "")

	f.Fuzz(func(t *testing.T, e2e, dbtrName, cdtrName, dbtrIBAN, cdtrIBAN string, amount int64, remittance string) {
		if amount <= 0 {
			t.Skip()
		}
		env, err := buildCreditTransfer(fuzzInput{
			EndToEndID:   e2e,
			DebtorName:   dbtrName,
			CreditorName: cdtrName,
			DebtorIBAN:   dbtrIBAN,
			CreditorIBAN: cdtrIBAN,
			Amount:       amount,
			Remittance:   remittance,
			Now:          time.Unix(0, 0).UTC(),
		})
		if err != nil {
			// A refusal is fine: the translator is allowed to reject input this
			// system would never produce. What is not fine is producing a
			// message the codec then rejects.
			return
		}
		raw, err := iso20022.Marshal(env)
		if err != nil {
			t.Fatalf("built a message Marshal refuses: %v", err)
		}
		back, err := iso20022.Unmarshal(raw)
		if err != nil {
			t.Fatalf("built a message Unmarshal refuses: %v\n%s", err, raw)
		}
		if back.AppHdr.MsgDefIdr != env.AppHdr.MsgDefIdr {
			t.Fatalf("round trip changed the message definition: %q -> %q",
				env.AppHdr.MsgDefIdr, back.AppHdr.MsgDefIdr)
		}
	})
}
