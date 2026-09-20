package main

// lw session show (005 contract §4): one session's transcript, either the
// session.ndjson bytes verbatim (--json) or rendered for reading — the
// question and every role's turn under a rule line, the thinking folded
// unless --thinking, tool arguments and results in full, and the answers
// through the same fragment renderer the TUI's Ask panel draws with, so a
// transcript reads exactly as the live turn did.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui/markdown"
)

// cmdSessionShow implements `lw session show [<id>] [--plain] [--json]
// [--thinking]`. Users put flags on either side of the id, and Go's flag
// package stops at the first positional, so the parse runs twice: once to
// reach the positional, once over whatever followed it. More than one
// positional is a usage error.
func cmdSessionShow(args []string) error {
	fs := flag.NewFlagSet("session show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	plain := fs.Bool("plain", false, "strip every escape sequence, even on a terminal")
	jsonOut := fs.Bool("json", false, "emit session.ndjson's bytes verbatim")
	thinking := fs.Bool("thinking", false, "show the reasoning that is folded by default")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	positional := fs.Args()
	if len(positional) > 0 {
		if err := fs.Parse(positional[1:]); err != nil {
			return &exitError{code: 2}
		}
		if fs.NArg() > 0 {
			fmt.Fprintf(os.Stderr, "lw session: unexpected argument %q\n", fs.Arg(0))
			return &exitError{code: 2}
		}
	}
	if *plain && *jsonOut {
		fmt.Fprintln(os.Stderr, "lw session: --plain and --json are mutually exclusive")
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
	id := ""
	if len(positional) > 0 {
		id = positional[0]
	}
	entry, err := resolveSession(sessions, id)
	if err != nil {
		return err
	}

	if *jsonOut {
		// Verbatim means verbatim: the file's own bytes, unknown-to-this-
		// version fields included. Nothing is decoded, so nothing is lost.
		b, err := os.ReadFile(entry.path)
		if err != nil {
			return fmt.Errorf("session: read %s: %w", entry.id, err)
		}
		_, err = os.Stdout.Write(b)
		return err
	}
	return renderSession(os.Stdout, entry, sessionRenderFlags{plain: *plain, thinking: *thinking})
}

// sessionRenderFlags carries the two mode flags the rendered path reads.
// --thinking is accepted with --json and ignored there: the JSON already
// holds everything.
type sessionRenderFlags struct {
	plain    bool
	thinking bool
}

// renderSession writes the human transcript: a one-line header, then one
// rule/body block per record shown, in file order.
//
//	session <full-id> · <state> · <started> · <N> records
//	<blank>
//	── <rule title> ────
//	<blank>
//	<body lines>
//	<blank>
//
// Terminal facts come from renderOptions, exactly as lw diff --render
// resolves them; --plain forces escape-free output even on a terminal.
func renderSession(w io.Writer, entry sessionEntry, flags sessionRenderFlags) error {
	b, err := os.ReadFile(entry.path)
	if err != nil {
		return fmt.Errorf("session: read %s: %w", entry.id, err)
	}
	recs, decErr := decodeSessionRecords(b)
	if decErr != nil {
		// Unlike list, which flags the row "?" and moves on, show would
		// silently print a partial transcript as if it were whole. Fail
		// instead and name the record that tore.
		return fmt.Errorf("session %s: %v", entry.id, decErr)
	}
	o, err := renderOptions()
	if err != nil {
		return err
	}
	renderer := markdown.NewRenderer()
	plain := flags.plain || !o.tty

	fmt.Fprintf(w, "session %s · %s · %s · %d %s\n",
		entry.id, entry.state, formatSessionTime(entry.started), len(recs), plural(len(recs), "record", "records"))
	fmt.Fprintln(w)
	for i := range recs {
		body, show, err := sessionRecordBody(&recs[i], renderer, o.width, o.style, plain, flags.thinking)
		if err != nil {
			return err
		}
		if !show {
			continue
		}
		fmt.Fprintln(w, diffRule(sessionRuleTitle(&recs[i]), o.width))
		fmt.Fprintln(w)
		for _, line := range body {
			fmt.Fprintln(w, line)
		}
		fmt.Fprintln(w)
	}
	return nil
}

// plural picks the count noun for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// sessionRuleTitle is a record's rule title: "you" for the user's turn,
// "assistant", "tool <name>" — "· staged" when the call is staged — and
// the bare role string for any other role a future writer may record.
// A record the Ask pane copied forward from the earlier conversation of
// the same pane (Carried, 009 contract §1) is named "· carried", so a
// transcript tells carried history from the turn's own records.
func sessionRuleTitle(r *agent.Record) string {
	var title string
	switch r.Role {
	case "user":
		title = "you"
	case "assistant":
		title = "assistant"
	case "tool":
		title = "tool " + r.Tool
		if r.Staged {
			title += " · staged"
		}
	default:
		title = r.Role
	}
	if r.Carried {
		title += " · carried"
	}
	return title
}

// sessionRecordBody renders one record's body lines and reports whether
// the record is shown at all. The one silent shape is an assistant record
// with neither reasoning nor text; every other record prints its rule.
func sessionRecordBody(r *agent.Record, renderer *markdown.Renderer, width int, style markdown.Style, plain, thinking bool) (body []string, show bool, err error) {
	switch r.Role {
	case "assistant":
		return assistantRecordBody(r, renderer, width, style, plain, thinking)
	case "tool":
		return toolRecordBody(r), true, nil
	default:
		// The user's words, and any other role's: verbatim, not rendered,
		// not indented.
		return contentLines(r.Content), true, nil
	}
}

// assistantRecordBody renders an assistant record: the reasoning first —
// folded to one line unless thinking is set — then, when the round also
// produced text, the answer through the shared fragment renderer, each
// line's trailing spaces trimmed exactly as renderDiff trims its pages.
func assistantRecordBody(r *agent.Record, renderer *markdown.Renderer, width int, style markdown.Style, plain, thinking bool) ([]string, bool, error) {
	reasoning := strings.TrimRight(r.Reasoning, "\n")
	if reasoning == "" && r.Content == "" {
		return nil, false, nil
	}

	var body []string
	printed := false
	if reasoning != "" {
		if thinking {
			body = append(body, "thinking:")
			for _, line := range strings.Split(reasoning, "\n") {
				body = append(body, "  "+line)
			}
		} else {
			n := strings.Count(reasoning, "\n") + 1
			body = append(body, fmt.Sprintf("(thinking: %d %s hidden; --thinking shows them)", n, plural(n, "line", "lines")))
		}
		printed = true
	}
	if r.Content != "" {
		if printed {
			body = append(body, "")
		}
		lines, err := renderer.RenderFragment([]byte(r.Content), markdown.Options{Width: width, Style: style, Plain: plain})
		if err != nil {
			return nil, false, fmt.Errorf("session: render answer: %w", err)
		}
		for _, line := range lines {
			body = append(body, strings.TrimRight(line, " "))
		}
	}
	return body, true, nil
}

// toolRecordBody renders a tool record: every top-level argument in source
// order, then the full result. Nothing is truncated — the user asked for
// the details, and a 10 KB result prints all 10 KB.
func toolRecordBody(r *agent.Record) []string {
	body := []string{"args:"}
	body = append(body, toolArgLines(r.Args)...)
	body = append(body, "result:")
	if result := strings.TrimRight(r.Result, "\n"); result != "" {
		for _, line := range strings.Split(result, "\n") {
			body = append(body, "  "+line)
		}
	} else {
		body = append(body, "  (empty)")
	}
	return body
}

// argPair is one top-level member of a JSON object's args, kept in source
// order — a map would sort or scramble it, and the order the agent sent is
// part of the record.
type argPair struct {
	key string
	str *string // the decoded value when it is a JSON string
	raw []byte  // the value's own bytes for every other JSON type
}

// toolArgLines renders the args field: for a JSON object, one line per
// top-level key in source order — a short string value inline (decoded,
// unquoted), a multi-line string value one indented line per line, any
// other value compacted — and `(none)` for an empty object or an empty
// field. Args that are not a JSON object at all print raw, still indented,
// so an odd or historic shape is never silently dropped.
func toolArgLines(args string) []string {
	if args == "" {
		return []string{"  (none)"}
	}
	pairs, ok := decodeArgPairs(args)
	if !ok {
		return indentLines(args, "  ")
	}
	if len(pairs) == 0 {
		return []string{"  (none)"}
	}
	body := make([]string, 0, len(pairs))
	for _, p := range pairs {
		switch {
		case p.str != nil && !strings.Contains(*p.str, "\n"):
			body = append(body, "  "+p.key+": "+*p.str)
		case p.str != nil:
			body = append(body, "  "+p.key+":")
			body = append(body, indentLines(*p.str, "    ")...)
		default:
			var compacted bytes.Buffer
			_ = json.Compact(&compacted, p.raw) // raw decoded whole above; compaction cannot fail
			body = append(body, "  "+p.key+": "+compacted.String())
		}
	}
	return body
}

// decodeArgPairs walks args as a single JSON object with a Decoder and
// returns its top-level members in source order. ok is false when args is
// not exactly one JSON object — a different type, a torn tail, or trailing
// bytes — and the caller falls back to printing the raw lines.
func decodeArgPairs(args string) ([]argPair, bool) {
	dec := json.NewDecoder(strings.NewReader(args))
	t, err := dec.Token()
	if err != nil {
		return nil, false
	}
	if d, isDelim := t.(json.Delim); !isDelim || d != '{' {
		return nil, false
	}
	var pairs []argPair
	for {
		t, err := dec.Token()
		if err != nil {
			return nil, false
		}
		if d, isDelim := t.(json.Delim); isDelim && d == '}' {
			break
		}
		key, isString := t.(string)
		if !isString {
			return nil, false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, false
		}
		p := argPair{key: key, raw: raw}
		var v any
		if err := json.Unmarshal(raw, &v); err == nil {
			if s, isStr := v.(string); isStr {
				p.str = &s
			}
		}
		pairs = append(pairs, p)
	}
	if _, err := dec.Token(); err != io.EOF { // bytes after the closing brace: not an object's text
		return nil, false
	}
	return pairs, true
}

// contentLines splits a record field into its body lines, ignoring one
// trailing newline — a field that ends with one has no empty last line.
func contentLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// indentLines prefixes each of s's lines with prefix.
func indentLines(s string, prefix string) []string {
	lines := contentLines(s)
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = prefix + line
	}
	return out
}
