package stage

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/testutil"
)

// TestFormatStamp_MutatingVerbsLeavesStamp verifies that each mutating verb
// on a vault with NO format file leaves {"version":3}\n behind (072 S1 item 1).
func TestFormatStamp_MutatingVerbsLeavesStamp(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string) *Engine
		act   func(t *testing.T, e *Engine)
	}{
		{
			name: "OpenChangeset",
			setup: func(t *testing.T, dir string) *Engine {
				e, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				return e
			},
			act: func(t *testing.T, e *Engine) {
				if _, err := e.OpenChangeset("test open", testAuthor); err != nil {
					t.Fatalf("OpenChangeset: %v", err)
				}
			},
		},
		{
			name: "OpenOrJoin_open",
			setup: func(t *testing.T, dir string) *Engine {
				e, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				return e
			},
			act: func(t *testing.T, e *Engine) {
				if _, _, err := e.OpenOrJoin("test open_or_join", testAuthor); err != nil {
					t.Fatalf("OpenOrJoin: %v", err)
				}
			},
		},
		{
			name: "OpenOrJoin_join",
			setup: func(t *testing.T, dir string) *Engine {
				e0, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := e0.OpenChangeset("initial intent", testAuthor); err != nil {
					t.Fatal(err)
				}
				e0.Close()
				_ = os.Remove(filepath.Join(dir, ".llmwiki", formatFileName))
				e, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				return e
			},
			act: func(t *testing.T, e *Engine) {
				if _, joined, err := e.OpenOrJoin("joined intent", Author{Kind: "human", Session: "alice"}); err != nil || !joined {
					t.Fatalf("OpenOrJoin join: joined=%v, err=%v", joined, err)
				}
			},
		},
		{
			name: "Append",
			setup: func(t *testing.T, dir string) *Engine {
				e0, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := e0.OpenChangeset("test intent", testAuthor); err != nil {
					t.Fatal(err)
				}
				e0.Close()
				_ = os.Remove(filepath.Join(dir, ".llmwiki", formatFileName))
				e, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				return e
			},
			act: func(t *testing.T, e *Engine) {
				op := Op{
					Kind:       OpCreatePage,
					Path:       "wiki/concepts/test-concept.md",
					Content:    newConceptPageContent("Test Concept"),
					Rationale:  "test concept page",
					Provenance: []string{"raw/papers/leviathan-2023.md"},
				}
				if _, err := e.Append(op); err != nil {
					t.Fatalf("Append: %v", err)
				}
			},
		},
		{
			name: "DropHunk",
			setup: func(t *testing.T, dir string) *Engine {
				e0, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := e0.OpenChangeset("test intent", testAuthor); err != nil {
					t.Fatal(err)
				}
				_ = stageKVCachePatch(t, e0)
				e0.Close()
				_ = os.Remove(filepath.Join(dir, ".llmwiki", formatFileName))
				e, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				return e
			},
			act: func(t *testing.T, e *Engine) {
				c, err := e.Current()
				if err != nil || c == nil || len(c.Ops) == 0 || len(c.Ops[0].Hunks) == 0 {
					t.Fatalf("setup missing op/hunk: %v", err)
				}
				if err := e.DropHunk(c.Ops[0].ID, c.Ops[0].Hunks[0].ID); err != nil {
					t.Fatalf("DropHunk: %v", err)
				}
			},
		},
		{
			name: "UndropHunk",
			setup: func(t *testing.T, dir string) *Engine {
				e0, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := e0.OpenChangeset("test intent", testAuthor); err != nil {
					t.Fatal(err)
				}
				_ = stageKVCachePatch(t, e0)
				c, _ := e0.Current()
				_ = e0.DropHunk(c.Ops[0].ID, c.Ops[0].Hunks[0].ID)
				e0.Close()
				_ = os.Remove(filepath.Join(dir, ".llmwiki", formatFileName))
				e, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				return e
			},
			act: func(t *testing.T, e *Engine) {
				c, err := e.Current()
				if err != nil || c == nil || len(c.Ops) == 0 || len(c.Ops[0].Hunks) == 0 {
					t.Fatalf("setup missing op/hunk: %v", err)
				}
				if err := e.UndropHunk(c.Ops[0].ID, c.Ops[0].Hunks[0].ID); err != nil {
					t.Fatalf("UndropHunk: %v", err)
				}
			},
		},
		{
			name: "DropOp",
			setup: func(t *testing.T, dir string) *Engine {
				e0, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := e0.OpenChangeset("test intent", testAuthor); err != nil {
					t.Fatal(err)
				}
				_ = stageKVCachePatch(t, e0)
				e0.Close()
				_ = os.Remove(filepath.Join(dir, ".llmwiki", formatFileName))
				e, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				return e
			},
			act: func(t *testing.T, e *Engine) {
				c, err := e.Current()
				if err != nil || c == nil || len(c.Ops) == 0 {
					t.Fatalf("setup missing op: %v", err)
				}
				if err := e.DropOp(c.Ops[0].ID); err != nil {
					t.Fatalf("DropOp: %v", err)
				}
			},
		},
		{
			name: "DropOpsWith",
			setup: func(t *testing.T, dir string) *Engine {
				e0, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := e0.OpenChangeset("test intent", testAuthor); err != nil {
					t.Fatal(err)
				}
				_ = stageKVCachePatch(t, e0)
				e0.Close()
				_ = os.Remove(filepath.Join(dir, ".llmwiki", formatFileName))
				e, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				return e
			},
			act: func(t *testing.T, e *Engine) {
				c, err := e.Current()
				if err != nil || c == nil || len(c.Ops) == 0 {
					t.Fatalf("setup missing op: %v", err)
				}
				if err := e.DropOpsWith([]string{c.Ops[0].ID}, Drop{By: "reviewer", Reason: "unwanted"}); err != nil {
					t.Fatalf("DropOpsWith: %v", err)
				}
			},
		},
		{
			name: "RestoreOps",
			setup: func(t *testing.T, dir string) *Engine {
				e0, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := e0.OpenChangeset("test intent", testAuthor); err != nil {
					t.Fatal(err)
				}
				_ = stageKVCachePatch(t, e0)
				c, _ := e0.Current()
				_ = e0.DropOp(c.Ops[0].ID)
				e0.Close()
				_ = os.Remove(filepath.Join(dir, ".llmwiki", formatFileName))
				e, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				return e
			},
			act: func(t *testing.T, e *Engine) {
				c, err := e.Current()
				if err != nil || c == nil || len(c.Ops) == 0 {
					t.Fatalf("setup missing op: %v", err)
				}
				if err := e.RestoreOps([]string{c.Ops[0].ID}); err != nil {
					t.Fatalf("RestoreOps: %v", err)
				}
			},
		},
		{
			name: "MarkPartial",
			setup: func(t *testing.T, dir string) *Engine {
				e0, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := e0.OpenChangeset("test intent", testAuthor); err != nil {
					t.Fatal(err)
				}
				e0.Close()
				_ = os.Remove(filepath.Join(dir, ".llmwiki", formatFileName))
				e, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				return e
			},
			act: func(t *testing.T, e *Engine) {
				if err := e.MarkPartial("work-in-progress"); err != nil {
					t.Fatalf("MarkPartial: %v", err)
				}
			},
		},
		{
			name: "Refresh",
			setup: func(t *testing.T, dir string) *Engine {
				e0, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := e0.OpenChangeset("test intent", testAuthor); err != nil {
					t.Fatal(err)
				}
				_ = stageKVCachePatch(t, e0)
				e0.Close()
				_ = os.Remove(filepath.Join(dir, ".llmwiki", formatFileName))
				e, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				return e
			},
			act: func(t *testing.T, e *Engine) {
				if err := e.Refresh(); err != nil {
					t.Fatalf("Refresh: %v", err)
				}
			},
		},
		{
			name: "Reject",
			setup: func(t *testing.T, dir string) *Engine {
				e0, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := e0.OpenChangeset("test intent", testAuthor); err != nil {
					t.Fatal(err)
				}
				e0.Close()
				_ = os.Remove(filepath.Join(dir, ".llmwiki", formatFileName))
				e, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				return e
			},
			act: func(t *testing.T, e *Engine) {
				if err := e.Reject("rejected by reviewer"); err != nil {
					t.Fatalf("Reject: %v", err)
				}
			},
		},
		{
			name: "Commit",
			setup: func(t *testing.T, dir string) *Engine {
				e0, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := e0.OpenChangeset("test intent", testAuthor); err != nil {
					t.Fatal(err)
				}
				_ = stageKVCachePatch(t, e0)
				e0.Close()
				_ = os.Remove(filepath.Join(dir, ".llmwiki", formatFileName))
				e, err := OpenEngine(dir)
				if err != nil {
					t.Fatal(err)
				}
				return e
			},
			act: func(t *testing.T, e *Engine) {
				if _, err := e.Commit("commit message"); err != nil {
					t.Fatalf("Commit: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := testutil.CopyFixture(t, "minimal")
			formatPath := filepath.Join(dir, ".llmwiki", formatFileName)
			_ = os.Remove(formatPath)

			e := tt.setup(t, dir)
			defer e.Close()

			if _, err := os.Stat(formatPath); err == nil {
				t.Fatalf("setup left format file behind")
			}

			tt.act(t, e)

			b, err := os.ReadFile(formatPath)
			if err != nil {
				t.Fatalf("format file was not created: %v", err)
			}
			if got, want := string(b), "{\"version\":3}\n"; got != want {
				t.Fatalf("format file content = %q, want %q", got, want)
			}
		})
	}
}

// TestFormatStamp_NoRewriteAndUpgrade pins 072 S1 item 2:
// - a vault already at 3 is not rewritten (mtime/bytes unchanged);
// - at 2 is raised to 3;
// - never lowered (stamp can only be called when have <= 3, so a 3 file stays).
func TestFormatStamp_NoRewriteAndUpgrade(t *testing.T) {
	t.Run("version 3 not rewritten", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		formatPath := filepath.Join(dir, ".llmwiki", formatFileName)
		writeFormatFile(t, dir, "{\"version\":3}\n")

		past := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
		if err := os.Chtimes(formatPath, past, past); err != nil {
			t.Fatalf("chtimes: %v", err)
		}

		e, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		defer e.Close()

		if _, err := e.OpenChangeset("test intent", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}

		fi, err := os.Stat(formatPath)
		if err != nil {
			t.Fatalf("stat format file: %v", err)
		}
		if fi.ModTime().Unix() != past.Unix() {
			t.Fatalf("format file was rewritten: modTime = %v, want %v", fi.ModTime(), past)
		}
		b, err := os.ReadFile(formatPath)
		if err != nil {
			t.Fatalf("read format file: %v", err)
		}
		if got, want := string(b), "{\"version\":3}\n"; got != want {
			t.Fatalf("format file content = %q, want %q", got, want)
		}
	})

	t.Run("version 2 raised to 3", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		formatPath := filepath.Join(dir, ".llmwiki", formatFileName)
		writeFormatFile(t, dir, "{\"version\": 2}\n")

		e, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		defer e.Close()

		if _, err := e.OpenChangeset("test intent", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}

		b, err := os.ReadFile(formatPath)
		if err != nil {
			t.Fatalf("read format: %v", err)
		}
		if got, want := string(b), "{\"version\":3}\n"; got != want {
			t.Fatalf("format file = %q, want %q", got, want)
		}
	})

	t.Run("never lowered", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		writeFormatFile(t, dir, "{\"version\":3}\n")

		e, err := OpenEngine(dir)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		defer e.Close()

		if _, err := e.OpenChangeset("test intent", testAuthor); err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}

		have, err := ReadFormat(filepath.Join(dir, ".llmwiki"))
		if err != nil {
			t.Fatalf("ReadFormat: %v", err)
		}
		if have != 3 {
			t.Fatalf("format version lowered to %d, want 3", have)
		}
	})
}

