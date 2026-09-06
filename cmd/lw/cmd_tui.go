package main

import (
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

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

	theme, err := ui.LoadTheme("")
	if err != nil {
		return fmt.Errorf("load theme: %w", err)
	}
	keys, err := ui.LoadKeys()
	if err != nil {
		return fmt.Errorf("load keys: %w", err)
	}

	deps := ui.Deps{
		Engine: engine,
		// Agent stays nil until S5 wires a real agent.Agent (backbone §12,
		// s4-tui.md S4-T2 item 5): Deps.Agent is an interface, and a nil
		// interface value is exactly what "not built yet" means.
		Theme: theme,
		Keys:  keys,
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

// buildTUIOptions assembles the ui.Options ui.NewApp is built from: the
// five screens constructed from one ui.Deps and injected into Options.Panes
// under their Screen key (backbone §12; s4-tui.md S4-T8, C-103), with
// Options.Start pinned to ui.ScreenReview — /PLAN.md §13 says ship review
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
