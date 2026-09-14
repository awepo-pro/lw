package main

import (
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/ask"
	"github.com/awepo-pro/lw/internal/ui/browse"
	"github.com/awepo-pro/lw/internal/ui/lintview"
	"github.com/awepo-pro/lw/internal/ui/logview"
	"github.com/awepo-pro/lw/internal/ui/review"
)

// cmdTUI opens the interactive terminal UI (backbone §12/§13; s4-tui.md
// S4-T2): the shell from internal/ui, wired to a real stage.Engine, theme
// and key bindings. main.go dispatches here both for the explicit `lw tui`
// verb and as the default action when lw is invoked with no subcommand at
// all — that dispatch row has been in main.go's verbs slice since S0-T1 and
// is never added a second time (D-R, C-86).
func cmdTUI(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw tui: unexpected argument %q\n", fs.Arg(0))
		return &exitError{code: 2}
	}

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}

	engine, err := stage.OpenEngine(root)
	if err != nil {
		return fmt.Errorf("open vault %s: %w", root, err)
	}
	defer engine.Close()

	theme, keys, err := tuiTheme()
	if err != nil {
		return err
	}

	// The Ask pane drives a real agent.Agent; every other screen needs none
	// (backbone §12; s5-agent-loop.md S5-T5). So a config or provider
	// failure degrades the ask pane, not the TUI: lw still opens, and
	// review/browse/lint/log stay fully usable with no provider at all.
	// tuiAgent reports why it could not build one, and that reason goes to
	// stderr the way every other verb's diagnostics do.
	ag, agErr := tuiAgent(engine)
	if agErr != nil {
		fmt.Fprintf(os.Stderr, "lw tui: ask disabled: %v\n", agErr)
	}

	deps := ui.Deps{
		Engine: engine,
		Agent:  ag, // nil when tuiAgent failed; the ask pane says so
		Theme:  theme,
		Keys:   keys,
	}

	app := ui.NewApp(buildTUIOptions(deps))

	p := tea.NewProgram(app)
	// Kill restores the terminal unconditionally, so a panic inside
	// Update/View — or Run returning early on its own panic recovery —
	// can never leave the terminal in raw mode (s4-tui.md S4-T2 item 5).
	defer p.Kill()

	if _, err := p.Run(); err != nil {
		// Bare error: main prints "lw: tui: <err>" (MASTER §9 D-Q). Naming
		// the verb again here produced "lw: tui: tui: ...".
		return err
	}
	return nil
}

// loadTUITheme and loadTUIKeys are the seams tuiTheme goes through, for the
// same reason tuiAgent is a function and newAgent a var (cmd_ingest.go): a
// test has to prove the config's theme name reaches ui.LoadTheme without
// driving tea.Program.Run, which never returns once its input reaches EOF
// (C-83). Swapped in a test, restored on cleanup.
var (
	loadTUITheme = ui.LoadTheme
	loadTUIKeys  = ui.LoadKeys
)

// tuiTheme resolves the theme and the key bindings the shell is built from.
//
// The theme name comes from config.toml's `theme` key (backbone §12's "name
// comes from config.toml"), so a curator who wrote `theme = "nord"` gets
// nord and not whatever `LoadTheme("")` happens to fall back to. Reading the
// config is tolerant, exactly as the agent path below is: a malformed or
// unreadable config.toml is reported to stderr and the compiled-in default
// theme is used, because a broken theme setting must never cost the user the
// whole TUI.
//
// Every warning either LoadTheme or LoadKeys collected — an unknown key in
// theme.toml or hotkeys.toml, an unrecognised theme name with no file behind
// it — is printed once to stderr before the TUI starts. A typo in a hand-
// written file must not be silent: the pane it would have styled just
// renders with the default, and without this the only symptom is a theme
// that "does not work".
func tuiTheme() (ui.Theme, ui.KeyMap, error) {
	name := ""
	if cfg, err := config.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "lw tui: %v\n", err)
	} else {
		name = cfg.Theme
	}

	theme, err := loadTUITheme(name)
	if err != nil {
		return ui.Theme{}, ui.KeyMap{}, fmt.Errorf("load theme: %w", err)
	}
	keys, err := loadTUIKeys()
	if err != nil {
		return ui.Theme{}, ui.KeyMap{}, fmt.Errorf("load keys: %w", err)
	}

	seen := make(map[string]bool, len(theme.Warnings)+len(keys.Warnings))
	for _, w := range append(append([]string{}, theme.Warnings...), keys.Warnings...) {
		if seen[w] {
			continue
		}
		seen[w] = true
		fmt.Fprintf(os.Stderr, "lw tui: warning: %s\n", w)
	}
	return theme, keys, nil
}

// tuiAgent constructs the agent.Agent the Ask pane drives: the config, the
// package-level newAgent seam, and the file-backed session store that keeps
// a turn's transcript beside its changeset (backbone §9, C-102 — never an
// ephemeral store, since review's Commit and Reject move the very directory
// that session lives in, and the transcript has to move with it).
//
// It is a function rather than inline code so a test can prove both halves
// of the contract — wired and degraded — without driving tea.Program.Run
// (C-83); newAgent is a package-level var for exactly that reason
// (cmd_ingest.go).
func tuiAgent(e *stage.Engine) (agent.Agent, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return newAgent(e, cfg, agent.NewFileSessions(e.Vault().Root()))
}

// buildTUIOptions assembles the ui.Options ui.NewApp is built from: the
// five screens constructed from one ui.Deps and injected into Options.Panes
// under their Screen key (backbone §12; s4-tui.md S4-T8, C-103), with
// Options.Start pinned to ui.ScreenReview — /.dev-notes/PLAN-v1.md §13 says ship review
// first and §9 calls it the reason this project exists.
//
// cmd/lw is the only package allowed to import a screen: internal/ui itself
// never imports review/browse/ask/lintview/logview, which is what let wave
// 3 build all five as independent, parallel packages (00-conventions.md §1
// rule 5a). Split out of cmdTUI so a test can inspect the wiring directly
// without invoking tea.Program.Run(), which never returns when its input
// reaches EOF (C-83) — headless tests drive Update/View, never Run.
func buildTUIOptions(d ui.Deps) ui.Options {
	return ui.Options{
		Deps: d,
		Panes: map[ui.Screen]ui.Pane{
			ui.ScreenBrowse: browse.New(d),
			ui.ScreenReview: review.New(d),
			ui.ScreenAsk:    ask.New(d),
			ui.ScreenLint:   lintview.New(d),
			ui.ScreenLog:    logview.New(d),
		},
		Start: ui.ScreenReview,
	}
}
