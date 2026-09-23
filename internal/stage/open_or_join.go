// open_or_join.go implements workflow 019 T1: Engine.OpenOrJoin, the
// open-or-join entry point `lw ingest`, `lw lint --fix` and the stage.open
// agent tool share. Where OpenChangeset refuses when changesets/open/ is
// non-empty (the strict verb, still pinned by TestOneOpenChangeset — `lw
// revert` and `stage import` keep it), OpenOrJoin JOINS the open changeset:
// the TUI ask turn has worked that way since 009 (internal/ui/ask
// resolveTurnChangeset), and 019 makes joining the rule for every staging
// verb, so parallel work streams into one reviewable changeset instead of
// bricking on "stage: a changeset is already open".
//
// A verb that joined must never destroy the other work in the changeset:
// its failure paths drop only ITS OWN ops (DropOps, 019's scoped rollback;
// the drop graph itself is drop_cascade.go from 030), never Reject.
package stage

import (
	"fmt"
	"os"
)

// maxIntentRunes bounds a changeset's Intent after a join appends to it, so
// `lw status` and `lw log` keep rendering a readable one-liner no matter
// how many verbs joined.
const maxIntentRunes = 200

// OpenOrJoin opens a changeset like OpenChangeset when none is open
// (joined == false). When one is open it returns that changeset with
// joined == true, appends " + <intent>" to its Intent (the whole Intent
// bounded to 200 runes; overflow ends in "…"), persists, and journals
// changeset_joined. The author of a joined changeset is unchanged.
//
// Contract (019): the join is the writerOpen pattern (A-803) — writeMu for
// the whole body, a private copy mutated, the cache published only by the
// persist — so a failed join publishes nothing. A journal failure after
// that persist keeps the joined intent in cache and disk alike and still
// reports, the same shape Append's op_proposed failure takes. The Author
// argument names the JOINER: it is the changeset_joined event's actor, and
// the changeset's own Author is left exactly as the original opener set it.
func (e *Engine) OpenOrJoin(intent string, a Author) (cs *Changeset, joined bool, err error) {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	openDir := e.changesetOpenDir()
	entries, err := os.ReadDir(openDir)
	if err != nil {
		return nil, false, fmt.Errorf("stage: open changeset: %w", err)
	}
	if len(entries) == 0 {
		c, err := e.openChangesetWriteLocked(intent, a)
		return c, false, err
	}

	c, err := e.writerOpen()
	if err != nil {
		return nil, false, fmt.Errorf("stage: join changeset: %w", err)
	}
	c.Intent = joinIntents(c.Intent, intent)
	if err := e.persistAndPublish(c); err != nil {
		return nil, false, fmt.Errorf("stage: join changeset: %w", err)
	}
	if err := e.appendJournal(Event{
		TS:        e.now().UTC(),
		Kind:      EvChangesetJoined,
		Changeset: c.ID,
		Actor:     a,
		Message:   intent,
	}); err != nil {
		return nil, false, fmt.Errorf("stage: join changeset: %w", err)
	}
	return c.clone(), true, nil
}

// joinIntents appends the joining verb's intent to the open changeset's
// existing one as " + <intent>", the whole result bounded to
// maxIntentRunes runes with a trailing "…" marking the cut — the same
// elision convention the ask pane's intent uses, so `lw log` never shows a
// truncated middle without an indicator.
func joinIntents(existing, join string) string {
	combined := existing + " + " + join
	r := []rune(combined)
	if len(r) <= maxIntentRunes {
		return combined
	}
	if maxIntentRunes <= 1 {
		return "…"
	}
	return string(r[:maxIntentRunes-1]) + "…"
}
