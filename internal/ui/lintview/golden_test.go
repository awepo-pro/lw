// golden_test.go is the subtask's frozen evidence (MASTER §5 T09):
// TestLintSweep holds the frame to the exactly-h-lines-of-exactly-w-cells
// invariant over uitest.SweepSizes, TestLintGolden writes the Findings
// panel's goldens, and TestLintOverlay renders the shell's `?` overlay over
// Lint through ui.NewApp. Lint has no frozen mockup grid — these goldens
// are the layout evidence the orchestrator reviews.
//
// The goldens run on uitest.PublicVault (s2-screens.md), staged with one
// extra page so the report carries findings of all three severities; see
// findingsVault for exactly how.
package lintview

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// dirtyPage is the page findingsVault adds to the PublicVault copy. Its
// frontmatter is valid (fm-required stays silent, fm-taxonomy's tag is in
// the fixture SCHEMA, the filename is canonical), but the page
// deliberately trips one check of each severity:
//
//	index-sync   error  it has no line in index.md
//	fm-quality   info   confidence: low
//	link-min-out warn   exactly one outbound wikilink
//	link-orphan  warn   no page links to it
//	link-broken  error  its only outbound wikilink resolves to nothing
//
// Every date is a fixed literal and no check reads the wall clock
// (backbone §4's determinism contract), so the findings — and the goldens —
// are byte-identical run to run.
const dirtyPage = `---
title: Windowpane Fragility
created: 2026-08-20
updated: 2026-08-27
type: concept
tags: [technique]
confidence: low
---

# Windowpane fragility

A dough that tears during the windowpane stretch has an underdeveloped
gluten network, not too much water. See [[gluten-network]] for the
structure side of that failure, and how folds rebuild it.
`

// dirtyPagePath is where dirtyPage lands, vault-relative.
const dirtyPagePath = "wiki/concepts/windowpane-fragility.md"

// dirtyPageFindings is the exact number of findings the staged vault
// reports. The PublicVault fixture itself carries one finding —
// src-provenance (warn) on wiki/entities/banneton.md, which cites a source
// without a ^[...] marker — so the staged page's five bring the total to
// six. 014 amendment (workflow §9 A6): page-abstract (warn) now fires on
// every wiki/ page of the vault — PublicVault's six plus the staged
// dirtyPage itself, none of which carries an ## Abstract — bringing the
// total to 13. Pinned here so a fixture or engine change that silently
// alters the report fails as a loud setup error instead of a mysterious
// golden diff.
const dirtyPageFindings = 13

// findingsVault returns a uitest.PublicVault copy carrying dirtyPage.
//
// How findings reach the pane through public API only: the page file is
// written into the harness's t.TempDir() copy of the vault — test setup on
// a directory the test owns — and the engine's vault is re-read with
// vault.Vault.Reload(), the same public reload Engine.Commit performs
// (apply.go step 7). The pane then hears it the way it would in production,
// via ui.VaultReloadedMsg.
func findingsVault(t *testing.T) *uitest.Vault {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	v := uitest.PublicVault(t, "ml-notes")
	path := filepath.Join(v.Root, filepath.FromSlash(dirtyPagePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("findingsVault: mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(dirtyPage), 0o644); err != nil {
		t.Fatalf("findingsVault: write %s: %v", dirtyPagePath, err)
	}
	if err := v.Engine.Vault().Reload(); err != nil {
		t.Fatalf("findingsVault: Vault.Reload: %v", err)
	}

	report := lint.Run(&lint.Context{
		Vault: v.Engine.Vault(),
		Index: v.Engine.Index(),
		Graph: v.Engine.Vault().Graph(),
	}, nil)
	if len(report.Findings) != dirtyPageFindings {
		t.Fatalf("findingsVault: the staged page produces %d findings, want %d: %+v",
			len(report.Findings), dirtyPageFindings, report.Findings)
	}
	return v
}

// initPane constructs the pane over d and runs its Init to quiescence (the
// report loads). Rebuilt per polarity, because the theme is captured at
// construction.
func initPane(t *testing.T, d ui.Deps) ui.Pane {
	t.Helper()
	return initModel(t, d)
}

// TestLintSweep holds the pane to conventions §4 rule 2 over the whole
// frozen size sweep: every width 80..220 × the sweep's heights (filtered to
// the ≥80×24 terminal the pane is given h-2 of), on uitest.PublicVault —
// once on the clean fixture (the empty state) and once with the staged
// findings (rows, clipping and the scrolled window at every width).
func TestLintSweep(t *testing.T) {
	cases := []struct {
		name string
		v    func(t *testing.T) *uitest.Vault
	}{
		{"publicvault", func(t *testing.T) *uitest.Vault {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			return uitest.PublicVault(t, "ml-notes")
		}},
		{"findings", findingsVault},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			p := initPane(t, uitest.Deps(tc.v(t), true, nil))
			for _, s := range uitest.SweepSizes() {
				if s.W < 80 || s.H < 24 {
					continue
				}
				_, plain := uitest.PaneScreen(p, s.W, s.H-2)
				uitest.AssertGrid(t, plain, s.W, s.H-2)
			}
		})
	}
}

