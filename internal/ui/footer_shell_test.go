// footer_shell_test.go pins the shell's footer suffix (contract §5 frame
// note 2 as amended, ORCH-13/D-3T): the shell appends `tab screen` — always
// — and `q quit` — unless the active pane is taking text input — after the
// pane's own list and before `? help`, and the drop-from-end rule applies to
// the combined list, so the suffix drops first at narrow widths. The
// assertions run on footerContent, the exact function the shell's render
// calls; the frozen conformance grids prove Review, Browse and Ask render
// byte-identically under the rule.
package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// footerTestPane is a minimal pane whose footer list and TextCapturer
// answer are set by the test: captures models Ask (types), its absence
// models Review/Browse/Lint/Log.
type footerTestPane struct {
	captures bool
	bindings []key.Binding
}

func (p *footerTestPane) FooterHelp() []key.Binding { return p.bindings }
func (p *footerTestPane) CapturesText() bool        { return p.captures }
func (p *footerTestPane) Init() tea.Cmd             { return nil }

func (p *footerTestPane) Update(msg tea.Msg) (Pane, tea.Cmd) { return p, nil }
func (p *footerTestPane) View(w, h int) string               { return "" }
func (p *footerTestPane) Title() string                      { return "footer" }
func (p *footerTestPane) Help() []key.Binding                { return p.FooterHelp() }

var (
	_ Pane         = (*footerTestPane)(nil)
	_ FooterHelper = (*footerTestPane)(nil)
	_ TextCapturer = (*footerTestPane)(nil)
)

// footerTestKeys loads the compiled-in theme and keymap hermetically — the
// suffix bindings are Keys.NextPane and Keys.Quit, so the real KeyMap is
// what the suffix must carry.
func footerTestFixtures(t *testing.T) (Theme, KeyMap) {
	t.Helper()
	setConfigDir(t)
	theme, err := LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	keys, err := LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}
	return theme, keys
}

// helpBinding builds one bound binding with a fixed help label.
func helpBinding(k, keys, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(keys), key.WithHelp(k, desc))
}

// plainFooter renders the shell footer for p at w — footerContent, the exact
// call App.render makes — and strips it to plain text, minus the row's
// trailing pad: the styled runs and exact width are the renderer's business,
// the words and their order are what these tests pin.
func plainFooter(theme Theme, keys KeyMap, p Pane, w int) string {
	return strings.TrimRight(ansi.Strip(footerContent(theme, keys, p, w)), " ")
}

// wideFooterPane returns a pane whose seven bindings are wide enough that
// the combined footer stops fitting at widths the shell can legally run at,
// so the drop-from-end rule is exercised above D11's 80-column minimum.
// Combined widths (pane 88 + tab 10 + q 6 + help 6 = 116 cells): everything
// fits at w ≥ 117; dropping q fits from 109; q+tab from 97; q+tab+g/G from
// 81; q+tab+g/G+j/k from 71.
func wideFooterPane() *footerTestPane {
	return &footerTestPane{bindings: []key.Binding{
		helpBinding("enter", "enter", "open"),
		helpBinding("/", "/", "find"),
		helpBinding("ctrl+r", "ctrl+r", "review"),
		helpBinding("y", "y", "accept hunk"),
		helpBinding("n", "n", "drop hunk"),
		helpBinding("g/G", "g", "top/bottom"),
		helpBinding("j/k", "j", "move"),
	}}
}

func TestFooterShellSuffix(t *testing.T) {
	theme, keys := footerTestFixtures(t)

	t.Run("non_capturing_pane_gets_tab_and_quit", func(t *testing.T) {
		pane := &footerTestPane{bindings: []key.Binding{
			helpBinding("enter", "enter", "open"),
			helpBinding("/", "/", "find"),
		}}
		got := plainFooter(theme, keys, pane, 120)

		if !strings.HasSuffix(got, "tab screen  q quit  ? help") {
			t.Fatalf("footer = %q, want it to end `tab screen  q quit  ? help`", got)
		}
		if !strings.Contains(got, "enter open") || !strings.Contains(got, "/ find") {
			t.Fatalf("footer = %q, want the pane's own bindings ahead of the suffix", got)
		}
	})

	t.Run("capturing_pane_gets_tab_only", func(t *testing.T) {
		pane := &footerTestPane{captures: true, bindings: []key.Binding{
			helpBinding("enter", "enter", "open"),
			helpBinding("/", "/", "find"),
		}}
		got := plainFooter(theme, keys, pane, 120)

		if !strings.HasSuffix(got, "tab screen  ? help") {
			t.Fatalf("footer = %q, want it to end `tab screen  ? help`", got)
		}
		if strings.Contains(got, "q quit") {
			t.Fatalf("footer = %q, want no `q quit`: the pane captures text, so q types", got)
		}
	})

	t.Run("narrow_width_drops_suffix_first", func(t *testing.T) {
		pane := wideFooterPane()

		// One drop: q quit goes first, tab screen and every pane binding stay.
		got := plainFooter(theme, keys, pane, 112)
		if strings.Contains(got, "q quit") {
			t.Fatalf("footer at 112 = %q, want `q quit` dropped first", got)
		}
		if !strings.Contains(got, "tab screen") || !strings.Contains(got, "enter open") {
			t.Fatalf("footer at 112 = %q, want `tab screen` and the pane's bindings kept", got)
		}
		if !strings.HasSuffix(got, "tab screen  ? help") {
			t.Fatalf("footer at 112 = %q, want it to end `tab screen  ? help`", got)
		}

		// Two drops: tab screen goes next, all seven pane bindings remain.
		got = plainFooter(theme, keys, pane, 100)
		if strings.Contains(got, "q quit") || strings.Contains(got, "tab screen") {
			t.Fatalf("footer at 100 = %q, want the whole suffix dropped before any pane binding", got)
		}
		for _, want := range []string{"enter open", "j/k move", "g/G top/bottom"} {
			if !strings.Contains(got, want) {
				t.Fatalf("footer at 100 = %q, want the pane binding %q kept", got, want)
			}
		}
		if !strings.HasSuffix(got, "? help") {
			t.Fatalf("footer at 100 = %q, want it to still end `? help`", got)
		}

		// Still narrower: pane bindings give way from the end too, and
		// `? help` always remains.
		got = plainFooter(theme, keys, pane, 80)
		for _, gone := range []string{"q quit", "tab screen", "g/G top/bottom", "j/k move"} {
			if strings.Contains(got, gone) {
				t.Fatalf("footer at 80 = %q, want %q dropped (suffix first, then the tail)", got, gone)
			}
		}
		if !strings.Contains(got, "enter open") || !strings.HasSuffix(got, "? help") {
			t.Fatalf("footer at 80 = %q, want the head kept and `? help` last", got)
		}
	})
}
