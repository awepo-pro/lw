package main

// ingest_prepare.go is `lw ingest --prepare` (063): the deterministic half of
// an ingest, for an external agent to finish.
//
// Why it exists: the in-process ingest is welded to internal/llm, and the
// provider behind it can be unfunded or rate-limited while the user already
// runs a capable agent (Claude Code, Codex, Antigravity) with a shell. --prepare
// does everything an ingest does that needs no model — extract, dedupe, the
// limit check, stage.ingest_source for each kept source, and a brief — and
// stops. The agent curates the pages through `lw tool` (cmd_tool.go), one
// process per call. Nothing here constructs an agent or resolves an API key.
//
// What binds the two halves is the PREPARE RECORD: one user record in the
// changeset's session.ndjson, written here and read by `lw tool`. Its presence
// is the gate (a changeset someone else opened is not driven through `lw tool`),
// and its recompile lines name the committed raws the read-every-chunk guard
// must hold, since no ingest_source op does. prepareRecordText and
// parsePrepareRecord are the only code that writes or reads it.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/vault"
)

// prepareRecordHead opens the first line of a prepare record; the agent's name
// follows and a closing parenthesis ends the line.
const prepareRecordHead = "lw ingest --prepare (agent: "

// prepareEntryRe is one entry line of a prepare record: the kind, the raw's
// vault path and the chunk count raw.get serves for it.
var prepareEntryRe = regexp.MustCompile(`^(source|recompile): (.+) \(([0-9]+) chunk\(s\)\)$`)

// prepareEntry is one raw a prepare names. kind is "source" for a raw this
// invocation staged and "recompile" for a committed raw it hands the agent as it
// is (055).
type prepareEntry struct {
	kind   string
	path   string
	chunks int
}

// prepareRecord is what a prepare record says: which agent was briefed, and
// which raws, in the order they were listed.
type prepareRecord struct {
	agent   string
	entries []prepareEntry
}

// prepareRecordText renders p as the content of the session's user record:
//
//	lw ingest --prepare (agent: <name>)
//	source: raw/articles/a.md (2 chunk(s))
//	recompile: raw/articles/b.md (1 chunk(s))
//
// No trailing newline. The first line is exact, so a person reading
// `lw session show` knows what the record is.
func prepareRecordText(p prepareRecord) string {
	var b strings.Builder
	b.WriteString(prepareRecordHead + p.agent + ")")
	for _, e := range p.entries {
		fmt.Fprintf(&b, "\n%s: %s (%d chunk(s))", e.kind, e.path, e.chunks)
	}
	return b.String()
}

// parsePrepareRecord reads a prepare record back. It is strict on purpose: the
// first line and every entry line must be exactly what prepareRecordText writes,
// so a user message that merely mentions the command, or a half-edited record,
// is not taken for one — the gate in `lw tool` depends on that.
func parsePrepareRecord(s string) (prepareRecord, bool) {
	lines := strings.Split(s, "\n")
	rest, ok := strings.CutPrefix(lines[0], prepareRecordHead)
	if !ok {
		return prepareRecord{}, false
	}
	name, ok := strings.CutSuffix(rest, ")")
	if !ok || name == "" {
		return prepareRecord{}, false
	}
	p := prepareRecord{agent: name}
	for _, line := range lines[1:] {
		m := prepareEntryRe.FindStringSubmatch(line)
		if m == nil {
			return prepareRecord{}, false
		}
		n, err := strconv.Atoi(m[3])
		if err != nil {
			return prepareRecord{}, false
		}
		p.entries = append(p.entries, prepareEntry{kind: m[1], path: m[2], chunks: n})
	}
	return p, true
}

// validAgentName reports whether name can be an --agent value: something to
// show on one line, in the prepare record's first line and in the brief. An
// empty name, one with surrounding space and one with a control character
// (a newline would end the record's first line early) are not names.
func validAgentName(name string) bool {
	if name == "" || strings.TrimSpace(name) != name {
		return false
	}
	return !strings.ContainsFunc(name, unicode.IsControl)
}

// prepareHowToWork is the fixed "How to work" part of the brief (063 D5a): the
// one place the external agent learns the commands. It names the call shape, the
// schema listing, the reading rule with the chunk bound (kept equal to raw.get's
// own by TestPrepareBriefHowToWork), the staging tools, the way to finish, what a
// refusal is, how to abandon the work and who commits. There is no `lw reject`
// verb; discarding a changeset is `lw doctor --discard-changeset`.
const prepareHowToWork = "Every call is `lw tool <name> '<json>'`, or `lw tool <name> --from -` with a heredoc when the arguments are long, as page bodies are:\n" +
	"\n" +
	"    lw tool stage.create_page --from - <<'EOF'\n" +
	"    {\"path\": \"wiki/concepts/example.md\", \"title\": \"Example\", ...}\n" +
	"    EOF\n" +
	"\n" +
	"`lw tool --list` prints every tool with its JSON schema; read the schemas of the stage.* tools before the first call.\n" +
	"\n" +
	"1. Read every chunk of each source above with `raw.get` ({\"source\": \"<raw path>\", \"chunk\": N}). Several calls may run in parallel. A chunk is at most 16 000 characters: never truncate it with head or tail.\n" +
	"2. Look up related pages with `wiki.search` and read only those with `wiki.get`; do not survey the wiki.\n" +
	"3. Stage pages with `stage.create_page` and `stage.patch_page`, citing the raw path as `^[raw/...]`. Several calls may run in parallel.\n" +
	"4. Finish with `lw tool stage.close`. It refuses once while a chunk is unread: read it, or repeat the call to close anyway.\n" +
	"5. A refusal exits 1 and its text says how to fix the call; fix it and call again.\n" +
	"6. To abandon the work, run `lw doctor --discard-changeset`: the changeset moves to rejected/ and nothing is deleted.\n" +
	"7. You do not commit. The human reviews with `lw tui` or `lw diff` and commits.\n" +
	"\n" +
	"A `recompile:` source is already in the vault as a committed raw: read it with `raw.get` and cite its raw path; it needs no `stage.ingest_source`.\n"