// TestFormatStamp_RefusesNewerFormat pins 072 S1 item 3:
// a vault whose file says 4 after OpenEngine (written behind its back)
// makes the writer return *FormatError and write nothing (asserting
// the changeset dir and file bytes).
func TestFormatStamp_RefusesNewerFormat(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	// Write version 4 behind OpenEngine's back
	formatPath := filepath.Join(dir, ".llmwiki", formatFileName)
	const v4Content = "{\"version\":4}\n"
	if err := os.WriteFile(formatPath, []byte(v4Content), 0o644); err != nil {
		t.Fatalf("write format 4: %v", err)
	}

	openDir := filepath.Join(dir, ".llmwiki", "changesets", "open")
	entriesBefore, err := os.ReadDir(openDir)
	if err != nil {
		t.Fatalf("read openDir: %v", err)
	}

	_, err = e.OpenChangeset("test intent", testAuthor)
	if err == nil {
		t.Fatalf("OpenChangeset succeeded; want *FormatError")
	}

	var fe *FormatError
	if !errors.As(err, &fe) {
		t.Fatalf("OpenChangeset err = %T (%v), want *FormatError", err, err)
	}
	if fe.Have != 4 || fe.Max != FormatVersion {
		t.Fatalf("FormatError = %+v, want Have 4, Max %d", fe, FormatVersion)
	}

	// Assert nothing was written:
	// 1. Changeset directory was not created
	entriesAfter, err := os.ReadDir(openDir)
	if err != nil {
		t.Fatalf("read openDir after: %v", err)
	}
	if len(entriesAfter) != len(entriesBefore) {
		t.Fatalf("changeset dir created: before=%d, after=%d", len(entriesBefore), len(entriesAfter))
	}

	// 2. format file was not overwritten
	b, err := os.ReadFile(formatPath)
	if err != nil {
		t.Fatalf("read format file: %v", err)
	}
	if string(b) != v4Content {
		t.Fatalf("format file was modified: got %q, want %q", string(b), v4Content)
	}
}

