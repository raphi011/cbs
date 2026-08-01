package iso20022

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzUnmarshal seeds from the golden documents and asserts the only property
// that must hold for arbitrary bytes: Unmarshal returns or errors, and never
// panics. Anything it does parse must also survive being re-marshalled.
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
			return
		}
		if _, err := Marshal(env); err != nil {
			t.Fatalf("a document that unmarshalled failed to marshal: %v", err)
		}
	})
}
