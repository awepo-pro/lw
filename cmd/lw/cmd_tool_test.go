package main

// cmd_tool_test.go pins 063 S2's second half: `lw tool`. An external agent
// curates an ingest by calling one tool per process, so everything the
// in-process agent loop kept in memory has to survive the process boundary:
// the 040 read log and the 043 shrink confirmation (Registry.Replay over the
// changeset's session), the order of concurrent calls (one flock around the
// whole call), and the record each call leaves. Each test runs the real run()
// dispatch against a vault whose changeset `lw ingest --prepare` opened — no
// fake agent, no fake tool, no network. Permanent regression tests (D-10C).

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
)

// preparedVault is a vault with the changeset `lw ingest --prepare` opened over
// one source.
type preparedVault struct {
	root string
	raw  string // the staged raw's vault path
	csID string
}

// prepareVault runs `lw ingest --prepare --agent agy` over one markdown source
// of about runes runes (16 000 runes = one chunk of raw.get) and returns the
// vault. It is hermetic: no key in the environment, no agent construction.
func prepareVault(t *testing.T, runes int) preparedVault {
	t.Helper()
	prepHermetic(t)
	prepNoAgent(t)
	root := testutil.CopyFixture(t, "minimal")
	src := prepSource(t, "long-source.md", "Long Source", runes)
	stdout, stderr, code := prepareRun(t, root, src)
	if code != 0 {
		t.Fatalf("lw ingest --prepare: exit %d stderr %q stdout %q", code, stderr, stdout)
	}
	cs := rcOpenChangeset(t, root)
	raws := prepLiveIngest(cs)
	if len(raws) != 1 {
		t.Fatalf("lw ingest --prepare staged %v, want one raw", raws)
	}
	return preparedVault{root: root, raw: raws[0], csID: cs.ID}
}

// withStdin makes os.Stdin read content for the rest of the test.
func withStdin(t *testing.T, content string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		w.WriteString(content)
		w.Close()
	}()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		r.Close()
	})
}

// lwTool runs `lw tool --vault root <args>`; stdin, when non-empty, is what
// `--from -` reads.
func lwTool(t *testing.T, root, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	if stdin != "" {
		withStdin(t, stdin)
	}
	full := append([]string{"tool", "--vault", root}, args...)
	return captureRun(t, func() int { return run(full) })
}

// getArgs is the arguments of a raw.get for one chunk of raw.
func getArgs(raw string, chunk int) string {
	return `{"source":"` + raw + `","chunk":` + strconv.Itoa(chunk) + `}`
}

// pageArgs is a valid stage.create_page for a page of its own that cites raw.
func pageArgs(t *testing.T, path, title, raw string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"path":       path,
		"title":      title,
		"type":       "concept",
		"tags":       []string{"inference"},
		"sources":    []string{raw},
		"confidence": "medium",
		"contested":  false,
		"body":       "# " + title + "\n\n" + title + " summarises the source.^[" + raw + "]\n\n## Abstract\n\n" + title + " is a short page written from one source, for the test.\n\nSee [[kv-cache]] and [[flash-attention]].\n",
		"rationale":  "063 test page",
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// toolRecords returns the session's tool records of changeset id.
func toolRecords(t *testing.T, root, id string) []agent.Record {
	t.Helper()
	return prepToolRecords(prepSession(t, root, id))
}

// lastTool returns the newest tool record of the session.
func lastTool(t *testing.T, root, id string) agent.Record {
	t.Helper()
	recs := toolRecords(t, root, id)
	if len(recs) == 0 {
		t.Fatal("the session has no tool record")
	}
	return recs[len(recs)-1]
}

// TestToolList: `lw tool --list` prints a JSON array, 2-space indented, sorted,
// of exactly the ingest offer set with each tool's description and JSON schema —
// from a vault with no changeset, and without writing anything.
func TestToolList(t *testing.T) {
	prepHermetic(t)
	root := testutil.CopyFixture(t, "minimal")

	stdout, stderr, code := captureRun(t, func() int { return run([]string{"tool", "--list", "--vault", root}) })
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d stderr %q, want 0 and nothing", code, stderr)
	}
	if !strings.HasPrefix(stdout, "[\n  {\n    \"name\": \"") || !strings.HasSuffix(stdout, "\n  }\n]\n") {
		t.Errorf("output is not a 2-space-indented array:\n%.200s", stdout)
	}
	var list []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	if err := json.Unmarshal([]byte(stdout), &list); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, stdout)
	}
	var names []string
	for _, tl := range list {
		names = append(names, tl.Name)
		if tl.Description == "" {
			t.Errorf("%s has no description", tl.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(tl.InputSchema, &schema); err != nil || schema["type"] != "object" {
			t.Errorf("%s input_schema = %s, want an object schema (err %v)", tl.Name, tl.InputSchema, err)
		}
	}
	if want := tools.IngestToolNames(); strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("names = %v, want exactly IngestToolNames() %v", names, want)
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("names are not sorted: %v", names)
	}
	for _, banned := range []string{"web.search", "stage.open"} {
		if strings.Contains(stdout, `"name": "`+banned+`"`) {
			t.Errorf("--list offers %s", banned)
		}
	}

	if _, err := os.Stat(filepath.Join(root, ".llmwiki", "tmp", "tool.lock")); err == nil {
		t.Error("--list took the tool lock")
	}
	if _, err := os.Stat(filepath.Join(root, ".llmwiki", "changesets")); err == nil {
		t.Error("--list opened an engine (changesets/ exists)")
	}

	if _, stderr, code := captureRun(t, func() int { return run([]string{"tool", "--list", "raw.get", "--vault", root}) }); code != 2 || !strings.HasPrefix(stderr, "usage: lw tool") {
		t.Errorf("--list with a tool name: exit %d stderr %q, want 2 and the usage", code, stderr)
	}
}

