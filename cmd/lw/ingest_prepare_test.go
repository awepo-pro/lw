package main

// ingest_prepare_test.go pins 063 S2's first half: `lw ingest --prepare`. The
// remote provider is unfunded or rate-limited while the user already runs an
// agent with a shell, so --prepare does the deterministic half of an ingest
// (extract, dedupe, limits, stage.ingest_source, a brief) and hands the
// curation to that agent, which drives it through `lw tool`. Every test runs
// the real run() dispatch against a copy of the minimal fixture; the
// newIngestAgent seam is swapped for a function that fails the test when it is
// called, because --prepare must never construct an agent or resolve a key.
// Permanent regression tests (D-10C).

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/vault"
)

const prepareBriefTitle = "# lw ingest --prepare — curator brief"

// prepHermetic pins config.Load to a temp config and removes every provider
// credential from the environment: a --prepare that reached for a key would
// have none to find.
func prepHermetic(t *testing.T) {
	t.Helper()
	maxTokens8192Env(t)
	for _, k := range []string{"DEEPSEEK_API_KEY", "GLM_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_AUTH_TOKEN", "TAVILY_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(k, "") // registers the restore
		os.Unsetenv(k)
	}
}

// prepNoAgent swaps newIngestAgent for a function that fails the test when it
// is called.
func prepNoAgent(t *testing.T) {
	t.Helper()
	orig := newIngestAgent
	newIngestAgent = func(*stage.Engine, *config.Config, agent.SessionStore, extract.Extractor, []string) (agent.Agent, error) {
		t.Error("lw ingest --prepare called newIngestAgent: it must construct no agent and resolve no key")
		return nil, errors.New("--prepare constructed an agent")
	}
	t.Cleanup(func() { newIngestAgent = orig })
}

// prepSource writes a markdown source whose body is about runes runes long —
// 16 000 runes make one raw.get chunk, so 20 000 make two.
func prepSource(t *testing.T, name, title string, runes int) string {
	t.Helper()
	body := strings.Repeat("Alpha beta gamma delta. ", runes/24+1)
	return writtenSource(t, name, "# "+title+"\n\n"+body+"\n")
}

// prepareRun runs `lw ingest --prepare --vault root <args>`.
func prepareRun(t *testing.T, root string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	full := append([]string{"ingest", "--prepare", "--vault", root}, args...)
	return captureRun(t, func() int { return run(full) })
}

// prepSession reads back the session of changeset id.
func prepSession(t *testing.T, root, id string) *agent.Session {
	t.Helper()
	s, err := agent.NewFileSessions(root).Get(id)
	if err != nil {
		t.Fatalf("read session %s: %v", id, err)
	}
	return s
}

// prepRecords returns every prepare record in s, oldest first.
func prepRecords(s *agent.Session) []prepareRecord {
	var out []prepareRecord
	for _, r := range s.Records {
		if r.Role != "user" {
			continue
		}
		if p, ok := parsePrepareRecord(r.Content); ok {
			out = append(out, p)
		}
	}
	return out
}

// prepToolRecords returns the session's tool records, oldest first.
func prepToolRecords(s *agent.Session) []agent.Record {
	var out []agent.Record
	for _, r := range s.Records {
		if r.Role == "tool" {
			out = append(out, r)
		}
	}
	return out
}

// prepLiveIngest returns the paths of cs's live ingest_source ops.
func prepLiveIngest(cs *stage.Changeset) []string {
	var out []string
	for _, op := range cs.Live() {
		if op.Kind == stage.OpIngestSource {
			out = append(out, op.Path)
		}
	}
	return out
}

// prepIndexesInOrder fails unless every needle occurs in s, each after the one
// before it.
func prepIndexesInOrder(t *testing.T, s string, needles ...string) {
	t.Helper()
	from := 0
	for _, n := range needles {
		i := strings.Index(s[from:], n)
		if i < 0 {
			t.Fatalf("%q is missing, or out of order, after offset %d in:\n%s", n, from, s)
		}
		from += i + len(n)
	}
}

// prepOrientation is what the vault.orient tool answers over root, the text the
// brief must carry verbatim.
func prepOrientation(t *testing.T, root string) string {
	t.Helper()
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer e.Close()
	res, err := tools.NewRegistry(tools.Deps{Vault: e.Vault(), Index: e.Index(), Engine: e}).Call(t.Context(), "vault.orient", nil)
	if err != nil || res.IsError {
		t.Fatalf("vault.orient: %+v, %v", res, err)
	}
	return res.Content
}

