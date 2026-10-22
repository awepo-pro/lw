package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

type waitingNoteFakeAgent struct {
	sessions agent.SessionStore
	script   []agent.Event
}

func (f *waitingNoteFakeAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	defer close(out)
	for _, ev := range f.script {
		select {
		case out <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (f *waitingNoteFakeAgent) Sessions() agent.SessionStore { return f.sessions }

// setupTestChangeset creates an open changeset in root with opsCount live ops,
// and openedAt timestamp.
func setupTestChangeset(t *testing.T, root string, opsCount int, openedAt time.Time) string {
	t.Helper()
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	cs, err := e.OpenChangeset("test waiting note", stage.Author{Kind: "human"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	csID := cs.ID
	const body = "ingested body\n"
	ingested, _ := vault.ParseDate("2026-01-01")
	for i := 1; i <= opsCount; i++ {
		rawPath := fmt.Sprintf("raw/articles/source-%d.md", i)
		content := (&vault.RawSource{
			SourceURL: fmt.Sprintf("source-%d", i),
			Ingested:  ingested,
			SHA256:    vault.BodySHA256(body),
			Body:      body,
		}).Serialize()
		_, err := e.Append(stage.Op{
			Kind:      stage.OpIngestSource,
			Path:      rawPath,
			Content:   content,
			SHA256:    vault.BodySHA256(body),
			Extractor: "test-fake",
		})
		if err != nil {
			t.Fatalf("Append op %d: %v", i, err)
		}
	}
	e.Close()

	csPath := filepath.Join(root, ".llmwiki", "changesets", "open", csID, "changeset.json")
	data, err := os.ReadFile(csPath)
	if err != nil {
		t.Fatalf("read changeset.json: %v", err)
	}
	var readCS stage.Changeset
	if err := json.Unmarshal(data, &readCS); err != nil {
		t.Fatalf("unmarshal changeset.json: %v", err)
	}
	readCS.OpenedAt = openedAt
	updated, err := json.MarshalIndent(readCS, "", "  ")
	if err != nil {
		t.Fatalf("marshal updated changeset: %v", err)
	}
	if err := os.WriteFile(csPath, append(updated, '\n'), 0o644); err != nil {
		t.Fatalf("write updated changeset.json: %v", err)
	}
	return csID
}

func vaultTreeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if strings.HasPrefix(rel, ".llmwiki/logs") || strings.HasPrefix(rel, ".llmwiki/traces") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		files[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return files
}

func assertSnapshotsEqual(t *testing.T, before, after map[string]string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("vault file count changed: before %d, after %d", len(before), len(after))
	}
	for k, vBefore := range before {
		vAfter, ok := after[k]
		if !ok {
			t.Fatalf("file %s was removed", k)
		}
		if vBefore != vAfter {
			t.Fatalf("file %s content changed", k)
		}
	}
}

// TestCmdQueryStagedReadNote pins 067 D4: fake agent emitting a staged raw.get
// result → stderr ends with note, stdout == the answer only; vault tree + journal
// byte-identical before/after.
func TestCmdQueryStagedReadNote(t *testing.T) {
	fixedNow := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	oldNow := waitingNoteNow
	waitingNoteNow = func() time.Time { return fixedNow }
	defer func() { waitingNoteNow = oldNow }()

	root := testutil.CopyFixture(t, "minimal")
	opened := fixedNow.Add(-48 * time.Hour) // 2d old
	csID := setupTestChangeset(t, root, 19, opened)

	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		return &waitingNoteFakeAgent{
			sessions: sessions,
			script: []agent.Event{
				agent.TextDelta{Text: "the answer"},
				agent.ToolCallEv{ID: "t1", Name: "raw.get", Args: `{"path":"raw/articles/sample.md"}`},
				agent.ToolResEv{
					ID:      "t1",
					Name:    "raw.get",
					Content: "(staged in the open changeset, not yet committed)\n# Sample\nStaged body",
					IsError: false,
				},
				agent.DoneEv{Reason: "stop", Rounds: 1},
			},
		}, nil
	})

	warmEng, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	warmEng.Close()

	snapBefore := vaultTreeSnapshot(t, root)

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"query", "--vault", root, "what does the sample say?"})
	})
	if code != 0 {
		t.Fatalf("query exit code = %d, want 0; stderr=%q", code, stderr)
	}

	if stdout != "the answer\n" {
		t.Fatalf("stdout = %q, want exactly \"the answer\\n\"", stdout)
	}

	wantSuffix := fmt.Sprintf("note: this answer read unreviewed content from %s (19 ops, open 2d) — lw tui → Review to keep it\n", csID)
	if !strings.HasSuffix(stderr, wantSuffix) {
		t.Fatalf("stderr does not end with wantSuffix:\nwant suffix: %q\ngot stderr: %q", wantSuffix, stderr)
	}

	snapAfter := vaultTreeSnapshot(t, root)
	assertSnapshotsEqual(t, snapBefore, snapAfter)
}