// TestToolGate: dispatch needs an open changeset that `lw ingest --prepare`
// opened — its session holds the prepare record — and a tool `lw tool` offers.
func TestToolGate(t *testing.T) {
	prepHermetic(t)

	t.Run("no changeset", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		stdout, stderr, code := lwTool(t, root, "", "raw.get", `{}`)
		if want := "lw: tool: no open changeset — start with lw ingest --prepare <source>\n"; code != 2 || stderr != want || stdout != "" {
			t.Errorf("exit %d stderr %q stdout %q; want 2 and %q", code, stderr, stdout, want)
		}
	})

	t.Run("changeset not opened by prepare", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		cs := openCreatePageChangeset(t, root, "wiki/concepts/earlier-page.md", "Earlier Page")
		want := "lw: tool: changeset " + cs.ID + " was not opened by lw ingest --prepare\n"
		stdout, stderr, code := lwTool(t, root, "", "raw.get", `{}`)
		if code != 2 || stderr != want || stdout != "" {
			t.Errorf("no session: exit %d stderr %q stdout %q; want 2 and %q", code, stderr, stdout, want)
		}

		// A session that exists but holds no prepare record is not prepared either
		// — a plain user message, and a prepare record the Ask pane carried in from
		// an earlier conversation (Carried: it happened in another changeset).
		sessions := agent.NewFileSessions(root)
		if _, err := sessions.Create(cs.ID); err != nil {
			t.Fatal(err)
		}
		if err := sessions.Append(cs.ID, agent.Record{Role: "user", Content: "tidy the kv-cache page"}); err != nil {
			t.Fatal(err)
		}
		carried := prepareRecordText(prepareRecord{agent: "agy", entries: []prepareEntry{{kind: "source", path: "raw/articles/x.md", chunks: 1}}})
		if err := sessions.Append(cs.ID, agent.Record{Role: "user", Content: carried, Carried: true}); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code = lwTool(t, root, "", "raw.get", `{}`)
		if code != 2 || stderr != want || stdout != "" {
			t.Errorf("unprepared session: exit %d stderr %q stdout %q; want 2 and %q", code, stderr, stdout, want)
		}
		if n := len(prepSession(t, root, cs.ID).Records); n != 2 {
			t.Errorf("a refused call wrote to the session (%d records, want 2)", n)
		}
	})

	t.Run("not offered", func(t *testing.T) {
		// The name is checked before the vault is: the same text in a vault with
		// no changeset and in a prepared one.
		empty := testutil.CopyFixture(t, "minimal")
		prepared := prepareVault(t, 100)
		for _, root := range []string{empty, prepared.root} {
			for _, name := range []string{"web.search", "web_search", "stage.open", "stage_open", "nope", "foo_bar", "raw"} {
				stdout, stderr, code := lwTool(t, root, "", name, `{}`)
				want := "lw: tool: " + name + " is not offered for ingest (lw tool --list shows the tools)\n"
				if code != 2 || stderr != want || stdout != "" {
					t.Errorf("%s: exit %d stderr %q stdout %q; want 2 and %q", name, code, stderr, stdout, want)
				}
			}
		}
		if n := len(toolRecords(t, prepared.root, prepared.csID)); n != 1 {
			t.Errorf("refused names wrote tool records: %d, want only the ingest_source one", n)
		}
	})
}

