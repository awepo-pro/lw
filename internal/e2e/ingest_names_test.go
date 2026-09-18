// ingest_names_test.go walks A-807's three cmdIngest behaviours end to end
// (MASTER §5 R-810): the scratch path the agent is told to ingest ends in
// the user's own file name so an untitled source never lands under the
// 01- prefix in raw/ (BUG-1), a source the vault already holds is skipped
// before any LLM round is spent on it (BUG-2), and an ingest whose
// changeset lint-regresses says so at ingest time instead of letting the
// user discover it at lw commit (FINDING-3). The scratch fakes act on the
// "- path: " line of the request's first user message — what a real model
// does with buildIngestMessage — closing C-817's gap: every earlier fake
// named the original source path, so the scratch prefix was never on the
// wire and BUG-1 could not reproduce.
package e2e

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// stageIngestScratchSSE scripts one round that calls stage_ingest_source on
// the uri from the n-th (1-based) "- path: " line of the request's FIRST
// USER MESSAGE — the path lw ingest's own message told the model to ingest,
// scratch directory and all. It fails the round (an empty answer, which
// serve turns into a 500) when there is no such line, so a fake that runs
// against a message shape it does not understand fails loudly instead of
// silently staging something else.
func stageIngestScratchSSE(id string, n int) fakeRound {
	return fakeRound{
		Name: "stage_ingest_scratch_" + id,
		BodyFunc: func(req []byte) string {
			uri, ok := nthUserIngestPath(req, n)
			if !ok {
				return ""
			}
			return stageIngestSSE(id, uri).Body
		},
	}
}

// createPageSSE scripts one round that calls stage_create_page with the
// given arguments JSON and finishes with tool_calls — the round an ingest
// scenario adds when its changeset must carry a page as well as the raw
// source. The arguments shape is smoke_ingest_round2.sse's.
func createPageSSE(id, args string) fakeRound {
	call := `{"index":0,"id":"call_` + id + `","type":"function","function":{"name":"stage_create_page","arguments":` + strconv.Quote(args) + `}}`
	body := "data: {\"id\":\"chatcmpl-" + id + "\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-" + id + "\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[" + call + "]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-" + id + "\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n"
	return fakeRound{Name: "stage_create_page_" + id, Body: body}
}

// ingestMessage is the wire shape of the request body this package's fakes
// need to read: the messages array of an OpenAI-style chat-completions
// request, narrowed to role and content.
type ingestMessage struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

// firstUserMessage returns the content of the request's first user message
// (system messages come first and are skipped), and whether one exists.
func firstUserMessage(req []byte) (string, bool) {
	var body ingestMessage
	if err := json.Unmarshal(req, &body); err != nil {
		return "", false
	}
	for _, m := range body.Messages {
		if m.Role == "user" {
			return m.Content, true
		}
	}
	return "", false
}

// nthUserIngestPath returns the uri on the n-th (1-based) "- path: " line
// of the request's first user message — the scratch path buildIngestMessage
// listed n-th — and whether that line exists.
func nthUserIngestPath(req []byte, n int) (string, bool) {
	msg, ok := firstUserMessage(req)
	if !ok {
		return "", false
	}
	for _, line := range strings.Split(msg, "\n") {
		path, found := strings.CutPrefix(line, "- path: ")
		if !found {
			continue
		}
		if n == 1 {
			return strings.TrimSpace(path), true
		}
		n--
	}
	return "", false
}

// countUserIngestPaths returns how many "- path: " lines the request's
// first user message carries — how many sources lw told the model to ingest.
func countUserIngestPaths(req []byte) int {
	msg, ok := firstUserMessage(req)
	if !ok {
		return 0
	}
	n := 0
	for _, line := range strings.Split(msg, "\n") {
		if strings.HasPrefix(line, "- path: ") {
			n++
		}
	}
	return n
}

// ingestSources is the untitled, heading-less body every scenario here
// stages under sources/m3-note.md — the literal shape of the user's G5b
// re-run finding: nothing in the text names a title, so the raw source can
// only be named from the file it came from.
const m3NoteBody = "Loose fragments about quaternions with no heading and no frontmatter,\nso the raw source can only be named from the file it came from.\n"

