// liveharness_test.go is the live-turn tests' shared harness (s5-agent-loop.md
// S5-T5): a scripted agent.Agent standing in for agent.Loop, a real
// file-backed SessionStore beside a real *stage.Engine, and no network, no
// LLM and no provider key anywhere. Everything here is local-disk and
// deterministic — the only asynchronous actor is the goroutine startTurn
// spawns, and the channel contract it depends on (backbone §9, C-105: the
// caller makes the channel, Send closes it on every exit path) is what makes
// the runs orderable. No test lives here — only the plumbing the submit,
// refusal, stream and session test files share.
package ask

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
)

// fakeTurnAgent is the scripted agent.Agent these tests install as
// Deps.Agent. Send records the turn it was given, waits on release when one
// is set — the pause a "mid-turn" assertion needs — then replays script on
// out and closes it, exactly the ownership contract Loop.Send honours
// (backbone §9, C-105). Sessions returns the store the test built, so the
// pane exercises the real file-backed session store rather than a stub of
// it.
//
// entered is closed as Send's very first action. runTurn resolves the
// session before it calls Send, so a test waiting on entered knows the
// turn's session file exists — the synchronisation that keeps a test's
// commit/reject from racing the goroutine startTurn spawned.
//
// persist (009's carry-over tests) is what Send writes into the session
// store under the turn's session id before the script replays — the
// record-writing half of Loop.Send, without which a fake turn leaves no
// transcript for a later session to be seeded from. Nil (the default
// everywhere the carry tests are not running) leaves the store untouched,
// exactly as before 009.
type fakeTurnAgent struct {
	mu       sync.Mutex
	script   []agent.Event
	persist  []agent.Record
	release  chan struct{}
	entered  chan struct{}
	sends    int
	gotMsg   string
	gotSess  string
	msgs     []string
	sessions agent.SessionStore
}

func (f *fakeTurnAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	f.mu.Lock()
	f.sends++
	f.gotMsg = msg
	f.gotSess = sessionID
	f.msgs = append(f.msgs, msg)
	script := append([]agent.Event(nil), f.script...)
	persist := append([]agent.Record(nil), f.persist...)
	f.mu.Unlock()

	if f.entered != nil {
		close(f.entered)
	}
	if f.release != nil {
		<-f.release
	}

	if persist != nil && f.sessions != nil {
		for _, r := range persist {
			_ = f.sessions.Append(sessionID, r)
		}
	}

	for _, ev := range script {
		select {
		case out <- ev:
		case <-ctx.Done():
			close(out)
			return ctx.Err()
		}
	}
	close(out)
	return nil
}

func (f *fakeTurnAgent) Sessions() agent.SessionStore { return f.sessions }

// sendCount reports how many times Send has been entered.
func (f *fakeTurnAgent) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sends
}

// sentMsgs returns every msg Send has been entered with, in order — the
// history the single gotMsg cannot answer once two turns have run.
func (f *fakeTurnAgent) sentMsgs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.msgs...)
}

// recordingSessions wraps a SessionStore and records every Close, so a test
// can prove the pane archives a turn's session at the right moment — Close
// itself is only a directory fsync, with nothing to assert on afterwards.
type recordingSessions struct {
	agent.SessionStore

	mu     sync.Mutex
	closed []string
	errs   []error
}

func (r *recordingSessions) Close(id string) error {
	r.mu.Lock()
	r.closed = append(r.closed, id)
	r.mu.Unlock()

	err := r.SessionStore.Close(id)

	r.mu.Lock()
	r.errs = append(r.errs, err)
	r.mu.Unlock()
	return err
}

// closedIDs returns every id Close was called with, in order.
func (r *recordingSessions) closedIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.closed...)
}

// closeErr returns the error the i'th Close call returned.
func (r *recordingSessions) closeErr(i int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.errs) {
		return nil
	}
	return r.errs[i]
}

// liveVault copies the minimal fixture, opens a real engine on it and opens
// one changeset, so a submitted question has a session to run under
// (backbone §9, C-102: a session is keyed by its changeset).
func liveVault(t *testing.T) (root string, e *stage.Engine, csID string) {
	t.Helper()
	root = testutil.CopyFixture(t, "minimal")
	engine, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { engine.Close() })

	cs, err := engine.OpenChangeset("ask pane live test", stage.Author{Kind: "human"})
	if err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	return root, engine, cs.ID
}

// liveDeps is newTestDeps plus the engine and agent a live turn needs.
func liveDeps(t *testing.T, e *stage.Engine, ag agent.Agent) ui.Deps {
	t.Helper()
	d := newTestDeps(t)
	d.Engine = e
	d.Agent = ag
	return d
}

// typeAndSubmit types msg into the pane's input box and presses enter,
// returning the model and the tea.Cmd the submit produced (nil when the
// submit was refused).
func typeAndSubmit(t *testing.T, m *Model, msg string) (*Model, tea.Cmd) {
	t.Helper()
	var pane ui.Pane = m
	for _, r := range msg {
		var cmd tea.Cmd
		pane, cmd = pane.Update(keyPress(r))
		if cmd != nil {
			t.Fatalf("typing %q produced a command", string(r))
		}
	}
	pane, cmd := pane.Update(specialKey(tea.KeyEnter, 0))
	return pane.(*Model), cmd
}

// lastEntry returns the scrollback's last entry.
func lastEntry(m *Model) entry { return m.entries[len(m.entries)-1] }

// assistantText returns every kindAssistant entry's text, in order.
func assistantText(m *Model) []string {
	var out []string
	for _, e := range m.entries {
		if e.kind == kindAssistant {
			out = append(out, e.text)
		}
	}
	return out
}

// sessionPath is where the file-backed store puts id's transcript (backbone
// §9, C-102) — the changeset's own directory, beside changeset.json.
func sessionPath(root, id string) string {
	return filepath.Join(root, ".llmwiki", "changesets", "open", id, "session.ndjson")
}

// pump runs cmd and feeds every message it produces — expanding tea.BatchMsg
// — to m, but deliberately does not chase the command Update returns: it
// hands that command back instead. It is runCmd with the recursion cut one
// link short, which is what lets a test leave a turn's stream mounted on a
// still-open channel instead of blocking inside it.
func pump(t *testing.T, m ui.Pane, cmd tea.Cmd) (*Model, tea.Cmd) {
	t.Helper()
	if cmd == nil {
		mm, ok := m.(*Model)
		if !ok {
			t.Fatalf("pump: pane is %T, want *Model", m)
		}
		return mm, nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m, _ = pump(t, m, c)
		}
		return m.(*Model), nil
	}
	updated, next := m.Update(msg)
	mm, ok := updated.(*Model)
	if !ok {
		t.Fatalf("pump: pane is %T, want *Model", updated)
	}
	return mm, next
}
