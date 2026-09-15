// layout_test.go holds the 003 frozen checks for the redesigned ask screen:
// the size-sweep invariant (TestAskSweep), the D10 prompt derivation
// (TestAskPromptsFromIndex), and the pane-level goldens on the public
// fixture vault (TestAskGolden). The byte-exact chrome against the private
// mockup vault lives in internal/ui/conformance; these tests are what CI
// (no LW_MOCKUP_VAULT) sees.
package ask

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// TestAskSweep drives the pane through every harness sweep size at the pane
// level (the shell gives a pane the terminal's h-2): the View invariant —
// exactly h lines of exactly w cells — must hold everywhere above D11's
// 80×24 minimum (conventions §4 rule 2, s2-screens.md "Every screen").
func TestAskSweep(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := uitest.PublicVault(t, "sweep-vault")
	p := New(uitest.Deps(v, true, nil)) // empty transcript: prompts from the fixture index

	// SweepSizes() is 991 entries (uitest's own frozen count) minus the 4
	// below-minimum sizes (79,24) (80,23) (72,20) (120,20) — each has
	// W < 80 or H < 24.
	const wantSizes = 987

	rendered := 0
	for _, sz := range uitest.SweepSizes() {
		if sz.W < 80 || sz.H < 24 {
			continue
		}
		rendered++
		_, plain := uitest.PaneScreen(p, sz.W, sz.H-2)
		uitest.AssertGrid(t, plain, sz.W, sz.H-2)
		if t.Failed() {
			t.Fatalf("ask sweep failed at %dx%d (pane height %d)", sz.W, sz.H, sz.H-2)
		}
	}
	if rendered != wantSizes {
		t.Fatalf("sweep rendered %d sizes, want %d", rendered, wantSizes)
	}
}

// TestAskPromptsFromIndex pins D10: the suggested prompts are derived from
// index.md's `[[link]] — description` bullets, with the fallback set when
// fewer than two entries exist (s2-screens.md T08).
func TestAskPromptsFromIndex(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	t.Run("two entries become the T1/T2 prompts", func(t *testing.T) {
		// The mockup vault's first two bullets, verbatim — this pins the
		// frozen grids' exact strings (ask-80x24's Try block).
		src := "# Index\n\n## Entities\n\n- [[vertex-ai]] — Vertex AI\n- [[claude]] — Claude\n"
		want := []string{
			"What does the wiki say about Vertex AI?",
			"How is Claude related to Vertex AI?",
			"Which pages rest on a single source?",
		}
		got := promptsFromIndex([]byte(src))
		if diff := diffStrings(want, got); diff != "" {
			t.Fatalf("prompts wrong for the mockup-shape index:\n%s", diff)
		}
	})

	t.Run("wikilink label form uses the target", func(t *testing.T) {
		src := "- [[private-network-access|Private Network Access]] — Private Network Access to Model Endpoints\n" +
			"- [[claude|Claude]] — Claude\n"
		want := []string{
			"What does the wiki say about Private Network Access to Model Endpoints?",
			"How is Claude related to Private Network Access to Model Endpoints?",
			"Which pages rest on a single source?",
		}
		got := promptsFromIndex([]byte(src))
		if diff := diffStrings(want, got); diff != "" {
			t.Fatalf("prompts wrong for the |label form:\n%s", diff)
		}
	})

	t.Run("fewer than two entries falls back", func(t *testing.T) {
		for name, src := range map[string]string{
			"empty index":  "",
			"prose only":   "# Index\n\nNothing linkable here.\n",
			"one entry":    "- [[claude]] — Claude\n",
			"broken links": "- [claude] — Claude\n- [[unclosed — Claude\n",
		} {
			got := promptsFromIndex([]byte(src))
			if diff := diffStrings(fallbackPrompts(), got); diff != "" {
				t.Fatalf("%s: want the fallback prompts:\n%s", name, diff)
			}
		}
	})

	t.Run("no engine falls back", func(t *testing.T) {
		m := New(ui.Deps{}).(*Model)
		if diff := diffStrings(fallbackPrompts(), m.prompts); diff != "" {
			t.Fatalf("New without an engine: want the fallback prompts:\n%s", diff)
		}
	})

	t.Run("prompts come from the public vault's index", func(t *testing.T) {
		v := uitest.PublicVault(t, "prompt-vault")
		m := New(uitest.Deps(v, true, nil)).(*Model)
		want := []string{
			// uitest's fixture index.md, first two bullets in order:
			// `[[autolyse]] — Resting flour…` then `[[windowpane-test]] —
			// Stretching dough…`.
			"What does the wiki say about Resting flour and water before any kneading starts.?",
			"How is Stretching dough thin to judge gluten development. related to Resting flour and water before any kneading starts.?",
			"Which pages rest on a single source?",
		}
		if diff := diffStrings(want, m.prompts); diff != "" {
			t.Fatalf("prompts wrong for the fixture vault:\n%s", diff)
		}

		// D10: the prompts also refresh on ui.VaultReloadedMsg — rewrite the
		// copied vault's index and the pane must follow.
		newIndex := "# Index\n\n## Entities\n\n- [[dutch-oven]] — Dutch Oven\n- [[banneton]] — Banneton\n"
		if err := os.WriteFile(filepath.Join(v.Root, "index.md"), []byte(newIndex), 0o644); err != nil {
			t.Fatalf("rewrite index.md: %v", err)
		}
		pane, _ := m.Update(ui.VaultReloadedMsg{})
		m = pane.(*Model)
		wantRefreshed := []string{
			"What does the wiki say about Dutch Oven?",
			"How is Banneton related to Dutch Oven?",
			"Which pages rest on a single source?",
		}
		if diff := diffStrings(wantRefreshed, m.prompts); diff != "" {
			t.Fatalf("prompts not refreshed on VaultReloadedMsg:\n%s", diff)
		}
	})
}

