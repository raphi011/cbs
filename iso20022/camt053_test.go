package iso20022

import (
	"testing"
	"time"
)

// testTime is this package's fixture instant.
var testTime = time.Date(2026, 8, 1, 10, 30, 0, 0, time.UTC)

// TestCamt053GoldenRoundTrip is the statement's round trip through the codec,
// against a committed sample.
func TestCamt053GoldenRoundTrip(t *testing.T) {
	env := assertGoldenRoundTrip(t, "camt053.xml")

	doc, ok := env.Document.(*Camt053)
	if !ok {
		t.Fatalf("document is %T, want *Camt053", env.Document)
	}
	if got, want := len(doc.BkToCstmrStmt.Stmt), 1; got != want {
		t.Fatalf("statements = %d, want %d", got, want)
	}
	stmt := doc.BkToCstmrStmt.Stmt[0]
	if got := stmt.Acct.Id.Othr; got == nil || got.Id != "acc_cb_reserve_bank_2" {
		t.Errorf("statement account = %v, want the Othr arm carrying acc_cb_reserve_bank_2", got)
	}
	if got, want := len(stmt.Ntry), 1; got != want {
		t.Fatalf("entries = %d, want %d", got, want)
	}
	if got, want := stmt.Ntry[0].CdtDbtInd, CreditDebitCredit; got != want {
		t.Errorf("entry indicator = %q, want %q", got, want)
	}
	if got, want := stmt.Bal[0].Tp.CdOrPrtry.Cd, BalanceTypeClosingBooked; got != want {
		t.Errorf("balance type = %q, want %q", got, want)
	}
}

// TestCamt053RefusesAStatementWithNoBalance pins the element that makes this
// message worth choosing over a camt.054.
func TestCamt053RefusesAStatementWithNoBalance(t *testing.T) {
	doc := &Camt053{BkToCstmrStmt: BankToCustomerStatement{
		GrpHdr: StatementGroupHeader{MsgId: "msg_1", CreDtTm: ISODateTime{Time: testTime}},
		Stmt: []AccountStatement{{
			Id:      "set_1",
			CreDtTm: ISODateTime{Time: testTime},
			Acct:    CashAccount{Id: AccountIdentification4Choice{Othr: &GenericAccountIdentification{Id: "acc_1"}}},
			Ntry: []StatementEntry{{
				Amt:       ActiveCurrencyAndAmount{Ccy: "EUR", Value: "10.00"},
				CdtDbtInd: CreditDebitCredit,
				Sts:       EntryStatusChoice{Cd: EntryStatusBooked},
				BookgDt:   DateAndDateTime{Dt: &ISODate{Time: testTime}},
				ValDt:     DateAndDateTime{Dt: &ISODate{Time: testTime}},
			}},
		}},
	}}
	if err := doc.validate(); err == nil {
		t.Fatal("a statement with no balance was accepted; camt.053 is the balance-anchored message")
	}
}
