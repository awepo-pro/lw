package trace

// verbs_test.go is 054 S1-c's frozen block: every entry point names its turn's
// verb through the Verb* constants in meta.go, never a string literal, so a
// typo is a compile error rather than a turn that silently runs as a curator
// with every stage tool (agent.modeFromVerb sends an unknown verb there).
// Permanent regression tests (D-10C).

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryEntryPointVerbIsDeclared scans the non-test Go files under cmd/ and
// internal/ for calls to WithVerb and fails on any whose verb argument is a
// string literal. internal/eval is scanned like the rest: it reads verbs back
// from run JSON and never calls WithVerb, so it has nothing to exempt.
func TestEveryEntryPointVerbIsDeclared(t *testing.T) {
	root := filepath.Join("..", "..")
	var literals []string
	calls := 0

	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) != 2 || calleeName(call.Fun) != "WithVerb" {
					return true
				}
				calls++
				if lit, ok := call.Args[1].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					rel, _ := filepath.Rel(root, path)
					literals = append(literals, fmt.Sprintf("%s:%d %s", filepath.ToSlash(rel), fset.Position(lit.Pos()).Line, lit.Value))
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", dir, err)
		}
	}

	// The scan must have seen the entry points it exists to guard: lw ingest,
	// query and lint, and the TUI ask pane. A walk that found none (a moved
	// directory, a renamed function) would pass for the wrong reason.
	if calls < 4 {
		t.Fatalf("scan found %d WithVerb call(s), want at least the 4 entry points (ingest, query, lint, ask pane)", calls)
	}
	for _, l := range literals {
		t.Errorf("WithVerb is called with a string literal: %s — use a trace.Verb* constant", l)
	}
}

// TestVerbConstantsAreTheWireVerbs pins what each constant spells: the verbs
// are written into every turn's trace, read back by lw trace and lweval, and
// matched by agent.modeFromVerb, so renaming one is a format change and not a
// refactor.
func TestVerbConstantsAreTheWireVerbs(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{VerbAsk, "ask"},
		{VerbQuery, "query"},
		{VerbIngest, "ingest"},
		{VerbLint, "lint"},
		{VerbFile, "file"},
	} {
		if tc.got != tc.want {
			t.Errorf("verb constant = %q, want %q", tc.got, tc.want)
		}
	}
}

// calleeName is the called function's bare name: WithVerb for both
// `WithVerb(...)` and `trace.WithVerb(...)`.
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}
