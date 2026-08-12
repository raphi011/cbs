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
var interElementWhitespace = regexp.MustCompile(`>\s+<`)

func normaliseXML(b []byte) string {
	return strings.TrimSpace(interElementWhitespace.ReplaceAllString(string(b), "><"))
}

// assertGoldenRoundTrip runs both directions against a committed sample.
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
