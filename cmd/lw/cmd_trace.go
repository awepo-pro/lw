package main

// cmd_trace.go implements `lw trace` (038 T5): the list — one line per
// agent turn, newest first — and `trace show`, which plays one turn back in
// three modes: rendered for reading, --body for the exact request bytes one
// attempt POSTed, and --json for events.ndjson verbatim. The verb is
// read-only by construction, like lw session: it never opens the engine,
// and every byte it prints comes from trace's own readers.

import (
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/trace"
)

// cmdTrace dispatches lw trace: no subcommand lists the turns, `show`
// renders one. Anything else is list's args to misparse, so `lw trace -n 5`
// works without a subcommand keyword.
func cmdTrace(args []string) error {
	if len(args) > 0 && args[0] == "show" {
		return cmdTraceShow(args[1:])
	}
	return cmdTraceList(args)
}

// traceDir is the directory every verb's traces live under — the same join
// traceLoopConfig produces for a keeping config, so `lw trace` reads the
// directory tracing writes even while keep_mb = 0 has tracing itself off.
func traceDir(root string) string {
	return filepath.Join(root, stateDirName, "traces")
}

// cmdTraceList prints one line per turn, newest first, then the dir-level
// facts: how much the traces dir holds against its cap — the disk-usage
// fact 038 C-2 moved here out of lw doctor.
func cmdTraceList(args []string) error {
	fs := flag.NewFlagSet("trace", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	n := fs.Int("n", 20, "how many turns to list")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw trace: unexpected argument %q\n", fs.Arg(0))
		return &exitError{code: 2}
	}
	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root) // read-only: join the trail, never create it
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	return writeTraceList(os.Stdout, traceDir(root), cfg, *n)
}

// writeTraceList writes the list, the footer, and — when tracing is off —
// the one line that says so. A fresh vault's only output is the no-traces
// sentence: a footer over zero turns would be a report about nothing.
func writeTraceList(w io.Writer, dir string, cfg *config.Config, n int) error {
	sums, err := trace.List(dir)
	if err != nil {
		return err
	}
	if len(sums) == 0 {
		fmt.Fprintln(w, "no traces yet — every agent turn (ingest, ask, file, query, lint --fix) records one under .llmwiki/traces")
	} else {
		// -n is a count of rows, so 0 — and any negative, clamped to it —
		// lists nothing while the footer keeps the dir-level facts, the
		// same reading head -n 0 gives.
		if n < 0 {
			n = 0
		}
		if len(sums) > n {
			sums = sums[:n]
		}
		for _, s := range sums {
			fmt.Fprintln(w, traceListLine(s))
		}
		turns, bytes, err := trace.Size(dir)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "%d turn(s) · %.1f MB of %d MB cap · .llmwiki/traces\n",
			turns, float64(bytes)/(1<<20), cfg.TraceKeepBytes()>>20)
	}
	if cfg.TraceKeepBytes() == 0 {
		fmt.Fprintln(w, "tracing is off (trace.keep_mb = 0)")
	}
	return nil
}

// traceListLine renders one Summary as the list's row: id, verb, rounds,
// why the turn ended, its token bill, the wall time — and, when tools
// failed, how many. Columns are two spaces apart; the reason "" (a turn
// with no done event) reads "incomplete", because that is what it is.
func traceListLine(s trace.Summary) string {
	reason := s.Reason
	if reason == "" {
		reason = "incomplete"
	}
	line := strings.Join([]string{
		s.ID,
		fmt.Sprintf("%-6s", s.Verb),
		fmt.Sprintf("%d %s", s.Rounds, plural(s.Rounds, "round", "rounds")),
		fmt.Sprintf("%-10s", reason),
		traceTokens(s.InputTokens, s.CachedTokens, s.OutputTokens, s.ReasoningTokens, s.HasUsage),
		fmt.Sprintf("%.1fs", float64(s.WallMS)/1000),
	}, "  ")
	if s.ToolErrors > 0 {
		line += fmt.Sprintf("  %d tool error(s)", s.ToolErrors)
	}
	return line
}

// traceTokens renders the token bill both the list row and show's attempt
// line carry: "in <T> (<P>% cached)  out <T> (thinking <T>)", or "no usage"
// when the provider sent no usage object at all. A whole turn's line sums
// its responses; one attempt's line is that response's alone.
func traceTokens(in, cached, out, thinking int, hasUsage bool) string {
	if !hasUsage {
		return "no usage"
	}
	pct := 0
	if in > 0 {
		pct = int(math.Round(100 * float64(cached) / float64(in)))
	}
	return fmt.Sprintf("in %s (%d%% cached)  out %s (thinking %s)",
		traceK(in), pct, traceK(out), traceK(thinking))
}

