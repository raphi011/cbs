package payment

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
)

// TestReasonTableCoversEverySentinel is the mechanism 7a asked for: a new
// error added to errors.go must be classified here, or this fails.
//
// It parses errors.go rather than holding a hand-written list, because a
// hand-written list is a second copy that drifts in exactly the way the table
// itself would. The AST is the only source that cannot be forgotten.
func TestReasonTableCoversEverySentinel(t *testing.T) {
	declared := sentinelNames(t)
	if len(declared) == 0 {
		t.Fatal("parsed errors.go and found no Err* sentinels; the parser is wrong, not the table")
	}

	mapped := make(map[string]bool, len(reasonTable))
	for _, m := range reasonTable {
		if mapped[m.Name] {
			t.Errorf("%s appears twice in reasonTable", m.Name)
		}
		mapped[m.Name] = true
	}

	for _, name := range declared {
		if !mapped[name] {
			t.Errorf("payment.%s has no entry in reasonTable.\n"+
				"Every sentinel must be classified: give it a StatusReason, or map it to\n"+
				"the empty code with a comment saying why it never reaches a counterparty.", name)
		}
	}
	for name := range mapped {
		if !contains(declared, name) {
			t.Errorf("reasonTable names %s, which is not a sentinel in errors.go", name)
		}
	}
}

// TestReasonTableNamesMatchTheirValues closes the drift the name-based check
// alone would leave open: an entry could pair ErrFoo with the string "ErrBar".
// Distinct error values plus a count that matches the declaration count makes
// that impossible — a mislabelled entry either duplicates a value or leaves a
// name uncovered.
func TestReasonTableNamesMatchTheirValues(t *testing.T) {
	seen := make(map[error]string, len(reasonTable))
	for _, m := range reasonTable {
		if m.Err == nil {
			t.Errorf("%s maps to a nil error", m.Name)
			continue
		}
		if prev, dup := seen[m.Err]; dup {
			t.Errorf("%s and %s are the same error value", prev, m.Name)
		}
		seen[m.Err] = m.Name
	}
	if len(seen) != len(sentinelNames(t)) {
		t.Errorf("reasonTable holds %d distinct errors, errors.go declares %d sentinels",
			len(seen), len(sentinelNames(t)))
	}
}

func TestReasonForKnownErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want iso20022.StatusReason
	}{
		{ErrAccountNotInParticipant, iso20022.StatusReasonIncorrectAccountNumber},
		{ErrDuplicateEndToEndID, iso20022.StatusReasonDuplication},
		{ErrMandateRevoked, iso20022.StatusReasonNoMandate},
		{ErrMandateExceeded, iso20022.StatusReasonNotSpecifiedAgentGenerated},
		{ErrCycleNotOpen, iso20022.StatusReasonInvalidCutOffTime},
		{ErrAssetMismatch, iso20022.StatusReasonNotSpecifiedAgentGenerated},
	} {
		if got := ReasonFor(tc.err); got != tc.want {
			t.Errorf("ReasonFor(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// TestReasonTableExplicitlyClassifiesAmbiguousMS03Cases pins that certain
// sentinels are DELIBERATELY mapped to MS03 in reasonTable, as opposed to
// merely falling through to it.
//
// A case in TestReasonForKnownErrors asserting ReasonFor's output cannot
// tell those two situations apart, because MS03 is also ReasonFor's fallback
// for an error the table has never heard of: mutating one of these entries'
// Code to "" leaves such a case green. This test asserts against reasonTable
// directly instead, so it fails on exactly that mutation.
func TestReasonTableExplicitlyClassifiesAmbiguousMS03Cases(t *testing.T) {
	for _, name := range []string{
		// A valid mandate exists but this collection falls outside it — see
		// the comment on ErrMandateMismatch/ErrMandateExceeded in
		// reasonTable for why MD01 would misstate the condition.
		"ErrMandateMismatch",
		"ErrMandateExceeded",
		// A currency mismatch SEPA's own code set never needed a member
		// for — see the comment on ErrAssetMismatch in reasonTable.
		"ErrAssetMismatch",
	} {
		var found bool
		for _, m := range reasonTable {
			if m.Name != name {
				continue
			}
			found = true
			if m.Code != iso20022.StatusReasonNotSpecifiedAgentGenerated {
				t.Errorf("reasonTable[%s].Code = %q, want explicit MS03", name, m.Code)
			}
		}
		if !found {
			t.Errorf("reasonTable has no entry named %s", name)
		}
	}
}

// TestReasonForEmptyCodeEntriesFallToMS03 pins the claim in reasonTable's
// "never reaching a counterparty" section: today, before the mesh (Task 6)
// exists to make that classification observable as a dead letter,
// ReasonFor cannot tell one of these sentinels apart from an error it has
// never heard of at all. Both return MS03 by exactly the same fallback path.
func TestReasonForEmptyCodeEntriesFallToMS03(t *testing.T) {
	var checked int
	for _, m := range reasonTable {
		if m.Code != "" {
			continue
		}
		checked++
		if got := ReasonFor(m.Err); got != iso20022.StatusReasonNotSpecifiedAgentGenerated {
			t.Errorf("ReasonFor(%s) = %q, want MS03 (same fallback as an unmapped error)", m.Name, got)
		}
	}
	if checked == 0 {
		t.Fatal("reasonTable has no empty-code entries; the table changed and this test no longer checks anything")
	}
}

// An error the table does not know is MS03 and not a panic. A rejection that
// crashed the actor rather than reaching the counterparty would be a worse
// failure than an imprecise code.
func TestReasonForUnknownErrorIsUnspecified(t *testing.T) {
	if got := ReasonFor(errors.New("something new")); got != iso20022.StatusReasonNotSpecifiedAgentGenerated {
		t.Errorf("ReasonFor(unknown) = %q, want MS03", got)
	}
}

// TestReasonForUnwraps pins that a wrapped sentinel still finds its code. The
// payment layer wraps errors freely, so a table keyed on identity alone would
// silently degrade to MS03 for most real failures.
func TestReasonForUnwraps(t *testing.T) {
	if got := ReasonFor(errors.Join(errors.New("context"), ErrAccountNotInParticipant)); got != iso20022.StatusReasonIncorrectAccountNumber {
		t.Errorf("ReasonFor(joined) = %q, want AC01", got)
	}
	if got := ReasonFor(fmt.Errorf("checking the creditor: %w", ErrAccountNotInParticipant)); got != iso20022.StatusReasonIncorrectAccountNumber {
		t.Errorf("ReasonFor(wrapped) = %q, want AC01", got)
	}
}

// ---------------------------------------------------------------------------
// Inbound: reading a status report and a settlement instruction
// ---------------------------------------------------------------------------
//
// These two readers need no Network, so they are tested in this package rather
// than in message_test.go — the split between the two files is mechanical, and
// described at the top of that one.

// readNow is the instant these tests write their messages at. It is not the
// network's clock, for the reason messageNow is not: a message is created when
// it is sent, not when the thing it reports on happened.
var readNow = time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)

// ReadStatus separates what the report says about the GROUP from what it says
// about each transaction, because a bulk message's fate is two different facts.
//
// The report is built, marshalled and read back off the wire rather than
// inspected as a struct: a reader that read its own input would be a tautology,
// and the claim is about what a receiver sees.
func TestReadStatusSeparatesGroupFromTransaction(t *testing.T) {
	orig := OriginalMessage{MsgID: "AURO-1", MsgDefIdr: "pacs.008.001.08", CreDtTm: readNow.Add(-time.Hour)}
	sent := []TransactionStatusReport{
		{EndToEndID: "e2e-1", TxID: "pay_1", Status: iso20022.TransactionStatusAccepted},
		{
			EndToEndID: "e2e-2",
			TxID:       "pay_2",
			Status:     iso20022.TransactionStatusRejected,
			Code:       iso20022.StatusReasonClosedAccountNumber,
			Text:       "creditor account is closed",
		},
	}
	env, err := StatusMessage(orig, sent, MessageContext{
		From: "VERDITMMXXX", To: "CSMXFRPPXXX", MsgID: "VERDE-1", Now: readNow,
	})
	if err != nil {
		t.Fatalf("StatusMessage: %v", err)
	}
	raw, err := iso20022.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := iso20022.Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	got, reports := ReadStatus(back.Document.(*iso20022.Pacs002))

	// The GROUP half. All three fields point BACKWARDS at the message being
	// reported on — never at this report, whose own MsgId is VERDE-1. A reader
	// that took the status report's own header here would hand the mesh an
	// identifier that matches nothing it ever sent.
	if got.MsgID != orig.MsgID {
		t.Errorf("original message id = %q, want %q", got.MsgID, orig.MsgID)
	}
	if got.MsgDefIdr != orig.MsgDefIdr {
		t.Errorf("original definition = %q, want %q", got.MsgDefIdr, orig.MsgDefIdr)
	}
	if !got.CreDtTm.Equal(orig.CreDtTm) {
		t.Errorf("original creation time = %s, want %s", got.CreDtTm, orig.CreDtTm)
	}

	// The TRANSACTION half: one report per TxInfAndSts, in order.
	if len(reports) != len(sent) {
		t.Fatalf("got %d transaction reports, want one per TxInfAndSts (%d)", len(reports), len(sent))
	}
	for i, want := range sent {
		if reports[i] != want {
			t.Errorf("report[%d] = %+v, want %+v", i, reports[i], want)
		}
	}
	// Spelled out for the rejection, because the two elements are separately
	// losable and each loss is silent: the code is what makes the rejection
	// machine-actionable and the text is what the person working the exception
	// queue reads.
	if reports[1].Code != iso20022.StatusReasonClosedAccountNumber {
		t.Errorf("rejection code = %q, want AC04", reports[1].Code)
	}
	if reports[1].Text != "creditor account is closed" {
		t.Errorf("rejection text = %q, want the free text beside the code", reports[1].Text)
	}
	// An acceptance carries no reason element at all — StatusReasonChoice
	// requires exactly one arm, so there is nothing truthful to put in one — and
	// a reader must not invent a code for it.
	if reports[0].Code != "" {
		t.Errorf("accepted transaction came back with code %q, want none", reports[0].Code)
	}
}

// A report whose original creation time was omitted comes back as the zero
// time, not as a fabricated instant. The element is optional precisely because
// a sender may not know it; see originalCreationOf, which omits it on the way
// out for the same reason.
func TestReadStatusLeavesAnAbsentCreationTimeZero(t *testing.T) {
	env, err := StatusMessage(
		OriginalMessage{MsgID: "AURO-1", MsgDefIdr: "pacs.008.001.08"},
		[]TransactionStatusReport{{EndToEndID: "e2e-1", Status: iso20022.TransactionStatusAccepted}},
		MessageContext{From: "VERDITMMXXX", To: "CSMXFRPPXXX", MsgID: "VERDE-1", Now: readNow},
	)
	if err != nil {
		t.Fatalf("StatusMessage: %v", err)
	}
	got, _ := ReadStatus(env.Document.(*iso20022.Pacs002))
	if !got.CreDtTm.IsZero() {
		t.Errorf("original creation time = %s, want the zero time: the message did not say", got.CreDtTm)
	}
}

// One leg per transaction, both parties by BIC, and the reference that says
// which cycle the leg discharges.
func TestReadSettlementIsOneLegPerTransaction(t *testing.T) {
	sent := []SettlementLeg{
		{From: "AURODEFFXXX", To: "VERDITMMXXX", Amount: 250000, Asset: "EUR", Reference: "cyc_1:bank_1"},
		{From: "NORDSESSXXX", To: "VERDITMMXXX", Amount: 100000, Asset: "EUR", Reference: "cyc_1:bank_3"},
	}
	env, err := SettlementMessage(sent, MessageContext{From: "CSMXFRPPXXX", To: "CBSEDEFFXXX", MsgID: "CSM-1", Now: readNow})
	if err != nil {
		t.Fatalf("SettlementMessage: %v", err)
	}
	raw, err := iso20022.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := iso20022.Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	got, err := ReadSettlement(back.Document.(*iso20022.Pacs009))
	if err != nil {
		t.Fatalf("ReadSettlement: %v", err)
	}
	if len(got) != len(sent) {
		t.Fatalf("got %d legs, want %d", len(got), len(sent))
	}
	for i := range sent {
		if got[i] != sent[i] {
			t.Errorf("leg[%d] = %+v, want %+v", i, got[i], sent[i])
		}
	}
	// Spelled out, because a reader that swapped them produces a settlement that
	// pays the wrong bank and is otherwise indistinguishable.
	if got[0].From != "AURODEFFXXX" || got[0].To != "VERDITMMXXX" {
		t.Errorf("leg 0 settles %s -> %s, want AURODEFFXXX -> VERDITMMXXX", got[0].From, got[0].To)
	}
}

// The scale comes from ledger.LookupAsset on the message's own currency, never
// from a constant — and an eight-decimal asset is what makes the difference
// visible.
//
// The document is built by hand rather than by SettlementMessage, and that is
// the finding rather than a shortcut: ActiveCurrencyAndAmount caps any currency
// at five fraction digits, so a BTC leg cannot be marshalled and this message
// cannot arrive over the wire. What it can do is reach ReadSettlement, which is
// enough to distinguish the two implementations — at the asset's scale of 8,
// 0.00250000 is 250000 satoshi; at a hardcoded 2 it is ErrAmountScale, an error
// where an amount should be.
func TestReadSettlementTakesItsScaleFromTheAsset(t *testing.T) {
	doc := &iso20022.Pacs009{FICdtTrf: iso20022.FIToFIFinancialInstitutionCreditTransfer{
		GrpHdr: iso20022.CreditTransferGroupHeader{MsgId: "CSM-1", NbOfTxs: "1"},
		CdtTrfTxInf: []iso20022.FinancialInstitutionCreditTransferTransaction{{
			PmtId:          iso20022.PaymentIdentification{EndToEndId: "cyc_1:bank_1"},
			IntrBkSttlmAmt: iso20022.ActiveCurrencyAndAmount{Ccy: "BTC", Value: "0.00250000"},
			Dbtr:           iso20022.BranchAndFinancialInstitution{FinInstnId: iso20022.FinancialInstitutionIdentification{BICFI: "AURODEFFXXX"}},
			Cdtr:           iso20022.BranchAndFinancialInstitution{FinInstnId: iso20022.FinancialInstitutionIdentification{BICFI: "VERDITMMXXX"}},
		}},
	}}

	got, err := ReadSettlement(doc)
	if err != nil {
		t.Fatalf("ReadSettlement of a BTC leg: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d legs, want 1", len(got))
	}
	if got[0].Amount != 250000 || got[0].Asset != "BTC" {
		t.Errorf("leg = %d %s, want 250000 BTC (0.00250000 at the asset's scale of 8)", got[0].Amount, got[0].Asset)
	}
}

// A currency the ledger has never heard of is refused rather than read at some
// default scale. This is a settlement instruction, so the alternative to
// refusing is moving central-bank reserves by a number nobody can interpret.
func TestReadSettlementRefusesAnUnknownCurrency(t *testing.T) {
	doc := &iso20022.Pacs009{FICdtTrf: iso20022.FIToFIFinancialInstitutionCreditTransfer{
		GrpHdr: iso20022.CreditTransferGroupHeader{MsgId: "CSM-1", NbOfTxs: "1"},
		CdtTrfTxInf: []iso20022.FinancialInstitutionCreditTransferTransaction{{
			PmtId:          iso20022.PaymentIdentification{EndToEndId: "r"},
			IntrBkSttlmAmt: iso20022.ActiveCurrencyAndAmount{Ccy: "XYZ", Value: "25.00"},
			Dbtr:           iso20022.BranchAndFinancialInstitution{FinInstnId: iso20022.FinancialInstitutionIdentification{BICFI: "AURODEFFXXX"}},
			Cdtr:           iso20022.BranchAndFinancialInstitution{FinInstnId: iso20022.FinancialInstitutionIdentification{BICFI: "VERDITMMXXX"}},
		}},
	}}
	if _, err := ReadSettlement(doc); !errors.Is(err, ledger.ErrAssetNotFound) {
		t.Fatalf("ReadSettlement of an undefined currency = %v, want ledger.ErrAssetNotFound", err)
	}
}

// The receiver that TestSettlementMessageNbOfTxsSurvivesATruncatedFile was
// waiting for.
//
// That test truncates a settlement instruction after it is built, marshals it,
// reads it back and asserts the sender's count of 2 is still there beside the
// one leg that survived — and then stops, because nothing existed to act on the
// discrepancy. This is the act: a settlement instruction that declares two legs
// and carries one is refused, rather than settling the one and leaving a bank
// unpaid with no record of why.
func TestReadSettlementRefusesATruncatedFile(t *testing.T) {
	env, err := SettlementMessage([]SettlementLeg{
		{From: "AURODEFFXXX", To: "VERDITMMXXX", Amount: 250000, Asset: "EUR", Reference: "cyc_1:bank_1"},
		{From: "NORDSESSXXX", To: "VERDITMMXXX", Amount: 100000, Asset: "EUR", Reference: "cyc_1:bank_3"},
	}, MessageContext{From: "CSMXFRPPXXX", To: "CBSEDEFFXXX", MsgID: "CSM-1", Now: readNow})
	if err != nil {
		t.Fatalf("SettlementMessage: %v", err)
	}
	raw, err := iso20022.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := iso20022.Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	doc := back.Document.(*iso20022.Pacs009)
	doc.FICdtTrf.CdtTrfTxInf = doc.FICdtTrf.CdtTrfTxInf[:1]

	_, err = ReadSettlement(doc)
	if err == nil {
		t.Fatal("settled a file that declared two legs and carried one")
	}
	if !strings.Contains(err.Error(), "NbOfTxs") {
		t.Errorf("error = %v, want it to name the count the sender asserted", err)
	}
}

// A count that is not a number is refused rather than ignored. Atoi's zero value
// on failure is 0, so a reader that dropped the error would compare every
// transaction list against zero and refuse every well-formed message — or, worse,
// treat the check as satisfied. NbOfTxs is a string in this package because it
// is what the sender asserted, and an assertion that is not a count is not one.
func TestReadSettlementRefusesACountThatIsNotANumber(t *testing.T) {
	doc := &iso20022.Pacs009{FICdtTrf: iso20022.FIToFIFinancialInstitutionCreditTransfer{
		GrpHdr: iso20022.CreditTransferGroupHeader{MsgId: "CSM-1", NbOfTxs: "many"},
		CdtTrfTxInf: []iso20022.FinancialInstitutionCreditTransferTransaction{{
			PmtId:          iso20022.PaymentIdentification{EndToEndId: "r"},
			IntrBkSttlmAmt: iso20022.ActiveCurrencyAndAmount{Ccy: "EUR", Value: "25.00"},
			Dbtr:           iso20022.BranchAndFinancialInstitution{FinInstnId: iso20022.FinancialInstitutionIdentification{BICFI: "AURODEFFXXX"}},
			Cdtr:           iso20022.BranchAndFinancialInstitution{FinInstnId: iso20022.FinancialInstitutionIdentification{BICFI: "VERDITMMXXX"}},
		}},
	}}
	_, err := ReadSettlement(doc)
	if err == nil {
		t.Fatal("accepted a settlement instruction whose NbOfTxs is not a number")
	}
	// There is no sentinel for "your file contradicts itself" — see
	// onlyTransaction — so the specific thing asserted is that the error names
	// the element, which is what reaches the sender as free text beside MS03.
	if !strings.Contains(err.Error(), "NbOfTxs") {
		t.Errorf("error = %v, want it to name the count that was not a count", err)
	}
}

func sentinelNames(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "errors.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing errors.go: %v", err)
	}
	var out []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range vs.Names {
				if strings.HasPrefix(name.Name, "Err") {
					out = append(out, name.Name)
				}
			}
		}
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
