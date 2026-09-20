package main

// lw session (005 contract §4): `session list` and `session show`, reading
// a recorded agent transcript back out of the vault.
//
// The command is read-only by construction: it never builds a stage
// engine, because opening one mkdirs .llmwiki's directory tree and saves
// index.gob — writes a read command must not make (and on a vault with no
// .llmwiki yet, it would create the very directory it failed to find).
// This file therefore stats and reads the changeset directories directly.
// The one invariant that makes that safe against hostile input: an <id>
// argument typed by the user is only ever COMPARED against the ids
// discovery listed, never joined into a filesystem path — which is why
// `lw session show ../../etc` can only ever answer "no session matches".

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// sessionStates are the three changeset state directories, in backbone §9
// layout order. A session's state IS the name of the directory whose
// <id>/session.ndjson exists (C-102): the layout is frozen, so the
// directory's name is the state — nothing needs decoding to know it.
var sessionStates = []string{"open", "committed", "rejected"}

// sessionEntry is one session found on disk: everything list and show need
// except the records themselves.
type sessionEntry struct {
	id      string
	state   string
	started time.Time // zero when neither changeset.json nor the first record carries a time
	records int       // records decoded from session.ndjson
	decoded bool      // false when session.ndjson did not fully decode (a torn last line)
	size    int64     // session.ndjson's size in bytes
	path    string    // session.ndjson's location, derived only from discovery
}

// cmdSession dispatches lw session's two subcommands. No subcommand, or an
// unknown one, prints a short usage to stderr and exits 2.
func cmdSession(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "lw session: a subcommand is required")
		sessionUsage(os.Stderr)
		return &exitError{code: 2}
	}
	switch sub, rest := args[0], args[1:]; sub {
	case "list":
		return cmdSessionList(rest)
	case "show":
		return cmdSessionShow(rest)
	default:
		fmt.Fprintf(os.Stderr, "lw session: unknown subcommand %q\n", sub)
		sessionUsage(os.Stderr)
		return &exitError{code: 2}
	}
}

// sessionUsage prints the two-line subcommand summary.
func sessionUsage(w io.Writer) {
	fmt.Fprint(w, `usage: lw session list [--json]
       lw session show [<id>] [--plain] [--json] [--thinking]
`)
}

// cmdSessionList prints one row per session, newest first.
func cmdSessionList(args []string) error {
	fs := flag.NewFlagSet("session list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON instead of the table")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw session: unexpected argument %q\n", fs.Arg(0))
		return &exitError{code: 2}
	}

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root) // read-only: join the trail, never create it
	sessions, err := findSessions(root)
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeSessionListJSON(os.Stdout, sessions)
	}
	return writeSessionListText(os.Stdout, sessions)
}

// findSessions returns every session on the vault, newest first.
// Discovery goes through the agent package's read-only List — the same
// walk its SessionStore does — and then stats the three state directories
// per id, because the state is the name of the directory that holds the
// session, and List does not carry it.
func findSessions(root string) ([]sessionEntry, error) {
	ids, err := agent.NewFileSessions(root).List()
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	sessions := make([]sessionEntry, 0, len(ids))
	for _, id := range ids {
		state, ok := sessionStateOf(root, id)
		if !ok {
			continue // no session.ndjson under any state dir any more
		}
		e, err := loadSessionEntry(root, state, id)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, e)
	}
	sortSessions(sessions)
	return sessions, nil
}

// sessionStateOf stats the three state directories for id's session.ndjson
// and returns the name of the first one holding it, open first — the same
// order the agent store resolves with. id always comes from discovery,
// never from user input; see the file comment.
func sessionStateOf(root, id string) (string, bool) {
	for _, state := range sessionStates {
		if _, err := os.Stat(sessionNDJSONPath(root, state, id)); err == nil {
			return state, true
		}
	}
	return "", false
}

// sessionNDJSONPath is the frozen session.ndjson location (backbone §9,
// C-102): <root>/.llmwiki/changesets/<state>/<id>/session.ndjson. Only
// ever called with a state from sessionStates and an id discovery listed.
func sessionNDJSONPath(root, state, id string) string {
	return filepath.Join(root, ".llmwiki", "changesets", state, id, "session.ndjson")
}

// loadSessionEntry reads one session's facts: byte size, decoded record
// count, and start time — changeset.json's opened_at when it decodes to a
// real timestamp, else the first record's, else unknown.
func loadSessionEntry(root, state, id string) (sessionEntry, error) {
	p := sessionNDJSONPath(root, state, id)
	b, err := os.ReadFile(p)
	if err != nil {
		return sessionEntry{}, fmt.Errorf("session: read %s: %w", id, err)
	}
	recs, decErr := decodeSessionRecords(b)
	e := sessionEntry{
		id:      id,
		state:   state,
		records: len(recs),
		decoded: decErr == nil,
		size:    int64(len(b)),
		path:    p,
	}
	e.started = changesetOpenedAt(filepath.Join(filepath.Dir(p), "changeset.json"))
	if e.started.IsZero() && len(recs) > 0 {
		e.started = recs[0].TS
	}
	return e, nil
}