// TestPrepareNoKeyNoAgent is the headline: with no api key anywhere and a seam
// that fails the test if an agent is built, `lw ingest --prepare` exits 0,
// leaves an open changeset authored by agent/agy holding one live raw, records
// the ingest_source call and the prepare record in the session, and prints the
// brief — its five parts in order.
func TestPrepareNoKeyNoAgent(t *testing.T) {
	prepHermetic(t)
	prepNoAgent(t)
	root := testutil.CopyFixture(t, "minimal")
	src := prepSource(t, "long-source.md", "Long Source", 20000)

	stdout, stderr, code := prepareRun(t, root, src)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}

	cs := rcOpenChangeset(t, root)
	if cs.Author.Kind != "agent" || cs.Author.Model != "agy" {
		t.Errorf("author = %+v, want kind agent, model agy", cs.Author)
	}
	raws := prepLiveIngest(cs)
	if len(raws) != 1 || raws[0] != "raw/articles/long-source.md" {
		t.Fatalf("live ingest_source ops = %v, want [raw/articles/long-source.md]", raws)
	}
	if cs.Intent != "ingest "+src {
		t.Errorf("intent = %q, want %q", cs.Intent, "ingest "+src)
	}

	// The raw's provenance is the original file, not the scratch path the
	// extraction was staged at (C-123): the pre-extracted chain is in use.
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	b, staged, err := e.StagedFile(raws[0])
	if err != nil || !staged {
		t.Fatalf("StagedFile(%s): staged %v, err %v", raws[0], staged, err)
	}
	rs, err := vault.ParseRawSource(raws[0], b)
	if err != nil {
		t.Fatal(err)
	}
	if rs.SourceURL != src {
		t.Errorf("raw source_url = %q, want the original path %q", rs.SourceURL, src)
	}

	s := prepSession(t, root, cs.ID)
	calls := prepToolRecords(s)
	if len(calls) != 1 || calls[0].Tool != "stage.ingest_source" || !calls[0].Staged {
		t.Fatalf("tool records = %+v, want one staged stage.ingest_source", calls)
	}
	if !strings.Contains(calls[0].Result, raws[0]) || !strings.Contains(calls[0].Result, "2 chunk(s)") {
		t.Errorf("ingest_source result = %q, want it to name %s and 2 chunk(s)", calls[0].Result, raws[0])
	}
	recs := prepRecords(s)
	want := prepareRecord{agent: "agy", entries: []prepareEntry{{kind: "source", path: raws[0], chunks: 2}}}
	if len(recs) != 1 || !reflect.DeepEqual(recs[0], want) {
		t.Errorf("prepare records = %+v, want [%+v]", recs, want)
	}
	// The ingest call comes first, the prepare record after it.
	if n := len(s.Records); n != 2 || s.Records[0].Role != "tool" || s.Records[1].Role != "user" {
		t.Errorf("session records = %+v, want the tool record then the prepare record", s.Records)
	}

	// The scratch directory is gone: the call's uri was a path under it.
	if uri := calls[0].Args; !strings.Contains(uri, `"uri":"`) {
		t.Errorf("ingest_source args = %q, want a uri", uri)
	} else if _, err := os.Stat(filepath.Dir(filepath.Dir(prepURI(t, uri)))); err == nil {
		t.Errorf("the scratch directory of %s survived the command", uri)
	}

	// The brief: its title, the changeset line, then the four parts in order.
	wantHead := prepareBriefTitle + "\nchangeset: " + cs.ID + " (opened)   agent: agy\n"
	if !strings.HasPrefix(stdout, wantHead) {
		t.Errorf("stdout starts %q, want %q", stdout[:min(len(stdout), len(wantHead)+20)], wantHead)
	}
	prepIndexesInOrder(t, stdout,
		prepareBriefTitle, "changeset: "+cs.ID, "\n## Sources to compile\n", "\n## How to work\n",
		"\n## Curator rules\n", agent.IngestSystemPrompt(), "\n## Vault orientation\n", prepOrientation(t, root))
	if !strings.HasSuffix(stdout, prepOrientation(t, root)) {
		t.Errorf("the brief does not end with the orientation digest:\n%s", stdout)
	}
	// The source block: raw path, chunks, title, original source.
	block := "- raw: raw/articles/long-source.md\n  chunks: 2\n  title: Long Source\n  original source: " + src + "\n"
	if !strings.Contains(stdout, block) {
		t.Errorf("stdout has no source block\n%q\nin:\n%s", block, stdout)
	}
	if strings.Contains(stdout, "- recompile:") {
		t.Errorf("an ingest with no recompile target lists one:\n%s", stdout)
	}
}