// traceK renders a token count: plain below 1000, else one decimal in k.
func traceK(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// traceSize renders a byte count: whole bytes below 1 KiB, then KB, then
// MB — the unit every size in show's rendered form quotes.
func traceSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
}

// cmdTraceShow implements `lw trace show <ref> [--thinking] [--body R[.A]]
// [--json]`. Users put flags on either side of the ref, and Go's flag
// package stops at the first positional, so the parse runs twice — the same
// shape `session show` parses with.
func cmdTraceShow(args []string) error {
	fs := flag.NewFlagSet("trace show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	thinking := fs.Bool("thinking", false, "show the reasoning that is folded by default")
	bodyRef := fs.String("body", "", "print round R's (or attempt R.A's) exact request bytes instead of the rendered turn")
	jsonOut := fs.Bool("json", false, "emit events.ndjson's bytes verbatim")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	positional := fs.Args()
	ref := ""
	if len(positional) > 0 {
		ref = positional[0]
		if err := fs.Parse(positional[1:]); err != nil {
			return &exitError{code: 2}
		}
		if fs.NArg() > 0 {
			fmt.Fprintf(os.Stderr, "lw trace: unexpected argument %q\n", fs.Arg(0))
			return &exitError{code: 2}
		}
	}
	if ref == "" {
		// No ref names the newest turn, the way a bare `session show` names
		// the open session: most often the turn a person just finished.
		ref = "last"
	}
	if *jsonOut && *bodyRef != "" {
		fmt.Fprintln(os.Stderr, "lw trace: --json and --body are mutually exclusive")
		return &exitError{code: 2}
	}

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root) // read-only: join the trail, never create it
	dir := traceDir(root)

	// The resolver's error text is what a person who just typed the ref gets
	// back; a bare return lets dispatch deliver it with the house prefix.
	id, err := trace.Resolve(dir, ref)
	if err != nil {
		return err
	}

	if *jsonOut {
		// Verbatim means verbatim: the file's own bytes, unknown-to-this-
		// version fields included. Nothing is decoded, so nothing is lost.
		b, err := os.ReadFile(filepath.Join(dir, id, "events.ndjson"))
		if err != nil {
			return fmt.Errorf("trace: read %s: %w", id, err)
		}
		_, err = os.Stdout.Write(b)
		return err
	}

	if *bodyRef != "" {
		round, attempt, err := parseBodyRef(*bodyRef)
		if err != nil {
			fmt.Fprintf(os.Stderr, "lw trace: %v\n", err)
			return &exitError{code: 2}
		}
		b, err := trace.Body(dir, id, round, attempt)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(b)
		return err
	}

	t, err := trace.Load(dir, id)
	if err != nil {
		return err
	}
	return writeTraceShow(os.Stdout, t, *thinking)
}

// parseBodyRef parses --body's value: "1" is round 1's first attempt,
// "1.2" names the attempt. Anything else — a word, a zero round or
// attempt, more than two parts — is a usage error: rounds and attempts
// count from 1, so "1.0" or "0" would only fail at trace.Body, a turn
// later and with a resolver-shaped error a mistyped flag never earns.
func parseBodyRef(v string) (round, attempt int, err error) {
	round, attempt = 0, 1
	parts := strings.Split(v, ".")
	if len(parts) > 2 {
		return 0, 0, fmt.Errorf("--body wants round[.attempt], got %q", v)
	}
	round, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("--body wants round[.attempt], got %q", v)
	}
	if len(parts) == 2 {
		attempt, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, fmt.Errorf("--body wants round[.attempt], got %q", v)
		}
	}
	if round < 1 || attempt < 1 {
		return 0, 0, fmt.Errorf("--body wants round[.attempt], got %q", v)
	}
	return round, attempt, nil
}