// TestReliabilityScratchName is A-807's BUG-1 end to end: the scratch file
// the agent is handed is <tmp>/<NN>/<source name>, so stage.ingest_source's
// basename fallback sees the user's own name and an untitled m3-note.md
// lands at raw/articles/m3-note.md — never at a 01- prefixed path derived
// from lw's own scratch numbering.
func TestReliabilityScratchName(t *testing.T) {
	e := newEnv(t)
	vault := newVault(t)
	fake := newFakeLLM(t,
		stageIngestScratchSSE("scratch1", 1),
		stopSSE("scratch1", "Staged the untitled source; no page proposed, so the changeset is raw-only."),
	)
	writeConfig(t, e.config, fake.URL()+"/v1")

	src := e.writeSource(t, "sources/m3-note.md", m3NoteBody)

	ingest := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", src)

	t.Run("untitled_source_keeps_its_own_name", func(t *testing.T) {
		if ingest.Code != 0 {
			t.Fatalf("lw ingest: exit %d, want 0\n%s", ingest.Code, ingest.Output)
		}
		diff := runLW(t, e, "diff", "--vault", vault, "--stat")
		if diff.Code != 0 {
			t.Fatalf("lw diff --stat: exit %d, want 0\n%s", diff.Code, diff.Output)
		}
		if !strings.Contains(diff.Output, "raw/articles/m3-note.md") {
			t.Errorf("lw diff --stat: output does not name raw/articles/m3-note.md\n%s", diff.Output)
		}
		if strings.Contains(diff.Output, "01-") {
			t.Errorf("lw diff --stat: the scratch prefix leaked into the staged raw path\n%s", diff.Output)
		}
	})

	t.Run("scratch_path_ends_in_the_source_name", func(t *testing.T) {
		reqs := fake.Requests()
		if len(reqs) == 0 {
			t.Fatalf("the fake LLM served no requests; ingest output:\n%s", ingest.Output)
		}
		path, ok := nthUserIngestPath(reqs[0].Body, 1)
		if !ok {
			t.Fatalf("the first request's user message carries no \"- path: \" line\n%s", reqs[0].Body)
		}
		if got := filepath.Base(path); got != "m3-note.md" {
			t.Errorf("scratch path base = %q, want the source's own name m3-note.md", got)
		}
		if got := filepath.Base(filepath.Dir(path)); got != "01" {
			t.Errorf("scratch parent directory = %q, want the per-source number 01", got)
		}
	})

	t.Run("clean_ingest_has_no_lint_warning", func(t *testing.T) {
		if strings.Contains(ingest.Output, "warning: lint") {
			t.Errorf("a raw-only ingest that lint-regresses nothing printed a lint warning:\n%s", ingest.Output)
		}
	})
}

// TestReliabilityDuplicateIngest is A-807's BUG-2 end to end: a source the
// vault already holds — committed earlier, or seen earlier in the same
// command — is skipped before the agent is built, and when nothing else
// remains lw ingest exits 0 with no changeset and no LLM request.
func TestReliabilityDuplicateIngest(t *testing.T) {
	// commitOneRawSource ingests name/body through the scratch fake into a
	// fresh sandbox and commits the raw-only changeset, so a subtest can
	// re-ingest the same body afterwards. Returns the env (for its work
	// directory and config) and the vault root.
	commitOneRawSource := func(t *testing.T, name, body string) (*env, string) {
		t.Helper()
		e := newEnv(t)
		vault := newVault(t)
		src := e.writeSource(t, name, body)
		fake := newFakeLLM(t,
			stageIngestScratchSSE("dup-pre", 1),
			stopSSE("dup-pre", "Staged the source; no page proposed."),
		)
		writeConfig(t, e.config, fake.URL()+"/v1")
		ingest := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", src)
		if ingest.Code != 0 {
			t.Fatalf("lw ingest (setup): exit %d, want 0\n%s", ingest.Code, ingest.Output)
		}
		commit := runLW(t, e, "commit", "--vault", vault, "-m", "setup: one raw source")
		if commit.Code != 0 {
			t.Fatalf("lw commit (setup): exit %d, want 0\n%s", commit.Code, commit.Output)
		}
		return e, vault
	}

	t.Run("committed_duplicate_names_its_path", func(t *testing.T) {
		const dupBody = "# Attention Notes\n\nA body the vault will already hold once the setup commit lands.\n"
		e, vault := commitOneRawSource(t, "sources/dup/attention-notes.md", dupBody)

		copied := e.writeSource(t, "sources/dup/copy-of-notes.md", dupBody)
		empty := newFakeLLM(t) // no rounds scripted: any request would hit its 500
		writeConfig(t, e.config, empty.URL()+"/v1")

		ingest := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", copied)
		if ingest.Code != 0 {
			t.Fatalf("lw ingest (committed duplicate): exit %d, want 0\n%s", ingest.Code, ingest.Output)
		}
		for _, want := range []string{
			"skipped " + copied + ": already in the vault at raw/articles/attention-notes.md",
			"nothing to ingest: every source is already in the vault",
		} {
			if !strings.Contains(ingest.Output, want) {
				t.Errorf("lw ingest (committed duplicate): output does not carry %q\n%s", want, ingest.Output)
			}
		}
	})

	t.Run("duplicate_only_ingest_makes_no_request", func(t *testing.T) {
		const dupBody = "# Attention Notes\n\nA body whose only later appearance is the duplicate re-ingest.\n"
		e, vault := commitOneRawSource(t, "sources/dup2/attention-notes.md", dupBody)

		copied := e.writeSource(t, "sources/dup2/copy-of-notes.md", dupBody)
		empty := newFakeLLM(t) // 0 rounds: Served() must stay 0 after the run
		writeConfig(t, e.config, empty.URL()+"/v1")

		ingest := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", copied)
		if ingest.Code != 0 {
			t.Fatalf("lw ingest (duplicate only): exit %d, want 0\n%s", ingest.Code, ingest.Output)
		}
		if got := empty.Served(); got != 0 {
			t.Errorf("fake LLM served %d request(s), want 0 — the duplicate was not caught before the agent ran", got)
		}
		assertNoOpenChangeset(t, e, vault)
	})

	t.Run("batch_duplicate_is_skipped", func(t *testing.T) {
		e := newEnv(t)
		vault := newVault(t)
		body := "# Batch Ingest\n\nTwo files, one body, one command: the second must be skipped.\n"
		a := e.writeSource(t, "sources/batch/alpha.md", body)
		b := e.writeSource(t, "sources/batch/beta.md", body)
		fake := newFakeLLM(t,
			stageIngestScratchSSE("dup-batch", 1),
			stopSSE("dup-batch", "Staged the first source; lw skipped the second as its duplicate."),
		)
		writeConfig(t, e.config, fake.URL()+"/v1")

		ingest := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", a, b)
		if ingest.Code != 0 {
			t.Fatalf("lw ingest (batch duplicate): exit %d, want 0\n%s", ingest.Code, ingest.Output)
		}
		want := "skipped " + b + ": same content as " + a
		if !strings.Contains(ingest.Output, want) {
			t.Errorf("lw ingest (batch duplicate): output does not carry %q\n%s", want, ingest.Output)
		}
		reqs := fake.Requests()
		if len(reqs) == 0 {
			t.Fatalf("the fake LLM served no requests; ingest output:\n%s", ingest.Output)
		}
		if got := countUserIngestPaths(reqs[0].Body); got != 1 {
			t.Errorf("the agent was told to ingest %d source(s), want exactly the one survivor", got)
		}
	})
}