// TestFormatStamp_ReadOnlyLeavesVaultIdentical pins 072 S1 item 4:
// OpenEngine + Current + Status-like readers + Projection leave the tree hash
// identical and no format file.
func TestFormatStamp_ReadOnlyLeavesVaultIdentical(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	formatPath := filepath.Join(dir, ".llmwiki", formatFileName)

	// Pre-condition: no format file
	if _, err := os.Stat(formatPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("format file exists initially: %v", err)
	}

	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	// Initial digest after OpenEngine
	before := treeDigest(t, dir)

	// Call read-only readers
	_, _ = e.Current()
	_, _ = e.ProjectedVault()
	_, _ = e.ProjectedReport()
	_ = e.Vault()
	_ = e.Journal()
	_ = e.Index()
	_, _ = e.Snapshot("nonexistent")
	_, _ = e.ReloadIfChanged()
	_ = e.VaultIdentity()

	// Assert tree digest is identical
	after := treeDigest(t, dir)
	if after != before {
		t.Fatalf("read-only operations modified vault tree: before digest %s, after %s", before, after)
	}

	// Assert no format file appeared
	if _, err := os.Stat(formatPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf(".llmwiki/format appeared after read-only operations: %v", err)
	}
}

// TestFormatStamp_GarbledFormatFailsClosed pins 072 S1 item 5:
// a garbled format file after open makes the writer fail closed.
func TestFormatStamp_GarbledFormatFailsClosed(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()

	formatPath := filepath.Join(dir, ".llmwiki", formatFileName)
	const garbled = "{\"version\": broken json"
	if err := os.WriteFile(formatPath, []byte(garbled), 0o644); err != nil {
		t.Fatalf("write garbled format: %v", err)
	}

	openDir := filepath.Join(dir, ".llmwiki", "changesets", "open")
	entriesBefore, _ := os.ReadDir(openDir)

	_, err = e.OpenChangeset("test intent", testAuthor)
	if err == nil {
		t.Fatalf("OpenChangeset succeeded on garbled format; want error")
	}
	if !strings.Contains(err.Error(), "read .llmwiki/format: ") {
		t.Fatalf("OpenChangeset err = %q, want error naming 'read .llmwiki/format: '", err.Error())
	}

	// Fail closed assertions
	entriesAfter, _ := os.ReadDir(openDir)
	if len(entriesAfter) != len(entriesBefore) {
		t.Fatalf("changeset dir created on failed format check")
	}

	b, err := os.ReadFile(formatPath)
	if err != nil {
		t.Fatalf("read format: %v", err)
	}
	if string(b) != garbled {
		t.Fatalf("format file was overwritten: got %q, want %q", string(b), garbled)
	}
}

