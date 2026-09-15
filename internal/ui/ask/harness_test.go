// harness_test.go is the ask tests' shared plumbing: the Deps builder, the
// synthetic key builders, and the command driver that stands in for
// tea.Program's event loop. No test lives here.
package ask

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/ui"
)

// newTestDeps builds ui.Deps with lw's compiled-in Theme/KeyMap and no
// Engine — the ask screen never reads Deps.Engine or Deps.Agent in this
// subtask (backbone §12, D-CN). XDG_CONFIG_HOME points at an empty temp
// dir so these tests never pick up a real user config, mirroring
// internal/ui/review's own (unexported, different-package) helper.
func newTestDeps(t *testing.T) ui.Deps {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	theme, err := ui.LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	keys, err := ui.LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}
	return ui.Deps{Theme: theme, Keys: keys}
}

// keyPress builds a synthetic tea.KeyPressMsg for a single printable rune
// — the shape internal/ui/review's own tests use (C-83: never via
// tea.Program.Run()).
func keyPress(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

// specialKey builds a synthetic tea.KeyPressMsg for a non-printable key or
// a modified one (e.g. enter, backspace, ctrl+r): code and modifier only,
// no Text — matching how a real terminal reports these (backbone §12
// C-80).
func specialKey(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod}
}

// runCmd executes cmd, if non-nil, against m, expanding any tea.BatchMsg
// it returns into its constituent commands and feeding every resulting
// message back through m.Update — until nothing is left to run —
// recording every message observed along the way into *seen. Headless
// tests drive Update/View directly (C-83); this is the harness's
// stand-in for what tea.Program's event loop would otherwise do, the same
// shape as internal/ui/review_test.go's own runCmd/feedMsg pair. Driving
// the pump this way — rather than calling applyEvent directly — is what
// proves Listen and its re-arm, not just the event-handling logic behind
// it.
func runCmd(t *testing.T, m ui.Pane, cmd tea.Cmd, seen *[]tea.Msg) ui.Pane {
	t.Helper()
	if cmd == nil {
		return m
	}
	return feedMsg(t, m, cmd(), seen)
}

func feedMsg(t *testing.T, m ui.Pane, msg tea.Msg, seen *[]tea.Msg) ui.Pane {
	t.Helper()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = runCmd(t, m, c, seen)
		}
		return m
	}
	*seen = append(*seen, msg)
	updated, cmd := m.Update(msg)
	return runCmd(t, updated, cmd, seen)
}
