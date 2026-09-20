// capture_test.go is package ask_test on purpose (the same shape
// ask_external_test.go argues for): C27's bug lives in the shell's key
// routing (internal/ui), and what it loses is user-visible — the typed
// question — so this test drives a real ui.NewApp with the real ask pane
// active, through exported API only, and asserts on what the frame renders
// and on the commands Update returns. It is the end-to-end half of the
// TextCapturer contract; internal/ui's textcapture_test.go is the routing
// half.
package ask_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/ask"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// question is the conformance gate's ask-conversation script line
// (mockupQuestion): it ends in `?`, which the shell used to steal for the
// keys overlay instead of typing it.
const question = "How does calling Claude through Vertex AI differ from the Anthropic API?"

// ask.Model must satisfy ui.TextCapturer.
var _ ui.TextCapturer = (*ask.Model)(nil)

func TestAskCapturesText(t *testing.T) {
	// Deps the way uitest.Deps builds them: compiled-in theme and keys at
	// the given polarity, read through a pinned config dir so a real user
	// config never leaks in. Engine stays nil — the input box reads no
	// engine state, and NewApp is constructible headless.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	theme, err := ui.LoadTheme("")
	if err != nil {
		t.Fatalf("LoadTheme: %v", err)
	}
	keys, err := ui.LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}
	d := ui.Deps{WebSearch: true, Theme: theme.WithDark(true), Keys: keys}

	// The input always takes typing: New's pane reports true unconditionally.
	// WebSearch true: fixtures pin the configured vault's unchanged UI (012
	// contract §5).
	if p := ask.New(ui.Deps{WebSearch: true}); !p.(*ask.Model).CapturesText() {
		t.Fatal("ask.Model.CapturesText() = false, want true")
	}

	// The real shell with the real ask pane active (agent nil: nothing here
	// submits).
	app := ui.NewApp(ui.Options{
		Deps:  d,
		Panes: map[ui.Screen]ui.Pane{ui.ScreenAsk: ask.New(d)},
		Start: ui.ScreenAsk,
	})

	m := uitest.Drive(t, app, tea.WindowSizeMsg{Width: 120, Height: 40})

	// Type the question rune by rune — the space as its own key — and fail
	// the moment any keystroke makes the shell answer with a command: a
	// quit here is the program dying with the question in the box.
	for _, r := range question {
		var cmd tea.Cmd
		m, cmd = m.Update(uitest.Key(string(r)))
		if cmd != nil {
			t.Fatalf("key %q made Update return a Cmd (%v), want nil — typing must not quit or otherwise interrupt the program", string(r), cmd)
		}
	}

	_, plain := uitest.Screen(m)

	// The input row is the Message panel's one content row (view.go's
	// messagePanel): accent `›`, the typed text from column 2, the accent
	// `█` cursor right after it — the only `█` on the frame. Strip the
	// panel borders and it must be exactly `› ` + question + `█` — nothing
	// eaten (q, ?), nothing swallowed by an overlay.
	var input string
	for _, line := range strings.Split(plain, "\n") {
		if trimmed := strings.TrimRight(line, " "); strings.Contains(trimmed, "█") {
			input = trimmed
		}
	}
	input = strings.TrimPrefix(strings.TrimPrefix(input, "│ "), "› ")
	input = strings.TrimSuffix(input, " │")
	input = strings.TrimRight(input, " ") // the content's own padding, in front of the border
	if want := question + "█"; input != want {
		t.Fatalf("ask input row = %q, want %q", input, want)
	}

	// And the keys overlay never opened mid-question.
	if strings.Contains(plain, "esc to close") {
		t.Fatal("the keys overlay opened while the question was being typed")
	}
}
