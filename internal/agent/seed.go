package agent

// seed.go is 009's conversation carry-over primitive. An Ask turn that
// stages nothing has its changeset auto-rejected and its session archived
// with it, so the conversation's next turn runs in a session with no
// records (sessions are keyed by changeset, backbone §9 C-102).
// SeedSession copies the previous session's conversation text into the
// fresh one; wiring it into the Ask pane is T-C's job.
//
// Since 057 the Ask pane no longer seeds: its conversation is one in-memory
// session (asksessions.go) whose history view IS this transform, through
// carryRecord. SeedSession stays — it is the transform applied to a file session,
// and seed_test.go pins what it carries.

import "fmt"

// SeedSession copies the conversation text of session from into session to,
// so a turn that runs in a fresh session still sees the Ask conversation it
// continues (009 plan §4.1). It appends, in order, one record per record of
// from whose Role is "user" or "assistant", whose Tool is empty and whose
// Content is non-empty — each as Record{TS: r.TS, Role: r.Role, Content:
// r.Content, Carried: true}. Tool calls, tool results, reasoning and Finish
// are never carried. from == "" is a no-op. It returns how many records it
// appended. Callers seed only a session they have just created.
func SeedSession(ss SessionStore, from, to string) (int, error) {
	if from == "" || from == to {
		return 0, nil
	}

	src, err := ss.Get(from)
	if err != nil {
		return 0, fmt.Errorf("agent: seed session %s from %s: %w", to, from, err)
	}

	n := 0
	for _, r := range src.Records {
		carried, ok := carryRecord(r)
		if !ok {
			continue
		}
		if err := ss.Append(to, carried); err != nil {
			return n, fmt.Errorf("agent: seed session %s from %s: %w", to, from, err)
		}
		n++
	}
	return n, nil
}

// carryRecord is the one definition of what a conversation carries from one
// session to the next: a record with Role "user" or "assistant", no Tool and
// non-empty Content, reduced to Record{TS, Role, Content, Carried: true}. ok is
// false for everything else — tool calls and results, reasoning-only records,
// Finish. SeedSession applies it to copy a session; since 057 the in-memory ask
// conversation's Get (asksessions.go) and AdoptTurn apply the same function, so
// the history a multi-turn ask sends the model cannot drift from what the
// seeding it replaced produced (TestAskSessionsViewEqualsSeed pins the parity).
func carryRecord(r Record) (carried Record, ok bool) {
	if (r.Role != "user" && r.Role != "assistant") || r.Tool != "" || r.Content == "" {
		return Record{}, false
	}
	return Record{TS: r.TS, Role: r.Role, Content: r.Content, Carried: true}, true
}
