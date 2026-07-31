package api

import (
	"slices"
	"strings"
	"testing"
)

// The deliberate overlaps, as an allowlist rather than a tolerance, so a third
// accidental one fails.
//
//   - GET /assets is a compiled-in constant every operator needs to render money
//     at the right scale. Duplicating a constant is not duplicating state.
//   - GET /directory is on the bank as well as the clearing house because a bank
//     is a scheme participant with directory access, and the alternative — a
//     customer's browser querying the CSM — gives a retail app a clearing-house
//     connection no retail app has.
//   - GET /audit means "this operator's own log" on every operator: a bank's
//     ledger events on a bank, the reserve movements on the central bank. Same
//     pattern, different operator, different answer — which is what the split is
//     for, and is worth the consistency.
var allowedOverlaps = []string{"GET /assets", "GET /directory", "GET /audit"}

func surfaces(t *testing.T) map[string][]string {
	t.Helper()
	s := newServer(t, nil)
	return map[string][]string{
		"central-bank":   s.centralBankRouter().Patterns(),
		"clearing-house": s.clearingHouseRouter().Patterns(),
		"bank":           s.forBank("bank_1").bankRouter().Patterns(),
	}
}

// TestSurfacesAreDisjoint is the guard against a route quietly appearing on two
// operators — which would put a bank's ledger back within reach of a
// central-bank operator with a URL bar.
func TestSurfacesAreDisjoint(t *testing.T) {
	seen := map[string]string{}
	for _, op := range []string{"bank", "central-bank", "clearing-house"} {
		for _, p := range surfaces(t)[op] {
			if other, dup := seen[p]; dup && !slices.Contains(allowedOverlaps, p) {
				t.Errorf("%q is on both %s and %s", p, other, op)
			}
			seen[p] = op
		}
	}
}

// TestEveryRouteLandsSomewhere is the guard against losing one. Every pattern
// the single server served must arrive on the right operator, with its
// /participants/{pid} prefix stripped where the port now carries it.
func TestEveryRouteLandsSomewhere(t *testing.T) {
	got := surfaces(t)

	for _, old := range newServer(t, nil).RoutePatterns() {
		op, want := movedTo(old)
		if op == "" {
			continue // deliberately not moved yet; see the switch below
		}
		if !slices.Contains(got[op], want) {
			t.Errorf("%q should have become %q on the %s, which does not serve it",
				old, want, op)
		}
	}
}

// movedTo maps a pre-split pattern to the operator that serves it now and the
// pattern it became. It returns an empty operator for the routes a later task
// moves.
//
// Returning the operator as well as the pattern matters: a bank's audit log and
// the central bank's both become "GET /audit", so a flat set of landed patterns
// would let either one mask the other's absence.
func movedTo(old string) (operator, pattern string) {
	method, path, _ := strings.Cut(old, " ")
	switch {
	case path == "/participants" && method == "POST":
		return "central-bank", "POST /members"
	case path == "/participants" && method == "GET":
		return "clearing-house", "GET /members"
	case path == "/participants/{pid}":
		return "bank", "GET /me"
	case strings.HasPrefix(path, "/participants/{pid}/"):
		return "bank", method + " /" + strings.TrimPrefix(path, "/participants/{pid}/")

	case strings.HasPrefix(path, "/central-bank/"):
		return "central-bank", method + " /" + strings.TrimPrefix(path, "/central-bank/")
	case path == "/admin/reset":
		return "central-bank", old

	case path == "/cycles/{cid}/settle":
		return "", "" // Task 4 turns this into POST /settlements on the central bank

	case path == "/assets":
		// On every operator; the disjointness allowlist is what records that.
		return "clearing-house", old

	default:
		// Payments, mandates, cycles, settlements, schemes, the directory.
		return "clearing-house", old
	}
}
