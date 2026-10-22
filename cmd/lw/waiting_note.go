package main

import (
	"fmt"
	"os"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
)

var waitingNoteNow = time.Now

func opWord(n int) string {
	if n == 1 {
		return "op"
	}
	return "ops"
}

// waitingNoteLine formats the CLI waiting reminder line for an open changeset (067 D3).
func waitingNoteLine(cs *stage.Changeset, now time.Time) string {
	age := now.Sub(cs.OpenedAt)
	ops := len(cs.Live())
	return fmt.Sprintf("note: changeset %s (%d %s) has waited %s for review — lw tui → Review\n",
		cs.ID, ops, opWord(ops), stage.AgeLabel(age))
}

// stagedReadNoteLine formats the CLI notice when a query read staged content (067 D4).
func stagedReadNoteLine(cs *stage.Changeset, now time.Time) string {
	age := now.Sub(cs.OpenedAt)
	ops := len(cs.Live())
	return fmt.Sprintf("note: this answer read unreviewed content from %s (%d %s, open %s) — lw tui → Review to keep it\n",
		cs.ID, ops, opWord(ops), stage.AgeLabel(age))
}

// waitingNoteTracker records whether an open changeset was already present
// before a command ran, so the waiting reminder is only printed for changesets
// not opened by this invocation (067 D3).
type waitingNoteTracker struct {
	root     string
	priorID  string
	priorHas bool
}

func newWaitingNoteTracker(root string) *waitingNoteTracker {
	t := &waitingNoteTracker{root: root}
	if cs, err := stage.PeekOpenChangeset(root); err == nil && cs != nil {
		t.priorID = cs.ID
		t.priorHas = true
	}
	return t
}

func (t *waitingNoteTracker) maybePrintNote(e *stage.Engine) {
	if t == nil || !t.priorHas {
		return
	}
	var cs *stage.Changeset
	var err error
	if e != nil {
		cs, err = e.Current()
	} else if t.root != "" {
		cs, err = stage.PeekOpenChangeset(t.root)
	}
	if err != nil || cs == nil || cs.ID != t.priorID {
		return
	}
	age := waitingNoteNow().Sub(cs.OpenedAt)
	if age < 24*time.Hour {
		return
	}
	fmt.Fprint(os.Stderr, waitingNoteLine(cs, waitingNoteNow()))
}