// preparedSource is one raw this invocation staged, with what the brief says of
// it: the path stage.ingest_source landed it at, the chunk count raw.get serves,
// and the title and original source the extraction carried.
type preparedSource struct {
	path     string
	chunks   int
	title    string
	original string
}

// prepareBrief is everything the brief prints.
type prepareBrief struct {
	changeset   string
	joined      bool
	agent       string
	sources     []preparedSource
	targets     []recompileTarget
	cites       map[string][]string // rawCitations of the vault, for the targets' "cited by"
	orientation string              // the vault.orient tool's Content
}

// render is the curator brief, deterministic for its inputs: a title and the
// changeset line, then four parts in a fixed order — the sources, how to work,
// the curator rules (agent.IngestSystemPrompt, verbatim: the very rules the
// remote model would have been given) and the vault orientation (verbatim).
func (b prepareBrief) render() string {
	how := "opened"
	if b.joined {
		how = "joined"
	}
	var w strings.Builder
	fmt.Fprintf(&w, "# lw ingest --prepare — curator brief\nchangeset: %s (%s)   agent: %s\n\n", b.changeset, how, b.agent)
	w.WriteString("## Sources to compile\n\n")
	for _, s := range b.sources {
		fmt.Fprintf(&w, "- raw: %s\n  chunks: %d\n  title: %s\n  original source: %s\n", s.path, s.chunks, s.title, s.original)
	}
	for _, t := range b.targets {
		fmt.Fprintf(&w, "- recompile: %s\n  chunks: %d\n  title: %s\n  cited by: %s\n", t.raw, t.chunks, t.title, citedByText(b.cites[t.raw]))
	}
	w.WriteString("\n## How to work\n\n")
	writeBriefPart(&w, prepareHowToWork)
	w.WriteString("\n## Curator rules\n\n")
	writeBriefPart(&w, agent.IngestSystemPrompt())
	w.WriteString("\n## Vault orientation\n\n")
	writeBriefPart(&w, b.orientation)
	return w.String()
}

// writeBriefPart writes text as it is, adding the final newline it may lack so
// the next heading starts a line.
func writeBriefPart(w *strings.Builder, text string) {
	w.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		w.WriteString("\n")
	}
}

// externalToolDeps is the tools.Deps a registry serving an external agent is
// built over (063), for `lw ingest --prepare` and `lw tool` alike. It is not
// agentToolDeps: that function resolves the web search provider's key, and an
// external agent has its own web, so Search stays nil — web.search is not
// offered, the way an in-process ingest turn is not offered it (056) — and no
// key of any kind is touched. recompile is the committed raws the registry
// holds to the read-every-chunk rule; `lw tool` passes nil because Replay
// holds the targets a prepare record names.
func externalToolDeps(e *stage.Engine, ex extract.Extractor, agentName string, recompile []string) tools.Deps {
	return tools.Deps{
		Vault:     e.Vault(),
		Index:     e.Index(),
		Engine:    e,
		Extract:   ex,
		Author:    stage.Author{Kind: "agent", Model: agentName},
		Recompile: recompile,
	}
}

// stagedRawChunks is the number of chunks raw.get serves for the raw source the
// open changeset stages at path: the chunker's own count over the body of the
// staged file, the number stage.ingest_source reported when it staged it.
func stagedRawChunks(e *stage.Engine, path string) (int, error) {
	b, staged, err := e.StagedFile(path)
	if err != nil {
		return 0, fmt.Errorf("read staged %s: %w", path, err)
	}
	if !staged {
		return 0, fmt.Errorf("%s is not staged in the open changeset", path)
	}
	r, err := vault.ParseRawSource(path, b)
	if err != nil {
		return 0, fmt.Errorf("parse staged %s: %w", path, err)
	}
	return tools.RawChunkCount(r.Body), nil
}

// prepareInput is what cmdIngest hands prepareIngest once it has extracted,
// deduped and limit-checked the sources.
type prepareInput struct {
	e       *stage.Engine
	agent   string // --agent
	intent  string // the changeset intent, exactly as lw ingest builds it
	items   []ingestItem
	targets []recompileTarget
	extract extract.Extractor // the pre-extracted chain: scratch path → the Doc already read
	cites   map[string][]string
}

