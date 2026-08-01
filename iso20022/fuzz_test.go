package iso20022

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// FuzzUnmarshal seeds from the golden documents and asserts the properties that
// must hold for arbitrary bytes: Unmarshal returns or errors and never panics,
// anything it does parse survives being re-marshalled, and no error it returns
// is io.EOF.
//
// That last one is here rather than only in TestUnmarshalSurvivesHostileInput
// because the claim in Unmarshal's doc comment is quantified over EVERY error
// the function returns, and a table of hand-written inputs cannot hold a claim
// of that shape — it can only pin the cases somebody thought of. Arbitrary
// bytes can. io.EOF reads as "the peer closed cleanly" to the transport 7b is
// about to put around this, which is why the property is worth a fuzz target's
// time rather than a comment.
func FuzzUnmarshal(f *testing.F) {
	entries, err := filepath.Glob(filepath.Join("testdata", "*.xml"))
	if err != nil {
		f.Fatalf("globbing testdata: %v", err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(e)
		if err != nil {
			f.Fatalf("reading %s: %v", e, err)
		}
		f.Add(b)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		env, err := Unmarshal(data)
		if err != nil {
			if errors.Is(err, io.EOF) {
				t.Fatalf("Unmarshal() = %v, which is io.EOF; a transport reads that as a clean close, not as bad content", err)
			}
			return
		}
		if _, err := Marshal(env); err != nil {
			t.Fatalf("a document that unmarshalled failed to marshal: %v", err)
		}
	})
}
