// reliability_test.go walks workflow 008's reliability fixes end to end
// (MASTER §5 T-H): a truncated agent turn rejects the ingest loudly instead
// of committing nothing (U1), two same-named untitled sources land at two
// raw/ paths and their raw-only changeset commits with a warning (U6), and
// raw.list answers a real query over the wire (U2). One scenario pins the
// doctor budget warn. Every LLM conversation is scripted against the fake
// server; no scenario relies on an ambient provider key.
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeConfigMaxTokens is writeConfig with an explicit llm.max_tokens, for
// the scenarios whose assertion reads the number back out of lw's output.
func writeConfigMaxTokens(t *testing.T, dir, baseURL string, maxTokens int) string {
	t.Helper()

	path := filepath.Join(dir, "lw", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("e2e: create %s: %v", filepath.Dir(path), err)
	}

	config := "[llm]\n" +
		"base_url = " + tomlQuote(baseURL) + "\n" +
		"model = \"e2e-fake\"\n" +
		"api_key = \"" + fakeAPIKey + "\"\n" +
		"max_tokens = " + fmt.Sprint(maxTokens) + "\n" +
		"\n[llm.limits]\n" +
		"max_tool_rounds = 24\n" +
		"context_tokens = 96000\n"
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("e2e: write %s: %v", path, err)
	}
	return path
}

// stageIngestSSE scripts one round that calls stage_ingest_source on uri and
// finishes with tool_calls — round 1 of every ingest scenario here. The shape
// is smoke_ingest_round1.sse's, with the uri a parameter because the
// reliability scenarios stage sources at paths of their own choosing.
func stageIngestSSE(id, uri string) fakeRound {
	arguments := strconv.Quote(fmt.Sprintf(`{"uri":"%s","kind":"article"}`, uri))
	call := `{"index":0,"id":"call_` + id + `","type":"function","function":{"name":"stage_ingest_source","arguments":` + arguments + `}}`
	body := "data: {\"id\":\"chatcmpl-" + id + "\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-" + id + "\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[" + call + "]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-" + id + "\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n"
	return fakeRound{Name: "stage_ingest_" + id, Body: body}
}

// stopSSE scripts one round of assistant prose ending in a clean stop — the
// round that closes a scripted turn.
func stopSSE(id, content string) fakeRound {
	text, _ := json.Marshal(content)
	body := "data: {\"id\":\"chatcmpl-" + id + "\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":" + string(text) + "},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-" + id + "\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n"
	return fakeRound{Name: "stop_" + id, Body: body}
}

// truncatedSSE scripts the W0-measured shape of an output-token truncation:
// the round streams only reasoning_content deltas, then ends
// finish_reason "length" with no tool call completed. Before 008 lw treated
// that as a clean stop and committed zero pages (U1).
func truncatedSSE(id string) fakeRound {
	body := "data: {\"id\":\"chatcmpl-" + id + "\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"The source is about caching; I should structure the page around eviction...\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-" + id + "\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n" +
		"data: [DONE]\n"
	return fakeRound{Name: "truncated_" + id, Body: body}
}

// rawListSSE scripts one round that calls raw_list with the given query
// arguments JSON and finishes with tool_calls.
func rawListSSE(id, args string) fakeRound {
	call := `{"index":0,"id":"call_` + id + `","type":"function","function":{"name":"raw_list","arguments":` + strconv.Quote(args) + `}}`
	body := "data: {\"id\":\"chatcmpl-" + id + "\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[" + call + "]},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-" + id + "\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n"
	return fakeRound{Name: "raw_list_" + id, Body: body}
}