// prepareIngest opens or joins the changeset, stages every kept source through
// the same stage.ingest_source tool the in-process turn would have called,
// writes the prepare record and prints the brief. It is the --prepare half of
// cmdIngest's tail: no agent, no key, no model round.
//
// Each stage.ingest_source call is recorded in the session as the loop would
// have recorded it, so `lw session show` reads the same whichever way the
// ingest ran and `lw tool` replays the sources it staged. A call the tool
// refuses fails the whole command through ingestRollback — a changeset opened
// here is rejected, a joined one loses only the ops this command staged — with
// the tool's text in the error, because the source it names is the one to fix.
func prepareIngest(ctx context.Context, p prepareInput) error {
	e := p.e
	cs, joined, err := e.OpenOrJoin(p.intent, stage.Author{Kind: "agent", Model: p.agent})
	if err != nil {
		return fmt.Errorf("open changeset: %w", err)
	}
	n0 := len(cs.Ops)
	if joined {
		fmt.Fprintf(os.Stderr, "joined open changeset %s (%d op(s) already staged; they will be reviewed and committed together)\n",
			cs.ID, len(cs.Live()))
	}
	fail := func(err error) error { return ingestRollback(e, joined, cs.ID, n0, err) }

	sessions := agent.NewFileSessions(e.Vault().Root())
	sess, err := verbSession(sessions, cs.ID, joined)
	if err != nil {
		return fail(fmt.Errorf("create session: %w", err))
	}

	reg := tools.NewRegistry(externalToolDeps(e, p.extract, p.agent, recompilePaths(p.targets)))
	for _, it := range p.items {
		if cerr := ctx.Err(); cerr != nil {
			return fail(fmt.Errorf("interrupted: %w", cerr))
		}
		args := ingestSourceArgs(it)
		res, cerr := reg.Call(ctx, "stage.ingest_source", []byte(args))
		if aerr := sessions.Append(sess.ID, toolRecord(toolNow().UTC(), "stage.ingest_source", args, res, cerr)); aerr != nil {
			return fail(fmt.Errorf("record stage.ingest_source in the session: %w", aerr))
		}
		switch {
		case cerr != nil:
			return fail(fmt.Errorf("stage.ingest_source %s: %w", it.source, cerr))
		case res.IsError:
			return fail(fmt.Errorf("stage.ingest_source %s: %s", it.source, res.Content))
		}
	}

	// Which raws did the calls stage? Read off the changeset, not off the tool's
	// text: this command's own live ops, in the order they were staged.
	final, err := e.Current()
	if err != nil {
		return fail(fmt.Errorf("read back changeset %s: %w", cs.ID, err))
	}
	var own []stage.Op
	for i := n0; i < len(final.Ops); i++ {
		if op := final.Ops[i]; op.Kind == stage.OpIngestSource && op.State != stage.StateDropped && op.State != stage.StateRejected {
			own = append(own, op)
		}
	}
	if len(own) != len(p.items) {
		return fail(fmt.Errorf("staged %d raw source(s) for %d source(s)", len(own), len(p.items)))
	}

	rec := prepareRecord{agent: p.agent}
	brief := prepareBrief{changeset: cs.ID, joined: joined, agent: p.agent, targets: p.targets, cites: p.cites}
	for i, op := range own {
		n, cerr := stagedRawChunks(e, op.Path)
		if cerr != nil {
			return fail(cerr)
		}
		rec.entries = append(rec.entries, prepareEntry{kind: "source", path: op.Path, chunks: n})
		brief.sources = append(brief.sources, preparedSource{path: op.Path, chunks: n, title: p.items[i].title, original: p.items[i].source})
	}
	for _, t := range p.targets {
		rec.entries = append(rec.entries, prepareEntry{kind: "recompile", path: t.raw, chunks: t.chunks})
	}
	if aerr := sessions.Append(sess.ID, agent.Record{TS: toolNow().UTC(), Role: "user", Content: prepareRecordText(rec)}); aerr != nil {
		return fail(fmt.Errorf("record the prepare in the session: %w", aerr))
	}

	orient, cerr := reg.Call(ctx, "vault.orient", []byte("{}"))
	if cerr != nil {
		return fail(fmt.Errorf("vault.orient: %w", cerr))
	}
	if orient.IsError {
		return fail(fmt.Errorf("vault.orient: %s", orient.Content))
	}
	brief.orientation = orient.Content

	fmt.Print(brief.render())
	return nil
}

// ingestSourceArgs is the compact JSON arguments of the stage.ingest_source call
// that stages it: the scratch path under "uri" (C-063-2: the tool's schema key,
// decoded strictly) and the extraction's kind (a --kind outside the schema's
// enum is normalized to article by the tool, as in-process). Marshalling two
// strings cannot fail.
func ingestSourceArgs(it ingestItem) string {
	b, _ := json.Marshal(struct {
		URI  string `json:"uri"`
		Kind string `json:"kind,omitempty"`
	}{URI: it.path, Kind: it.kind})
	return string(b)
}
