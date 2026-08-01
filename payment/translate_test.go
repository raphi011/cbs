package payment

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/raphi011/cbs/iso20022"
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
		if got := reasonFor(tc.err); got != tc.want {
			t.Errorf("reasonFor(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// TestReasonTableExplicitlyClassifiesAmbiguousMS03Cases pins that certain
// sentinels are DELIBERATELY mapped to MS03 in reasonTable, as opposed to
// merely falling through to it.
//
// A case in TestReasonForKnownErrors asserting reasonFor's output cannot
// tell those two situations apart, because MS03 is also reasonFor's fallback
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
// reasonFor cannot tell one of these sentinels apart from an error it has
// never heard of at all. Both return MS03 by exactly the same fallback path.
func TestReasonForEmptyCodeEntriesFallToMS03(t *testing.T) {
	var checked int
	for _, m := range reasonTable {
		if m.Code != "" {
			continue
		}
		checked++
		if got := reasonFor(m.Err); got != iso20022.StatusReasonNotSpecifiedAgentGenerated {
			t.Errorf("reasonFor(%s) = %q, want MS03 (same fallback as an unmapped error)", m.Name, got)
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
	if got := reasonFor(errors.New("something new")); got != iso20022.StatusReasonNotSpecifiedAgentGenerated {
		t.Errorf("reasonFor(unknown) = %q, want MS03", got)
	}
}

// TestReasonForUnwraps pins that a wrapped sentinel still finds its code. The
// payment layer wraps errors freely, so a table keyed on identity alone would
// silently degrade to MS03 for most real failures.
func TestReasonForUnwraps(t *testing.T) {
	if got := reasonFor(errors.Join(errors.New("context"), ErrAccountNotInParticipant)); got != iso20022.StatusReasonIncorrectAccountNumber {
		t.Errorf("reasonFor(joined) = %q, want AC01", got)
	}
	if got := reasonFor(fmt.Errorf("checking the creditor: %w", ErrAccountNotInParticipant)); got != iso20022.StatusReasonIncorrectAccountNumber {
		t.Errorf("reasonFor(wrapped) = %q, want AC01", got)
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
