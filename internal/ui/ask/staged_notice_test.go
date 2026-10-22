package ask

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
)

// setupChangesetVault creates a test vault with a changeset with the specified ID,
// op count, and opened time.
func setupChangesetVault(t *testing.T, csID string, opsCount int, openedAt time.Time) (string, *stage.Engine) {
	t.Helper()
	root := testutil.CopyFixture(t, "minimal")
	openDir := filepath.Join(root, ".llmwiki", "changesets", "open", csID)
	if err := os.MkdirAll(openDir, 0o755); err != nil {
		t.Fatalf("mkdir open changeset dir: %v", err)
	}
	var ops []stage.Op
	for i := 1; i <= opsCount; i++ {
		ops = append(ops, stage.Op{
			ID:    fmt.Sprintf("op%d", i),
			Kind:  stage.OpCreatePage,
			Path:  fmt.Sprintf("wiki/concepts/test-%d.md", i),
			State: stage.StateProposed,
		})
	}
	cs := stage.Changeset{
		ID:       csID,
		Intent:   "test waiting notice",
		Author:   stage.Author{Kind: "human"},
		OpenedAt: openedAt,
		Ops:      ops,
		Checks:   stage.Checks{Schema: "pass", Lint: "pass"},
	}
	b, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
		t.Fatalf("marshal changeset: %v", err)
	}
	if err := os.WriteFile(filepath.Join(openDir, "changeset.json"), append(b, '\n'), 0o644); err != nil {
		t.Fatalf("write changeset.json: %v", err)
	}

	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	return root, engine
}

// TestAskEmptyStateWaitingNotice pins 067 D3: when transcript is empty and a
// changeset is open, empty state shows waiting notice.
func TestAskEmptyStateWaitingNotice(t *testing.T) {
	fixedNow := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	csID := "cs-857cb0bbcd3427ae"
	opened := fixedNow.Add(-48 * time.Hour) // 2d old

	_, engine := setupChangesetVault(t, csID, 19, opened)
	deps := newTestDeps(t)
	deps.Engine = engine
	deps.Now = func() time.Time { return fixedNow }

	m := New(deps).(*Model)
	view := ansi.Strip(m.View(100, 30))

	// Welcome art still rendered
	if !strings.Contains(view, "██▀██▀█") && !strings.Contains(view, "▀███████▀") {
		t.Errorf("empty state does not contain welcome art:\n%s", view)
	}

	// Intro sentence present
	if !strings.Contains(view, "Ask the wiki a question.") {
		t.Errorf("empty state does not contain intro sentence:\n%s", view)
	}

	// Waiting notice present (wrapped across lines since W <= 100 and notice is 108 chars)
	if !strings.Contains(view, "A changeset is waiting for review: cs-857cb0bbcd3427ae · 19 ops · open 2d. Open the Review tab") ||
		!strings.Contains(view, "to keep it.") {
		t.Fatalf("empty state does not contain wrapped waiting notice, got:\n%s", view)
	}

	// No changeset -> empty state byte-identical to pre-067 (no waiting notice rendered)
	rootNoCS := testutil.CopyFixture(t, "minimal")
	engNoCS, err := stage.OpenEngine(rootNoCS)
	if err != nil {
		t.Fatalf("OpenEngine no CS: %v", err)
	}
	t.Cleanup(func() { engNoCS.Close() })

	depsNoCS := newTestDeps(t)
	depsNoCS.Engine = engNoCS
	depsNoCS.Now = func() time.Time { return fixedNow }

	mNoCS := New(depsNoCS).(*Model)
	viewNoCS := mNoCS.View(100, 30)
	if strings.Contains(viewNoCS, "A changeset is waiting for review") {
		t.Fatalf("empty state with no changeset contains waiting notice:\n%s", viewNoCS)
	}
}