// prepURI pulls the uri value out of an ingest_source record's compact args.
func prepURI(t *testing.T, args string) string {
	t.Helper()
	_, rest, ok := strings.Cut(args, `"uri":"`)
	if !ok {
		t.Fatalf("no uri in %q", args)
	}
	uri, _, _ := strings.Cut(rest, `"`)
	return uri
}

// TestPrepareAgentFlag: --agent names the author's model, the brief and the
// prepare record.
func TestPrepareAgentFlag(t *testing.T) {
	prepHermetic(t)
	prepNoAgent(t)
	root := testutil.CopyFixture(t, "minimal")
	src := prepSource(t, "note.md", "A Note", 100)

	stdout, stderr, code := prepareRun(t, root, "--agent", "claude-code", src)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	cs := rcOpenChangeset(t, root)
	if cs.Author.Kind != "agent" || cs.Author.Model != "claude-code" {
		t.Errorf("author = %+v, want kind agent, model claude-code", cs.Author)
	}
	if want := "changeset: " + cs.ID + " (opened)   agent: claude-code\n"; !strings.Contains(stdout, want) {
		t.Errorf("stdout has no line %q:\n%s", want, stdout)
	}
	recs := prepRecords(prepSession(t, root, cs.ID))
	if len(recs) != 1 || recs[0].agent != "claude-code" {
		t.Errorf("prepare records = %+v, want one for claude-code", recs)
	}
	first := prepSession(t, root, cs.ID).Records[1].Content
	if got, _, _ := strings.Cut(first, "\n"); got != "lw ingest --prepare (agent: claude-code)" {
		t.Errorf("prepare record starts %q, want the exact first line", got)
	}
}

// TestPrepareFlagErrors: the three misuses exit 2 with their exact text, open
// nothing and build no agent.
func TestPrepareFlagErrors(t *testing.T) {
	prepHermetic(t)
	prepNoAgent(t)
	src := prepSource(t, "note.md", "A Note", 100)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"prepare with dry-run", []string{"ingest", "--prepare", "--dry-run", src}, "lw: ingest: cannot use --prepare with --dry-run\n"},
		{"agent without prepare", []string{"ingest", "--agent", "x", src}, "lw: ingest: --agent needs --prepare\n"},
		{"empty agent without prepare", []string{"ingest", "--agent", "", src}, "lw: ingest: --agent needs --prepare\n"},
		{"empty agent", []string{"ingest", "--prepare", "--agent", "", src}, "lw: ingest: --agent needs a name\n"},
		{"agent with a newline", []string{"ingest", "--prepare", "--agent", "a\nb", src}, "lw: ingest: --agent needs a name\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := testutil.CopyFixture(t, "minimal")
			args := append(append([]string{}, tc.args[:1]...), append([]string{"--vault", root}, tc.args[1:]...)...)
			stdout, stderr, code := captureRun(t, func() int { return run(args) })
			if code != 2 || stderr != tc.want || stdout != "" {
				t.Fatalf("exit %d stderr %q stdout %q; want 2, %q and nothing", code, stderr, stdout, tc.want)
			}
			for _, state := range []string{"open", "rejected", "committed"} {
				if _, err := os.Stat(filepath.Join(root, ".llmwiki", "changesets", state)); err == nil {
					if n := countChangesets(t, root, state); n != 0 {
						t.Errorf("%d changeset(s) under %s after a refused invocation", n, state)
					}
				}
			}
		})
	}
}

