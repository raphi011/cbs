package iso20022

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// modulePath is this repository's module path. An import beginning with it is
// an import of this repository.
const modulePath = "github.com/raphi011/cbs"

// TestPackageImportsNothingFromThisRepository enforces the constraint the
// package doc calls load-bearing: these are the STANDARD's types, and an import
// of ledger.Amount or payment.PaymentID would quietly make that false, because
// the next reader could no longer tell which fields came from ISO 20022 and
// which came from here.
//
// It was a line in a plan's verification block before it was a test — a command
// someone had to remember to run, guarding a claim the package doc states as
// fact. That is the arrangement this project has been burned by repeatedly, so
// it is a test now.
//
// Non-test files only. A test may legitimately need something from the
// repository, and nothing a test imports ends up in the package's own
// dependency graph.
func TestPackageImportsNothingFromThisRepository(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("globbing package files: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no .go files found; this test is not looking where it thinks it is")
	}

	checked := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		checked++

		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}
		for _, spec := range parsed.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquoting import %s: %v", file, spec.Path.Value, err)
			}
			if path == modulePath || strings.HasPrefix(path, modulePath+"/") {
				t.Errorf("%s imports %q; this package must import nothing from this repository — "+
					"the conversion boundary belongs on the payment side", file, path)
			}
		}
	}

	if checked == 0 {
		t.Fatal("every .go file was a test file; the constraint went unchecked")
	}
}