// TestToolArgsErrors: the argument mistakes exit 2 before any vault is read.
func TestToolArgsErrors(t *testing.T) {
	pv := prepareVault(t, 100)
	before := len(prepSession(t, pv.root, pv.csID).Records)

	tests := []struct {
		name  string
		stdin string
		args  []string
		want  string // a substring of stderr
	}{
		{"no tool name", "", nil, "usage: lw tool"},
		{"three positionals", "", []string{"raw.get", `{}`, `{}`}, "usage: lw tool"},
		{"json and --from", `{}`, []string{"raw.get", `{}`, "--from", "-"}, "lw: tool: "},
		{"not json", "", []string{"raw.get", `{"source":`}, "lw: tool: arguments are not valid JSON: "},
		{"trailing text", "", []string{"raw.get", `{} x`}, "lw: tool: arguments are not valid JSON: "},
		{"stdin not json", "not json", []string{"raw.get", "--from", "-"}, "lw: tool: arguments are not valid JSON: "},
		{"empty --from", " \n", []string{"raw.get", "--from", "-"}, "lw: tool: arguments are not valid JSON: "},
		{"--from with no name", "", []string{"raw.get", "--from", ""}, "lw: tool: --from needs a file"},
		{"missing --from file", "", []string{"raw.get", "--from", filepath.Join(t.TempDir(), "absent.json")}, "lw: tool: read "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := lwTool(t, pv.root, tc.stdin, tc.args...)
			if code != 2 || stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Errorf("exit %d stdout %q stderr %q; want 2, nothing on stdout and %q on stderr", code, stdout, stderr, tc.want)
			}
		})
	}
	if n := len(prepSession(t, pv.root, pv.csID).Records); n != before {
		t.Errorf("refused invocations wrote to the session: %d records, had %d", n, before)
	}

	// No arguments at all means {}: raw.get then reports the missing source, a
	// tool refusal (exit 1), not an argument error.
	if _, _, code := lwTool(t, pv.root, "", "raw.get"); code != 1 {
		t.Errorf("raw.get with no arguments: exit %d, want 1 (the tool's own refusal)", code)
	}
	// --from FILE reads a file.
	file := filepath.Join(t.TempDir(), "args.json")
	if err := os.WriteFile(file, []byte(getArgs(pv.raw, 1)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if stdout, stderr, code := lwTool(t, pv.root, "", "raw.get", "--from", file); code != 0 || !strings.Contains(stdout, "chunk 1 of 1") {
		t.Errorf("--from file: exit %d stdout %.80q stderr %q, want a served chunk", code, stdout, stderr)
	}
}

// TestToolReadAndStage: after a prepare, a read prints the chunk, a page goes
// in through stdin as one staged op, and a refused page stages nothing — each
// call leaves its record in the session.
func TestToolReadAndStage(t *testing.T) {
	pv := prepareVault(t, 20000)

	// Reads: the canonical name, and the wire spelling of it.
	stdout, stderr, code := lwTool(t, pv.root, "", "raw.get", getArgs(pv.raw, 2))
	if code != 0 || stderr != "" || !strings.Contains(stdout, "chunk 2 of 2\n\n") || !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("raw.get: exit %d stderr %q stdout %.120q", code, stderr, stdout)
	}
	stdout, _, code = lwTool(t, pv.root, "", "raw_get", `{"source":"`+pv.raw+`"}`)
	if code != 0 || !strings.Contains(stdout, "chunk 1 of 2\n\n") {
		t.Fatalf("raw_get: exit %d stdout %.120q", code, stdout)
	}
	if rec := lastTool(t, pv.root, pv.csID); rec.Tool != "raw.get" || rec.Staged || rec.Args != `{"source":"`+pv.raw+`"}` {
		t.Errorf("record of the wire-named call = %+v, want canonical raw.get, not staged", rec)
	}

	// A page, through stdin; the arguments are recorded compact.
	pretty := new(bytes.Buffer)
	page := pageArgs(t, "wiki/concepts/long-source-notes.md", "Long Source Notes", pv.raw)
	if err := json.Indent(pretty, []byte(page), "", "    "); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = lwTool(t, pv.root, pretty.String(), "stage.create_page", "--from", "-")
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "proposed ") {
		t.Fatalf("stage.create_page: exit %d stderr %q stdout %q", code, stderr, stdout)
	}
	cs := rcOpenChangeset(t, pv.root)
	if n := rcCount(cs, stage.OpCreatePage); n != 1 || len(cs.Live()) != 2 {
		t.Fatalf("live ops = %+v, want the ingest_source and one create_page", cs.Live())
	}
	rec := lastTool(t, pv.root, pv.csID)
	if rec.Tool != "stage.create_page" || !rec.Staged || rec.Args != page || rec.Result != strings.TrimSuffix(stdout, "\n") || rec.TS.IsZero() {
		t.Errorf("record = %+v\nwant stage.create_page, staged, the compact args %q and the printed result", rec, page)
	}

	// Refusals: an out-of-taxonomy tag, and arguments of the wrong shape. Exit 1,
	// the refusal on stdout (no lw: line), nothing staged, not marked staged.
	bad := strings.Replace(pageArgs(t, "wiki/concepts/bad-page.md", "Bad Page", pv.raw), `"inference"`, `"not-a-tag"`, 1)
	for name, args := range map[string]string{"bad tag": bad, "bad args": `{"path":5}`} {
		stdout, stderr, code = lwTool(t, pv.root, "", "stage.create_page", args)
		if code != 1 || stderr != "" || stdout == "" || strings.HasPrefix(stdout, "proposed ") {
			t.Errorf("%s: exit %d stderr %q stdout %q; want 1, the refusal on stdout and nothing on stderr", name, code, stderr, stdout)
		}
		if got := rcOpenChangeset(t, pv.root); len(got.Live()) != 2 {
			t.Errorf("%s: a refused page staged an op: %+v", name, got.Live())
		}
		if rec := lastTool(t, pv.root, pv.csID); rec.Staged || rec.Tool != "stage.create_page" || rec.Result != strings.TrimSuffix(stdout, "\n") {
			t.Errorf("%s: record = %+v, want an unstaged one carrying the refusal", name, rec)
		}
	}

	// The wire spelling stages too, recorded under the canonical name.
	stdout, _, code = lwTool(t, pv.root, "", "stage_create_page", pageArgs(t, "wiki/concepts/wire-page.md", "Wire Page", pv.raw))
	if code != 0 || !strings.HasPrefix(stdout, "proposed ") {
		t.Fatalf("stage_create_page: exit %d stdout %q", code, stdout)
	}
	if rec := lastTool(t, pv.root, pv.csID); rec.Tool != "stage.create_page" || !rec.Staged {
		t.Errorf("record of stage_create_page = %+v, want canonical and staged", rec)
	}
}

