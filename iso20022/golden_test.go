package iso20022

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// interElementWhitespace matches the whitespace between two elements.
//
// Golden comparison normalises it away, because indentation is not semantic.
// Everything that IS semantic survives: element order, element names,
// namespaces, attributes and text.
var interElementWhitespace = regexp.MustCompile(`>\s+<`)

func normaliseXML(b []byte) string {
	return strings.TrimSpace(interElementWhitespace.ReplaceAllString(string(b), "><"))
}

// assertGoldenRoundTrip runs both directions against a committed sample.
//
// Marshal-then-compare (direction 1) catches BOTH a field that serialises
// wrongly AND a field that silently fails to parse: a field that fails to
// unmarshal is left at its zero value, that zero value marshals differently
// from the golden file's non-zero text, and the comparison is against the
// GOLDEN FILE — not against a previous marshal — so the mismatch is caught
// right there.
//
// Unmarshal-then-compare-structs (direction 2) is a stability check layered
// on top, not a second, independent detector: it re-parses direction 1's
// output and requires the result to equal the first parse. Once direction 1
// has passed, direction 2 is nearly always redundant — a field that failed to
// parse already failed direction 1's string comparison, so by the time
// direction 2 runs, both sides it compares were produced from output already
// known to match golden. What direction 2 actually guards against is the
// narrow case where whitespace normalisation hid a real difference from
// direction 1's string comparison but a struct-level comparison would still
// see it. It costs nothing to keep, so it stays, but it is not the
// parse-failure detector and should not be described as one.
func assertGoldenRoundTrip(t *testing.T, file string) Envelope {
	t.Helper()

	golden, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}

	env, err := Unmarshal(golden)
	if err != nil {
		t.Fatalf("Unmarshal(%s) error = %v", file, err)
	}

	out, err := Marshal(env)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if got, want := normaliseXML(out), normaliseXML(golden); got != want {
		t.Fatalf("re-marshalled %s does not match the golden file\n got: %s\nwant: %s", file, got, want)
	}

	again, err := Unmarshal(out)
	if err != nil {
		t.Fatalf("Unmarshal(re-marshalled %s) error = %v", file, err)
	}
	if !reflect.DeepEqual(env, again) {
		t.Fatalf("round trip of %s changed the struct tree\n first: %#v\nsecond: %#v", file, env, again)
	}

	return env
}