// changesetOpenedAt decodes a changeset.json and returns its OpenedAt, or
// the zero Time when the file is missing, will not decode, or carries no
// timestamp. A missing or broken changeset.json must not lose the session.
func changesetOpenedAt(p string) time.Time {
	b, err := os.ReadFile(p)
	if err != nil {
		return time.Time{}
	}
	var c stage.Changeset
	if err := json.Unmarshal(b, &c); err != nil {
		return time.Time{}
	}
	return c.OpenedAt
}

// decodeSessionRecords parses one agent.Record per non-blank line, the
// ndjson shape backbone §14 freezes for session.ndjson. A line that will
// not decode is a torn tail — the realistic outcome of a crash mid-append
// — so the records decoded before it are returned along with the error and
// the caller decides whether that is fatal (show) or a "?" cell (list).
func decodeSessionRecords(b []byte) ([]agent.Record, error) {
	var recs []agent.Record
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r agent.Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return recs, fmt.Errorf("decode record %d: %w", i+1, err)
		}
		recs = append(recs, r)
	}
	return recs, nil
}

// sortSessions orders newest first by start time, sessions with an unknown
// start last, ties broken by id ascending.
func sortSessions(s []sessionEntry) {
	sort.Slice(s, func(i, j int) bool {
		zi, zj := s[i].started.IsZero(), s[j].started.IsZero()
		if zi != zj {
			return zj // the non-zero time is newer, so it sorts first
		}
		if !zi && !s[i].started.Equal(s[j].started) {
			return s[i].started.After(s[j].started)
		}
		return s[i].id < s[j].id
	})
}

// writeSessionListText prints the table: a header row, then one row per
// session, through a two-space-padded tabwriter.
func writeSessionListText(w io.Writer, sessions []sessionEntry) error {
	if len(sessions) == 0 {
		fmt.Fprintln(w, "no sessions")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATE\tSTARTED\tRECORDS\tBYTES")
	for _, s := range sessions {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n",
			ui.ShortID(s.id), s.state, formatSessionTime(s.started), sessionCountCell(s), s.size)
	}
	return tw.Flush()
}

// sessionListRow is one --json row. Records is null exactly when the text
// table prints "?" — a session.ndjson that did not fully decode.
type sessionListRow struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Started string `json:"started"`
	Records *int   `json:"records"`
	Bytes   int64  `json:"bytes"`
}

// writeSessionListJSON prints every row as indented JSON, the full id
// included — the table's ShortID cell is for humans.
func writeSessionListJSON(w io.Writer, sessions []sessionEntry) error {
	rows := make([]sessionListRow, 0, len(sessions)) // non-nil, so an empty list prints []
	for _, s := range sessions {
		row := sessionListRow{
			ID:      s.id,
			State:   s.state,
			Started: formatSessionStartedJSON(s.started),
			Bytes:   s.size,
		}
		if s.decoded {
			n := s.records
			row.Records = &n
		}
		rows = append(rows, row)
	}
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// formatSessionTime renders a start time the way list and show agree on:
// whole-second UTC RFC3339, or "-" when unknown.
func formatSessionTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

// formatSessionStartedJSON is the JSON row's started: RFC3339 as in the
// table, but "" rather than "-" when unknown, so the field stays a
// timestamp-or-empty.
func formatSessionStartedJSON(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// sessionCountCell is the table's RECORDS cell: the decoded count, or "?"
// for a session.ndjson whose torn tail stopped a full decode. list never
// dies on one bad file.
func sessionCountCell(s sessionEntry) string {
	if !s.decoded {
		return "?"
	}
	return strconv.Itoa(s.records)
}

// resolveSession picks the session a show id argument names. An empty q
// means the open session. Otherwise q is matched against the ids
// findSessions listed — never joined into a path — with a missing "cs-"
// prepended, and an exactly-matching id beating any longer id it prefixes
// (a legacy short id can be a prefix of a 16-hex one).
func resolveSession(sessions []sessionEntry, q string) (sessionEntry, error) {
	if q == "" {
		var open []sessionEntry
		for _, s := range sessions {
			if s.state == "open" {
				open = append(open, s)
			}
		}
		switch len(open) {
		case 0:
			return sessionEntry{}, errors.New("no open session; lw session list shows every session")
		case 1:
			return open[0], nil
		default:
			ids := make([]string, len(open))
			for i, s := range open {
				ids[i] = s.id
			}
			return sessionEntry{}, fmt.Errorf("vault has %d open sessions, which should never happen (one changeset at a time): %s",
				len(open), strings.Join(ids, ", "))
		}
	}

	if !strings.HasPrefix(q, "cs-") {
		q = "cs-" + q
	}
	var matches []string
	byID := make(map[string]sessionEntry, len(sessions))
	for _, s := range sessions {
		byID[s.id] = s
		if s.id == q {
			return s, nil // an exact id wins even when it prefixes a longer one
		}
		if strings.HasPrefix(s.id, q) {
			matches = append(matches, s.id)
		}
	}
	switch len(matches) {
	case 0:
		return sessionEntry{}, fmt.Errorf("no session matches %q; lw session list shows every session", q)
	case 1:
		return byID[matches[0]], nil
	default:
		sort.Strings(matches)
		return sessionEntry{}, fmt.Errorf("ambiguous id prefix %q matches %s", q, strings.Join(matches, ", "))
	}
}
