package main

// cmd_tool.go is `lw tool` (063): one tool call of an external agent's ingest,
// one process per call.
//
// Why it exists: after `lw ingest --prepare` (ingest_prepare.go) the curation is
// the external agent's, and a shell can only run a command at a time. The
// in-process agent loop keeps three kinds of state in memory between calls, and
// a fresh process per call would lose them: the 040 read log (stage.close
// refuses once while a chunk is unread), the 043 shrink confirmation
// (allow_shrink counts only after a refusal) and the order of calls (a
// changeset is read, changed and written back whole). So a call here
//
//   - takes an exclusive flock on <vault>/.llmwiki/tmp/tool.lock for its whole
//     length, so parallel calls (the agent is told it may) run one after another
//     and each reads the changeset the one before it left;
//   - rebuilds the first two from the changeset's own session.ndjson with
//     Registry.Replay — the live raws and the tool records so far — and makes
//     its one Call on that registry;
//   - appends its own record, the way the loop does, so the next call can
//     replay it and `lw session show` reads the whole conversation.
//
// A changeset that was not opened by --prepare — its session holds no prepare
// record — is refused: the guards above are rebuilt from records this command
// wrote, and a changeset without them would be a source of silent passes.
//
// A-063-2: the loop's cost guards (048 read budget, 051 repeat guard, 053 search
// budget, 039 round nudge) are not here; the external agent owns its loop. Every
// correctness guard — 040, 043, each stage.* validation, Engine.Append — is.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/tools"
)

// toolUsage is what a malformed `lw tool` invocation prints to stderr.
const toolUsage = `usage: lw tool [--vault <path>] <tool-name> [<json-args>]
       lw tool [--vault <path>] <tool-name> --from <file|->
       lw tool [--vault <path>] --list
`

// toolNow is the clock the records of `lw ingest --prepare` and `lw tool` are
// stamped with (UTC at the call): a seam, like noteNow, so no logic reads the
// wall clock directly.
var toolNow = time.Now

// toolLockName is the lock file inside <vault>/.llmwiki/tmp. That directory is
// per PC and outside the vault's on-disk format (TestFormatFingerprint), like
// the journal lock beside it.
const toolLockName = "tool.lock"

// toolLockWait is how long a call waits for the call holding the lock. A call
// holds it for a tool's length — milliseconds, a lint pass for stage.close — so
// a wait of minutes means the holder is wedged, and an error is better than an
// agent whose shell hangs. (The kernel drops a flock when its process dies, so
// a crashed holder never blocks anyone.) A var so a test can shorten it.
var toolLockWait = 2 * time.Minute

// toolLockPoll is how often a waiting call tries the lock again.
const toolLockPoll = 5 * time.Millisecond

// cmdTool runs one tool of the open, prepared changeset for an external agent.
// Exit codes: 0 the tool answered, 1 the tool refused (its text is on stdout) or
// an engine error (on stderr), 2 a usage error or the gate.
func cmdTool(args []string) error {
	fs := flag.NewFlagSet("tool", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	from := fs.String("from", "", "read the JSON arguments from a file, or from standard input with -")
	list := fs.Bool("list", false, "print every tool lw tool offers, with its JSON schema, and exit")
	pos, err := parseIngestSources(fs, args)
	if err != nil {
		return &exitError{code: 2}
	}
	fromSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "from" {
			fromSet = true
		}
	})
	usage := func() error {
		fmt.Fprint(os.Stderr, toolUsage)
		return &exitError{code: 2}
	}

	if *list {
		if len(pos) > 0 || fromSet {
			return usage()
		}
		return toolList(*vaultPath)
	}
	if len(pos) == 0 || len(pos) > 2 {
		return usage()
	}

	// The name and the arguments are checked before the vault is read, so a
	// mistake in them reads the same whatever the vault holds.
	name := pos[0]
	canonical := name // a name with a dot is canonical already (C-063-1)
	if !strings.Contains(name, ".") {
		canonical = tools.CanonicalName(name)
	}
	if !slices.Contains(tools.IngestToolNames(), canonical) {
		fmt.Fprintf(os.Stderr, "lw: tool: %s is not offered for ingest (lw tool --list shows the tools)\n", name)
		return &exitError{code: 2}
	}
	callArgs, err := toolCallArgs(pos[1:], fromSet, *from)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lw: tool: %v\n", err)
		return &exitError{code: 2}
	}

	root, err := writableVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	initLoggingAt(root)
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// The lock comes before the engine: an engine caches the changeset it first
	// reads, and one opened while another call was still writing would answer
	// from a changeset that call had already changed.
	release, err := lockTool(root)
	if err != nil {
		return err
	}
	defer release()
	e, err := openVaultEngine(root, nil)
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}
	defer e.Close()

	return runToolCall(e, cfg, canonical, callArgs)
}

