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
//
// The brief is a function of the changeset's state, not of this invocation: the
// raws it stages, the recompile targets its prepare records name, the last
// record's agent. briefOf renders it for --prepare and for `lw tool --brief`
// alike, so an agent whose context compacted gets back exactly what it lost.

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

// prepareJoinedLine is the optional last line of a prepare record: this prepare
// joined a changeset that was already open. The brief says "joined" or "opened",
// and `lw tool --brief` cannot tell which from the changeset alone.
const prepareJoinedLine = "changeset: joined"

// prepareRecord is what a prepare record says: which agent was briefed, which
// raws, in the order they were listed, and whether it joined an open changeset.
type prepareRecord struct {
	agent   string
	joined  bool
	entries []prepareEntry
}

// prepareRecordText renders p as the content of the session's user record:
//
//	lw ingest --prepare (agent: <name>)
//	source: raw/articles/a.md (2 chunk(s))
//	recompile: raw/articles/b.md (1 chunk(s))
//	changeset: joined
//
// The last line is there only for a prepare that joined an open changeset. No
// trailing newline. The first line is exact, so a person reading
// `lw session show` knows what the record is.
func prepareRecordText(p prepareRecord) string {
	var b strings.Builder
	b.WriteString(prepareRecordHead + p.agent + ")")
	for _, e := range p.entries {
		fmt.Fprintf(&b, "\n%s: %s (%d chunk(s))", e.kind, e.path, e.chunks)
	}
	if p.joined {
		b.WriteString("\n" + prepareJoinedLine)
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
	for i, line := range lines[1:] {
		if line == prepareJoinedLine && i == len(lines)-2 {
			p.joined = true // only ever the last line
			continue
		}
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
// argument shapes of the tools an agent reaches for first (FIX2 F7: a live agent
// sent wiki.search {"query": …}; the key is "q" — TestBriefArgShapesMatchSchemas
// holds each example to its tool's real schema), the schema listing, how to get
// the brief back, the reading rule with the chunk bound (kept equal to raw.get's
// own by TestPrepareBriefHowToWork), the staging tools, the way to finish, what a
// refusal is, how to abandon the work, who commits and that the review gate is a
// convention the agent keeps. There is no `lw reject` verb; discarding a
// changeset is `lw doctor --discard-changeset`, which prints its own first line
// and then exits 1 without an API key, because its api_key check fails (FIX2 F2).
const prepareHowToWork = "Every call is `lw tool <name> '<json>'`, or `lw tool <name> --from -` with a heredoc when the arguments are long, as page bodies are:\n" +
	"\n" +
	"    lw tool stage.create_page --from - <<'EOF'\n" +
	"    {\"path\": \"wiki/concepts/example.md\", \"title\": \"Example\", ...}\n" +
	"    EOF\n" +
	"\n" +
	"The key names matter. The shape of the tools you will reach for first:\n" +
	"\n" +
	"    lw tool raw.get '{\"source\":\"raw/articles/x.md\",\"chunk\":1}'\n" +
	"    lw tool wiki.search '{\"q\":\"kv cache\",\"limit\":5}'\n" +
	"    lw tool wiki.get '{\"page\":\"wiki/concepts/kv-cache.md\"}'\n" +
	"    lw tool raw.list '{\"query\":\"kv\"}'\n" +
	"    lw tool stage.close '{}'\n" +
	"\n" +
	"`lw tool --list` prints every tool with its JSON schema; read the schemas of the stage.* tools before the first call. If you lose this brief, `lw tool --brief` prints it again.\n" +
	"\n" +
	"1. Read every chunk of each source above with `raw.get`. Several calls may run in parallel. A chunk is at most 16 000 characters: never truncate it with head or tail.\n" +
	"2. Look up related pages with `wiki.search` and read only those with `wiki.get`; do not survey the wiki.\n" +
	"3. Stage pages with `stage.create_page` and `stage.patch_page`, citing the raw path as `^[raw/...]`. Several calls may run in parallel.\n" +
	"4. Finish with `lw tool stage.close`. It refuses once while a chunk is unread: read it, or repeat the call to close anyway.\n" +
	"5. A refusal exits 1 and its text says how to fix the call; fix it and call again.\n" +
	"6. To abandon the work, run `lw doctor --discard-changeset`: its first line says `discarded open changeset …` and the changeset moves to rejected/ (nothing is deleted). With no API key configured it then exits 1 on its api_key check; that exit is expected, the discard has happened.\n" +
	"7. You do not commit. The human reviews with `lw tui` or `lw diff` and commits.\n" +
	"\n" +
	"You have a shell, but the review gate is a convention you keep: never edit vault files directly and never run `lw commit`. Everything goes through `lw tool stage.*`.\n" +
	"\n" +
	"A `recompile:` source is already in the vault as a committed raw: read it with `raw.get` and cite its raw path; it needs no `stage.ingest_source`.\n"

// preparedSource is one raw the changeset stages, with what the brief says of
// it: its path, the chunk count raw.get serves, and the title (the staged
// file's first level-1 heading) and original source (its source_url) it carries.
type preparedSource struct {
	path     string
	chunks   int
	title    string
	original string
}

// briefTarget is one recompile target (055) as the brief lists it: the committed
// raw, its chunk count and title, and the pages that cite it, already rendered.
type briefTarget struct {
	path    string
	chunks  int
	title   string
	citedBy string
}

// prepareBrief is everything the brief prints.
type prepareBrief struct {
	changeset   string
	joined      bool
	agent       string
	sources     []preparedSource
	targets     []briefTarget
	orientation string // the vault.orient tool's Content
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
		fmt.Fprintf(&w, "- recompile: %s\n  chunks: %d\n  title: %s\n  cited by: %s\n", t.path, t.chunks, t.title, t.citedBy)
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

// stagedSourceInfo reads the raw source the open changeset stages at path back
// from its staged file: the chunk count raw.get serves (the chunker's own count
// over the body, the number stage.ingest_source reported when it staged it), the
// title and the original source the brief lists. `lw ingest --prepare` and
// `lw tool --brief` both take a source's description from here, so they cannot
// describe it differently.
func stagedSourceInfo(e *stage.Engine, path string) (preparedSource, error) {
	b, staged, err := e.StagedFile(path)
	if err != nil {
		return preparedSource{}, fmt.Errorf("read staged %s: %w", path, err)
	}
	if !staged {
		return preparedSource{}, fmt.Errorf("%s is not staged in the open changeset", path)
	}
	r, err := vault.ParseRawSource(path, b)
	if err != nil {
		return preparedSource{}, fmt.Errorf("parse staged %s: %w", path, err)
	}
	return preparedSource{path: path, chunks: tools.RawChunkCount(r.Body), title: r.Title, original: r.SourceURL}, nil
}

// orientBrief fetches the vault orientation the brief carries: the vault.orient
// tool's answer. A var so a test can make it fail, the newAgent seam's shape.
var orientBrief = func(ctx context.Context, reg *tools.Registry) (tools.Result, error) {
	return reg.Call(ctx, "vault.orient", []byte("{}"))
}

// briefOf renders the curator brief of the open changeset cs, whose prepare
// records are prepares (oldest first, at least one): its live ingest_source ops
// in their order, then the recompile targets any record names, then the
// orientation. The changeset line and agent come from the LAST record — how the
// changeset was last prepared and for whom, which is the changeset's author for
// one this command opened. Everything else is read off the changeset and the
// vault, so the same state prints the same brief whoever asks.
func briefOf(ctx context.Context, e *stage.Engine, cs *stage.Changeset, prepares []prepareRecord) (string, error) {
	last := prepares[len(prepares)-1]
	b := prepareBrief{changeset: cs.ID, joined: last.joined, agent: last.agent}
	seen := make(map[string]bool)
	for _, op := range cs.Live() {
		if op.Kind != stage.OpIngestSource || op.Before != "" || seen[op.Path] {
			continue
		}
		seen[op.Path] = true
		src, err := stagedSourceInfo(e, op.Path)
		if err != nil {
			return "", err
		}
		b.sources = append(b.sources, src)
	}
	var cites map[string][]string
	for _, p := range prepares {
		for _, ent := range p.entries {
			if ent.kind != "recompile" || seen[ent.path] {
				continue
			}
			r, ok := e.Vault().RawSource(ent.path)
			if !ok {
				continue // no longer in the vault: raw.get could not serve it either
			}
			seen[ent.path] = true
			if cites == nil {
				cites = rawCitations(e.Vault())
			}
			b.targets = append(b.targets, briefTarget{path: ent.path, chunks: tools.RawChunkCount(r.Body), title: r.Title, citedBy: citedByText(cites[ent.path])})
		}
	}
	bdeps := externalToolDeps(e, nil, last.agent, nil)
	bdeps.OrientCommittedOnly = true
	orient, err := orientBrief(ctx, tools.NewRegistry(bdeps))
	if err != nil {
		return "", fmt.Errorf("vault.orient: %w", err)
	}
	if orient.IsError {
		return "", fmt.Errorf("vault.orient: %s", orient.Content)
	}
	b.orientation = orient.Content
	return b.render(), nil
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
}

// prepareIngest opens or joins the changeset, stages every kept source through
// the same stage.ingest_source tool the in-process turn would have called,
// builds the brief, writes the prepare record and prints the brief. It is the
// --prepare half of cmdIngest's tail: no agent, no key, no model round.
//
// It holds the tool lock (FIX2 F1) from before the changeset is opened or
// joined until the brief is built, the same lock every `lw tool` call takes.
// Staging reads the changeset, changes it and writes it back whole, so a prepare
// joining a changeset while an agent's parallel calls ran lost its own op and
// some of theirs. Extraction is already done by now, so a slow source holds
// nothing up. An in-process `lw ingest` or the TUI joining the same changeset is
// not serialized with it.
//
// Each stage.ingest_source call is recorded in the session as the loop would
// have recorded it, so `lw session show` reads the same whichever way the
// ingest ran and `lw tool` replays the sources it staged. A call the tool
// refuses fails the whole command through ingestRollback — a changeset opened
// here is rejected, a joined one loses only the ops this command staged — with
// the tool's text in the error, because the source it names is the one to fix.
//
// The prepare record is the last thing written, once the brief exists (F5): it
// is what `lw tool` takes as proof the changeset was prepared, so a prepare that
// fails building the brief must not leave one behind in a joined changeset.
func prepareIngest(ctx context.Context, p prepareInput) error {
	e := p.e
	release, err := lockTool(e.Vault().Root())
	if err != nil {
		return err
	}
	defer release()

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
	var prepares []prepareRecord // the records the session already holds, then this one
	for _, r := range sess.Records {
		if pr, ok := parsePrepareRecord(r.Content); ok && r.Role == "user" && !r.Carried {
			prepares = append(prepares, pr)
		}
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
	rec := prepareRecord{agent: p.agent, joined: joined}
	staged := 0
	for i := n0; i < len(final.Ops); i++ {
		op := final.Ops[i]
		if op.Kind != stage.OpIngestSource || op.Before != "" || op.State == stage.StateDropped || op.State == stage.StateRejected {
			continue
		}
		src, serr := stagedSourceInfo(e, op.Path)
		if serr != nil {
			return fail(serr)
		}
		rec.entries = append(rec.entries, prepareEntry{kind: "source", path: op.Path, chunks: src.chunks})
		staged++
	}
	if staged != len(p.items) {
		return fail(fmt.Errorf("staged %d raw source(s) for %d source(s)", staged, len(p.items)))
	}
	for _, t := range p.targets {
		rec.entries = append(rec.entries, prepareEntry{kind: "recompile", path: t.raw, chunks: t.chunks})
	}

	text, err := briefOf(ctx, e, final, append(prepares, rec))
	if err != nil {
		return fail(err)
	}
	if aerr := sessions.Append(sess.ID, agent.Record{TS: toolNow().UTC(), Role: "user", Content: prepareRecordText(rec)}); aerr != nil {
		return fail(fmt.Errorf("record the prepare in the session: %w", aerr))
	}
	fmt.Print(text)
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
