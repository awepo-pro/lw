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
)

// queryPromptPrefix frames the user's question so the model treats the turn as
// read-only. This is advisory, not a technical wall — internal/tools'
// Registry (frozen, not owned by this subtask) has no filtered-definitions
// constructor, and every stage.* tool it registers uses the same real
// *stage.Engine the loop's own context needs (backbone §9's C-104 pinned
// instruction: "assemble the deps exactly as cmd_mcp.go does", which rules
// out handing the registry a different, crippled Engine). The hard
// guarantee cmdQuery actually enforces is structural, below: any
// changeset that exists when the turn ends is rejected before cmdQuery
// returns, so "no changeset" holds regardless of what the model attempts.
const queryPromptPrefix = "Answer the following question about the vault, citing the wiki pages you draw from by path. This is a read-only query: do not open a changeset or propose any change.\n\nQuestion: "

// cmdQuery asks the curator agent a one-shot, read-only question over the
// vault: no changeset is opened, and none is left behind even if the
// model attempts to stage something (backbone §13, /PLAN.md §11.3's
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

	sendErr := runAgentTurn(context.Background(), ag, sess.ID, queryPromptPrefix+question, os.Stdout)
	fmt.Println()

	// Enforce "no changeset" structurally: if the model called stage.open
	// (or any tool that opens one implicitly) despite the prompt above,
	// reject it immediately rather than leaving query's one invariant
	// dependent on the model's good behaviour. dispatch already prefixes
	// every returned error with "lw: query: ", so nothing here repeats
	// that prefix itself.
	if cs, curErr := e.Current(); curErr == nil {
		reason := "lw query must not stage changes; the agent attempted to during a read-only turn"
		if rejErr := e.Reject(reason); rejErr != nil {
			return fmt.Errorf("reject unexpected changeset %s: %w", cs.ID, rejErr)
		}
		if sendErr == nil {
			return fmt.Errorf("agent attempted to stage changeset %s; rejected", cs.ID)
		}
	}

	if sendErr != nil {
		return fmt.Errorf("agent turn: %w", sendErr)
	}
	return nil
}

// memSessionStore is a purely in-process agent.SessionStore for `lw
// query`: it never touches .llmwiki/changesets, so an ephemeral query
// session cannot collide with OpenChangeset's "is changesets/open/ empty"
// check and leaves no residue on disk once the process exits — there is,
// by design, no changeset for it to be bound to (/PLAN.md §11.3).
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
