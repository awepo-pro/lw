// mascot_test.go pins workflow 016's frozen expectations: byte-exact
// single-width frames and the state→frame table (F.M1), the welcome mount
// (F.M2), the status-row and footer mounts (F.M3), and the pane-state
// machine (F.M4) — plus workflow 023's motion, logged through the
// amendment path, never silently: A-023-1 replaces 016 F.M5's
// TestMascotNoTimers with TestMascotAnimContract (the tick contract), and
// A-023-2 amends the thinking mount's main-view pin to F.A4's rise (the
// ctrl+t pin is byte-unchanged). The monochrome legibility check runs the
// styled frames through an Ascii colour profile — the mechanism this
// lipgloss stack actually exposes (v2 styles always emit SGR; the output
// profile is what strips it).
package ask

import (
	"bytes"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// TestMascotFramesPure pins the frozen art byte for byte (plan 016 §2/§3)
// and 023's additions to it (F.A1: the two scan darts and the blink, same
// 9-cell rows, same body rows), and proves every rune single-width: the
// frame vocabulary is exactly {'█','▀','▄',' '}, so a rune-set membership
// assertion is the sanctioned equivalent of runewidth==1 (go-runewidth is
// indirect; no new deps).
func TestMascotFramesPure(t *testing.T) {
	wantFull := [5][3]string{
		frameIdle:      {" ██▀██▀█ ", "▀███████▀", " ▀██▀▀██ "},
		frameThinking:  {" ██▄██▄█ ", "▀███████▀", " ▀██▀▀██ "},
		frameScanLeft:  {" █▀██▀██ ", "▀███████▀", " ▀██▀▀██ "},
		frameScanRight: {" ███▀██▀ ", "▀███████▀", " ▀██▀▀██ "},
		frameBlink:     {" ███████ ", "▀███████▀", " ▀██▀▀██ "},
	}
	wantCompact := [5]string{
		frameIdle:      "██▀██▀█ ",
		frameThinking:  "██▄██▄█ ",
		frameScanLeft:  "█▀██▀██ ",
		frameScanRight: "███▀██▀ ",
		frameBlink:     "███████ ",
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

		// A-023-2: the main view's status row is BARE — the full form rose
		// directly above it (F.A4), so no compact art sits at the line's
		// left any more and no double head shows. The 022 substring stays
		// byte-intact.
		_, plain := uitest.PaneScreen(m, 80, 22)
		lines := strings.Split(plain, "\n")
		row := -1
		for i, l := range lines {
			if strings.Contains(l, "· thinking… (") {
				row = i
				break
			}
		}
		if row < 0 {
			t.Fatal("the thinking status line vanished from the view")
		}
		line := lines[row]
		if strings.ContainsAny(line, "█▀▄") {
			t.Fatalf("status row = %q, want it bare — the rise replaced the compact art (A-023-2)", line)
		}
		if !strings.Contains(line, "· thinking… (600 chars)") {
			t.Fatalf("status row = %q, want the 022 line byte-intact", line)
		}
		// The rise: the full form's three rows — the static thinking pose
		// with anim off — stand directly above the bare line.
		for i, art := range []string{" ██▄██▄█ ", "▀███████▀", " ▀██▀▀██ "} {
			if row-3+i < 0 || !strings.Contains(lines[row-3+i], art) {
				t.Fatalf("rise row %d (%q) is not directly above the bare status row:\n%s",
					i, art, strings.Join(lines, "\n"))
			}
		}

		// While thinking the footer prefix is still withdrawn — the rise
		// lives inside the transcript and the footer keeps its exact shape.
		if text, _ := m.FooterPrefix(); text != "" {
			t.Fatalf("FooterPrefix = %q while thinking, want it withdrawn", text)
		}

		// The ctrl+t view replaces the transcript area wholesale while the
		// round is still thinking: its reasoning tail carries the same
		// status row, compact form at its left (022 T2's second mount) —
		// byte-unchanged by 023 (F.A4).
		m.showReasoning = true
		_, plain = uitest.PaneScreen(m, 80, 22)
		var seen string
		for _, l := range strings.Split(plain, "\n") {
			if strings.Contains(l, "· thinking… (") {
				seen = l
				break
			}
		}
		art := strings.Index(seen, "██▄██▄█")
		if art < 0 || art > strings.Index(seen, "· thinking… (") {
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
				// 025 F.W1: the pre-first-byte window IS the waiting state —
				// this leg pinned the old table's msIdle for it, which is
				// the pane the cold-start fix exists to change. See the
				// waiting_state_test.go table for the waiting legs' new home.
				{name: "a running turn with no events yet waits", active: true, want: msWaiting},
				{name: "ReasoningDelta thinks", ev: agent.ReasoningDelta{Text: "hmm"}, want: msThinking},
				{name: "TextDelta answers, deliberately still", ev: agent.TextDelta{Text: "so"}, want: msIdle},
				{name: "ToolCallEv holds the tool, round flags reset", ev: agent.ToolCallEv{ID: "t1", Name: "wiki_search"}, want: msIdle},
				// The resolved tool hands the round back to the pre-first-byte
				// window: waiting again, recurring before every round (025).
				{name: "ToolResEv hands back to the wait", ev: agent.ToolResEv{ID: "t1", Name: "wiki_search", Content: "ok"}, want: msWaiting},
				{name: "DoneEv idles", ev: agent.DoneEv{Reason: "stop", Rounds: 1}, want: msIdle},
			},
		},
		{
			name: "error_and_reset",
			steps: []mascotStep{
				{name: "ErrorEv reverses", active: true, ev: agent.ErrorEv{Err: boom}, want: msError},
				{name: "the error survives the scrollback tail", want: msError},
				// 025 F.W1: the fresh turn's no-events-yet window waits; the
				// mask still holds — waitingVisible outranks turnErrored and
				// requires the turnActive only a new turn sets.
				{name: "a new turn masks the stale verdict, waiting", active: true, want: msWaiting},
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

// TestMascotAnimContract is 023's tick contract (F.A3), substituted for
// 016 F.M5's TestMascotNoTimers through the pre-authorized amendment
// A-023-1 — logged here and in the workflow MASTER, never silent. It
// restates the old file-level ban at the package level: the anim's motion
// lives in mascot_anim.go on Update's thread, tea.Tick is the only clock
// in the package's non-test source, no goroutines drive the motion, tick
// chains re-arm conditionally and stop by not re-issuing, and the scan
// cycle runs UP → LEFT → UP → RIGHT deterministically — the tests inject
// the tick MSG, never sleep.
func TestMascotAnimContract(t *testing.T) {
	t.Run("source", func(t *testing.T) {
		entries, err := os.ReadDir(".")
		if err != nil {
			t.Fatalf("read the package dir: %v", err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			src, err := os.ReadFile(name)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			// Every clock but tea.Tick is banned package-wide; tea.Tick
			// itself may appear in mascot_anim.go alone.
			for _, b := range []string{"time." + "After", "time." + "NewTicker", "time." + "Tick"} {
				if strings.Contains(string(src), b) {
					t.Fatalf("%s contains %q: tea.Tick is the only clock in the package (F.A3)", name, b)
				}
			}
			if name != "mascot_anim.go" && strings.Contains(string(src), "tea."+"Tick") {
				t.Fatalf("%s carries a tea tick: the anim's clock lives in mascot_anim.go alone (F.A3)", name)
			}
			// The motion code runs on Update's thread — no goroutines of
			// its own (stream.go's pump predates 023 and drives no anim).
			if (name == "mascot.go" || name == "mascot_anim.go") && strings.Contains(string(src), "go "+"func") {
				t.Fatalf("%s spawns a goroutine: the anim has none (F.A3)", name)
			}
			// mascot.go keeps 016's own purity — art and the state table,
			// never a tick of any kind (A-023-1's restated file-level ban).
			if name == "mascot.go" && strings.Contains(string(src), "Ti"+"ck") {
				t.Fatalf("mascot.go mentions a tick: motion lives in mascot_anim.go (A-023-1)")
			}
		}
	})

	t.Run("constants", func(t *testing.T) {
		if scanEvery != 200*time.Millisecond {
			t.Fatalf("scanEvery = %v, want 200ms (F.A2)", scanEvery)
		}
		if blinkEvery != 4*time.Second {
			t.Fatalf("blinkEvery = %v, want 4s (F.A2)", blinkEvery)
		}
		if blinkHold != 120*time.Millisecond {
			t.Fatalf("blinkHold = %v, want 120ms (F.A2)", blinkHold)
		}
	})

	t.Run("scan_cycle", func(t *testing.T) {
		m := New(newTestDeps(t)).(*Model)
		m.anim = true // F.A6: tests that need motion set the flag directly
		m.echoUser("what are you thinking")
		m.turnActive = true // as beginTurn sets it
		if cmd := m.applyEvent(agent.ReasoningDelta{Text: "hmm"}); cmd != nil {
			t.Fatal("ReasoningDelta produced a command")
		}
		// UP → LEFT → UP → RIGHT → repeat (F.A2): pose 0 is the frozen
		// thinking frame, and every injected tick advances one step and
		// re-arms while the round is still thinking.
		for i, want := range []mascotFrame{
			frameThinking, frameScanLeft, frameThinking, frameScanRight, frameThinking,
		} {
			if got := m.mascotPose(msThinking, true); got != want {
				t.Fatalf("pose %d = %d, want %d", i, got, want)
			}
			if _, cmd := m.Update(scanTickMsg{}); cmd == nil {
				t.Fatalf("scan tick %d re-armed nothing while thinking (F.A3)", i)
			}
		}
		// The compact form never scans: wherever it shows while thinking
		// (the ctrl+t row) the frozen thinking pose holds (F.A4).
		for i := 0; i < 4; i++ {
			if got := m.mascotPose(msThinking, false); got != frameThinking {
				t.Fatalf("compact pose = %d mid-scan, want the frozen thinking frame (F.A4)", got)
			}
			m.Update(scanTickMsg{})
		}
	})

	t.Run("no_rearm_when_still", func(t *testing.T) {
		// (c): a tick handled while the turn answers or waits on a tool
		// round re-arms nothing — the chain dies at the first silent beat.
		m := New(newTestDeps(t)).(*Model)
		m.anim = true
		m.echoUser("q")
		m.turnActive = true
		if cmd := m.applyEvent(agent.ReasoningDelta{Text: "hmm"}); cmd != nil {
			t.Fatal("ReasoningDelta produced a command")
		}
		if cmd := m.applyEvent(agent.TextDelta{Text: "so"}); cmd != nil {
			t.Fatal("TextDelta produced a command")
		}
		if _, cmd := m.Update(scanTickMsg{}); cmd != nil {
			t.Fatal("a scan tick re-armed while answering (F.A3 c)")
		}
		if _, cmd := m.Update(blinkTickMsg{}); cmd != nil {
			t.Fatal("a blink tick re-armed while answering (F.A3 c)")
		}
		if m.eyesShut {
			t.Fatal("the eyes shut while the pane answers (F.A5)")
		}
		// The tool round is the other still phase F.A3(c) names: the round
		// flags reset, so the thinking line is gone and both chains die at
		// their next beat.
		if cmd := m.applyEvent(agent.ToolCallEv{ID: "t1", Name: "wiki_search"}); cmd != nil {
			t.Fatal("ToolCallEv produced a command")
		}
		if _, cmd := m.Update(scanTickMsg{}); cmd != nil {
			t.Fatal("a scan tick re-armed during a tool round (F.A3 c)")
		}
		if _, cmd := m.Update(blinkTickMsg{}); cmd != nil {
			t.Fatal("a blink tick re-armed during a tool round (F.A3 c)")
		}
		if m.eyesShut {
			t.Fatal("the eyes shut during a tool round (F.A5)")
		}
		// The error state is frozen too (F.A5): no blink re-arm, and a hold
		// caught mid-flight when the verdict landed opens into stillness.
		me := New(newTestDeps(t)).(*Model)
		me.anim = true
		me.turnErrored = true
		me.eyesShut = true
		if _, cmd := me.Update(blinkTickMsg{}); cmd != nil {
			t.Fatal("a blink tick re-armed in the error state (F.A5)")
		}
		if got := ansi.Strip(me.renderMascotFull()[0]); got != mascotFull[frameIdle][0] {
			t.Fatalf("error full-form top row = %q, want the static idle row %q", got, mascotFull[frameIdle][0])
		}
	})

	t.Run("no_double_arm", func(t *testing.T) {
		// animArm runs beside the event pump's own re-arm on EVERY agent
		// event, so a guard-less arm would stack one more 200ms scan ticker
		// per reasoning delta — the scan would spin at the stream's rate and
		// the tickers would multiply for the length of the thinking (F.A3:
		// each chain is single-file, held by its *Armed flag). The Cmds
		// here are asserted and dropped, never invoked, so no test waits on
		// a real timer.
		m := New(newTestDeps(t)).(*Model)
		m.anim = true
		if m.animArm() == nil {
			t.Fatal("a fresh idle pane armed no blink tick (the Init path)")
		}
		if m.animArm() != nil {
			t.Fatal("animArm stacked a second blink tick on the armed one (F.A3)")
		}
		m.blinkArmed = false
		m.eyesShut = true // the hold is in flight: nothing to arm meanwhile
		if m.animArm() != nil {
			t.Fatal("animArm armed while the eyes are shut mid-hold (F.A3)")
		}
		m.eyesShut = false
		m.echoUser("q")
		m.turnActive = true
		if cmd := m.applyEvent(agent.ReasoningDelta{Text: "hmm"}); cmd != nil {
			t.Fatal("ReasoningDelta produced a command")
		}
		if m.animArm() == nil {
			t.Fatal("a thinking pane armed no scan tick")
		}
		if m.animArm() != nil {
			t.Fatal("animArm stacked a second scan tick on the armed one (F.A3)")
		}
	})

	t.Run("anim_off_is_inert", func(t *testing.T) {
		// F.A6's zero-timer half, pinned directly: with the switch off —
		// how every harness and every other test builds the pane — Init
		// stays nil, an agent event through the real Update arms nothing,
		// and even a hand-fed tick is inert. The handlers' !anim guards are
		// the second half of the switch; the frozen pose is the proof the
		// render never moved.
		m := New(newTestDeps(t)).(*Model)
		if cmd := m.Init(); cmd != nil {
			t.Fatal("Init armed a timer with anim off (F.A6)")
		}
		m.echoUser("q")
		m.turnActive = true
		if _, cmd := m.Update(EventMsg{Ev: agent.ReasoningDelta{Text: "hmm"}}); cmd != nil {
			t.Fatal("an agent event armed a timer with anim off (F.A6)")
		}
		if _, cmd := m.Update(scanTickMsg{}); cmd != nil {
			t.Fatal("a scan tick re-armed with anim off (F.A6)")
		}
		if _, cmd := m.Update(blinkTickMsg{}); cmd != nil {
			t.Fatal("a blink tick re-armed with anim off (F.A6)")
		}
		if m.eyesShut {
			t.Fatal("a blink tick shut the eyes with anim off (F.A6)")
		}
		if got := ansi.Strip(m.renderMascotFull()[0]); got != mascotFull[frameThinking][0] {
			t.Fatalf("full top row = %q with anim off, want the frozen thinking row %q", got, mascotFull[frameThinking][0])
		}
	})

	t.Run("wait_chain_stops_at_first_delta", func(t *testing.T) {
		// 025 F.W3/F.W5: the wait blink is the cold-start window's motion —
		// its own single-file chain (waitArmed, separate from the idle
		// blink's), armed while the pane waits, re-armed only while it
		// still waits, dead by not re-issuing the moment any delta lands.
		if waitBlinkEvery != 1200*time.Millisecond {
			t.Fatalf("waitBlinkEvery = %v, want 1200ms (F.W3)", waitBlinkEvery)
		}
		m := New(newTestDeps(t)).(*Model)
		m.anim = true
		m.echoUser("q")
		m.turnActive = true // as beginTurn sets it
		if m.animArm() == nil {
			t.Fatal("a waiting pane armed no wait tick (F.W3)")
		}
		if m.animArm() != nil {
			t.Fatal("animArm stacked a second wait tick on the armed one (F.A3)")
		}
		// One delivered beat shuts the wait pose for one blinkHold and arms
		// the reopen; the reopen re-arms the every-1.2s beat.
		if _, cmd := m.Update(waitTickMsg{}); cmd == nil {
			t.Fatal("the wait tick armed no hold (F.W3)")
		}
		if !m.waitShut {
			t.Fatal("the wait tick did not shut the wait pose (F.W3)")
		}
		if _, cmd := m.Update(waitOpenMsg{}); cmd == nil {
			t.Fatal("the wait open re-armed nothing while still waiting (F.W3)")
		}
		if m.waitShut {
			t.Fatal("the wait open left the pose shut (F.W3)")
		}
		// A reasoning delta ends the window — thinkingVisible outranks
		// waitingVisible — and the wait chain's next beats die without
		// re-arming: the frozen leg this run exists for (F.W5).
		if cmd := m.applyEvent(agent.ReasoningDelta{Text: "hmm"}); cmd != nil {
			t.Fatal("ReasoningDelta produced a command")
		}
		if _, cmd := m.Update(waitTickMsg{}); cmd != nil {
			t.Fatal("a wait tick re-armed after a delta landed (F.W5)")
		}
		if _, cmd := m.Update(waitOpenMsg{}); cmd != nil {
			t.Fatal("a wait open re-armed after a delta landed (F.W5)")
		}
		if m.waitShut {
			t.Fatal("the wait pose shut after the delta ended the window (F.W5)")
		}
		// The chain's death leaves the frozen idle frame: waiting
		// alternates idle↔blink through its own chain only (F.W2).
		if got := m.mascotPose(msWaiting, false); got != frameIdle {
			t.Fatalf("waiting pose after the chain died = %d, want the idle frame (F.W2)", got)
		}
	})
}

// TestMascotBlink is F.A5's table: an idle pane blinks — the welcome full
// form and the footer morsel from one frame source, body rows constant —
// and the blink is suppressed wherever stillness is the point. Frame index
// 0 (every pre-023 byte pin) runs unchanged in all the other tests: anim
// is false there, so no pose ever moves.
func TestMascotBlink(t *testing.T) {
	// The swap is same-width by construction — the footer's width math
	// never notices it; assert it, don't assume it.
	if got, want := len([]rune(mascotCompact[frameBlink])), len([]rune(mascotCompact[frameIdle])); got != want {
		t.Fatalf("blink compact is %d cells, want the idle compact's %d", got, want)
	}
	if got, want := len([]rune(mascotFull[frameBlink][0])), len([]rune(mascotFull[frameIdle][0])); got != want {
		t.Fatalf("blink top row is %d cells, want the idle top row's %d", got, want)
	}

	t.Run("welcome_and_morsel_in_sync", func(t *testing.T) {
		m := New(newTestDeps(t)).(*Model)
		m.anim = true
		// One blinkTickMsg shuts the eyes and holds them for one blinkHold
		// — the blinkOpen tick it arms through Update, the real path.
		if _, cmd := m.Update(blinkTickMsg{}); cmd == nil {
			t.Fatal("the blink tick armed no hold (F.A2)")
		}
		if got := ansi.Strip(m.renderMascotFull()[0]); got != mascotFull[frameBlink][0] {
			t.Fatalf("welcome top row = %q, want the shut frame %q", got, mascotFull[frameBlink][0])
		}
		// Body rows constant through the blink.
		for i, row := range m.renderMascotFull()[1:] {
			if got := ansi.Strip(row); got != mascotFull[frameIdle][i+1] {
				t.Fatalf("body row %d moved in the blink: %q", i+1, got)
			}
		}
		if text, _ := m.FooterPrefix(); text != mascotCompact[frameBlink] {
			t.Fatalf("footer morsel = %q, want the same frame source's %q", text, mascotCompact[frameBlink])
		}
		// The reopen: eyes open, and the every-4s tick re-arms.
		if _, cmd := m.Update(blinkOpenMsg{}); cmd == nil {
			t.Fatal("the blink open re-armed nothing (F.A3)")
		}
		if got := ansi.Strip(m.renderMascotFull()[0]); got != mascotFull[frameIdle][0] {
			t.Fatalf("welcome top row = %q, want the open frame %q", got, mascotFull[frameIdle][0])
		}
		if text, _ := m.FooterPrefix(); text != mascotCompact[frameIdle] {
			t.Fatalf("footer morsel = %q, want the open frame's %q", text, mascotCompact[frameIdle])
		}
	})

	t.Run("morsel_alone_with_content", func(t *testing.T) {
		m := New(newTestDeps(t)).(*Model)
		m.anim = true
		m.echoUser("still there?")
		if _, cmd := m.Update(blinkTickMsg{}); cmd == nil {
			t.Fatal("the blink tick armed no hold (F.A2)")
		}
		if text, _ := m.FooterPrefix(); text != mascotCompact[frameBlink] {
			t.Fatalf("footer morsel = %q, want the shut frame %q", text, mascotCompact[frameBlink])
		}
		_, plain := uitest.PaneScreen(m, 80, 22)
		if strings.Contains(plain, "▀███████▀") {
			t.Fatalf("a full form rendered over the conversation:\n%s", plain)
		}
	})

	t.Run("answering_is_still", func(t *testing.T) {
		// turnActive suppresses the blink at the render too: a hold caught
		// mid-flight when the turn started opens into stillness (F.A5).
		//
		// 025: roundSawText is what makes this leg ANSWERING. Before F.W1
		// the setup was turnActive alone, which the old table read as
		// msIdle — but that is the pre-first-byte window, now msWaiting, so
		// the leg was never exercising the state its name claims. The
		// missing delta is the setup's own blind spot (the 023 Tier-2
		// lesson), not a weakening: with it the assertion is the same one,
		// now actually about answering.
		m := New(newTestDeps(t)).(*Model)
		m.anim = true
		m.turnActive = true
		m.roundSawText = true // a TextDelta landed: the pane is answering
		m.eyesShut = true
		if got := ansi.Strip(m.renderMascotFull()[0]); got != mascotFull[frameIdle][0] {
			t.Fatalf("answering top row = %q, want the still idle row %q", got, mascotFull[frameIdle][0])
		}
		if text, _ := m.FooterPrefix(); text != mascotCompact[frameIdle] {
			t.Fatalf("answering morsel = %q, want the still idle morsel %q", text, mascotCompact[frameIdle])
		}
	})

	// A-025-3 (user, 2026-09-22, settled on the acceptance capture): one
	// head per busy state. The compact form shows in exactly ONE of the two
	// slots at a time, so whenever it has moved into the transcript — the
	// thinking status row or the waiting sending row — the footer withdraws
	// its morsel. The acceptance capture showed waiting rendering two.
	t.Run("waiting_withdraws_the_footer_morsel", func(t *testing.T) {
		m := New(newTestDeps(t)).(*Model)
		m.anim = true
		m.turnActive = true // no deltas yet: the pre-first-byte window

		if !m.waitingVisible() {
			t.Fatal("setup did not reach the waiting window")
		}
		if text, _ := m.FooterPrefix(); text != "" {
			t.Fatalf("waiting morsel = %q, want \"\" — the compact form is on the sending row (A-025-3)", text)
		}
		// Thinking already withdrew it; the two busy states now agree.
		m.roundSawReasoning = true
		if text, _ := m.FooterPrefix(); text != "" {
			t.Fatalf("thinking morsel = %q, want \"\" (F.M3)", text)
		}
		// A finished turn hands the morsel back.
		m.turnActive, m.roundSawReasoning = false, false
		if text, _ := m.FooterPrefix(); text != mascotCompact[frameIdle] {
			t.Fatalf("idle morsel = %q, want the idle morsel back %q", text, mascotCompact[frameIdle])
		}
	})
}

// TestMascotThinkingRise pins F.A4's edges beyond the mount itself (the
// mount's shape lives in TestMascotStatusRow/thinking_status_row, amended
// by A-023-2): the first answer token drops rise and bare line together,
// the scan moves the risen rows while anim is on, and the welcome and rise
// mounts never coexist — beginTurn's echo precedes every turn, so the rise
// is only ever mounted inside conversationLines.
func TestMascotThinkingRise(t *testing.T) {
	t.Run("first_text_delta_drops_it", func(t *testing.T) {
		m := New(newTestDeps(t)).(*Model)
		m.echoUser("q")
		m.turnActive = true
		if cmd := m.applyEvent(agent.ReasoningDelta{Text: "hmm"}); cmd != nil {
			t.Fatal("ReasoningDelta produced a command")
		}
		if cmd := m.applyEvent(agent.TextDelta{Text: "so,"}); cmd != nil {
			t.Fatal("TextDelta produced a command")
		}
		_, plain := uitest.PaneScreen(m, 80, 22)
		if strings.Contains(plain, "██") || strings.Contains(plain, "· thinking… (") {
			t.Fatalf("the rise outlived the first TextDelta:\n%s", plain)
		}
	})

	t.Run("scan_moves_the_risen_rows", func(t *testing.T) {
		m := New(newTestDeps(t)).(*Model)
		m.anim = true
		m.echoUser("q")
		m.turnActive = true
		if cmd := m.applyEvent(agent.ReasoningDelta{Text: "hmm"}); cmd != nil {
			t.Fatal("ReasoningDelta produced a command")
		}
		_, plain := uitest.PaneScreen(m, 80, 22)
		if !strings.Contains(plain, " ██▄██▄█ ") {
			t.Fatalf("the rise does not open on the frozen thinking pose:\n%s", plain)
		}
		if _, cmd := m.Update(scanTickMsg{}); cmd == nil {
			t.Fatal("the scan tick re-armed nothing while thinking (F.A3)")
		}
		_, plain = uitest.PaneScreen(m, 80, 22)
		if !strings.Contains(plain, " █▀██▀██ ") {
			t.Fatalf("the risen rows did not scan to LEFT:\n%s", plain)
		}
	})

	t.Run("second_phase_opens_at_up", func(t *testing.T) {
		// The scan cycle resets at each thinking-phase start: a chain that
		// died mid-cycle leaves scanStep where it stopped, and an un-reset
		// step makes the NEXT phase's rise render its first beat on a
		// mid-cycle pose — mascotScanCycle[3], scan RIGHT — for up to one
		// scanEvery before the first tick advances it. That breaks F.A2
		// (UP is the cycle's head) and F.M4 (entering msThinking renders
		// the thinking frame). The reset rides the thinking phase's
		// invisible→visible edge (animArm's scanPhaseLive tracking, not
		// the re-arm): this leg is the dead-chain shape — the tick dies in
		// the silent window, so the phase-2 reasoning also re-arms; its
		// surviving-chain sibling below covers the arm that defers.
		m := New(newTestDeps(t)).(*Model)
		m.anim = true
		m.echoUser("q")
		m.turnActive = true
		if cmd := m.applyEvent(agent.ReasoningDelta{Text: "hmm"}); cmd != nil {
			t.Fatal("ReasoningDelta produced a command")
		}
		for i := 0; i < 3; i++ {
			if _, cmd := m.Update(scanTickMsg{}); cmd == nil {
				t.Fatal("scan tick re-armed nothing while thinking (F.A3)")
			}
		}
		if m.scanStep != 3 {
			t.Fatalf("scanStep = %d after three ticks, want 3", m.scanStep)
		}
		// The first answer token stills the round; the in-flight tick then
		// dies without re-arming (F.A3 c) — scanStep stays where it was.
		if cmd := m.applyEvent(agent.TextDelta{Text: "so,"}); cmd != nil {
			t.Fatal("TextDelta produced a command")
		}
		if _, cmd := m.Update(scanTickMsg{}); cmd != nil {
			t.Fatal("the dying scan tick re-armed while answering (F.A3 c)")
		}
		if cmd := m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1}); cmd != nil {
			t.Fatal("DoneEv produced a command")
		}
		// Phase 2 (a tool round inside the turn is the same transition): a
		// new turn's first reasoning re-arms the scan through the real
		// path (Update's animArm, ask.go) — and no tick has fired yet, so
		// the risen rows must ALREADY be the frozen thinking frame,
		// byte-exact.
		m.turnActive = true
		if cmd := m.applyEvent(agent.ReasoningDelta{Text: "again"}); cmd != nil {
			t.Fatal("ReasoningDelta produced a command")
		}
		if cmd := m.animArm(); cmd == nil {
			t.Fatal("the second phase's re-arm produced no scan tick")
		}
		if m.scanStep != 0 {
			t.Fatalf("scanStep = %d at the second phase's start, want 0 (the cycle opens at UP, F.A2)", m.scanStep)
		}
		rows := m.renderMascotFull()
		for i, want := range mascotFull[frameThinking] {
			if got := ansi.Strip(rows[i]); got != want {
				t.Fatalf("risen row %d = %q, want the frozen thinking row %q", i, got, want)
			}
		}
	})

	t.Run("second_phase_opens_at_up_surviving_chain", func(t *testing.T) {
		// The surviving-chain sibling of the leg above: when a scan tick is
		// still in flight across a fast round boundary — TextDelta →
		// ToolCallEv → ToolResEv → the next round's ReasoningDelta all
		// inside one scanEvery — the chain never dies, scanArmed stays
		// true, and the re-arm defers. The reset must therefore ride the
		// thinking phase's invisible→visible edge, not the re-arm: the leg
		// above kills the chain before phase 2 by construction, so it
		// cannot see this path, where an un-reset cycle opens phase 2 on
		// phase 1's last pose (mascotScanCycle[3], scan RIGHT) for up to
		// one scanEvery.
		m := New(newTestDeps(t)).(*Model)
		m.anim = true
		m.echoUser("q")
		m.turnActive = true
		// Phase 1 driven through the real Update path — EventMsg is where
		// animArm runs beside applyEvent (ask.go).
		if _, cmd := m.Update(EventMsg{Ev: agent.ReasoningDelta{Text: "hmm"}}); cmd == nil {
			t.Fatal("ReasoningDelta armed no scan tick (F.A3)")
		}
		// Three delivered beats park the cycle on its last pose —
		// mascotScanCycle[3] — and the third beat's re-arm hands the fourth
		// tick back undelivered: this is the state the boundary must
		// survive, scanArmed true with a tick in flight.
		for i := 0; i < 3; i++ {
			if _, cmd := m.Update(scanTickMsg{}); cmd == nil {
				t.Fatal("scan tick re-armed nothing while thinking (F.A3)")
			}
		}
		if m.scanStep != 3 {
			t.Fatalf("scanStep = %d after three ticks, want 3", m.scanStep)
		}
		if !m.scanArmed {
			t.Fatal("scanArmed false with a tick still in flight")
		}
		// The whole round boundary inside one scanEvery: no tick fires
		// between the last phase-1 beat and the next round's reasoning. The
		// scan chain survives into phase 2 untouched — 025 F.W3 adds the
		// one legitimate arm in this window: the ToolResEv hands the round
		// back to the waiting state, whose wait chain arms there (a
		// different chain, single-file on its own waitArmed — the F.A3
		// point, no second SCAN tick, still holds).
		for _, ev := range []agent.Event{
			agent.TextDelta{Text: "so,"},
			agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{}`},
		} {
			if _, cmd := m.Update(EventMsg{Ev: ev}); cmd != nil {
				t.Fatalf("%T armed a timer inside the silent window (F.A3)", ev)
			}
		}
		if _, cmd := m.Update(EventMsg{Ev: agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "[]"}}); cmd == nil {
			t.Fatal("the waiting window after ToolResEv armed no wait tick (F.W3)")
		}
		if !m.scanArmed {
			t.Fatal("the wait chain's arm disturbed the surviving scan chain (F.A3)")
		}
		// Phase 2's first reasoning re-enters thinking through the real
		// path with the old chain still armed — the arm must defer (single
		// file, F.A3) and the cycle must STILL open at UP, before any tick
		// has fired.
		if _, cmd := m.Update(EventMsg{Ev: agent.ReasoningDelta{Text: "again"}}); cmd != nil {
			t.Fatal("the surviving chain's re-entry stacked a second scan tick (F.A3)")
		}
		if m.scanStep != 0 {
			t.Fatalf("scanStep = %d at the second phase's start, want 0 (the cycle opens at UP, F.A2)", m.scanStep)
		}
		rows := m.renderMascotFull()
		for i, want := range mascotFull[frameThinking] {
			if got := ansi.Strip(rows[i]); got != want {
				t.Fatalf("risen row %d = %q, want the frozen thinking row %q", i, got, want)
			}
		}
	})

	t.Run("welcome_and_rise_never_coexist", func(t *testing.T) {
		// A forced thinking state on an EMPTY transcript — unreachable in
		// production, where beginTurn's echo always precedes the first
		// event — still renders the welcome mount and no thinking line:
		// the rise exists only inside conversationLines.
		m := New(newTestDeps(t)).(*Model)
		m.turnActive = true
		if cmd := m.applyEvent(agent.ReasoningDelta{Text: "hmm"}); cmd != nil {
			t.Fatal("ReasoningDelta produced a command")
		}
		_, plain := uitest.PaneScreen(m, 80, 22)
		if !strings.Contains(plain, "Ask the wiki a question.") {
			t.Fatalf("the welcome mount is gone in the forced state:\n%s", plain)
		}
		if strings.Contains(plain, "· thinking… (") {
			t.Fatalf("the rise mounted beside the welcome art:\n%s", plain)
		}
	})
}
