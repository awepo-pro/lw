// mascot_test.go pins workflow 016's frozen expectations: byte-exact
// single-width frames and the state→frame table (F.M1), the welcome mount
// (F.M2), the status-row and footer mounts (F.M3), the pane-state machine
// (F.M4), and the mascot path's total lack of a clock (F.M5). The
// monochrome legibility check runs the styled frames through an Ascii
// colour profile — the mechanism this lipgloss stack actually exposes
// (v2 styles always emit SGR; the output profile is what strips it).
package ask

import (
	"bytes"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// TestMascotFramesPure pins the frozen art byte for byte (plan 016 §2/§3)
// and proves every rune single-width: the frame vocabulary is exactly
// {'█','▀','▄',' '}, so a rune-set membership assertion is the sanctioned
// equivalent of runewidth==1 (go-runewidth is indirect; no new deps).
func TestMascotFramesPure(t *testing.T) {
	wantFull := [2][3]string{
		frameIdle:     {" ██▀██▀█ ", "▀███████▀", " ▀██▀▀██ "},
		frameThinking: {" ██▄██▄█ ", "▀███████▀", " ▀██▀▀██ "},
	}
	wantCompact := [2]string{
		frameIdle:     "██▀██▀█ ",
		frameThinking: "██▄██▄█ ",
	}
	if mascotFull != wantFull {
		t.Fatalf("mascotFull drifted from the frozen frames:\n got %#v\nwant %#v", mascotFull, wantFull)
	}
	if mascotCompact != wantCompact {
		t.Fatalf("mascotCompact drifted from the frozen frames:\n got %#v\nwant %#v", mascotCompact, wantCompact)
	}

	const cells = "█▀▄ "
	for f, rows := range mascotFull {
		for i, row := range rows {
			if len([]rune(row)) != 9 {
				t.Fatalf("full frame %d row %d is %d cells, want 9", f, i, len([]rune(row)))
			}
			for _, r := range row {
				if !strings.ContainsRune(cells, r) {
					t.Fatalf("full frame %d row %d has a non-single-width rune %q", f, i, r)
				}
			}
		}
		// The compact form is the full form's top row minus its leading
		// cell — the two sizes share their eyes and cannot drift apart.
		if got, want := mascotCompact[f], rows[0][1:]; got != want {
			t.Fatalf("compact frame %d = %q, want the full form's trimmed top row %q", f, got, want)
		}
	}

	// state→frame table: only the error state reverses, and it reuses the
	// idle frame to do it.
	frameWant := []struct {
		s   mascotState
		f   mascotFrame
		rev bool
	}{
		{msIdle, frameIdle, false},
		{msThinking, frameThinking, false},
		{msError, frameIdle, true},
	}
	for _, w := range frameWant {
		f, rev := mascotFrameFor(w.s)
		if f != w.f || rev != w.rev {
			t.Fatalf("mascotFrameFor(%d) = (%d, %v), want (%d, %v)", w.s, f, rev, w.f, w.rev)
		}
	}
}

// mascotMonoStates drives a fresh pane into each state by the fields the
// pane's own rules set (never the mascot's): a running round's first
// reasoning for thinking, the terminal verdict for error.
func mascotMonoStates() []struct {
	name  string
	style func(m *Model)
} {
	return []struct {
		name  string
		style func(m *Model)
	}{
		{"idle", func(m *Model) {}},
		{"thinking", func(m *Model) { m.turnActive, m.roundSawReasoning = true, true }},
		{"error", func(m *Model) { m.turnErrored = true }},
	}
}

// TestMascotMonochrome renders every state's frames through the pane's own
// renderers — renderMascotFull and renderMascotCompact, not a re-derived
// copy — and pipes the output through an Ascii colour profile, the
// degradation a colourless terminal actually performs: what strips out
// must be exactly the frame's own cells, block glyphs and spaces, no
// escape bytes, nothing eaten, nothing added.
func TestMascotMonochrome(t *testing.T) {
	for _, st := range mascotMonoStates() {
		t.Run(st.name, func(t *testing.T) {
			m := New(newTestDeps(t)).(*Model)
			st.style(m)
			f, _ := mascotFrameFor(m.mascotState())
			want := append(append([]string{}, mascotFull[f][:]...), mascotCompact[f])
			got := append(m.renderMascotFull(), m.renderMascotCompact())
			for i, row := range got {
				var buf bytes.Buffer
				cw := colorprofile.Writer{Forward: &buf, Profile: colorprofile.Ascii}
				if _, err := cw.WriteString(row); err != nil {
					t.Fatalf("profile writer: %v", err)
				}
				// The Ascii profile strips the colour but leaves empty SGR
				// envelopes; ansi.Strip is what the test stack does with
				// styled output after the profile has had its say.
				if plain := ansi.Strip(buf.String()); plain != want[i] {
					t.Fatalf("row %d under Ascii = %q, want the plain frame %q", i, plain, want[i])
				}
			}
		})
	}
}

// TestMascotWelcome pins F.M2: the empty transcript carries the full
// three-row form above the intro, and the first user entry retires it.
func TestMascotWelcome(t *testing.T) {
	m := New(newTestDeps(t)).(*Model)
	styled, plain := screenLines(t, m) // webhint_test's 80x22 render helpers

	top := lineContaining(plain, "██▀██▀█")
	arms := lineContaining(plain, "▀███████▀")
	legs := lineContaining(plain, "▀██▀▀██")
	intro := lineContaining(plain, "Ask the wiki a question.")
	if top <= 0 || !(top < arms && arms < legs && legs < intro) {
		t.Fatalf("the full form is not intact above the intro (top %d, arms %d, legs %d, intro %d):\n%s",
			top, arms, legs, intro, strings.Join(plain, "\n"))
	}
	// One accent: the rows carry the Accent token's truecolor run.
	if !hasSGR(styled[top], sgrRGBRun(m.theme.Accent.Render("x"))) {
		t.Fatalf("the welcome art is not in the Accent token: %q", styled[top])
	}

	// With one user entry in the scrollback the art is gone — the space is
	// the conversation's now.
	m.echoUser("is anybody in there")
	_, plain = screenLines(t, m)
	if joined := strings.Join(plain, "\n"); strings.Contains(joined, "██") {
		t.Fatalf("the welcome art outlived the first entry:\n%s", joined)
	}
}

// TestMascotStatusRow pins F.M3: the compact form sits left of the footer
// hotkeys when idle (through the shell's own render, footer row included),
// and left of the 022 status line while thinking, whose `· thinking… (`
// substring stays intact.
func TestMascotStatusRow(t *testing.T) {
	t.Run("idle_footer", func(t *testing.T) {
		d := newTestDeps(t)
		var shell tea.Model = ui.NewApp(ui.Options{
			Deps:  d,
			Panes: map[ui.Screen]ui.Pane{ui.ScreenAsk: New(d)},
			Start: ui.ScreenAsk,
		})
		shell = uitest.Drive(t, shell, tea.WindowSizeMsg{Width: 100, Height: 30})
		_, plain := uitest.Screen(shell)
		footer := strings.Split(plain, "\n")[30-1] // the shell's last row
		art := strings.Index(footer, "██▀██▀█")
		keys := strings.Index(footer, "enter send")
		if art < 0 || keys < 0 || art > keys {
			t.Fatalf("footer = %q, want the compact form left of the hotkeys", footer)
		}

		// At D11's 80-column minimum the ask footer fits beside the morsel
		// (79 ≤ w-1): every binding keeps its place AND the mascot shows.
		// Below 80 the shell renders its too-small notice instead, so the
		// morsel-yields-whole rule is pinned where the seam lives
		// (internal/ui's footer_prefix_test.go).
		shell = uitest.Drive(t, shell, tea.WindowSizeMsg{Width: 80, Height: 24})
		_, plain = uitest.Screen(shell)
		footer = strings.Split(plain, "\n")[24-1]
		for _, want := range []string{"██▀██▀█", "enter send", "tab screen", "? help"} {
			if !strings.Contains(footer, want) {
				t.Fatalf("footer at 80 = %q, want it to keep %q", footer, want)
			}
		}
	})

	t.Run("thinking_status_row", func(t *testing.T) {
		m := New(newTestDeps(t)).(*Model)
		m.echoUser("what are you thinking") // beginTurn's echo precedes every turn
		m.turnActive = true                 // as beginTurn sets it
		if cmd := m.applyEvent(agent.ReasoningDelta{Text: strings.Repeat("hmm", 200)}); cmd != nil {
			t.Fatal("ReasoningDelta produced a command")
		}

		_, plain := uitest.PaneScreen(m, 80, 22)
		row := -1
		for i, l := range strings.Split(plain, "\n") {
			if strings.Contains(l, "· thinking… (") {
				row = i
				break
			}
		}
		if row < 0 {
			t.Fatal("the thinking status line vanished from the view")
		}
		lines := strings.Split(plain, "\n")
		art, line := strings.Index(lines[row], "██▄██▄█"), lines[row]
		if art < 0 || art > strings.Index(line, "· thinking… (") {
			t.Fatalf("status row = %q, want the compact form left of the status text", line)
		}
		if !strings.Contains(line, "· thinking… (600 chars)") {
			t.Fatalf("status row = %q, want the 022 line byte-intact", line)
		}

		// While thinking the mascot has moved to the status row, so the
		// footer prefix withdraws and the footer keeps its exact shape.
		if text, _ := m.FooterPrefix(); text != "" {
			t.Fatalf("FooterPrefix = %q while thinking, want it withdrawn", text)
		}

		// The ctrl+t view replaces the transcript area wholesale while the
		// round is still thinking: its reasoning tail carries the same
		// status row, mascot at its left (022 T2's second mount).
		m.showReasoning = true
		_, plain = uitest.PaneScreen(m, 80, 22)
		var seen string
		for _, l := range strings.Split(plain, "\n") {
			if strings.Contains(l, "· thinking… (") {
				seen = l
				break
			}
		}
		if art < 0 || !strings.Contains(seen, "██▄██▄█") ||
			strings.Index(seen, "██▄██▄█") > strings.Index(seen, "· thinking… (") {
			t.Fatalf("ctrl+t status row = %q, want the compact form left of the status text", seen)
		}
		if !strings.Contains(seen, "· thinking… (600 chars)") {
			t.Fatalf("ctrl+t status row = %q, want the 022 line byte-intact", seen)
		}
	})

	t.Run("error_footer_prefix_reverses", func(t *testing.T) {
		m := New(newTestDeps(t)).(*Model)
		m.turnErrored = true
		text, style := m.FooterPrefix()
		if text != mascotCompact[frameIdle] {
			t.Fatalf("FooterPrefix text = %q, want the idle art reversed", text)
		}
		if !hasReverse(style.Render(text)) {
			t.Fatalf("the error state's footer morsel is not reversed: %q", style.Render(text))
		}
	})
}

// hasReverse reports whether any SGR sequence in styled carries the
// reverse-video attribute (parameter 7) — the error state's whole
// difference from idle.
func hasReverse(styled string) bool {
	re := regexp.MustCompile(`\x1b\[([0-9;]*)m`)
	for _, m := range re.FindAllStringSubmatch(styled, -1) {
		for _, p := range strings.Split(m[1], ";") {
			if p == "7" {
				return true
			}
		}
	}
	return false
}

// mascotStep is one checkpoint of a state-machine scenario: an event to
// fold through the real applyEvent (nil asserts only), whether to mark the
// turn active first — as beginTurn does — and the mascot state wanted
// after the step.
type mascotStep struct {
	name   string
	active bool
	ev     agent.Event
	want   mascotState
}

// TestMascotStateMachine is F.M4's table: every row is one scenario driven
// end to end through applyEvent on a New(newTestDeps) pane, reading only
// the pane's own state (022's round flags, the turn flags, the terminal
// verdict) — the mascot never re-derives any of it.
func TestMascotStateMachine(t *testing.T) {
	boom := errors.New("boom")
	scenarios := []struct {
		name  string
		steps []mascotStep
	}{
		{
			name: "turn_lifecycle",
			steps: []mascotStep{
				{name: "a running turn with no events yet idles", active: true, want: msIdle},
				{name: "ReasoningDelta thinks", ev: agent.ReasoningDelta{Text: "hmm"}, want: msThinking},
				{name: "TextDelta answers, deliberately still", ev: agent.TextDelta{Text: "so"}, want: msIdle},
				{name: "ToolCallEv waits, round flags reset", ev: agent.ToolCallEv{ID: "t1", Name: "wiki_search"}, want: msIdle},
				{name: "ToolResEv still waits", ev: agent.ToolResEv{ID: "t1", Name: "wiki_search", Content: "ok"}, want: msIdle},
				{name: "DoneEv idles", ev: agent.DoneEv{Reason: "stop", Rounds: 1}, want: msIdle},
			},
		},
		{
			name: "error_and_reset",
			steps: []mascotStep{
				{name: "ErrorEv reverses", active: true, ev: agent.ErrorEv{Err: boom}, want: msError},
				{name: "the error survives the scrollback tail", want: msError},
				{name: "a new turn masks the stale verdict", active: true, want: msIdle},
				{name: "and the turn's clean stop resets it", ev: agent.DoneEv{Reason: "stop", Rounds: 1}, want: msIdle},
			},
		},
		{
			name: "fresh_pane",
			steps: []mascotStep{
				{name: "a pane that never turned idles", want: msIdle},
			},
		},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			m := New(newTestDeps(t)).(*Model)
			for _, st := range sc.steps {
				if st.active {
					m.turnActive = true
				}
				if st.ev != nil {
					if cmd := m.applyEvent(st.ev); cmd != nil {
						t.Fatalf("%s: applyEvent(%T) produced a command", st.name, st.ev)
					}
				}
				if got := m.mascotState(); got != st.want {
					t.Fatalf("%s: mascotState = %d, want %d", st.name, got, st.want)
				}
			}
			// The clean stop is the reset: the verdict flag is down again.
			if m.turnErrored {
				t.Fatalf("%s: turnErrored still set after the scenario", sc.name)
			}
		})
	}
}

// TestMascotNoTimers reads mascot.go's source and asserts the mascot path
// carries no clock of any kind (F.M5). The banned forms are built by
// concatenation so this file's own source never carries one whole — the
// orchestrator greps the mascot path too.
func TestMascotNoTimers(t *testing.T) {
	src, err := os.ReadFile("mascot.go")
	if err != nil {
		t.Fatalf("read mascot.go: %v", err)
	}
	banned := []string{
		"time." + "After",
		"time." + "NewTicker",
		"time." + "Ticker",
		"tea." + "Tick",
		"Ti" + "ck",
	}
	for _, b := range banned {
		if strings.Contains(string(src), b) {
			t.Fatalf("mascot.go contains %q: the mascot must stay fully static (F.M5)", b)
		}
	}
}
