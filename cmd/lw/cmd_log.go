package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
)

// cmdLog prints the journal, one line per event, oldest first (backbone
// §5.7 D-AU: Filter.Limit selects the most recent N and returns them
// oldest-first — "newest last" in this subtask's own words).
func cmdLog(args []string) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	rejected := fs.Bool("rejected", false, "show only changeset_rejected events")
	agent := fs.Bool("agent", false, "show only agent-authored events")
	page := fs.String("page", "", "show only events touching this vault path")
	since := fs.String("since", "", "show only events at or after this RFC3339 timestamp or YYYY-MM-DD date")
	limit := fs.Int("limit", 0, "show only the most recent N events (0 = unlimited)")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw log: unexpected argument %q\n", fs.Arg(0))
		return &exitError{code: 2}
	}

	f := stage.Filter{Path: *page, Limit: *limit}
	if *rejected {
		f.Kinds = []stage.EventKind{stage.EvChangesetRejected}
	}
	if *agent {
		f.ActorKind = "agent"
	}
	if *since != "" {
		t, err := parseLogSince(*since)
		if err != nil {
			fmt.Fprintf(os.Stderr, "lw log: --since: %v\n", err)
			return &exitError{code: 2}
		}
		f.Since = t
	}

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}

	e, err := stage.OpenEngine(root)
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}
	defer e.Close()

	evs, err := e.Journal().Query(f)
	if err != nil {
		return err
	}

	for _, ev := range evs {
		fmt.Println(formatLogLine(ev))
	}
	return nil
}

// parseLogSince parses --since as either a full RFC3339 timestamp or a
// bare YYYY-MM-DD date (midnight UTC).
func parseLogSince(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("%q is not RFC3339 or YYYY-MM-DD", s)
}

// formatLogLine renders one journal Event as a single deterministic line:
// its UTC RFC3339 timestamp and kind, then every other field it carries,
// each as "key=value", empty fields omitted.
func formatLogLine(e stage.Event) string {
	var b strings.Builder
	b.WriteString(e.TS.UTC().Format(time.RFC3339))
	b.WriteByte(' ')
	b.WriteString(string(e.Kind))
	if e.Changeset != "" {
		fmt.Fprintf(&b, " changeset=%s", e.Changeset)
	}
	if e.Op != "" {
		fmt.Fprintf(&b, " op=%s", e.Op)
	}
	if e.Hunk != "" {
		fmt.Fprintf(&b, " hunk=%s", e.Hunk)
	}
	if e.Commit != "" {
		fmt.Fprintf(&b, " commit=%s", e.Commit)
	}
	if e.Actor.Kind != "" {
		fmt.Fprintf(&b, " actor=%s", e.Actor.Kind)
	}
	if len(e.Paths) > 0 {
		fmt.Fprintf(&b, " paths=%s", strings.Join(e.Paths, ","))
	}
	if e.Message != "" {
		fmt.Fprintf(&b, " message=%q", e.Message)
	}
	if len(e.Data) > 0 {
		fmt.Fprintf(&b, " data=%s", string(e.Data))
	}
	return b.String()
}