// TestAskStagedReadNotice pins 067 D4: a turn that read staged content appends
// notice after terminal line and after file hint.
func TestAskStagedReadNotice(t *testing.T) {
	fixedNow := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	csID := "cs-857cb0bbcd3427ae"
	opened := fixedNow.Add(-48 * time.Hour) // 2d old

	wantStatus := "this answer read unreviewed content from cs-857cb0bbcd3427ae (19 ops, open 2d) — review it in the Review tab to keep it"

	t.Run("turn_with_staged_read_and_file_hint", func(t *testing.T) {
		_, engine := setupChangesetVault(t, csID, 19, opened)
		deps := newTestDeps(t)
		deps.Engine = engine
		deps.Now = func() time.Time { return fixedNow }

		m := New(deps).(*Model)
		m.turnActive = true
		// Question that is fileable
		m.echoUser("what is kv cache?")
		m.applyEvent(agent.TextDelta{Text: "KV cache is a technique ^[wiki/concepts/kv-cache.md]."})
		m.applyEvent(agent.ToolCallEv{ID: "t1", Name: "wiki.get", Args: `{"path":"wiki/concepts/kv-cache.md"}`})
		m.applyEvent(agent.ToolResEv{
			ID:      "t1",
			Name:    "wiki.get",
			Content: "(staged in the open changeset, not yet committed)\n# KV Cache\nbody",
			IsError: false,
		})
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})

		// Find the last entries
		var statuses []string
		for _, e := range m.entries {
			if e.kind == kindStatus {
				statuses = append(statuses, e.text)
			}
		}
		if len(statuses) < 2 {
			t.Fatalf("expected at least 2 status entries (terminal/file hint/notice), got %#v", statuses)
		}
		lastStatus := statuses[len(statuses)-1]
		if lastStatus != wantStatus {
			t.Fatalf("last status = %q, want %q", lastStatus, wantStatus)
		}
		// Confirm file hint comes before notice
		prevStatus := statuses[len(statuses)-2]
		if prevStatus != fileHint {
			t.Fatalf("status before notice = %q, want file hint %q", prevStatus, fileHint)
		}
	})

	t.Run("turn_with_no_staged_result", func(t *testing.T) {
		_, engine := setupChangesetVault(t, csID, 19, opened)
		deps := newTestDeps(t)
		deps.Engine = engine
		deps.Now = func() time.Time { return fixedNow }

		m := New(deps).(*Model)
		m.turnActive = true
		m.echoUser("what is kv cache?")
		m.applyEvent(agent.TextDelta{Text: "KV cache is a technique."})
		m.applyEvent(agent.ToolCallEv{ID: "t1", Name: "wiki.get", Args: `{"path":"wiki/concepts/kv-cache.md"}`})
		m.applyEvent(agent.ToolResEv{
			ID:      "t1",
			Name:    "wiki.get",
			Content: "# KV Cache\ncommitted content",
			IsError: false,
		})
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})

		for _, e := range m.entries {
			if e.kind == kindStatus && strings.Contains(e.text, "this answer read unreviewed content") {
				t.Fatalf("turn with no staged result produced staged notice: %q", e.text)
			}
		}
	})

	t.Run("turn_with_is_error_and_staged_marker", func(t *testing.T) {
		_, engine := setupChangesetVault(t, csID, 19, opened)
		deps := newTestDeps(t)
		deps.Engine = engine
		deps.Now = func() time.Time { return fixedNow }

		m := New(deps).(*Model)
		m.turnActive = true
		m.echoUser("what is kv cache?")
		m.applyEvent(agent.TextDelta{Text: "KV cache is a technique."})
		m.applyEvent(agent.ToolCallEv{ID: "t1", Name: "wiki.get", Args: `{"path":"wiki/concepts/kv-cache.md"}`})
		m.applyEvent(agent.ToolResEv{
			ID:      "t1",
			Name:    "wiki.get",
			Content: "(staged in the open changeset, not yet committed)\n# KV Cache\nbody",
			IsError: true, // error!
		})
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})

		for _, e := range m.entries {
			if e.kind == kindStatus && strings.Contains(e.text, "this answer read unreviewed content") {
				t.Fatalf("turn with IsError result produced staged notice: %q", e.text)
			}
		}
	})
}

// TestAskEmptyStateStageChangedRefresh pins 067 G3: the empty state reflects a
// changeset staged after New once a StageChangedMsg is delivered, and
// emptyTranscriptLines performs no Engine call during render.
func TestAskEmptyStateStageChangedRefresh(t *testing.T) {
	fixedNow := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	root := testutil.CopyFixture(t, "minimal")
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { engine.Close() })

	deps := newTestDeps(t)
	deps.Engine = engine
	deps.Now = func() time.Time { return fixedNow }

	// 1. Initially no changeset is open.
	m := New(deps).(*Model)
	initialCalls := m.engineCurrentCalls
	view1 := ansi.Strip(m.View(100, 30))
	if strings.Contains(view1, "A changeset is waiting for review") {
		t.Fatalf("unexpected waiting notice before staging: %s", view1)
	}
	// emptyTranscriptLines performed no engine call during render
	if m.engineCurrentCalls != initialCalls {
		t.Fatalf("render called Engine.Current: got %d calls, want %d", m.engineCurrentCalls, initialCalls)
	}

	// 2. Open a changeset in the vault after New
	cs, err := engine.OpenChangeset("test staged after New", stage.Author{Kind: "human"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	// Append one op so it has 1 live op
	_, err = engine.Append(stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       "wiki/concepts/test-refresh.md",
		Content:    []byte("---\ntitle: Test Refresh\ntype: concept\ncreated: 2026-10-22\nupdated: 2026-10-22\n---\n\n# Test Refresh\n\nReferencing [[wiki/concepts/kv-cache.md]] and [[wiki/concepts/transformer.md]].\n"),
		Rationale:  "test refresh",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	// 3. Deliver StageChangedMsg
	callsBeforeMsg := m.engineCurrentCalls
	m.Update(ui.StageChangedMsg{ChangesetID: cs.ID})
	if m.engineCurrentCalls != callsBeforeMsg+1 {
		t.Fatalf("StageChangedMsg did not refresh Engine.Current: got %d calls, want %d", m.engineCurrentCalls, callsBeforeMsg+1)
	}

	// 4. Render multiple times - prove emptyTranscriptLines performs NO Engine call
	callsAfterRefresh := m.engineCurrentCalls
	view2 := ansi.Strip(m.View(100, 30))
	view3 := ansi.Strip(m.View(100, 30))
	_ = view3
	if m.engineCurrentCalls != callsAfterRefresh {
		t.Fatalf("emptyTranscriptLines called Engine.Current during render: got %d calls, want %d", m.engineCurrentCalls, callsAfterRefresh)
	}

	// 5. The notice reflects the new changeset
	wantNotice := fmt.Sprintf("A changeset is waiting for review: %s · 1 op · open <1h. Open the Review tab", cs.ID)
	if !strings.Contains(view2, wantNotice) {
		t.Fatalf("empty state does not reflect changeset staged after New, want %q in:\n%s", wantNotice, view2)
	}

	// 6. Further proof: even if engine is closed / deps.Engine is removed, render succeeds and shows notice
	m.deps.Engine = nil
	view4 := ansi.Strip(m.View(100, 30))
	if !strings.Contains(view4, wantNotice) {
		t.Fatalf("empty state failed to render after Engine cleared:\n%s", view4)
	}
}