// TestPrepareRecompile: a committed raw named with --recompile becomes a target:
// no raw op, the prepare record lists it, and the brief lists it with its
// citers.
func TestPrepareRecompile(t *testing.T) {
	prepHermetic(t)
	prepNoAgent(t)
	root, rawBytes := rcVault(t)
	rcWritePage(t, root, "wiki/concepts/alpha-notes.md", []string{rcRawA}, "Notes on alpha.^["+rcRawA+"]\n")
	before := snapshotVaultFiles(t, root)

	stdout, stderr, code := prepareRun(t, root, "--recompile", rcRawA)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	cs := rcOpenChangeset(t, root)
	if n := rcCount(cs, stage.OpIngestSource); n != 0 || len(cs.Ops) != 0 {
		t.Errorf("changeset holds %d op(s) (%d ingest_source), want none", len(cs.Ops), n)
	}
	if cs.Intent != "recompile "+rcRawA {
		t.Errorf("intent = %q, want %q", cs.Intent, "recompile "+rcRawA)
	}

	s := prepSession(t, root, cs.ID)
	recs := prepRecords(s)
	want := prepareRecord{agent: "agy", entries: []prepareEntry{{kind: "recompile", path: rcRawA, chunks: 1}}}
	if len(recs) != 1 || !reflect.DeepEqual(recs[0], want) {
		t.Errorf("prepare records = %+v, want [%+v]", recs, want)
	}
	if len(s.Records) != 1 {
		t.Fatalf("session holds %d record(s), want only the prepare record", len(s.Records))
	}
	if !strings.Contains(s.Records[0].Content, "\nrecompile: "+rcRawA+" (1 chunk(s))") {
		t.Errorf("prepare record = %q, want a `recompile:` line for %s", s.Records[0].Content, rcRawA)
	}

	block := "- recompile: " + rcRawA + "\n  chunks: 1\n  title: Alpha Source\n  cited by: wiki/concepts/alpha-notes.md\n"
	if !strings.Contains(stdout, block) {
		t.Errorf("stdout has no recompile block\n%q\nin:\n%s", block, stdout)
	}
	prepIndexesInOrder(t, stdout, "## Sources to compile", block, "## How to work")

	// Raw is immutable and nothing is committed.
	if got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rcRawA))); err != nil || string(got) != string(rawBytes) {
		t.Errorf("the recompiled raw changed (err %v)", err)
	}
	after := snapshotVaultFiles(t, root)
	for p, b := range before {
		if after[p] != b {
			t.Errorf("%s changed on disk", p)
		}
	}
}

// TestPrepareJoin: a changeset already open is joined — the notice on stderr,
// the earlier ops intact, and the prepare record appended to the existing
// session after what was already in it.
func TestPrepareJoin(t *testing.T) {
	prepHermetic(t)
	prepNoAgent(t)
	root := testutil.CopyFixture(t, "minimal")
	earlier := openCreatePageChangeset(t, root, "wiki/concepts/earlier-page.md", "Earlier Page")
	sessions := agent.NewFileSessions(root)
	if _, err := sessions.Create(earlier.ID); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Append(earlier.ID, agent.Record{Role: "user", Content: "earlier work"}); err != nil {
		t.Fatal(err)
	}
	src := prepSource(t, "joined.md", "Joined Source", 100)

	stdout, stderr, code := prepareRun(t, root, "--agent", "codex", src)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if want := "joined open changeset " + earlier.ID + " (1 op(s) already staged; they will be reviewed and committed together)\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	cs := rcOpenChangeset(t, root)
	if cs.ID != earlier.ID {
		t.Fatalf("changeset = %s, want the open one %s", cs.ID, earlier.ID)
	}
	if len(cs.Ops) != 2 || cs.Ops[0].Path != "wiki/concepts/earlier-page.md" || cs.Ops[0].State == stage.StateDropped || cs.Ops[1].Kind != stage.OpIngestSource {
		t.Errorf("ops = %+v, want the earlier page then one ingest_source", cs.Ops)
	}
	if cs.Author.Model != "test" {
		t.Errorf("author model = %q, want the original opener's %q", cs.Author.Model, "test")
	}
	if want := "changeset: " + earlier.ID + " (joined)   agent: codex\n"; !strings.Contains(stdout, want) {
		t.Errorf("stdout has no line %q:\n%s", want, stdout)
	}

	s := prepSession(t, root, cs.ID)
	if len(s.Records) != 3 || s.Records[0].Content != "earlier work" || s.Records[1].Role != "tool" || s.Records[2].Role != "user" {
		t.Fatalf("session records = %+v, want [earlier work, ingest_source, prepare record]", s.Records)
	}
	if recs := prepRecords(s); len(recs) != 1 || recs[0].agent != "codex" {
		t.Errorf("prepare records = %+v, want one for codex", recs)
	}
}