// TestReliabilityTruncatedIngest is U1 end to end: round 1 stages the
// source, round 2 burns the whole budget on reasoning and stops with
// finish_reason "length". lw must exit non-zero naming the output limit, the
// changeset must be rejected, and the transcript must record the abnormal
// finish on the round's assistant record.
func TestReliabilityTruncatedIngest(t *testing.T) {
	e := newEnv(t)
	vault := newVault(t)
	fake := newFakeLLM(t,
		stageIngestSSE("trunc1", "sources/t/truncated.md"),
		truncatedSSE("trunc2"),
	)
	// 8192 is the pre-008 default and the budget a truncated round is
	// pinned against; the error text carries it verbatim.
	writeConfigMaxTokens(t, e.config, fake.URL()+"/v1", 8192)

	src := e.writeSource(t, "sources/t/truncated.md", "# Truncated Ingest\n\nA local source whose agent turn the fake cuts off mid-reasoning.\n")

	ingest := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", src)

	t.Run("exit_is_nonzero_and_names_output_limit", func(t *testing.T) {
		if ingest.Code == 0 {
			t.Fatalf("lw ingest (truncated round): exit 0, want non-zero\n%s", ingest.Output)
		}
		for _, want := range []string{
			"the output limit (llm.max_tokens = 8192",
			"the changeset was rejected",
		} {
			if !strings.Contains(ingest.Output, want) {
				t.Errorf("lw ingest (truncated round): output does not carry %q\n%s", want, ingest.Output)
			}
		}
		assertNoPanic(t, ingest.Output)
	})

	t.Run("changeset_is_rejected", func(t *testing.T) {
		assertNoOpenChangeset(t, e, vault)
	})

	t.Run("transcript_records_finish", func(t *testing.T) {
		matches, err := filepath.Glob(filepath.Join(vault, ".llmwiki", "changesets", "rejected", "*", "session.ndjson"))
		if err != nil {
			t.Fatalf("glob rejected sessions: %v", err)
		}
		if len(matches) == 0 {
			t.Fatalf("no rejected changeset carries a session.ndjson\n%s", ingest.Output)
		}
		b, err := os.ReadFile(matches[0])
		if err != nil {
			t.Fatalf("read %s: %v", matches[0], err)
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		var rec struct {
			Role   string `json:"role"`
			Finish string `json:"finish"`
		}
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &rec); err != nil {
			t.Fatalf("last line of %s does not decode: %v\n%s", matches[0], err, lines[len(lines)-1])
		}
		if rec.Finish != "length" {
			t.Errorf("last transcript record of %s carries finish %q, want \"length\"", matches[0], rec.Finish)
		}
	})
}

