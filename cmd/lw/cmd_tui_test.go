package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
)

// testTUIDeps builds a ui.Deps around lw's compiled-in theme and key
// defaults, with no *stage.Engine (matching every screen's own
// "constructible headless" contract). XDG_CONFIG_HOME points at a fresh
// empty temp dir for the test's duration so LoadTheme/LoadKeys never read a
// real user config (00-conventions.md §3: tests must be deterministic).
func testTUIDeps(t *testing.T) ui.Deps {
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

// TestBuildTUIOptionsInjectsAllFivePanes is s4-tui.md S4-T8's own goal: lw
// opens with all five screens live, and no "screen not loaded" placeholder
// (C-103). Exercises buildTUIOptions directly rather than cmdTUI, since
// cmdTUI calls tea.Program.Run(), which never returns when its input
// reaches EOF (C-83) — a test must drive the wiring, not the program.
func TestBuildTUIOptionsInjectsAllFivePanes(t *testing.T) {
	opts := buildTUIOptions(testTUIDeps(t))

	want := []ui.Screen{ui.ScreenBrowse, ui.ScreenReview, ui.ScreenAsk, ui.ScreenLint, ui.ScreenLog}
	if len(opts.Panes) != len(want) {
		t.Fatalf("len(Panes) = %d, want %d (%v)", len(opts.Panes), len(want), want)
	}
	for _, s := range want {
		p, ok := opts.Panes[s]
		if !ok || p == nil {
			t.Errorf("Panes[%v] missing or nil", s)
		}
	}
}

// TestBuildTUIOptionsStartsOnReview pins /PLAN.md §13's "ship review before
// anything else" — §9 calls review "the reason this project exists" — to
// Options.Start rather than leaving it to whatever NewApp happens to
// default to.
func TestBuildTUIOptionsStartsOnReview(t *testing.T) {
	opts := buildTUIOptions(testTUIDeps(t))

	if opts.Start != ui.ScreenReview {
		t.Fatalf("Start = %v, want ui.ScreenReview", opts.Start)
	}
}

// fakeScriptAgent is the scripted agent.Agent the TUI wiring test drives: it
// proposes one real op through the same *stage.Engine a real stage.* tool
// call would use, streams a short event script (backbone §9, C-105: exactly
// one terminal event, channel closed by Send itself) and records what it was
// handed. No network, no LLM, no provider key.
type fakeScriptAgent struct {
	e        *stage.Engine
	sessions agent.SessionStore

	mu      sync.Mutex
	sends   int
	gotMsg  string
	gotSess string
}

func (f *fakeScriptAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	f.mu.Lock()
	f.sends++
	f.gotMsg, f.gotSess = msg, sessionID
	f.mu.Unlock()

	defer close(out)

	op := stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       "wiki/concepts/tui-wiring-test.md",
		Content:    []byte("---\ntitle: TUI wiring test\ncreated: 2026-09-09\nupdated: 2026-09-09\ntype: concept\ntags: [inference, decoding]\nsources: [raw/articles/kv-cache-explained.md]\nconfidence: high\n---\n\nA page proposed from the ask pane, linking [[kv-cache]] and [[flash-attention]].\n\nProvenance: ^[raw/articles/kv-cache-explained.md]\n"),
		Rationale:  "prove the ask pane reaches the stage engine",
		Provenance: []string{"^[raw/articles/kv-cache-explained.md]"},
	}
	if _, err := f.e.Append(op); err != nil {
		out <- agent.ErrorEv{Err: err}
		return err
	}
	cs, err := f.e.Current()
	if err != nil {
		out <- agent.ErrorEv{Err: err}
		return err
	}

	out <- agent.TextDelta{Text: "staged "}
	out <- agent.TextDelta{Text: "a page"}
	out <- agent.ToolCallEv{ID: "t1", Name: "stage.create_page", Args: `{"path":"wiki/concepts/tui-wiring-test.md"}`}
	out <- agent.ToolResEv{ID: "t1", Name: "stage.create_page", Content: "staged op1", IsError: false}
	out <- agent.StageEv{ChangesetID: cs.ID, Ops: len(cs.Live())}
	out <- agent.DoneEv{Reason: "stop", Rounds: 1}
	return nil
}

func (f *fakeScriptAgent) Sessions() agent.SessionStore { return f.sessions }

// sendCount reports how many times Send has been entered.
func (f *fakeScriptAgent) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sends
}