// goldenSizes are the checkpoint sizes, at the pane level (s2-screens.md
// "Every screen": the shell gives panes h-2, so the pane golden is the
// terminal size minus its two chrome rows).
var goldenSizes = [][2]int{{80, 22}, {100, 28}, {120, 38}, {200, 58}}

// TestAskGolden renders the scripted turn at the checkpoint sizes in both
// polarities against the public fixture vault: one plain golden per size
// (polarity-independent) and one ANSI golden per polarity (conventions
// §5). The turn is the conformance gate's ask-conversation script, so the
// goldens show the full frozen shape — question, tool rows, marked answer,
// turn boundary — under CI conditions.
func TestAskGolden(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := uitest.PublicVault(t, "golden-vault")
	ag := &uitest.FakeAgent{Events: scriptedTurnEvents(), Store: agent.NewFileSessions(v.Root)}
	m := New(uitest.Deps(v, true, ag)).(*Model)

	// Type the conformance gate's question and submit it, draining every
	// command the turn produces (the FakeAgent replays synchronously).
	var pane ui.Pane = m
	for _, r := range goldenQuestion {
		var cmd tea.Cmd
		pane, cmd = pane.Update(keyPress(r))
		if cmd != nil {
			t.Fatalf("typing %q produced a command", string(r))
		}
	}
	pane, cmd := pane.Update(specialKey(tea.KeyEnter, 0))
	if cmd == nil {
		t.Fatal("submit produced no command")
	}
	m = runCmd(t, pane, cmd, new([]tea.Msg)).(*Model)
	if m.turnActive {
		t.Fatal("turn still active after the scripted events drained")
	}

	render := func(polarity string) {
		t.Helper()
		for _, sz := range goldenSizes {
			styled, plain := uitest.PaneScreen(m, sz[0], sz[1])
			uitest.AssertGrid(t, plain, sz[0], sz[1])

			// testutil normalizes to exactly one trailing newline; pass the
			// payload with it, the way internal/ui/markdown's goldens do.
			base := filepath.Join("testdata", "golden", fmt.Sprintf("ask-%dx%d", sz[0], sz[1]))
			testutil.GoldenString(t, base+".txt.golden", plain+"\n")
			testutil.GoldenString(t, base+"-"+polarity+".ansi.golden", styled+"\n")
		}
	}

	render("dark")

	// Light polarity: the pane rebuilds its own theme copy (C-81); the
	// plain text must not change with it (contract §9 note 6).
	plainDark := plainAt(t, m, goldenSizes[0])
	pane, _ = m.Update(tea.BackgroundColorMsg{Color: color.White})
	m = pane.(*Model)
	if m.theme.IsDark {
		t.Fatal("BackgroundColorMsg did not flip the pane's theme to light")
	}
	if plainLight := plainAt(t, m, goldenSizes[0]); plainLight != plainDark {
		t.Fatalf("plain output depends on polarity")
	}
	render("light")
}