// TestToolCloseGuardAcrossProcesses: the 040 read-every-chunk guard holds across
// processes. A source of two chunks, one read: the first stage.close is refused,
// the second goes through saying what was never read, and carries the summary
// tail. With both chunks read the first close is clean.
func TestToolCloseGuardAcrossProcesses(t *testing.T) {
	pv := prepareVault(t, 20000)
	if _, _, code := lwTool(t, pv.root, "", "raw.get", getArgs(pv.raw, 1)); code != 0 {
		t.Fatalf("raw.get chunk 1: exit %d", code)
	}

	stdout, stderr, code := lwTool(t, pv.root, "", "stage.close")
	if code != 1 || stderr != "" {
		t.Fatalf("first close: exit %d stderr %q stdout %q, want 1 and nothing on stderr", code, stderr, stdout)
	}
	refusal := strings.TrimSuffix(stdout, "\n")
	if key, ok := tools.UnreadRefusalKey(refusal); !ok || key != pv.raw+" chunks 2 of 2 unread" {
		t.Fatalf("first close printed %q, want the 040 refusal over chunk 2 (key %q, ok %v)", stdout, key, ok)
	}
	if strings.Contains(stdout, "opened changeset") {
		t.Errorf("a refused close printed the summary tail:\n%s", stdout)
	}

	stdout, stderr, code = lwTool(t, pv.root, "", "stage.close")
	if code != 0 || stderr != "" {
		t.Fatalf("second close: exit %d stderr %q stdout %q, want 0 — the same set goes through", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "\nunread chunks: "+pv.raw+" 2\n") {
		t.Errorf("second close does not name the unread chunk:\n%s", stdout)
	}
	if !strings.Contains(stdout, "\n\nopened changeset "+pv.csID+": ") {
		t.Errorf("second close has no summary tail:\n%s", stdout)
	}
	if recs := toolRecords(t, pv.root, pv.csID); len(recs) != 4 || recs[2].Staged || !recs[3].Staged {
		t.Errorf("records = %+v, want ingest, raw.get, the refused close (unstaged), the passed close (staged: the loop's rule for a stage.* call)", recs)
	}

	// Both chunks read: the first close passes with no unread line.
	pv2 := prepareVault(t, 20000)
	for _, chunk := range []int{2, 1} {
		if _, _, code := lwTool(t, pv2.root, "", "raw.get", getArgs(pv2.raw, chunk)); code != 0 {
			t.Fatalf("raw.get chunk %d: exit %d", chunk, code)
		}
	}
	stdout, stderr, code = lwTool(t, pv2.root, "", "stage.close")
	if code != 0 || stderr != "" || strings.Contains(stdout, "unread chunks") || strings.Contains(stdout, "not every chunk") {
		t.Errorf("close after reading everything: exit %d stderr %q stdout %q, want a clean pass", code, stderr, stdout)
	}
}

// TestToolCloseHoldsRecompileAndLiveSources: the live list `lw tool` hands
// Replay is the changeset's — a recompile target a prepare record names is held
// to the read-every-chunk rule though no ingest_source op names it, and a source
// whose op was dropped is not.
func TestToolCloseHoldsRecompileAndLiveSources(t *testing.T) {
	t.Run("recompile target", func(t *testing.T) {
		prepHermetic(t)
		prepNoAgent(t)
		root := testutil.CopyFixture(t, "minimal")
		body := "# Long Committed\n\n" + strings.Repeat("Alpha beta gamma delta. ", 840) + "\n"
		rcCommitRaw(t, root, "raw/articles/long-committed.md", body)
		if stdout, stderr, code := prepareRun(t, root, "--recompile", "raw/articles/long-committed.md"); code != 0 {
			t.Fatalf("prepare: exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		const raw = "raw/articles/long-committed.md"

		stdout, _, code := lwTool(t, root, "", "stage.close")
		if code != 1 || !strings.Contains(stdout, raw+" chunks 1, 2 of 2 unread") {
			t.Fatalf("close over an unread recompile target: exit %d stdout %q, want the refusal naming both chunks", code, stdout)
		}
		for _, chunk := range []int{1, 2} {
			if _, _, code := lwTool(t, root, "", "raw.get", getArgs(raw, chunk)); code != 0 {
				t.Fatalf("raw.get chunk %d: exit %d", chunk, code)
			}
		}
		stdout, stderr, code := lwTool(t, root, "", "stage.close")
		if code != 0 || strings.Contains(stdout, "unread chunks") {
			t.Errorf("close after reading the target: exit %d stderr %q stdout %q", code, stderr, stdout)
		}
	})

	t.Run("dropped source", func(t *testing.T) {
		pv := prepareVault(t, 20000)
		cs := rcOpenChangeset(t, pv.root)
		e, err := stage.OpenEngine(pv.root)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.DropOp(cs.Ops[0].ID); err != nil {
			t.Fatal(err)
		}
		e.Close()
		stdout, stderr, code := lwTool(t, pv.root, "", "stage.close")
		if code != 0 || strings.Contains(stdout, "unread chunks") || strings.Contains(stdout, "not every chunk") {
			t.Errorf("close with the only source dropped: exit %d stderr %q stdout %q, want a plain pass", code, stderr, stdout)
		}
	})
}

// TestToolShrinkConfirmAcrossProcesses: the 043 two-step works when each step is
// its own process. A replace_section that drops most of a 400-byte section is
// refused; the identical call with allow_shrink, in a new process, applies.
func TestToolShrinkConfirmAcrossProcesses(t *testing.T) {
	pv := prepareVault(t, 100)
	long := strings.Repeat("The details of this section run long enough to trip the shrink rule when they are cut. ", 8)
	putFile(t, pv.root, "wiki/concepts/shrink-me.md", "---\ntitle: Shrink Me\ncreated: 2026-10-01\nupdated: 2026-10-01\ntype: concept\ntags: [inference]\nsources: [raw/articles/kv-cache-explained.md]\nconfidence: medium\n---\n\n# Shrink Me\n\nA page for the shrink test.^[raw/articles/kv-cache-explained.md]\n\n## Abstract\n\nA page for the shrink test, linking [[kv-cache]] and [[flash-attention]].\n\n## Details\n\n"+long+"\n")
	args := func(allow bool) string {
		return `{"path":"wiki/concepts/shrink-me.md","section":"## Details","op":"replace_section","content":"Short details. See [[kv-cache]].","rationale":"condense","allow_shrink":` + strconv.FormatBool(allow) + `}`
	}

	stdout, stderr, code := lwTool(t, pv.root, "", "stage.patch_page", args(false))
	if code != 1 || stderr != "" || !tools.ShrinkRefusal(strings.TrimSuffix(stdout, "\n")) {
		t.Fatalf("unconfirmed shrink: exit %d stderr %q stdout %q, want 1 and the 043 shrink refusal", code, stderr, stdout)
	}
	if got := rcOpenChangeset(t, pv.root); rcCount(got, stage.OpPatchPage) != 0 {
		t.Fatal("the refused shrink staged a patch")
	}

	stdout, stderr, code = lwTool(t, pv.root, "", "stage.patch_page", args(true))
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "proposed ") {
		t.Fatalf("confirmed shrink: exit %d stderr %q stdout %q, want 0 — the confirmation must cross the process boundary", code, stderr, stdout)
	}
	if got := rcOpenChangeset(t, pv.root); rcCount(got, stage.OpPatchPage) != 1 {
		t.Errorf("the confirmed shrink staged %d patch(es), want 1", rcCount(got, stage.OpPatchPage))
	}

	// Control: a call that sets allow_shrink without a refusal behind it is
	// refused, in this same kind of fresh process (043: it confirms, it never
	// licenses).
	other := prepareVault(t, 100)
	putFile(t, other.root, "wiki/concepts/shrink-me.md", "---\ntitle: Shrink Me\ncreated: 2026-10-01\nupdated: 2026-10-01\ntype: concept\ntags: [inference]\nsources: [raw/articles/kv-cache-explained.md]\nconfidence: medium\n---\n\n# Shrink Me\n\nA page for the shrink test.^[raw/articles/kv-cache-explained.md]\n\n## Abstract\n\nA page for the shrink test, linking [[kv-cache]] and [[flash-attention]].\n\n## Details\n\n"+long+"\n")
	stdout, _, code = lwTool(t, other.root, "", "stage.patch_page", args(true))
	if code != 1 || !tools.ShrinkRefusal(strings.TrimSuffix(stdout, "\n")) {
		t.Errorf("allow_shrink with no refusal behind it: exit %d stdout %q, want the shrink refusal", code, stdout)
	}
}

// TestToolParallelStageCalls: eight `lw tool` calls at once, each in its own
// engine, each staging a page of its own. The whole call runs under one flock,
// so every call reads the changeset the one before it left: eight new live ops,
// eight tool records, a changeset.json that parses and unique op ids.
func TestToolParallelStageCalls(t *testing.T) {
	const n = 8
	pv := prepareVault(t, 100)

	calls := make([][]string, n)
	for i := range n {
		path := "wiki/concepts/parallel-" + strconv.Itoa(i) + ".md"
		calls[i] = []string{"--vault", pv.root, "stage.create_page", pageArgs(t, path, "Parallel Page "+strconv.Itoa(i), pv.raw)}
	}
	errs := make([]error, n)
	_, _, _ = captureRun(t, func() int {
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = cmdTool(calls[i])
			}()
		}
		wg.Wait()
		return 0
	})
	for i, err := range errs {
		if err != nil {
			t.Errorf("call %d: %v", i, err)
		}
	}

	cs := rcOpenChangeset(t, pv.root)
	if got := rcCount(cs, stage.OpCreatePage); got != n {
		t.Fatalf("changeset holds %d create_page op(s), want %d (ops: %+v)", got, n, cs.Ops)
	}
	ids := map[string]bool{}
	for _, op := range cs.Ops {
		ids[op.ID] = true
	}
	if len(ids) != len(cs.Ops) {
		t.Errorf("op ids are not unique: %d ids for %d ops", len(ids), len(cs.Ops))
	}
	b, err := os.ReadFile(filepath.Join(pv.root, ".llmwiki", "changesets", "open", cs.ID, "changeset.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed stage.Changeset
	if err := json.Unmarshal(b, &parsed); err != nil || len(parsed.Ops) != len(cs.Ops) {
		t.Errorf("changeset.json: err %v, %d ops, want a valid file with %d", err, len(parsed.Ops), len(cs.Ops))
	}

	var creates int
	for _, rec := range toolRecords(t, pv.root, pv.csID) {
		if rec.Tool == "stage.create_page" && rec.Staged {
			creates++
		}
	}
	if creates != n {
		t.Errorf("the session has %d staged create_page record(s), want %d", creates, n)
	}
}

// holdToolLock takes the tool lock of root the way a running call would, and
// returns the file whose Close lets it go.
func holdToolLock(t *testing.T, root string) *os.File {
	t.Helper()
	path := filepath.Join(root, ".llmwiki", "tmp", "tool.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestToolLock: the lock is a flock on .llmwiki/tmp/tool.lock held for the
// whole call. A call waits for a holder and runs when it lets go; one that
// waits past the bound gives up with an error rather than hanging.
func TestToolLock(t *testing.T) {
	pv := prepareVault(t, 100)
	hold := func() *os.File { return holdToolLock(t, pv.root) }

	t.Run("waits for the holder", func(t *testing.T) {
		captureRun(t, func() int {
			f := hold()
			done := make(chan int, 1)
			go func() { done <- run([]string{"tool", "--vault", pv.root, "raw.get", getArgs(pv.raw, 1)}) }()
			select {
			case code := <-done:
				f.Close()
				t.Errorf("lw tool ran while the lock was held (exit %d)", code)
				return 0
			case <-time.After(300 * time.Millisecond):
			}
			f.Close() // closing the descriptor releases the flock
			select {
			case code := <-done:
				if code != 0 {
					t.Errorf("lw tool after the lock was released: exit %d", code)
				}
			case <-time.After(10 * time.Second):
				t.Error("lw tool never ran after the lock was released")
			}
			return 0
		})
	})

	t.Run("gives up after the bound", func(t *testing.T) {
		orig := toolLockWait
		toolLockWait = 100 * time.Millisecond
		t.Cleanup(func() { toolLockWait = orig })
		f := hold()
		defer f.Close()
		before := len(toolRecords(t, pv.root, pv.csID))
		_, stderr, code := lwTool(t, pv.root, "", "raw.get", getArgs(pv.raw, 1))
		if code != 1 || !strings.HasPrefix(stderr, "lw: tool: ") || !strings.Contains(stderr, "another lw tool call") {
			t.Errorf("exit %d stderr %q, want 1 and an error naming the other call", code, stderr)
		}
		if n := len(toolRecords(t, pv.root, pv.csID)); n != before {
			t.Errorf("a call that never got the lock wrote a record (%d, had %d)", n, before)
		}
	})
}

// TestToolReadsTheChangesetAfterTheLock: a call reads the changeset only once
// it holds the lock. A call waiting behind another must see what that one
// staged: an engine opened (and its changeset cached) before the wait would
// answer from the changeset as it was, and refuse to patch a page the call
// before it had just created.
func TestToolReadsTheChangesetAfterTheLock(t *testing.T) {
	pv := prepareVault(t, 100)
	patch := `{"path":"wiki/concepts/late-page.md","section":"## Abstract","op":"append_section","content":"One more sentence. See [[kv-cache]].","rationale":"extend the page the call before staged"}`

	captureRun(t, func() int {
		f := holdToolLock(t, pv.root)
		done := make(chan int, 1)
		go func() { done <- run([]string{"tool", "--vault", pv.root, "stage.patch_page", patch}) }()
		time.Sleep(300 * time.Millisecond) // the call is waiting on the lock

		// What the call that holds the lock does meanwhile: stage the page.
		e, err := stage.OpenEngine(pv.root)
		if err != nil {
			f.Close()
			t.Error(err)
			return 0
		}
		_, err = e.Append(stage.Op{
			Kind: stage.OpCreatePage, Path: "wiki/concepts/late-page.md",
			Content:    []byte("---\ntitle: Late Page\ncreated: 2026-10-19\nupdated: 2026-10-19\ntype: concept\ntags: [inference]\nsources: [" + pv.raw + "]\nconfidence: medium\n---\n\n# Late Page\n\nA page staged while another call waited.^[" + pv.raw + "]\n\n## Abstract\n\nA page staged while another call waited, linking [[kv-cache]] and [[flash-attention]].\n"),
			Rationale:  "063 test: the page the waiting call patches",
			Provenance: []string{pv.raw},
		})
		e.Close()
		f.Close()
		if err != nil {
			t.Error(err)
			return 0
		}
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("the waiting call exited %d: it patched from a changeset read before the lock", code)
			}
		case <-time.After(10 * time.Second):
			t.Error("the waiting call never finished")
		}
		return 0
	})
	if got := rcOpenChangeset(t, pv.root); rcCount(got, stage.OpPatchPage) != 1 {
		t.Errorf("the changeset holds %d patch_page op(s), want the waiting call's one", rcCount(got, stage.OpPatchPage))
	}
}

// TestToolSessionShowTrail: `lw session show` lists the prepare record and every
// `lw tool` record, in the order they were made.
func TestToolSessionShowTrail(t *testing.T) {
	pv := prepareVault(t, 100)
	if _, _, code := lwTool(t, pv.root, "", "raw.get", getArgs(pv.raw, 1)); code != 0 {
		t.Fatal("raw.get failed")
	}
	if _, _, code := lwTool(t, pv.root, "", "stage.create_page", pageArgs(t, "wiki/concepts/trail-page.md", "Trail Page", pv.raw)); code != 0 {
		t.Fatal("stage.create_page failed")
	}
	if _, _, code := lwTool(t, pv.root, "", "stage.close"); code != 0 {
		t.Fatal("stage.close failed")
	}

	stdout, stderr, code := captureRun(t, func() int { return run([]string{"session", "show", "--plain", "--vault", pv.root}) })
	if code != 0 {
		t.Fatalf("session show: exit %d stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "session "+pv.csID+" · open · ") || !strings.Contains(stdout, " · 5 records\n") {
		t.Errorf("header does not show the open session with 5 records:\n%.200s", stdout)
	}
	prepIndexesInOrder(t, stdout,
		"── tool stage.ingest_source · staged ",
		"── you ", "lw ingest --prepare (agent: agy)", "source: "+pv.raw+" (1 chunk(s))",
		"── tool raw.get ",
		"── tool stage.create_page · staged ", "wiki/concepts/trail-page.md",
		"── tool stage.close · staged ")
}

// TestToolCloseTail: a successful stage.close prints the changeset summary after
// its own text, then the raw-only warning (029) or the lint-regression warning
// (A-807) when they apply — what `lw ingest` prints at the end of a turn.
func TestToolCloseTail(t *testing.T) {
	t.Run("raw only", func(t *testing.T) {
		pv := prepareVault(t, 100)
		if _, _, code := lwTool(t, pv.root, "", "raw.get", getArgs(pv.raw, 1)); code != 0 {
			t.Fatal("raw.get failed")
		}
		stdout, stderr, code := lwTool(t, pv.root, "", "stage.close")
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		tail := "\n\nopened changeset " + pv.csID + ": ingest "
		_, after, ok := strings.Cut(stdout, tail)
		if !ok {
			t.Fatalf("no summary tail after the close text:\n%s", stdout)
		}
		if !strings.Contains(after, "(1 op(s))\n  op1 ingest_source "+pv.raw+"\n") {
			t.Errorf("summary lines = %q, want the op list", after)
		}
		if !strings.HasSuffix(stdout, "warning: 0 pages proposed — only raw source(s) staged; review before committing\n") {
			t.Errorf("no raw-only warning at the end:\n%s", stdout)
		}
		if strings.Contains(stdout, "lint regresses") {
			t.Errorf("a clean raw-only close warns of a lint regression:\n%s", stdout)
		}
	})

	t.Run("lint regression", func(t *testing.T) {
		pv := prepareVault(t, 100)
		if _, _, code := lwTool(t, pv.root, "", "raw.get", getArgs(pv.raw, 1)); code != 0 {
			t.Fatal("raw.get failed")
		}
		// A page of broken links, appended to the engine directly: the tool layer
		// would refuse it at proposal time, the lint projection counts its errors.
		e, err := stage.OpenEngine(pv.root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Append(stage.Op{
			Kind: stage.OpCreatePage, Path: "wiki/concepts/broken-links-page.md",
			Content:    []byte("---\ntitle: Broken Links Page\ncreated: 2026-08-30\nupdated: 2026-08-30\ntype: concept\ntags: [inference]\nconfidence: medium\n---\n\n# Broken Links Page\n\nLinks to [[does-not-exist-one]] and [[does-not-exist-two]].\n"),
			Rationale:  "063 test: a page that regresses lint",
			Provenance: []string{"raw/articles/kv-cache-explained.md"},
		}); err != nil {
			t.Fatal(err)
		}
		e.Close()

		stdout, stderr, code := lwTool(t, pv.root, "", "stage.close")
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d stderr %q stdout %q", code, stderr, stdout)
		}
		if !strings.Contains(stdout, "\nwarning: lint regresses — ") || !strings.Contains(stdout, "lw commit will refuse this (review with lw diff)\n") {
			t.Errorf("no lint-regression warning:\n%s", stdout)
		}
		if strings.Contains(stdout, "0 pages proposed") {
			t.Errorf("a changeset with a page warns that it proposed none:\n%s", stdout)
		}
		if !strings.Contains(stdout, "\n\nopened changeset "+pv.csID+": ") {
			t.Errorf("no summary tail:\n%s", stdout)
		}
	})
}

// TestToolLogsOneLinePerCall: lw.log gets one Info line per call — tool,
// changeset, is_error, dur_ms — and never the arguments or the content.
func TestToolLogsOneLinePerCall(t *testing.T) {
	t.Setenv("LW_LOG", "") // the default level, whatever the developer's environment says
	pv := prepareVault(t, 100)
	args := getArgs(pv.raw, 1)
	if _, _, code := lwTool(t, pv.root, "", "raw.get", args); code != 0 {
		t.Fatal("raw.get failed")
	}
	if _, _, code := lwTool(t, pv.root, "", "raw.get", `{"source":"raw/articles/absent.md"}`); code != 1 {
		t.Fatal("a missing source did not exit 1")
	}
	b, err := os.ReadFile(filepath.Join(pv.root, ".llmwiki", "logs", "lw.log"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, "lw tool") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("lw.log has %d lw tool line(s), want 2:\n%s", len(lines), b)
	}
	for i, l := range lines {
		for _, want := range []string{"level=INFO", "tool=raw.get", "changeset=" + pv.csID, "dur_ms="} {
			if !strings.Contains(l, want) {
				t.Errorf("line %d lacks %q: %s", i, want, l)
			}
		}
		if strings.Contains(l, "Alpha beta") || strings.Contains(l, "absent.md") || strings.Contains(l, pv.raw) {
			t.Errorf("line %d carries arguments or content: %s", i, l)
		}
	}
	if !strings.Contains(lines[0], "is_error=false") || !strings.Contains(lines[1], "is_error=true") {
		t.Errorf("is_error attributes wrong:\n%s\n%s", lines[0], lines[1])
	}
}

// TestToolRecordShape: toolRecord is the one place a call becomes a session
// record. Staged is the loop's own rule — a stage.* call without an error — an
// engine error is recorded as the result, and the arguments are stored as given.
func TestToolRecordShape(t *testing.T) {
	ts := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		tool string
		res  tools.Result
		err  error
		want agent.Record
	}{
		{"stage ok", "stage.create_page", tools.Result{Content: "proposed op1"}, nil, agent.Record{TS: ts, Role: "tool", Tool: "stage.create_page", Args: `{"a":1}`, Result: "proposed op1", Staged: true}},
		{"stage close ok", "stage.close", tools.Result{Content: "changeset cs-1"}, nil, agent.Record{TS: ts, Role: "tool", Tool: "stage.close", Args: `{"a":1}`, Result: "changeset cs-1", Staged: true}},
		{"stage refused", "stage.patch_page", tools.Result{Content: "refused", IsError: true}, nil, agent.Record{TS: ts, Role: "tool", Tool: "stage.patch_page", Args: `{"a":1}`, Result: "refused"}},
		{"read ok", "raw.get", tools.Result{Content: "chunk 1 of 1"}, nil, agent.Record{TS: ts, Role: "tool", Tool: "raw.get", Args: `{"a":1}`, Result: "chunk 1 of 1"}},
		{"engine error", "stage.create_page", tools.Result{Content: "ignored"}, errors.New("disk on fire"), agent.Record{TS: ts, Role: "tool", Tool: "stage.create_page", Args: `{"a":1}`, Result: "disk on fire"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolRecord(ts, tc.tool, `{"a":1}`, tc.res, tc.err); got != tc.want {
				t.Errorf("toolRecord = %+v\nwant %+v", got, tc.want)
			}
		})
	}
}
