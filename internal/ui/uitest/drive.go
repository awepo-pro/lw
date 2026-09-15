// drive.go implements contract §6's Drive: a headless stand-in for what
// tea.Program delivers to a model — the caller's messages in order, then
// every command result fed back through Update until quiescent — without
// ever calling View and without ever calling Program.Run (which does not
// return on EOF; backbone §12 C-83).
//
// Commands run in their own goroutine with a bounded wait, because real
// screens return commands that legitimately never produce a message: a
// ticker, or the ask pump parked on a quiet event channel (C-117/D-DA).
// A command that stays silent past the timeout is abandoned, exactly the
// behaviour the runtime's event loop would mask by simply never delivering.
package uitest

import (
	"fmt"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// cmdTimeout is how long Drive waits for one command to produce a message
// before abandoning it (contract §6: 2s).
const cmdTimeout = 2 * time.Second

// Drive delivers msgs to m in order, running every returned tea.Cmd
// synchronously (tea.BatchMsg and tea.Sequence expanded) until quiescent,
// and ignoring commands that block (tick, ask pump) after 2s. Returns the
// final model.
//
// Note 1 of contract §6 holds by construction: Drive delivers exactly the
// messages given plus exactly what the commands they produce return, and it
// never calls View itself.
func Drive(t *testing.T, m tea.Model, msgs ...tea.Msg) tea.Model {
	t.Helper()

	queue := append([]tea.Msg(nil), msgs...)
	for len(queue) > 0 {
		msg := queue[0]
		queue = queue[1:]
		var cmd tea.Cmd
		m, cmd = updateModel(t, m, msg)
		queue = append(queue, runCmd(t, cmd)...)
	}
	return m
}

// updateModel runs one Update, failing the test if the model panics — a
// panic a real program would crash on must not pass silently here.
func updateModel(t *testing.T, m tea.Model, msg tea.Msg) (model tea.Model, cmd tea.Cmd) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("uitest: Drive: Update(%T) panicked: %v\n%s", msg, r, debug.Stack())
		}
	}()
	return m.Update(msg)
}

// runCmd runs cmd and returns every tea.Msg it yields, in order, with
// tea.BatchMsg and sequence messages expanded recursively: a batch's
// commands run in slice order (the runtime gives them no ordering
// guarantee, so a fixed order is a legal linearization), a sequence's in
// its own order, and either may nest. A command that blocks past cmdTimeout
// yields nothing; a command that panics fails the test.
func runCmd(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()

	var out []tea.Msg
	for _, msg := range runOneCmd(t, cmd) {
		if msg == nil {
			continue // a command that produces no message delivers nothing
		}
		if cmds, ok := expandCmds(msg); ok {
			for _, c := range cmds {
				out = append(out, runCmd(t, c)...)
			}
			continue
		}
		out = append(out, msg)
	}
	return out
}

// runOneCmd executes cmd in its own goroutine — so a command that blocks
// forever cannot block the drive loop — and waits at most cmdTimeout for
// its single message.
func runOneCmd(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}

	type outcome struct {
		msg      tea.Msg
		panicked any
		stack    string
	}
	ch := make(chan outcome, 1) // sized for one late result from an abandoned run
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- outcome{panicked: r, stack: string(debug.Stack())}
			}
		}()
		ch <- outcome{msg: cmd()}
	}()

	select {
	case oc := <-ch:
		if oc.panicked != nil {
			t.Fatalf("uitest: Drive: command panicked: %v\n%s", oc.panicked, oc.stack)
		}
		return []tea.Msg{oc.msg}
	case <-time.After(cmdTimeout):
		return nil // tick, ask pump: ignored, per the contract
	}
}

// cmdType is tea.Cmd itself — expansion matches any message type whose
// element is exactly it, which covers tea.BatchMsg and the runtime's
// unexported sequence message alike.
var cmdType = reflect.TypeOf(tea.Cmd(nil))

// expandCmds reports whether msg is a slice of tea.Cmd — tea.BatchMsg or a
// sequence message — and returns its commands in order.
func expandCmds(msg tea.Msg) ([]tea.Cmd, bool) {
	if msg == nil {
		return nil, false
	}
	rv := reflect.ValueOf(msg)
	if rv.Kind() != reflect.Slice || rv.Type().Elem() != cmdType {
		return nil, false
	}
	out := make([]tea.Cmd, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface().(tea.Cmd)
	}
	return out, true
}

// scriptedModel is Drive's test double: it records every message it is
// given and answers the i'th Update with steps[i]. It lives beside Drive so
// the driver's own tests exercise expansion and the blocking-command
// timeout against exactly the tea.Model surface screens implement.
type scriptedModel struct {
	got   []string
	steps []tea.Cmd
	next  int
}

var _ tea.Model = (*scriptedModel)(nil)

func (m *scriptedModel) Init() tea.Cmd { return nil }

func (m *scriptedModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.got = append(m.got, fmt.Sprintf("%T", msg))
	if m.next < len(m.steps) {
		cmd := m.steps[m.next]
		m.next++
		return m, cmd
	}
	return m, nil
}

func (m *scriptedModel) View() tea.View { return tea.NewView(strings.Join(m.got, "\n")) }