// TestFormatStamp_ScanWriteMuLock pins 072 S1 item 6:
// no writeMu.Lock() call in non-test internal/stage files except inside lockWriter
// and the one exempt function (named in the test with a comment).
func TestFormatStamp_ScanWriteMuLock(t *testing.T) {
	// exemptFunctions: Refresh takes the bare lock on purpose (072 review): it
	// stamps inside refreshWriteLocked, only when an open changeset exists to
	// persist, because ReloadVault calls it on every Browse edit and a vault
	// lw never wrote to must stay unstamped (TestFormatStamp_RefreshWithoutChangesetLeavesNoStamp).
	exemptFunctions := map[string]bool{"Refresh": true}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	fset := token.NewFileSet()
	var lockWriterCalls int
	var violations []string

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		node, err := parser.ParseFile(fset, name, nil, parser.AllErrors)
		if err != nil {
			t.Fatalf("ParseFile %s: %v", name, err)
		}

		for _, decl := range node.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}

			funcName := fn.Name.Name
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}

				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Lock" {
					return true
				}

				// Check if receiver is writeMu
				isWriteMu := false
				if innerSel, ok := sel.X.(*ast.SelectorExpr); ok && innerSel.Sel.Name == "writeMu" {
					isWriteMu = true
				} else if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "writeMu" {
					isWriteMu = true
				}

				if !isWriteMu {
					return true
				}

				if funcName == "lockWriter" {
					lockWriterCalls++
					return true
				}

				if exemptFunctions[funcName] {
					return true
				}

				pos := fset.Position(call.Pos())
				violations = append(violations, fmt.Sprintf("%s:%d: writeMu.Lock() called inside %s()", name, pos.Line, funcName))
				return true
			})
		}
	}

	if lockWriterCalls == 0 {
		t.Fatalf("expected lockWriter to call writeMu.Lock(), but none found in internal/stage")
	}

	if len(violations) > 0 {
		t.Fatalf("found writeMu.Lock() outside lockWriter and exempt functions:\n%s", strings.Join(violations, "\n"))
	}
}

// TestFormatStamp_RefreshWithoutChangesetLeavesNoStamp pins the Refresh
// exemption: with no open changeset there is nothing to persist, so the
// vault stays unstamped (ReloadVault calls Refresh on every Browse edit).
func TestFormatStamp_RefreshWithoutChangesetLeavesNoStamp(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	e, err := OpenEngine(dir)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()
	_ = e.Refresh()
	if _, err := os.Stat(filepath.Join(dir, ".llmwiki", "format")); !os.IsNotExist(err) {
		t.Fatalf("Refresh with no open changeset stamped the vault (stat err = %v)", err)
	}
}
