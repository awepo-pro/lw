// reload_tick_test.go is 008 T-G's U4 evidence for the tick half (MASTER
// §5): with Options.ReloadEvery set, Init schedules a tea.Tick whose
// message — handled on App.Update's goroutine — calls Engine.ReloadIfChanged
// and, when another process's commit landed, produces the VaultReloadedMsg
// the existing handler refreshes header counts and panes on. Zero schedules
// nothing.
package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// newTickApp opens a real Engine over a private copy of the minimal fixture
// and builds the shell around it with every reloadEvery. recording is the
// pane the assertions read back.
func newTickApp(t *testing.T, reloadEvery time.Duration) (*App, *recordingPane) {
	t.Helper()
	root := testutil.CopyFixture(t, "minimal")
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { engine.Close() })

	deps := testDeps(t)
	deps.Engine = engine
	rec := &recordingPane{name: "recording"}
	a := NewApp(Options{
		Deps:        deps,
		Panes:       map[Screen]Pane{ScreenBrowse: rec},
		Start:       ScreenBrowse,
		ReloadEvery: reloadEvery,
	})
	return a, rec
}

// recordingPane records every message Update delivers to it.
type recordingPane struct {
	name    string
	updates int
	got     []tea.Msg
}

func (r *recordingPane) Init() tea.Cmd { return nil }

func (r *recordingPane) Update(msg tea.Msg) (Pane, tea.Cmd) {
	r.updates++
	r.got = append(r.got, msg)
	return r, nil
}

func (r *recordingPane) View(w, h int) string { return "[" + r.name + "]" }
func (r *recordingPane) Title() string        { return r.name }
func (r *recordingPane) Help() []key.Binding  { return nil }

var _ Pane = (*recordingPane)(nil)

// feedApp drives the shell the way tea.Program's loop would: every message
// cmd produces re-enters a.Update, batches expand, and the commands those
// updates return are executed too. A reloadTickMsg stops the feed — it is
// the tick's own re-arm, and following it would feed the tick forever.
func feedApp(t *testing.T, a *App, cmd tea.Cmd, budget int) {
	t.Helper()
	if cmd == nil || budget <= 0 {
		return
	}
	msg := cmd()
	if msg == nil {
		return
	}
	if _, ok := msg.(reloadTickMsg); ok {
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			feedApp(t, a, c, budget-1)
		}
		return
	}
	_, next := a.Update(msg)
	feedApp(t, a, next, budget-1)
}

// foreignCommit opens a second Engine over the same root — another lw
// process — and commits one new page through it.
func foreignCommit(t *testing.T, root string) {
	t.Helper()
	e2, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine (second): %v", err)
	}
	t.Cleanup(func() { e2.Close() })

	if _, err := e2.OpenChangeset("a foreign process commits", stage.Author{Kind: "agent", Model: "other"}); err != nil {
		t.Fatalf("OpenChangeset (second engine): %v", err)
	}
	content := []byte("---\n" +
		"title: Speculative Prefetch\n" +
		"created: 2026-09-17\n" +
		"updated: 2026-09-17\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"confidence: medium\n" +
		"---\n" +
		"\n" +
		"# Speculative Prefetch\n" +
		"\n" +
		"See [[kv-cache]] and [[flash-attention]] for background.\n")
	if _, err := e2.Append(stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       "wiki/concepts/speculative-prefetch.md",
		Content:    content,
		Rationale:  "a page only the other process knows about",
		Provenance: []string{"raw/articles/kv-cache-explained.md"},
	}); err != nil {
		t.Fatalf("Append (second engine): %v", err)
	}
	if _, err := e2.Commit("a foreign process commits"); err != nil {
		t.Fatalf("Commit (second engine): %v", err)
	}
}

func TestReloadTick(t *testing.T) {
	t.Run("zero_interval_schedules_no_tick", func(t *testing.T) {
		a, _ := newTickApp(t, 0)

		got := collectInitMsgs(t, a.Init(), 32)
		for _, msg := range got {
			if _, ok := msg.(reloadTickMsg); ok {
				t.Fatalf("Init's batch scheduled a reload tick with ReloadEvery == 0: %#v", got)
			}
		}
	})

	t.Run("foreign_commit_refreshes_header_counts", func(t *testing.T) {
		a, _ := newTickApp(t, time.Millisecond)
		a.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
		if before := a.View().Content; !strings.Contains(before, "4 pages") {
			t.Fatalf("header before the foreign commit = %q, want the fixture's 4 pages", before)
		}

		foreignCommit(t, a.deps.Engine.Vault().Root())

		// Feed the tick message directly — no real sleeping; the re-arm the
		// handler returns stops the feed (feedApp).
		_, cmd := a.Update(reloadTickMsg{})
		feedApp(t, a, cmd, 32)

		if after := a.View().Content; !strings.Contains(after, "5 pages") {
			t.Fatalf("header after the reload tick = %q, want the foreign commit's 5 pages", after)
		}
	})

	t.Run("reload_broadcasts_to_panes", func(t *testing.T) {
		a, rec := newTickApp(t, time.Millisecond)

		foreignCommit(t, a.deps.Engine.Vault().Root())

		_, cmd := a.Update(reloadTickMsg{})
		feedApp(t, a, cmd, 32)

		var seen int
		for _, msg := range rec.got {
			if _, ok := msg.(VaultReloadedMsg); ok {
				seen++
			}
		}
		if seen != 1 {
			t.Fatalf("recording pane saw %d VaultReloadedMsg after a reloading tick, want exactly 1; got %#v", seen, rec.got)
		}
	})

	t.Run("tick_rearms_when_nothing_changed", func(t *testing.T) {
		a, rec := newTickApp(t, time.Millisecond)

		_, cmd := a.Update(reloadTickMsg{})
		if cmd == nil {
			t.Fatal("tick with no journal change returned a nil cmd, want the re-armed tick")
		}
		if rec.updates != 0 {
			t.Fatalf("tick with no journal change delivered %d message(s) to the pane, want none: %#v", rec.updates, rec.got)
		}
	})
}

// collectInitMsgs executes Init's whole command tree and returns every
// message it produces — the only way to see into a tea.Batch.
func collectInitMsgs(t *testing.T, cmd tea.Cmd, budget int) []tea.Msg {
	t.Helper()
	if cmd == nil || budget <= 0 {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var got []tea.Msg
		for _, c := range batch {
			got = append(got, collectInitMsgs(t, c, budget-1)...)
		}
		return got
	}
	return []tea.Msg{msg}
}