// TestPrepareRollbackOnBadSource: a source whose stage.ingest_source is refused
// (its path holds " #", which a raw's YAML frontmatter cannot record) fails the
// command with the tool's text. A changeset opened here is rejected; a joined
// one loses only the ops this command staged.
func TestPrepareRollbackOnBadSource(t *testing.T) {
	prepHermetic(t)
	prepNoAgent(t)

	t.Run("opened here", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		good := prepSource(t, "good.md", "Good Source", 100)
		bad := prepSource(t, "bad #1.md", "Bad Source", 100)

		stdout, stderr, code := prepareRun(t, root, good, bad)
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if !strings.Contains(stderr, "lw: ingest: ") || !strings.Contains(stderr, bad) || !strings.Contains(stderr, "cannot be recorded in raw frontmatter") {
			t.Errorf("stderr = %q, want it to name %s and the tool's refusal", stderr, bad)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want no brief for a failed prepare", stdout)
		}
		if got := countChangesets(t, root, "open"); got != 0 {
			t.Errorf("open changesets = %d, want 0", got)
		}
		if got := countChangesets(t, root, "rejected"); got != 1 {
			t.Errorf("rejected changesets = %d, want 1", got)
		}
	})

	t.Run("joined", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		earlier := openCreatePageChangeset(t, root, "wiki/concepts/earlier-page.md", "Earlier Page")
		good := prepSource(t, "good.md", "Good Source", 100)
		bad := prepSource(t, "bad #1.md", "Bad Source", 100)

		stdout, stderr, code := prepareRun(t, root, good, bad)
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if !strings.Contains(stderr, "its own ops were dropped (the open changeset "+earlier.ID+" keeps the rest)") {
			t.Errorf("stderr = %q, want the joined rollback sentence", stderr)
		}
		cs := rcOpenChangeset(t, root)
		if cs.ID != earlier.ID {
			t.Fatalf("the open changeset is %s, want %s kept open", cs.ID, earlier.ID)
		}
		if live := cs.Live(); len(live) != 1 || live[0].Path != "wiki/concepts/earlier-page.md" {
			t.Errorf("live ops = %+v, want only the earlier page", live)
		}
		if got := countChangesets(t, root, "rejected"); got != 0 {
			t.Errorf("rejected changesets = %d, want 0 for a joined failure", got)
		}
	})

	t.Run("the same source twice", func(t *testing.T) {
		// Dedupe sees committed raws, not a raw an open changeset only stages:
		// the second prepare of one source joins the first's changeset and the
		// tool refuses the duplicate — rolled back, the first one's ops kept.
		root := testutil.CopyFixture(t, "minimal")
		src := prepSource(t, "twice.md", "Twice", 100)
		if _, stderr, code := prepareRun(t, root, src); code != 0 {
			t.Fatalf("first prepare: exit %d stderr %q", code, stderr)
		}
		_, stderr, code := prepareRun(t, root, src)
		if code != 1 || !strings.Contains(stderr, "already ingested or proposed in this changeset") {
			t.Fatalf("second prepare: exit %d stderr %q, want 1 and the duplicate refusal", code, stderr)
		}
		cs := rcOpenChangeset(t, root)
		if raws := prepLiveIngest(cs); len(raws) != 1 {
			t.Errorf("live raws = %v, want the first prepare's one", raws)
		}
	})
}

// TestPrepareNothingToIngest: a source the vault already holds stops before
// anything is opened, exactly as `lw ingest` does — the line, exit 0, no
// changeset, no agent.
func TestPrepareNothingToIngest(t *testing.T) {
	prepHermetic(t)
	prepNoAgent(t)
	root := testutil.CopyFixture(t, "minimal")
	body := "# Held\n\nThe vault already has this body.\n"
	rcCommitRaw(t, root, "raw/articles/held.md", body)
	src := writtenSource(t, "held-copy.md", body)

	stdout, stderr, code := prepareRun(t, root, src)
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d stderr %q, want 0 and nothing", code, stderr)
	}
	if !strings.Contains(stdout, "nothing to ingest: every source is already in the vault\n") || strings.Contains(stdout, prepareBriefTitle) {
		t.Errorf("stdout = %q, want the nothing-to-ingest line and no brief", stdout)
	}
	e, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err := e.Current(); !errors.Is(err, stage.ErrNoChangeset) {
		t.Errorf("Current = %v, want ErrNoChangeset", err)
	}
}

