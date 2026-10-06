package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/trace"
)

// queryPromptPrefix frames the user's question so the model treats the turn as
// read-only. This is advisory, not a technical wall; since 039 there are two
// walls behind it. The `query` verb on the turn's ctx makes the agent loop
// send the ask prompt and advertise only the read tools, and refuse a call to
// any other tool without dispatching it (internal/agent, toolsFor). And the
// guard cmdQuery itself enforces, below, is structural: any changeset the turn
// opened — one open at the end that was not open at the start — is rejected
// before cmdQuery returns, so "this turn opened nothing" holds regardless of
// what the model attempts. Since 019 stage.open JOINS an already-open
// changeset instead of failing, so the second half of the guarantee is scoped
// to the op: ops the turn appends to a changeset that was already open when
// `lw query` started are dropped again before the command returns — the 019
// scoped-rollback rule, DropOps over only what the turn added, never Reject
// (the changeset is the curator's own review in progress, C-116). The guard
// stays even though the tool set no longer offers a stage.* verb: it is the
// backstop for a loop that is wired wrong, not a second copy of the first wall.
//
// The prefix used to ask the model to cite "the wiki pages you draw from by
// path". The system prompt said "Never narrate your sources" and the
// provenance markers carry the citations, so the two instructions
// contradicted each other, and a wiki page path is not evidence anyway — the
// ask prompt (internal/agent, askPromptBase) now owns the citation rules, and
// the prefix says only what is specific to this command: the question is
// read-only and its text follows.
const queryPromptPrefix = "Answer the following question about the vault. This is a read-only query: do not open a changeset or propose any change.\n\nQuestion: "

// cmdQuery asks the curator agent a one-shot, read-only question over the
// vault: no changeset is opened, and none is left behind even if the
// model attempts to stage something (backbone §13, /docs/design.md §11.3's
// "lw ingest and lw query use ephemeral sessions").
func cmdQuery(args []string) error {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, `usage: lw query "..."`)
		return &exitError{code: 2}
	}
	question := fs.Arg(0)

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	initLoggingAt(root)

	e, err := stage.OpenEngine(root)
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}
	defer e.Close()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	sessions := newMemSessionStore()
	ag, err := newAgent(e, cfg, sessions)
	if err != nil {
		return fmt.Errorf("construct agent: %w", err)
	}
	sess, err := sessions.Create("query")
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	// C-116: snapshot the open changeset BEFORE the turn — its id and its op
	// count. The guard below can only enforce query's invariant against work
	// this turn created, and the only way it can tell that work from a
	// changeset (and the ops already in it) that was already open is to have
	// looked before it started. `lw ingest` leaves a changeset open for human
	// review as a matter of course, so "a changeset is open" is a normal
	// state of a vault a curator queries mid-review — and rejecting it on the
	// way out silently demoted live work to changesets/rejected/ (C-116,
	// found live at G5).
	priorID, priorOpen, priorOps := "", false, 0
	if cs, err := e.Current(); err == nil {
		priorID, priorOpen, priorOps = cs.ID, true, len(cs.Ops)
	}

	// 038: the turn's verb rides the ctx (038 C-3) — query here, as ingest
	// and lint --fix set theirs around their own Send. 039: the same verb is
	// what puts the turn in ask mode — the ask prompt, the read tools only —
	// so it is load-bearing, not just a trace label.
	sendErr := runAgentTurn(trace.WithVerb(context.Background(), trace.VerbQuery), ag, sess.ID, queryPromptPrefix+question, os.Stdout)
	fmt.Println()

	// Enforce "no changes" structurally, whatever the model attempted:
	//
	//   - a changeset open at the end that was NOT open at the start is one
	//     the turn opened (OpenChangeset on an empty changesets/open/), and
	//     it is rejected immediately rather than leaving query's invariant
	//     dependent on the model's good behaviour;
	//   - a changeset with the same id before and after was already open —
	//     the turn's own tools cannot close one (stage.close only
	//     summarizes) — so the changeset is left exactly as the turn found
	//     it, and (019) only the ops past the snapshot are undone: since
	//     stage.open JOINS the open changeset, ops the turn appended there
	//     used to survive a query silently. They are dropped by id — the
	//     same scoped rollback a joined ingest takes — never Reject, which
	//     would take the curator's own work down with the turn's.
	//
	// dispatch already prefixes every returned error with "lw: query: ", so
	// nothing here repeats that prefix itself.
	if cs, curErr := e.Current(); curErr == nil {
		if !priorOpen || cs.ID != priorID {
			reason := "lw query must not stage changes; the agent attempted to during a read-only turn"
			if rejErr := e.Reject(reason); rejErr != nil {
				return fmt.Errorf("reject unexpected changeset %s: %w", cs.ID, rejErr)
			}
			if sendErr == nil {
				return fmt.Errorf("agent attempted to stage changeset %s; rejected", cs.ID)
			}
		} else {
			// The ops the turn appended, past the snapshot — the ones still
			// standing get dropped; ones the turn already dropped itself
			// stay dropped (a re-drop is a no-op, and naming them again
			// would only pad the count in the message below).
			var ids []string
			for i := priorOps; i < len(cs.Ops); i++ {
				if cs.Ops[i].State != stage.StateDropped {
					ids = append(ids, cs.Ops[i].ID)
				}
			}
			if len(ids) > 0 {
				if err := e.DropOps(ids); err != nil {
					return fmt.Errorf("agent attempted to stage %d op(s) into changeset %s during a read-only query; dropping them failed: %w", len(ids), cs.ID, err)
				}
				if sendErr == nil {
					return fmt.Errorf("agent attempted to stage %d op(s) into changeset %s during a read-only query; they were dropped", len(ids), cs.ID)
				}
			}
		}
	}

	if sendErr != nil {
		// U1: a truncated turn names the output budget and the fix; any
		// other error keeps the pre-008 wording (agentErrorHint).
		return agentErrorHint(sendErr, cfg.LLM.MaxTokens, false)
	}
	return nil
}

// memSessionStore is a purely in-process agent.SessionStore for `lw
// query`: it never touches .llmwiki/changesets, so an ephemeral query
// session cannot collide with OpenChangeset's "is changesets/open/ empty"
// check and leaves no residue on disk once the process exits — there is,
// by design, no changeset for it to be bound to (/docs/design.md §11.3).
type memSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*agent.Session
}

func newMemSessionStore() *memSessionStore {
	return &memSessionStore{sessions: make(map[string]*agent.Session)}
}

func (s *memSessionStore) Create(changesetID string) (*agent.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessions[changesetID]; exists {
		return nil, fmt.Errorf("cmd/lw: ephemeral session %q already exists", changesetID)
	}
	sess := &agent.Session{ID: changesetID, ChangesetID: changesetID, Started: time.Now().UTC()}
	s.sessions[changesetID] = sess
	return sess, nil
}

func (s *memSessionStore) Get(id string) (*agent.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, fmt.Errorf("cmd/lw: ephemeral session %q not found", id)
	}
	cp := *sess
	cp.Records = append([]agent.Record(nil), sess.Records...)
	return &cp, nil
}

func (s *memSessionStore) Append(id string, r agent.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return fmt.Errorf("cmd/lw: ephemeral session %q not found", id)
	}
	sess.Records = append(sess.Records, r)
	return nil
}

func (s *memSessionStore) Close(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		return fmt.Errorf("cmd/lw: ephemeral session %q not found", id)
	}
	delete(s.sessions, id)
	return nil
}

func (s *memSessionStore) List() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}