// runThroughApp chases cmd and everything it produces through the shell's
// own App.Update — expanding tea.BatchMsg — until nothing is left. That is
// what tea.Program's loop does with every command a pane returns, and it is
// what carries an ask pane's ui.StageChangedMsg onto the shell's STAGE
// panel. Headless: tea.Program.Run is never called (C-83).
func runThroughApp(t *testing.T, app *ui.App, cmd tea.Cmd, seen *[]tea.Msg) {
	t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			runThroughApp(t, app, c, seen)
		}
		return
	}
	*seen = append(*seen, msg)
	_, next := app.Update(msg)
	runThroughApp(t, app, next, seen)
}

// typeIntoApp types msg through the shell, so the keys reach whichever pane
// is active. It deliberately does not press enter: the submit's command is
// the thing a test has to chase, and only the caller knows when.
func typeIntoApp(t *testing.T, app *ui.App, msg string) {
	t.Helper()
	for _, r := range msg {
		app.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// askQuestion drives one whole question through the shell: switch to Ask,
// type, submit, and run everything the submit produces.
func askQuestion(t *testing.T, app *ui.App, msg string) []tea.Msg {
	t.Helper()
	app.Update(ui.SwitchScreenMsg{To: ui.ScreenAsk})
	typeIntoApp(t, app, msg)
	_, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: 0})

	var seen []tea.Msg
	runThroughApp(t, app, cmd, &seen)
	return seen
}

// TestTUIAgentWiresThroughNewAgentToTheAskPane is S5-T5's cmd/lw half: the
// real tuiAgent path is used, newAgent is the seam it goes through, the
// sessions it hands over are the file-backed store bound to the vault's
// changesets (backbone §9, C-102) — and the agent it returns reaches the ask
// pane buildTUIOptions injects, proven by asking that pane a question and
// watching a fake agent answer through the shell.
func TestTUIAgentWiresThroughNewAgentToTheAskPane(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer engine.Close()
	cs, err := engine.OpenChangeset("tui wiring test", stage.Author{Kind: "human"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}

	sessions := agent.NewFileSessions(root)
	ag := &fakeScriptAgent{e: engine, sessions: sessions}
	var gotEngine *stage.Engine
	var gotSessions agent.SessionStore
	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, s agent.SessionStore) (agent.Agent, error) {
		gotEngine, gotSessions = e, s
		return ag, nil
	})

	wired, err := tuiAgent(engine)
	if err != nil {
		t.Fatalf("tuiAgent: %v", err)
	}
	if wired != agent.Agent(ag) {
		t.Fatalf("tuiAgent returned %T, want the swapped-in fake", wired)
	}
	if gotEngine != engine {
		t.Fatalf("newAgent was handed engine %p, want %p", gotEngine, engine)
	}
	if gotSessions == nil {
		t.Fatal("newAgent was handed a nil session store, want the file-backed one")
	}

	deps := testTUIDeps(t)
	deps.Engine = engine
	deps.Agent = wired
	app := ui.NewApp(buildTUIOptions(deps))

	// Before the turn, the STAGE panel shows the open changeset with no ops.
	if before := app.View().Content; !strings.Contains(before, "0 op(s)") {
		t.Fatalf("shell view before the turn = %q, want it to show 0 op(s)", before)
	}

	seen := askQuestion(t, app, "stage a page about the wiring test")

	if got := ag.sendCount(); got != 1 {
		t.Fatalf("Send called %d times, want 1", got)
	}
	if ag.gotMsg != "stage a page about the wiring test" {
		t.Fatalf("Send got msg %q, want the typed question", ag.gotMsg)
	}
	// The turn ran under the changeset's own session (backbone §9, C-102) —
	// never an ephemeral store.
	if ag.gotSess != cs.ID {
		t.Fatalf("turn ran under session %q, want the open changeset %q", ag.gotSess, cs.ID)
	}
	if _, err := os.Stat(filepath.Join(root, ".llmwiki", "changesets", "open", cs.ID, "session.ndjson")); err != nil {
		t.Fatalf("session file was not created beside the changeset: %v", err)
	}

	// The pane's StageEv came back through the shell as a live badge update:
	// the STAGE panel now shows the op the fake proposed.
	if after := app.View().Content; !strings.Contains(after, "1 op(s)") {
		t.Fatalf("shell view after the turn does not show 1 op(s):\n%s", after)
	}

	// And the ask pane's own scrollback — visible because Ask is the active
	// pane — shows the streamed reply and the collapsed stage.* call.
	if view := app.View().Content; !strings.Contains(view, "staged a page") {
		t.Fatalf("shell view does not show the streamed reply:\n%s", view)
	} else if !strings.Contains(view, "stage.create_page") {
		t.Fatalf("shell view does not show the tool call line:\n%s", view)
	}

	// Ctrl-R still jumps to review with the turn over, through the shell.
	app.Update(ui.SwitchScreenMsg{To: ui.ScreenAsk})
	_, jump := app.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if jump == nil {
		t.Fatal("ctrl+r produced no command")
	}
	var jumped bool
	for _, msg := range askQuestionCmd(t, app, jump) {
		if sw, ok := msg.(ui.SwitchScreenMsg); ok && sw.To == ui.ScreenReview {
			jumped = true
		}
	}
	if !jumped {
		t.Fatalf("ctrl+r never produced SwitchScreenMsg{ScreenReview}; seen = %#v", seen)
	}
}