// TestLintGolden writes the Findings panel's goldens at 120×38 (dark and
// light ANSI plus the plain review copy) and the plain copy at 80×22, over
// findingsVault. The light render must carry identical plain text (colour
// is the only difference, conventions §5).
func TestLintGolden(t *testing.T) {
	v := findingsVault(t)

	const w, h = 120, 38
	styledDark, dark := uitest.PaneScreen(initPane(t, uitest.Deps(v, true, nil)), w, h)
	uitest.AssertGrid(t, dark, w, h)

	styledLight, light := uitest.PaneScreen(initPane(t, uitest.Deps(v, false, nil)), w, h)
	uitest.AssertGrid(t, light, w, h)
	if light != dark {
		t.Fatalf("light polarity changed the plain text:\n%s", firstLineDiff(dark, light))
	}

	testutil.GoldenString(t, "testdata/golden/lint-120x38.txt.golden", dark+"\n")
	testutil.GoldenString(t, "testdata/golden/lint-120x38-dark.ansi.golden", styledDark+"\n")
	testutil.GoldenString(t, "testdata/golden/lint-120x38-light.ansi.golden", styledLight+"\n")

	_, small := uitest.PaneScreen(initPane(t, uitest.Deps(v, true, nil)), 80, 22)
	uitest.AssertGrid(t, small, 80, 22)
	testutil.GoldenString(t, "testdata/golden/lint-80x22.txt.golden", small+"\n")
}

// TestLintOverlay renders the shell's `?` overlay over Lint at 120×40
// through ui.NewApp with only this pane wired (s2-screens.md T09), mirroring
// the conformance gate's driving: resize, polarity, Init's command batch,
// then `?`.
func TestLintOverlay(t *testing.T) {
	v := findingsVault(t)
	d := uitest.Deps(v, true, nil)

	app := ui.NewApp(ui.Options{
		Deps:  d,
		Panes: map[ui.Screen]ui.Pane{ui.ScreenLint: New(d)},
		Start: ui.ScreenLint,
	})

	m := uitest.Drive(t, app,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		tea.BackgroundColorMsg{Color: color.Black},
	)
	m = uitest.Drive(t, m, initCmds(t, m)...)
	m = uitest.Drive(t, m, uitest.Key("?"))

	_, plain := uitest.Screen(m)
	uitest.AssertGrid(t, plain, 120, 40)
	testutil.GoldenString(t, "testdata/golden/overlay-120x40.txt.golden", plain+"\n")
}

// initCmds runs m.Init() — the shell batches each pane's Init behind its
// own — and returns every message the batch yields, exactly what
// tea.Program would deliver between Run and the caller's first key. The
// shell's tea.RequestBackgroundColor parks on the terminal's answer, which
// no headless command ever brings; Drive abandons it after its 2s bound,
// as the contract's blocking-command rule prescribes.
func initCmds(t *testing.T, m tea.Model) []tea.Msg {
	t.Helper()
	return collectCmdMsgs(t, m.Init())
}

// collectCmdMsgs expands cmd recursively through tea.BatchMsg.
func collectCmdMsgs(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collectCmdMsgs(t, c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// firstLineDiff reports the first line where want and got differ, for the
// polarity-identity failure message.
func firstLineDiff(want, got string) string {
	a, b := splitRows(want), splitRows(got)
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return fmt.Sprintf("line %d\n- %s\n+ %s", i+1, a[i], b[i])
		}
	}
	return fmt.Sprintf("line %d\n- <EOF>\n+ <EOF>", min(len(a), len(b))+1)
}