// plainAt renders the pane at w×h and returns its plain text.
func plainAt(t *testing.T, m *Model, sz [2]int) string {
	t.Helper()
	_, plain := uitest.PaneScreen(m, sz[0], sz[1])
	return plain
}

// diffStrings renders a want/got string-slice diff (empty when equal).
func diffStrings(want, got []string) string {
	if len(want) == len(got) {
		same := true
		for i := range want {
			if want[i] != got[i] {
				same = false
				break
			}
		}
		if same {
			return ""
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "want (%d):", len(want))
	for _, s := range want {
		fmt.Fprintf(&b, "\n  %q", s)
	}
	fmt.Fprintf(&b, "\ngot (%d):", len(got))
	for _, s := range got {
		fmt.Fprintf(&b, "\n  %q", s)
	}
	return b.String()
}

// goldenQuestion is mockgen.py's QUESTION (line 592), verbatim — the same
// line the conformance gate's ask-conversation script types.
const goldenQuestion = "How does calling Claude through Vertex AI differ from the Anthropic API?"

// goldenAnswer is mockgen.py's ANSWER (line 593), verbatim — the scripted
// turn's single TextDelta, carrying the inline markers the renderer strips.
const goldenAnswer = "Both paths call the same Claude weights, so the difference is governance, not model quality. " +
	"The direct API authenticates with static `x-api-key` keys, goes over the public internet to " +
	"`api.anthropic.com`, bills on an Anthropic invoice and gets beta features first. Vertex AI uses " +
	"GCP IAM and service accounts, can keep traffic on the private GCP backbone (VPC-SC / PSC), bills " +
	"against GCP commitments and trails new features by a stated 2–4 weeks. " +
	"See [[anthropic-api-vs-vertex-ai]] and [[claude]]."

// scriptedTurnEvents is the conformance gate's six scripted events
// (mockgen.py's TOOLS + ANSWER + done): two resolved tool calls, the answer
// as one delta, and the turn boundary.
func scriptedTurnEvents() []agent.Event {
	return []agent.Event{
		agent.ToolCallEv{ID: "c1", Name: "wiki_search", Args: `{"query":"claude vertex ai anthropic api"}`},
		agent.ToolResEv{ID: "c1", Name: "wiki_search",
			Content: "4 pages: anthropic-api-vs-vertex-ai, claude, vertex-ai, model-as-a-service"},
		agent.ToolCallEv{ID: "c2", Name: "wiki_get",
			Args: `{"path":"wiki/comparisons/anthropic-api-vs-vertex-ai.md"}`},
		agent.ToolResEv{ID: "c2", Name: "wiki_get",
			Content: "Anthropic Official API vs Vertex AI · 41 lines"},
		agent.TextDelta{Text: goldenAnswer},
		agent.DoneEv{Reason: "stop", Rounds: 2},
	}
}

// The footer list is pane state only in the sense that FooterHelp returns
// it; pin it here so a binding edit shows up in this package's own tests
// before it shows up in a frozen grid.
func TestAskFooterBindingsExist(t *testing.T) {
	want := []struct{ key, desc string }{
		{"enter", "send"},
		{"↑/↓", "select tool call"},
		{"ctrl+r", "review"},
		{"tab", "screen"},
	}
	bs := footerBindings()
	if len(bs) != len(want) {
		t.Fatalf("footer has %d bindings, want %d", len(bs), len(want))
	}
	for i, w := range want {
		h := bs[i].Help()
		if h.Key != w.key || h.Desc != w.desc {
			t.Fatalf("binding %d = %q %q, want %q %q", i, h.Key, h.Desc, w.key, w.desc)
		}
		for _, k := range bs[i].Keys() {
			if k == "" {
				t.Fatalf("binding %d (%s) is unbound; the footer drops unbound bindings", i, w.key)
			}
		}
	}
	// No `q`: Ask captures typing, so quit must not be offered here.
	for _, b := range bs {
		for _, k := range b.Keys() {
			if k == "q" {
				t.Fatal("the ask footer offers q, but q types into the input")
			}
		}
	}
}