// TestPrepareBriefHowToWork pins what the fixed part of the brief must tell the
// external agent (D5a) — the one place it learns the commands — and that the
// 16 000-character claim is the chunker's own number.
func TestPrepareBriefHowToWork(t *testing.T) {
	prepHermetic(t)
	prepNoAgent(t)
	root := testutil.CopyFixture(t, "minimal")
	src := prepSource(t, "note.md", "A Note", 100)
	stdout, stderr, code := prepareRun(t, root, src)
	if code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
	_, rest, _ := strings.Cut(stdout, "\n## How to work\n")
	how, _, _ := strings.Cut(rest, "\n## Curator rules\n")
	for _, want := range []string{
		"lw tool <name> '<json>'",
		"lw tool <name> --from -",
		"heredoc",
		"lw tool --list",
		"raw.get",
		"in parallel",
		"16 000 characters",
		"head",
		"tail",
		"stage.create_page",
		"stage.patch_page",
		"lw tool stage.close",
		"exits 1",
		"lw doctor --discard-changeset",
		"lw tui",
		"lw diff",
	} {
		if !strings.Contains(how, want) {
			t.Errorf("the how-to-work text does not say %q:\n%s", want, how)
		}
	}
	if strings.Contains(how, "lw reject") {
		t.Errorf("the how-to-work text names `lw reject`, which is not a verb:\n%s", how)
	}

	// 16 000 is raw.get's chunk size: a body of that many runes is one chunk,
	// one more rune makes two.
	if n := tools.RawChunkCount(strings.Repeat("a", 16000)); n != 1 {
		t.Errorf("16000 runes = %d chunk(s), want 1: the brief's number is stale", n)
	}
	if n := tools.RawChunkCount(strings.Repeat("a", 16001)); n != 2 {
		t.Errorf("16001 runes = %d chunk(s), want 2: the brief's number is stale", n)
	}
}

// TestPrepareRecordRoundTrip: the one pair that writes and reads the record the
// `lw tool` gate looks for. A plain user message, and near misses of the real
// shape, are not prepare records.
func TestPrepareRecordRoundTrip(t *testing.T) {
	tests := []prepareRecord{
		{agent: "agy"},
		{agent: "claude-code", entries: []prepareEntry{{kind: "source", path: "raw/articles/a.md", chunks: 2}}},
		{agent: "codex (v2)", entries: []prepareEntry{
			{kind: "source", path: "raw/papers/a-b.md", chunks: 11},
			{kind: "source", path: "raw/articles/c.md", chunks: 1},
			{kind: "recompile", path: "raw/articles/d.md", chunks: 3},
		}},
		{agent: "x", entries: []prepareEntry{{kind: "recompile", path: "raw/articles/my file.md", chunks: 1}}},
	}
	for _, x := range tests {
		text := prepareRecordText(x)
		if first, _, _ := strings.Cut(text, "\n"); first != "lw ingest --prepare (agent: "+x.agent+")" {
			t.Errorf("first line of %q is %q", text, first)
		}
		got, ok := parsePrepareRecord(text)
		if !ok || !reflect.DeepEqual(got, x) {
			t.Errorf("parsePrepareRecord(prepareRecordText(%+v)) = %+v, %v\ntext: %q", x, got, ok, text)
		}
	}

	if text := prepareRecordText(tests[2]); text != "lw ingest --prepare (agent: codex (v2))\nsource: raw/papers/a-b.md (11 chunk(s))\nsource: raw/articles/c.md (1 chunk(s))\nrecompile: raw/articles/d.md (3 chunk(s))" {
		t.Errorf("record text = %q", text)
	}

	for _, plain := range []string{
		"",
		"summarise raw/articles/a.md",
		"lw ingest --prepare",
		"lw ingest --prepare (agent: )",
		"lw ingest --prepare (agent: x",
		"lw ingest --prepare (agent: x)\nsource: raw/articles/a.md",
		"lw ingest --prepare (agent: x)\nsource: raw/articles/a.md (two chunk(s))",
		"lw ingest --prepare (agent: x)\nnote: raw/articles/a.md (1 chunk(s))",
		"please run lw ingest --prepare (agent: x)",
		"New source material has been extracted and saved locally, ready to ingest.",
	} {
		if p, ok := parsePrepareRecord(plain); ok {
			t.Errorf("parsePrepareRecord(%q) = %+v, true; want not a prepare record", plain, p)
		}
	}
}