// TestReliabilityLintWarning is A-807's FINDING-3 end to end: an ingest
// whose changeset projects a lint regression says so, in one line, at
// ingest time — still exiting 0 and leaving the changeset open — and the
// lw commit it warned about then really does refuse.
func TestReliabilityLintWarning(t *testing.T) {
	e := newEnv(t)
	vault := newVault(t)
	// The page cites the raw path the staged m3-note lands at (so the only
	// projected error is the broken link) and carries a second, resolving
	// link, since create_page demands at least two outbound wikilinks.
	fake := newFakeLLM(t,
		stageIngestScratchSSE("lint1", 1),
		createPageSSE("lint2", `{"path":"wiki/concepts/broken-link-probe.md","title":"Broken Link Probe","type":"concept","tags":["inference","memory"],"sources":["raw/articles/m3-note.md"],"confidence":"high","contested":false,"body":"A page whose Related list points at a page the vault does not have.\n\n## Related\n\n- [[nowhere-page]] - a page the vault lacks.\n- [[note-0000]] - a page the vault has.\n","rationale":"Proves the ingest warning names a changeset lw commit will refuse."}`),
		stopSSE("lint2", "Staged the source and proposed the page; the broken link stays for review."),
	)
	writeConfig(t, e.config, fake.URL()+"/v1")

	src := e.writeSource(t, "sources/m3-note.md", m3NoteBody)

	ingest := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", src)

	t.Run("ingest_warns_that_commit_will_refuse", func(t *testing.T) {
		if ingest.Code != 0 {
			t.Fatalf("lw ingest: exit %d, want 0 (a lint regression is a warning here, not a failure)\n%s", ingest.Code, ingest.Output)
		}
		for _, want := range []string{
			"warning: lint regresses — ",
			"lw commit will refuse this (review with lw diff)",
		} {
			if !strings.Contains(ingest.Output, want) {
				t.Errorf("lw ingest: output does not carry %q\n%s", want, ingest.Output)
			}
		}
	})

	t.Run("commit_refuses_as_warned", func(t *testing.T) {
		commit := runLW(t, e, "commit", "--vault", vault, "-m", "x")
		if commit.Code == 0 {
			t.Fatalf("lw commit (broken link): exit 0, want non-zero\n%s", commit.Output)
		}
		if !strings.Contains(commit.Output, "lint regressed") {
			t.Errorf("lw commit (broken link): output does not name the lint refusal\n%s", commit.Output)
		}
	})
}