// writeTraceShow renders one turn for reading: a header naming what ran,
// one block per attempt — its wire shape, its timings, its token bill, and
// what the model streamed back — and the done line. Thinking folds to a
// count unless asked for, the way session show folds it.
func writeTraceShow(w io.Writer, t *trace.Turn, thinking bool) error {
	fmt.Fprintf(w, "turn %s · %s · %s · %s @ %s · thinking %s · lw %s\n",
		t.ID, t.Meta.Verb, t.Meta.Session, t.Meta.Model, t.Meta.Server, t.Meta.Thinking, t.Meta.Version)

	for i := range t.Attempts {
		a := &t.Attempts[i]
		// An elision belongs to the round, not to one attempt of it: it is
		// printed once, ahead of the round's first attempt, where the
		// request it shrank is about to appear.
		if a.Attempt == 1 {
			if e, ok := elisionFor(t.Elisions, a.Round); ok {
				fmt.Fprintf(w, "  elided %d earlier result(s) (%s) to fit context_tokens\n",
					e.Count, traceSize(e.Bytes))
			}
		}
		label := fmt.Sprintf("round %d", a.Round)
		if a.Attempt > 1 {
			label += fmt.Sprintf(" (attempt %d)", a.Attempt)
		}
		fmt.Fprintf(w, "%s  sent %s (%d msgs, %d tools)", label, traceSize(a.Bytes), a.Messages, a.ToolDefs)
		if a.Response != nil {
			// Usage arrives as a pointer: a response the provider cut short
			// carries no usage object, and that must read "no usage", not
			// zero-shaped numbers that look measured.
			in, cached, out, reasoned, has := 0, 0, 0, 0, false
			if u := a.Response.Usage; u != nil {
				in, cached, out, reasoned, has = u.InputTokens, u.CachedTokens, u.OutputTokens, u.ReasoningTokens, true
			}
			fmt.Fprintf(w, "  first byte %.1fs  stream %.1fs  %s",
				secs(a.Response.FirstByteMS), secs(a.Response.StreamMS),
				traceTokens(in, cached, out, reasoned, has))
		}
		fmt.Fprintln(w)
		if a.Response == nil {
			continue // the request went out; nothing came back before the turn died
		}
		writeResponseDetail(w, a, thinking)
	}

	if t.Done != nil {
		fmt.Fprintf(w, "done: %s after %d round(s), %.1fs\n", t.Done.Reason, t.Done.Rounds, secs(t.Done.WallMS))
	} else {
		fmt.Fprintln(w, "done: no record — the turn did not finish")
	}
	return nil
}

// writeResponseDetail writes what one streamed answer said: the thinking
// (folded unless thinking), the prose, each tool call the model asked for,
// and the cut/error marks. Every line is indented under its attempt line.
func writeResponseDetail(w io.Writer, a *trace.Attempt, thinking bool) {
	r := a.Response
	// Emptiness is judged on content, not length: a stream that died after
	// only newlines has nothing to fold, count, or label.
	if strings.TrimSpace(r.Reasoning) != "" {
		if thinking {
			writeIndented(w, "thought: ", r.Reasoning)
		} else {
			fmt.Fprintf(w, "  thought: %d chars (--thinking to show)\n", len(r.Reasoning))
		}
	}
	if strings.TrimSpace(r.Text) != "" {
		writeIndented(w, "said: ", r.Text)
	}
	for _, c := range r.ToolCalls {
		size := len(c.Arguments) // a call never executed has only its own words
		errSuffix := ""
		for _, executed := range a.Calls {
			if executed.ID == c.ID {
				size = executed.ResultBytes
				if executed.IsError {
					errSuffix = " error"
				}
				break
			}
		}
		fmt.Fprintf(w, "  called: %s %s → %s%s\n", c.Name, cutRunes(c.Arguments, 120), traceSize(size), errSuffix)
	}
	if r.Cut {
		fmt.Fprintln(w, "  cut: the stream ended early")
	}
	if r.Error != "" {
		fmt.Fprintf(w, "  error: %s\n", r.Error)
	}
}

// writeIndented writes a labelled field whose text may be multi-line: the
// label and the first line on one line, each further line indented under
// it. A field holding only newlines — a stream that died before it said
// anything — has no line to label, so the whole line is omitted (038 T5).
func writeIndented(w io.Writer, label, text string) {
	lines := contentLines(text)
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s%s\n", label, lines[0])
	for _, line := range lines[1:] {
		fmt.Fprintf(w, "    %s\n", line)
	}
}

// elisionFor returns the elision recorded for a round, if the loop dropped
// anything in it.
func elisionFor(elisions []trace.Elision, round int) (trace.Elision, bool) {
	for _, e := range elisions {
		if e.Round == round {
			return e, true
		}
	}
	return trace.Elision{}, false
}

// cutRunes cuts s to at most max runes, appending an ellipsis when it cut —
// a tool argument the model wrote runs to kilobytes, and the rendered form
// is for reading; --json keeps every byte.
func cutRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	cut := 0
	for i := range s {
		if cut == max {
			return s[:i] + "…"
		}
		cut++
	}
	return s
}

// secs renders a millisecond duration the way every timing in show reads.
func secs(ms int64) float64 {
	return float64(ms) / 1000
}
