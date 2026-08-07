package iso20022

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// goModPath is where this package's test binary finds the module declaration.
// Tests run in the package's own directory, so the module root is one level up.
const goModPath = "../go.mod"

// modulePath reads this repository's module path out of go.mod.
//
// Read from go.mod rather than spelled out as a const, which would decouple the
// guard below from the only place the module path is actually declared: rename
// the module and a hand-written test keeps passing while checking a string that
// appears nowhere. Demonstrated — the package copied verbatim into a module
// named "scratch" passed unchanged.
//
// It fails rather than skips when it cannot find the declaration. A guard that
// cannot locate its own input has stopped guarding, and saying so is the whole
// point.
func modulePath(t *testing.T) string {
	t.Helper()

	b, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatalf("reading %s: %v; this test cannot check what it cannot read", goModPath, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module ")
		if !ok {
			continue
		}
		if path := strings.TrimSpace(rest); path != "" {
			return path
		}
	}
	t.Fatalf("no module declaration in %s; this test cannot tell what an import of this repository looks like", goModPath)
	return ""
}

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
// It asserts the stronger property too: every import is STDLIB. "No repository
// imports" was the narrower half of the constraint — a third-party library
// would have sailed through it — and the project's actual rule is that nothing
// outside the store package acquires a dependency of its own. The stdlib test is
// a cheap one: an import path whose first element
// carries a dot names a host, and no standard-library path does.
//
// Non-test files only. A test may legitimately need something from the
// repository, and nothing a test imports ends up in the package's own
// dependency graph.
func TestPackageImportsNothingFromThisRepository(t *testing.T) {
	module := modulePath(t)

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
			if path == module || strings.HasPrefix(path, module+"/") {
				t.Errorf("%s imports %q; this package must import nothing from this repository — "+
					"the conversion boundary belongs on the payment side", file, path)
				continue
			}
			if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
				t.Errorf("%s imports %q, which is not in the standard library; this package takes no dependencies", file, path)
			}
		}
	}

	if checked == 0 {
		t.Fatal("every .go file was a test file; the constraint went unchecked")
	}
}