// askQuestionCmd chases one already-captured command through the shell and
// returns every message it produced.
func askQuestionCmd(t *testing.T, app *ui.App, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	var seen []tea.Msg
	runThroughApp(t, app, cmd, &seen)
	return seen
}

// TestTUIAgentDegradesWhenNewAgentFails pins the degrade contract: a TUI
// whose provider cannot be constructed still opens, with Deps.Agent nil and
// a reason to show — not a failure to launch (s5-agent-loop.md S5-T5).
func TestTUIAgentDegradesWhenNewAgentFails(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer engine.Close()

	sentinel := errors.New("no provider configured")
	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, s agent.SessionStore) (agent.Agent, error) {
		return nil, sentinel
	})

	ag, err := tuiAgent(engine)
	if !errors.Is(err, sentinel) {
		t.Fatalf("tuiAgent error = %v, want %v", err, sentinel)
	}
	if ag != nil {
		t.Fatalf("tuiAgent returned %T alongside an error, want nil", ag)
	}

	// The shell still builds with a nil agent, and all five panes are there.
	opts := buildTUIOptions(testTUIDeps(t))
	if len(opts.Panes) != 5 {
		t.Fatalf("len(Panes) = %d, want 5", len(opts.Panes))
	}
}

// TestTUIAgentDegradesWhenConfigFails is the other degrade path: a
// malformed config.toml must not stop the TUI opening. It is the same
// tuiAgent seam, so the assertion is that the failure is reported rather
// than escalated into a launch failure.
func TestTUIAgentDegradesWhenConfigFails(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	if err := os.MkdirAll(filepath.Join(cfgDir, "lw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "lw", "config.toml"), []byte("not [valid toml"), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	root := testutil.CopyFixture(t, "minimal")
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer engine.Close()

	ag, err := tuiAgent(engine)
	if err == nil {
		t.Fatal("tuiAgent succeeded on a malformed config, want an error")
	}
	if !strings.Contains(err.Error(), "load config") {
		t.Fatalf("error = %v, want it to name the config load", err)
	}
	if ag != nil {
		t.Fatalf("tuiAgent returned %T alongside an error, want nil", ag)
	}
}

// withTUIThemeSeam swaps loadTUITheme for fn, recording every theme name the
// TUI hands ui.LoadTheme, and restoring the real loader on cleanup.
func withTUIThemeSeam(t *testing.T, fn func(name string) (ui.Theme, error)) *[]string {
	t.Helper()
	requested := new([]string)
	orig := loadTUITheme
	loadTUITheme = func(name string) (ui.Theme, error) {
		*requested = append(*requested, name)
		if fn == nil {
			return orig(name)
		}
		return fn(name)
	}
	t.Cleanup(func() { loadTUITheme = orig })
	return requested
}

// TestTUIThemeUsesConfigThemeName is the S6 wrap-up's cmd/lw half: `theme`
// in config.toml is what reaches ui.LoadTheme. LoadTheme("") — the call this
// replaces — ignores the config, so a curator who set nord got lw's default
// palette and had no way to tell why.
func TestTUIThemeUsesConfigThemeName(t *testing.T) {
	dir := configTestEnv(t)
	writeConfigFile(t, dir, `theme = "nord"
`)

	requested := withTUIThemeSeam(t, nil)

	theme, keys, err := tuiTheme()
	if err != nil {
		t.Fatalf("tuiTheme: %v", err)
	}
	if len(*requested) != 1 {
		t.Fatalf("LoadTheme called %d time(s) with %v, want exactly one call", len(*requested), *requested)
	}
	if got := (*requested)[0]; got != "nord" {
		t.Fatalf("LoadTheme was given %q, want the config's \"nord\"", got)
	}
	if theme.Warnings != nil {
		t.Errorf("warnings = %v, want none for a built-in theme name", theme.Warnings)
	}
	if keys.Warnings != nil {
		t.Errorf("keymap warnings = %v, want none with no hotkeys.toml", keys.Warnings)
	}
}

// TestTUIThemeEmptyConfigFallsBackToDefaultTheme pins the no-theme case: a
// config that does not name a theme asks LoadTheme for "", lw's own "use the
// default" spelling — not for some made-up name.
func TestTUIThemeEmptyConfigFallsBackToDefaultTheme(t *testing.T) {
	configTestEnv(t)

	requested := withTUIThemeSeam(t, nil)

	if _, _, err := tuiTheme(); err != nil {
		t.Fatalf("tuiTheme: %v", err)
	}
	if got := (*requested)[0]; got != "" {
		t.Fatalf("LoadTheme was given %q, want \"\" (lw's default)", got)
	}
}

// TestTUIThemeGarbageNameDegradesWithAWarning: a theme name nothing knows
// about must not abort the launch — LoadTheme falls back to the default
// palette and says so, and tuiTheme prints what it said instead of swallowing
// it.
func TestTUIThemeGarbageNameDegradesWithAWarning(t *testing.T) {
	dir := configTestEnv(t)
	writeConfigFile(t, dir, `theme = "solar-flare-9000"
`)

	stdout, stderr, code := captureRun(t, func() int {
		theme, _, err := tuiTheme()
		if err != nil {
			t.Errorf("tuiTheme: %v", err)
			return 1
		}
		if len(theme.Warnings) == 0 {
			t.Error("theme carries no warning; the garbage name went unnoticed")
		}
		return 0
	})
	_ = stdout

	if code != 0 {
		t.Fatalf("tuiTheme degraded to exit %d, want 0 — a garbage theme name is never fatal", code)
	}
	if !strings.Contains(stderr, `unknown theme "solar-flare-9000"`) {
		t.Errorf("stderr = %q, want the unknown-theme warning", stderr)
	}
	if !strings.Contains(stderr, "lw tui: warning:") {
		t.Errorf("stderr = %q, want warnings prefixed for stderr", stderr)
	}
}

// TestTUIThemeSurfacesFileWarnings covers the other half of the fix: an
// unknown key in hotkeys.toml (the same shape as one in theme.toml) reaches
// stderr before the TUI starts, once per warning, instead of being silently
// ignored by LoadKeys.
func TestTUIThemeSurfacesFileWarnings(t *testing.T) {
	dir := configTestEnv(t)
	writeConfigFile(t, dir, `theme = "nord"
`)
	if err := os.WriteFile(filepath.Join(dir, "lw", "hotkeys.toml"), []byte("move_down = [\"n\"]\nbogus_key = true\n"), 0o600); err != nil {
		t.Fatalf("write hotkeys.toml: %v", err)
	}

	_, stderr, code := captureRun(t, func() int {
		if _, _, err := tuiTheme(); err != nil {
			t.Errorf("tuiTheme: %v", err)
			return 1
		}
		return 0
	})

	if code != 0 {
		t.Fatalf("tuiTheme exited %d, want 0", code)
	}
	if !strings.Contains(stderr, "hotkeys.toml: unknown key") {
		t.Errorf("stderr = %q, want the hotkeys.toml warning", stderr)
	}
	if n := strings.Count(stderr, "hotkeys.toml: unknown key"); n != 1 {
		t.Errorf("warning printed %d time(s), want each exactly once:\n%s", n, stderr)
	}
}

// TestTUIThemeSurvivesMalformedConfig is the tolerance contract: the theme
// path reads the config itself, so a broken config.toml must degrade to the
// default theme — and still launch — rather than becoming a launch failure.
func TestTUIThemeSurvivesMalformedConfig(t *testing.T) {
	dir := configTestEnv(t)
	writeConfigFile(t, dir, "not [valid toml")

	requested := withTUIThemeSeam(t, nil)

	stdout, stderr, code := captureRun(t, func() int {
		_, _, err := tuiTheme()
		if err != nil {
			t.Errorf("tuiTheme: %v", err)
			return 1
		}
		return 0
	})
	_ = stdout

	if code != 0 {
		t.Fatalf("tuiTheme exited %d, want 0 — a config failure must not abort the TUI", code)
	}
	if got := (*requested)[0]; got != "" {
		t.Errorf("LoadTheme was given %q, want \"\" (the default) after the config failed", got)
	}
	if !strings.Contains(stderr, "lw tui: config: parse") {
		t.Errorf("stderr = %q, want the config failure reported", stderr)
	}
}