// TestCLIWaitingNote pins 067 D3: open changeset aged 25h → lw query, lw note,
// lw sync, lw ingest each print exactly one stderr line; aged 23h → no line;
// no changeset → no line; ingest opening a new changeset → no line.
func TestCLIWaitingNote(t *testing.T) {
	fixedNow := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	oldNow := waitingNoteNow
	waitingNoteNow = func() time.Time { return fixedNow }
	defer func() { waitingNoteNow = oldNow }()

	t.Run("aged_25h", func(t *testing.T) {
		opened := fixedNow.Add(-25 * time.Hour)

		// 1. lw query (no staged read)
		{
			root := testutil.CopyFixture(t, "minimal")
			csID := setupTestChangeset(t, root, 19, opened)
			wantNote := fmt.Sprintf("note: changeset %s (19 ops) has waited 25h for review — lw tui → Review", csID)
			withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
				return &waitingNoteFakeAgent{
					sessions: sessions,
					script: []agent.Event{
						agent.TextDelta{Text: "simple answer\n"},
						agent.DoneEv{Reason: "stop", Rounds: 1},
					},
				}, nil
			})
			_, stderr, code := captureRun(t, func() int {
				return run([]string{"query", "--vault", root, "some question"})
			})
			if code != 0 {
				t.Fatalf("query exit code = %d; stderr=%q", code, stderr)
			}
			lines := strings.Split(strings.TrimSpace(stderr), "\n")
			matched := 0
			for _, l := range lines {
				if l == wantNote {
					matched++
				}
			}
			if matched != 1 {
				t.Fatalf("query: expected exactly 1 line matching %q, got %d in:\n%s", wantNote, matched, stderr)
			}
		}

		// 2. lw note -m x
		{
			root := testutil.CopyFixture(t, "minimal")
			csID := setupTestChangeset(t, root, 19, opened)
			wantNote := fmt.Sprintf("note: changeset %s (19 ops) has waited 25h for review — lw tui → Review", csID)
			_, stderr, code := captureRun(t, func() int {
				return run([]string{"note", "--vault", root, "-m", "a note"})
			})
			if code != 0 {
				t.Fatalf("note exit code = %d; stderr=%q", code, stderr)
			}
			lines := strings.Split(strings.TrimSpace(stderr), "\n")
			matched := 0
			for _, l := range lines {
				if l == wantNote {
					matched++
				}
			}
			if matched != 1 {
				t.Fatalf("note: expected exactly 1 line matching %q, got %d in:\n%s", wantNote, matched, stderr)
			}
		}

		// 3. lw sync (fake remote)
		{
			syncHermetic(t)
			installFakeSSH(t)
			a := newSyncPC(t, "a")
			a.useFixture()
			if _, stderr, code := a.lw("sync", "init", "box:remote-vault", "--vault", a.root); code != 0 {
				t.Fatalf("sync init: exit %d stderr %q", code, stderr)
			}
			csID := setupTestChangeset(t, a.root, 19, opened)
			wantNote := fmt.Sprintf("note: changeset %s (19 ops) has waited 25h for review — lw tui → Review", csID)
			_, stderr, code := a.lw("sync", "--vault", a.root)
			if code != 0 {
				t.Fatalf("sync exit code = %d; stderr=%q", code, stderr)
			}
			lines := strings.Split(strings.TrimSpace(stderr), "\n")
			matched := 0
			for _, l := range lines {
				if l == wantNote {
					matched++
				}
			}
			if matched != 1 {
				t.Fatalf("sync: expected exactly 1 line matching %q, got %d in:\n%s", wantNote, matched, stderr)
			}
		}

		// 4. lw ingest <file> (joins)
		{
			root := testutil.CopyFixture(t, "minimal")
			csID := setupTestChangeset(t, root, 19, opened)
			wantNote := fmt.Sprintf("note: changeset %s (20 ops) has waited 25h for review — lw tui → Review", csID)
			inputFile := filepath.Join(t.TempDir(), "new-doc.md")
			if err := os.WriteFile(inputFile, []byte("# New Doc\nSome text\n"), 0o644); err != nil {
				t.Fatalf("write input: %v", err)
			}

			withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
				return &fakeStageAgent{
					e:        e,
					sessions: sessions,
					ops:      oneIngestOp(),
				}, nil
			})
			_, stderr, code := captureRun(t, func() int {
				return run([]string{"ingest", "--vault", root, inputFile})
			})
			if code != 0 {
				t.Fatalf("ingest exit code = %d; stderr=%q", code, stderr)
			}
			lines := strings.Split(strings.TrimSpace(stderr), "\n")
			matched := 0
			for _, l := range lines {
				if l == wantNote {
					matched++
				}
			}
			if matched != 1 {
				t.Fatalf("ingest: expected exactly 1 line matching %q, got %d in:\n%s", wantNote, matched, stderr)
			}
		}
	})

	t.Run("aged_23h", func(t *testing.T) {
		opened := fixedNow.Add(-23 * time.Hour)

		// lw query
		{
			root := testutil.CopyFixture(t, "minimal")
			setupTestChangeset(t, root, 19, opened)
			withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
				return &waitingNoteFakeAgent{
					sessions: sessions,
					script: []agent.Event{
						agent.TextDelta{Text: "simple answer\n"},
						agent.DoneEv{Reason: "stop", Rounds: 1},
					},
				}, nil
			})
			_, stderr, _ := captureRun(t, func() int {
				return run([]string{"query", "--vault", root, "some question"})
			})
			if strings.Contains(stderr, "has waited") {
				t.Fatalf("query aged 23h printed waiting note: %q", stderr)
			}
		}

		// lw note -m x
		{
			root := testutil.CopyFixture(t, "minimal")
			setupTestChangeset(t, root, 19, opened)
			_, stderr, _ := captureRun(t, func() int {
				return run([]string{"note", "--vault", root, "-m", "a note"})
			})
			if strings.Contains(stderr, "has waited") {
				t.Fatalf("note aged 23h printed waiting note: %q", stderr)
			}
		}
	})

	t.Run("no_changeset", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
			return &waitingNoteFakeAgent{
				sessions: sessions,
				script: []agent.Event{
					agent.TextDelta{Text: "simple answer\n"},
					agent.DoneEv{Reason: "stop", Rounds: 1},
				},
			}, nil
		})
		_, stderr, _ := captureRun(t, func() int {
			return run([]string{"query", "--vault", root, "some question"})
		})
		if strings.Contains(stderr, "has waited") {
			t.Fatalf("query with no changeset printed waiting note: %q", stderr)
		}
	})

	t.Run("ingest_opens_new_changeset", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		inputFile := filepath.Join(t.TempDir(), "new-doc.md")
		if err := os.WriteFile(inputFile, []byte("# New Doc\nSome text\n"), 0o644); err != nil {
			t.Fatalf("write input: %v", err)
		}

		withFakeIngestAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore, ex extract.Extractor) (agent.Agent, error) {
			return &fakeStageAgent{
				e:        e,
				sessions: sessions,
				ops:      oneIngestOp(),
			}, nil
		})
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"ingest", "--vault", root, inputFile})
		})
		if code != 0 {
			t.Fatalf("ingest exit code = %d; stderr=%q", code, stderr)
		}
		if strings.Contains(stderr, "has waited") {
			t.Fatalf("ingest opening new changeset printed waiting note: %q", stderr)
		}
	})
}
