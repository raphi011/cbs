package ledger

import "testing"

func TestAccountTypeCodeBlock(t *testing.T) {
	cases := map[AccountType]int{
		Asset: 100, Liability: 200, Equity: 300, Revenue: 400, Expense: 500,
	}
	for typ, want := range cases {
		if got := typ.codeBlock(); got != want {
			t.Errorf("%s.codeBlock() = %d, want %d", typ, got, want)
		}
	}
}

func TestSubledgerNumbering(t *testing.T) {
	book := NewBook()
	gl, _ := book.CreateLedger("GL")
	for i, want := range []string{"100", "200", "300"} {
		sl, err := book.CreateSubledger(gl.ID, "S")
		if err != nil {
			t.Fatalf("CreateSubledger #%d: %v", i, err)
		}
		if string(sl.ID) != want {
			t.Errorf("subledger #%d ID = %q, want %q", i, sl.ID, want)
		}
	}
}

func TestAccountNumbering(t *testing.T) {
	book := NewBook()
	gl, _ := book.CreateLedger("GL")
	deposits, _ := book.CreateSubledger(gl.ID, "Customer Deposits") // 100
	interbank, _ := book.CreateSubledger(gl.ID, "Interbank")        // 200

	alice, _ := book.CreateAccount(deposits.ID, "Alice", Liability)
	bob, _ := book.CreateAccount(deposits.ID, "Bob", Liability)
	suspense, _ := book.CreateAccount(interbank.ID, "Clearing Suspense", Liability)
	reserve, _ := book.CreateAccount(interbank.ID, "Reserve", Asset)

	want := map[string]string{
		"alice": "200.100.001", "bob": "200.100.002",
		"suspense": "200.200.001", "reserve": "100.200.001",
	}
	got := map[string]string{
		"alice": string(alice.ID), "bob": string(bob.ID),
		"suspense": string(suspense.ID), "reserve": string(reserve.ID),
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s ID = %q, want %q", k, got[k], w)
		}
	}
}

func TestAccountNumberingDeterministic(t *testing.T) {
	build := func() AccountID {
		book := NewBook()
		gl, _ := book.CreateLedger("GL")
		sl, _ := book.CreateSubledger(gl.ID, "Deposits")
		a, _ := book.CreateAccount(sl.ID, "Alice", Liability)
		return a.ID
	}
	if build() != build() {
		t.Error("account IDs are not deterministic across books")
	}
}