// toolCallArgs resolves the call's JSON arguments from the positional argument
// or --from (a file, or - for standard input), compacted: none given is {}; both
// given, or text that is not one JSON value, is an error cmdTool reports as a
// usage error. The tool decides whether the value is the object it expects.
func toolCallArgs(rest []string, fromSet bool, from string) (string, error) {
	var raw []byte
	switch {
	case len(rest) > 0 && fromSet:
		return "", errors.New("give the arguments as <json-args> or with --from, not both")
	case len(rest) > 0:
		raw = []byte(rest[0])
	case fromSet && from == "":
		return "", errors.New("--from needs a file, or - for standard input")
	case fromSet:
		var err error
		if from == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(from)
		}
		if err != nil {
			return "", fmt.Errorf("read %s: %w", from, err)
		}
	default:
		return "{}", nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return "", fmt.Errorf("arguments are not valid JSON: %v", err)
	}
	return compact.String(), nil
}

// toolListing is one entry of `lw tool --list`.
type toolListing struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// toolList prints every tool `lw tool` offers — tools.IngestToolNames, so the
// list and the gate cannot disagree — as a JSON array: name, description and
// input schema. It reads the schemas off a registry that has no vault behind it
// and opens no engine, so it needs a vault only to be run from one, and writes
// nothing. The encoder does not escape HTML: descriptions say "<" and "&", and
// an agent reads the text as it is.
func toolList(vaultPath string) error {
	root, err := findVaultRoot(vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root) // read-only: join the trail, never create it
	reg := tools.NewRegistry(tools.Deps{})
	var out []toolListing
	for _, name := range tools.IngestToolNames() {
		t, ok := reg.Get(name)
		if !ok {
			return fmt.Errorf("tool %s is offered but not registered", name)
		}
		out = append(out, toolListing{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// lockTool takes the exclusive flock on <root>/.llmwiki/tmp/tool.lock, waiting
// up to toolLockWait for the call that holds it, and returns the function that
// lets it go. The lock is on a file of its own, not on the changeset or the
// journal, which other verbs replace or rewrite — a lock on a replaced file
// excludes nobody. flock conflicts between separately opened descriptors even
// inside one process, so concurrent calls in a test are as exclusive as
// concurrent processes.
func lockTool(root string) (release func(), err error) {
	dir := filepath.Join(root, stateDirName, "tmp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("tool lock: %w", err)
	}
	path := filepath.Join(dir, toolLockName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("tool lock: %w", err)
	}
	deadline := time.Now().Add(toolLockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			var once sync.Once
			return func() {
				once.Do(func() {
					syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
					f.Close()
				})
			}, nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			if time.Now().After(deadline) {
				f.Close()
				return nil, fmt.Errorf("another lw tool call holds %s (waited %s); if none is running, delete the file", path, toolLockWait)
			}
			time.Sleep(toolLockPoll)
		default:
			f.Close()
			return nil, fmt.Errorf("tool lock: %w", err)
		}
	}
}

// runToolCall is the body of a call, under the lock: the gate, the replay, the
// one Call, the record and the output.
func runToolCall(e *stage.Engine, cfg *config.Config, canonical, callArgs string) error {
	root := e.Vault().Root()
	cs, err := e.Current()
	if err != nil {
		if errors.Is(err, stage.ErrNoChangeset) {
			fmt.Fprintln(os.Stderr, "lw: tool: no open changeset — start with lw ingest --prepare <source>")
			return &exitError{code: 2}
		}
		return fmt.Errorf("read changeset: %w", err)
	}
	notPrepared := func() error {
		fmt.Fprintf(os.Stderr, "lw: tool: changeset %s was not opened by lw ingest --prepare\n", cs.ID)
		return &exitError{code: 2}
	}
	sessions := agent.NewFileSessions(root)
	if _, ok := sessionStateOf(root, cs.ID); !ok {
		return notPrepared()
	}
	sess, err := sessions.Get(cs.ID)
	if err != nil {
		return fmt.Errorf("read session %s: %w", cs.ID, err)
	}

	// A record the Ask pane copied in from an earlier conversation (Carried)
	// happened in another changeset: it neither makes this one prepared nor
	// counts as a read of its raws.
	var (
		prepared  bool
		agentName string
		targets   []string // recompile raws any prepare record names, in order
		past      []tools.PastCall
	)
	for _, r := range sess.Records {
		if r.Carried {
			continue
		}
		switch r.Role {
		case "user":
			p, ok := parsePrepareRecord(r.Content)
			if !ok {
				continue
			}
			prepared, agentName = true, p.agent
			for _, ent := range p.entries {
				if ent.kind == "recompile" && !slices.Contains(targets, ent.path) {
					targets = append(targets, ent.path)
				}
			}
		case "tool":
			past = append(past, tools.PastCall{Tool: r.Tool, Args: r.Args, Result: r.Result})
		}
	}
	if !prepared {
		return notPrepared()
	}
	live, err := replayLive(e, cs, targets)
	if err != nil {
		return err
	}

	reg := tools.NewRegistry(externalToolDeps(e, agentExtractors(root, cfg), agentName, nil))
	reg.Replay(live, past)

	start := time.Now()
	res, callErr := reg.Call(context.Background(), canonical, json.RawMessage(callArgs))
	slog.Info("lw tool", "tool", canonical, "changeset", cs.ID, "is_error", callErr != nil || res.IsError, "dur_ms", msSince(start))

	recErr := sessions.Append(cs.ID, toolRecord(toolNow().UTC(), canonical, callArgs, res, callErr))
	if callErr != nil {
		if recErr != nil {
			return fmt.Errorf("%w (and recording the call in the session failed: %v)", callErr, recErr)
		}
		return callErr
	}
	fmt.Println(res.Content)
	if recErr != nil {
		return fmt.Errorf("record the call in the session: %w", recErr)
	}
	if res.IsError {
		return &exitError{code: 1}
	}
	if canonical == "stage.close" {
		final, err := e.Current()
		if err != nil {
			return fmt.Errorf("read back changeset %s: %w", cs.ID, err)
		}
		printIngestTail(e, final, false)
	}
	return nil
}

// replayLive is the list Registry.Replay is given: every raw the changeset
// stages (a live ingest_source op, at the chunk count of its staged body) and
// every recompile target a prepare record names (at the count of the committed
// body). A source whose op was dropped is not live, so the close does not hold
// the agent to it; a target the vault no longer holds has nothing raw.get could
// serve and is left out.
func replayLive(e *stage.Engine, cs *stage.Changeset, targets []string) ([]tools.LiveSource, error) {
	var live []tools.LiveSource
	seen := make(map[string]bool)
	for _, op := range cs.Live() {
		if op.Kind != stage.OpIngestSource || seen[op.Path] {
			continue
		}
		n, err := stagedRawChunks(e, op.Path)
		if err != nil {
			return nil, err
		}
		seen[op.Path] = true
		live = append(live, tools.LiveSource{Path: op.Path, Chunks: n})
	}
	for _, p := range targets {
		if seen[p] {
			continue
		}
		if r, ok := e.Vault().RawSource(p); ok {
			seen[p] = true
			live = append(live, tools.LiveSource{Path: p, Chunks: tools.RawChunkCount(r.Body)})
		}
	}
	return live, nil
}

// toolRecord is the session record of one tool call, the way the agent loop
// writes it (loop.go dispatchToolCall): the canonical name, the arguments as
// they were sent, the result text, and Staged for a stage.* call that came back
// without IsError — a close that went through counts, as it does in-process
// (C-063-3). An engine error has no result; its text is recorded in its place
// and the call is not staged.
func toolRecord(ts time.Time, tool, args string, res tools.Result, err error) agent.Record {
	rec := agent.Record{TS: ts, Role: "tool", Tool: tool, Args: args, Result: res.Content}
	if err != nil {
		rec.Result = err.Error()
		return rec
	}
	rec.Staged = !res.IsError && strings.HasPrefix(tool, "stage.")
	return rec
}