// TestReliabilityUntitledCollision is U6 end to end. Two different untitled
// sources named notes.md (under sources/x/ and sources/y/) must land at two
// raw/ paths — the second suffixed -2 — and each raw-only changeset must
// say, in one way or another, that it commits no pages.
func TestReliabilityUntitledCollision(t *testing.T) {
	e := newEnv(t)
	vault := newVault(t)

	srcX := e.writeSource(t, "sources/x/notes.md",
		"Loose reading notes with no heading at all, so the raw source is named\nfrom the file it came from rather than from a title.\n")
	srcY := e.writeSource(t, "sources/y/notes.md",
		"A second, deliberately different scratch pad: the body sha must not\nmatch the first source, or the ingest would dedupe instead of colliding.\n")

	fake1 := newFakeLLM(t,
		stageIngestSSE("coll1", "sources/x/notes.md"),
		stopSSE("coll1", "Staged the untitled notes; no page proposed, so the changeset is raw-only."),
	)
	writeConfig(t, e.config, fake1.URL()+"/v1")

	ingest1 := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", srcX)
	if ingest1.Code != 0 {
		t.Fatalf("lw ingest (first untitled source): exit %d, want 0\n%s", ingest1.Code, ingest1.Output)
	}
	if !strings.Contains(ingest1.Output, "warning: 0 pages proposed — only raw source(s) staged; review before committing") {
		t.Errorf("lw ingest (first untitled source): output does not carry the raw-only warning\n%s", ingest1.Output)
	}

	t.Run("raw_only_commit_warns_then_commits", func(t *testing.T) {
		commit := runLW(t, e, "commit", "--vault", vault, "-m", "raw-only: untitled notes")
		if commit.Code != 0 {
			t.Fatalf("lw commit (raw-only changeset): exit %d, want 0\n%s", commit.Code, commit.Output)
		}
		want := "warning: 0 pages proposed — committing raw source(s) only: raw/articles/notes.md"
		if !strings.Contains(commit.Stderr, want) {
			t.Errorf("lw commit (raw-only changeset): stderr does not carry %q\nstderr:\n%s", want, commit.Stderr)
		}
	})

	t.Run("log_md_names_raw_paths", func(t *testing.T) {
		b, err := os.ReadFile(filepath.Join(vault, "log.md"))
		if err != nil {
			t.Fatalf("read log.md: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		last := lines[len(lines)-1]
		if !strings.Contains(last, "→ raw/articles/notes.md") {
			t.Errorf("log.md's last line does not name the ingested raw path:\n%s", last)
		}
	})

	// The second ingest points at its own fake (the first script is spent),
	// so the config is rewritten to the new endpoint.
	fake2 := newFakeLLM(t,
		stageIngestSSE("coll2", "sources/y/notes.md"),
		stopSSE("coll2", "Staged the second untitled notes; again raw-only."),
	)
	writeConfig(t, e.config, fake2.URL()+"/v1")

	ingest2 := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", srcY)

	t.Run("second_source_lands_at_suffix_2", func(t *testing.T) {
		if ingest2.Code != 0 {
			t.Fatalf("lw ingest (second untitled source): exit %d, want 0\n%s", ingest2.Code, ingest2.Output)
		}
		if !strings.Contains(ingest2.Output, "warning: 0 pages proposed — only raw source(s) staged; review before committing") {
			t.Errorf("lw ingest (second untitled source): output does not carry the raw-only warning\n%s", ingest2.Output)
		}
		diff := runLW(t, e, "diff", "--vault", vault, "--stat")
		if diff.Code != 0 {
			t.Fatalf("lw diff --stat: exit %d, want 0\n%s", diff.Code, diff.Output)
		}
		if !strings.Contains(diff.Output, "raw/articles/notes-2.md") {
			t.Errorf("lw diff --stat: output does not name raw/articles/notes-2.md\n%s", diff.Output)
		}
	})
}

// TestReliabilityRawListOverWire is U2 end to end: after one committed raw
// source, `lw query` scripted to call raw_list with an uppercased word from
// the source's title must get the row back — the tool result the fake
// records for round 2 names the raw path.
func TestReliabilityRawListOverWire(t *testing.T) {
	e := newEnv(t)
	vault := newVault(t)

	// Phase 1: commit one raw source whose title the query can filter on.
	src := e.writeSource(t, "sources/q/attention-notes.md",
		"# Attention Notes\n\nA titled source, so raw.list's row carries a title and the\nquery can be a word from it.\n")
	fake1 := newFakeLLM(t,
		stageIngestSSE("rl-pre", "sources/q/attention-notes.md"),
		stopSSE("rl-pre", "Staged the titled notes."),
	)
	writeConfig(t, e.config, fake1.URL()+"/v1")
	ingest := runLW(t, e, "ingest", "--vault", vault, "--kind", "article", src)
	if ingest.Code != 0 {
		t.Fatalf("lw ingest: exit %d, want 0\n%s", ingest.Code, ingest.Output)
	}
	commit := runLW(t, e, "commit", "--vault", vault, "-m", "one titled raw source")
	if commit.Code != 0 {
		t.Fatalf("lw commit: exit %d, want 0\n%s", commit.Code, commit.Output)
	}

	// Phase 2: a query whose only tool round calls raw_list with a word
	// from the title, uppercased — matching is case-insensitive.
	fake2 := newFakeLLM(t,
		rawListSSE("rl-q", `{"query":"ATTENTION"}`),
		stopSSE("rl-q", "The vault holds one raw source about attention."),
	)
	writeConfig(t, e.config, fake2.URL()+"/v1")

	query := runLW(t, e, "query", "--vault", vault, "Which raw sources about attention does the vault hold?")
	if query.Code != 0 {
		t.Fatalf("lw query: exit %d, want 0\n%s", query.Code, query.Output)
	}

	t.Run("tool_result_names_the_raw_path", func(t *testing.T) {
		reqs := fake2.Requests()
		if len(reqs) != 2 {
			t.Fatalf("fake LLM recorded %d requests, want 2 (one raw_list round, one answer)", len(reqs))
		}
		// Round 2's request carries the conversation as it stands after the
		// raw_list tool result was appended — the only place the row (and
		// its path) can appear on the wire.
		want := "raw/articles/attention-notes.md"
		if !strings.Contains(string(reqs[1].Body), want) {
			t.Errorf("round 2's request body does not name %s\n%s", want, reqs[1].Body)
		}
	})
}

// TestReliabilityDoctorBudget pins the llm budget warn through the real
// binary: an explicit max_tokens = 8192 draws the "llm budget" check's warn
// naming the 16000 floor, while doctor still exits 0 — a warn is a pass the
// user is meant to read, not a failure (cmd_doctor.go's checkLLMBudget).
func TestReliabilityDoctorBudget(t *testing.T) {
	t.Run("low_budget_warns_exit_zero", func(t *testing.T) {
		e := newEnv(t)
		vault := newVault(t)
		fake := newFakeLLM(t, fakeRound{Name: "doctor_probe_budget", Body: doctorProbeSSE})
		writeConfigMaxTokens(t, e.config, fake.URL()+"/v1", 8192)

		res := runLW(t, e, "doctor", "--vault", vault)
		if res.Code != 0 {
			t.Fatalf("lw doctor (max_tokens 8192): exit %d, want 0 (a warn is not a failure)\n%s", res.Code, res.Output)
		}
		var line string
		for _, l := range strings.Split(res.Output, "\n") {
			if strings.Contains(l, "llm budget") {
				line = l
				break
			}
		}
		if line == "" {
			t.Fatalf("lw doctor: no llm budget check line in output\n%s", res.Output)
		}
		if !strings.Contains(line, "below 16000") {
			t.Errorf("lw doctor: llm budget line does not name the 16000 floor:\n%s", line)
		}
		if !strings.HasPrefix(line, "!") {
			t.Errorf("lw doctor: llm budget line is not a warn (%q)", line)
		}
	})
}
