package main

// tool_ask.go is `lw tool --ask` (068): single-tool execution for an external
// agent answering questions from the vault.
//
// Why it exists: `lw tool` (cmd_tool.go) gates every invocation on an
// ingest-prepared changeset (loadPrepared), takes an exclusive tool lock,
// replays the ingest session, and appends a record to session.ndjson. That is
// the stateful machinery of external ingest curation (063). An ask turn is
// read-only by invariant (I3): it needs neither lock nor session recording,
// does not require an open changeset, and restricts the tool registry to the
// read-only ask set (tools.AskToolNames()).
//
// Each call runs under trace.VerbQuery so that projected views (open changeset
// tag in wiki.search, unreviewed orient section, citable staged prefix in
// wiki.get) match what in-process `lw query` sees. When unreviewed content from
// an open changeset is read, a deterministic reminder line (067 D4) is printed
// to stderr.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

// toolAskList prints every tool offered for an external agent's ask
// (tools.AskToolNames) as `lw tool --list` prints the ingest set (068 D4.1).
func toolAskList(vaultPath string) error {
	return toolListOf(vaultPath, tools.AskToolNames())
}

// toolAskCall executes one read-only tool for an external agent's answer (068 D4.1).
// It opens the engine read-only, takes no lock, touches no session record,
// and evaluates under trace.VerbQuery.
func toolAskCall(vaultPath, canonical, callArgs string) error {
	root, err := findVaultRoot(vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root) // read-only: join the trail, never create it
	e, err := openVaultEngine(root, nil)
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}
	defer e.Close()

	reg := tools.NewRegistry(askToolDeps(e))
	ctx := trace.WithVerb(context.Background(), trace.VerbQuery)

	start := time.Now()
	res, callErr := reg.Call(ctx, canonical, json.RawMessage(callArgs))
	slog.Info("lw tool", "tool", canonical, "ask", true, "is_error", callErr != nil || res.IsError, "dur_ms", msSince(start))

	if callErr != nil {
		return callErr
	}
	fmt.Println(res.Content)
	if res.IsError {
		return &exitError{code: 1}
	}
	if tools.StagedRead(canonical, res.Content) {
		if cs, err := e.Current(); err == nil && cs != nil {
			fmt.Fprint(os.Stderr, stagedReadNoteLine(cs, waitingNoteNow()))
		}
	}
	return nil
}
