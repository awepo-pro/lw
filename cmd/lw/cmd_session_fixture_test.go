package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/agent"
)

// Fixtures for the lw session tests. Every vault is built under t.TempDir
// with the files written directly — changeset.json and session.ndjson in
// the layout backbone §9 freezes — so the tests pin the on-disk shape, not
// the agent writer that normally produces it. No test reads the wall
// clock: every timestamp is fixed (00-conventions.md §3).

// testSchema is the SCHEMA.md each test vault gets, shaped like
// spec/fixtures/minimal's so that a test proving a command does NOT open
// the engine fails for the right reason when one does.
const testSchema = `# SCHEMA

## Domain

test — a small domain for the session command tests.

## Tags

- testing — running a trained habit to produce outputs.

## Conventions

- Filenames are lowercase-hyphen.md.
`

// newSessionVault returns a fresh vault root holding only SCHEMA.md.
// Sessions are added with writeSession / writeSessionNDJSON.
func newSessionVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SCHEMA.md"), []byte(testSchema), 0o644); err != nil {
		t.Fatalf("write SCHEMA.md: %v", err)
	}
	return root
}

// sessionRoot returns the changeset directory for one state and id.
func sessionRoot(root, state, id string) string {
	return filepath.Join(root, ".llmwiki", "changesets", state, id)
}

// writeSession creates .llmwiki/changesets/<state>/<id>/ containing
// changeset.json (unless csJSON is empty) and a session.ndjson holding
// records, one JSON object per line.
func writeSession(t *testing.T, root, state, id, csJSON string, records ...agent.Record) {
	t.Helper()
	var b strings.Builder
	for _, r := range records {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal record: %v", err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	writeSessionNDJSON(t, root, state, id, b.String())
	if csJSON != "" {
		if err := os.WriteFile(filepath.Join(sessionRoot(root, state, id), "changeset.json"), []byte(csJSON), 0o644); err != nil {
			t.Fatalf("write changeset.json: %v", err)
		}
	}
}

// writeSessionNDJSON writes id's session.ndjson as raw bytes, for fixtures
// the agent writer could not produce: a torn last line, or a record
// carrying a field this lw version does not know yet.
func writeSessionNDJSON(t *testing.T, root, state, id, ndjson string) {
	t.Helper()
	dir := sessionRoot(root, state, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.ndjson"), []byte(ndjson), 0o644); err != nil {
		t.Fatalf("write session.ndjson: %v", err)
	}
}

// changesetJSON is a minimal changeset.json body with a fixed opened_at.
func changesetJSON(id, openedAt string) string {
	return fmt.Sprintf(`{"id":%q,"intent":"test changeset","opened_at":%q,"ops":[],"checks":{}}`, id, openedAt)
}

// sessionTS parses an RFC3339 stamp the fixtures share.
func sessionTS(s string) time.Time {
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic("fixture timestamp " + s + ": " + err.Error())
	}
	return ts
}

// srec builds a user/assistant/system record with a fixed timestamp.
func srec(ts, role, content string) agent.Record {
	return agent.Record{TS: sessionTS(ts), Role: role, Content: content}
}

// stool builds one tool-call record with a fixed timestamp.
func stool(ts, tool, args, result string) agent.Record {
	return agent.Record{TS: sessionTS(ts), Role: "tool", Tool: tool, Args: args, Result: result}
}

// stagedTool builds one staged tool-call record — the shape a recorded
// stage.* call carries until the changeset is committed.
func stagedTool(ts, tool, args, result string) agent.Record {
	r := stool(ts, tool, args, result)
	r.Staged = true
	return r
}

// sessionSize returns the size of one fixture session's session.ndjson, so
// expected list output can carry real byte counts without hand-computing
// them.
func sessionSize(t *testing.T, root, state, id string) int64 {
	t.Helper()
	info, err := os.Stat(filepath.Join(sessionRoot(root, state, id), "session.ndjson"))
	if err != nil {
		t.Fatalf("stat session.ndjson: %v", err)
	}
	return info.Size()
}

// goldenSessionID is the id of the one fixture session the two goldens
// render.
const goldenSessionID = "cs-gold000000000001"

// writeGoldenFixtureVault builds the session the two show goldens render.
// Its nine records exercise every rendering rule the frozen show spec
// states: a two-line user turn; an assistant round with reasoning AND an
// answer; a reasoning-only round; an assistant round with both fields
// empty (which must print nothing at all); object args mixing a short
// string, a multi-line string, a number and a nested object; a staged
// call; non-object args; an empty result; and a system record.
func writeGoldenFixtureVault(t *testing.T) string {
	t.Helper()
	root := newSessionVault(t)
	writeSession(t, root, "open", goldenSessionID,
		changesetJSON(goldenSessionID, "2026-09-16T10:30:00Z"),
		srec("2026-09-16T10:30:00Z", "user",
			"review the network-namespace draft\nand tell me if the tone fits the wiki"),
		agent.Record{
			TS:   sessionTS("2026-09-16T10:30:05Z"),
			Role: "assistant",
			Reasoning: "The draft has the right structure.\n" +
				"The second section drifts into tutorial voice.\n" +
				"Suggest cutting it or moving it to the raw notes.",
			Content: "## Review\n\nThe structure works, but the second section reads like a tutorial.\n\nCut it, or move it to [[raw-notes]] where drafts belong.",
		},
		srec("2026-09-16T10:30:12Z", "assistant", ""),
		agent.Record{
			TS:        sessionTS("2026-09-16T10:30:13Z"),
			Role:      "assistant",
			Reasoning: "Deciding what to stage next before any text goes out.",
		},
		stool("2026-09-16T10:30:20Z", "stage.patch_page",
			`{"path":"wiki/concepts/namespaces.md","note":"two-line\nargument body","limit":3,"opts":{"dry_run":true,"sections":["intro"]}}`,
			"patched 1 section\n2 hunks proposed"),
		stagedTool("2026-09-16T10:30:25Z", "stage.commit",
			`{"message":"add the namespaces page"}`,
			"staged, not run"),
		stool("2026-09-16T10:30:30Z", "raw.search", `["namespaces", 5]`, "3 hits"),
		stool("2026-09-16T10:30:35Z", "stage.close", `{}`, ""),
		srec("2026-09-16T10:30:36Z", "system", "transcript opened for "+goldenSessionID),
	)
	return root
}
