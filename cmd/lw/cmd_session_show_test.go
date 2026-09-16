package main

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui/markdown"
)

// showFixture writes a vault with one open and one committed session, the
// shape the default-id and prefix-resolution tests share.
func showFixture(t *testing.T) string {
	t.Helper()
	root := newSessionVault(t)
	writeSession(t, root, "open", "cs-dddd444444444444",
		changesetJSON("cs-dddd444444444444", "2026-09-16T12:00:00Z"),
		srec("2026-09-16T12:00:01Z", "user", "open question"),
		srec("2026-09-16T12:00:05Z", "assistant", "open answer"),
	)
	writeSession(t, root, "committed", "cs-eeee555555555555",
		changesetJSON("cs-eeee555555555555", "2026-09-16T11:00:00Z"),
		srec("2026-09-16T11:00:01Z", "user", "committed question"),
	)
	return root
}

// ttyTerm overrides stdoutTerm with a fixed TTY, for the styled-output
// tests. Same seam and restore discipline as the diff --render tests.
func ttyTerm(t *testing.T, width int) {
	t.Helper()
	orig := stdoutTerm
	stdoutTerm = termProbe{
		isTTY: func() bool { return true },
		width: func() int { return width },
		dark:  func() bool { return true },
	}
	t.Cleanup(func() { stdoutTerm = orig })
}

func TestSessionShow(t *testing.T) {
	t.Run("bare_show_uses_the_open_changeset", func(t *testing.T) {
		root := showFixture(t)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if !strings.HasPrefix(stdout, "session cs-dddd444444444444 · open · 2026-09-16T12:00:00Z · 2 records\n") {
			t.Errorf("bare show did not print the open session's header first:\n%q", stdout)
		}
		if strings.Contains(stdout, "cs-eeee555555555555") {
			t.Errorf("bare show leaked the committed session:\n%s", stdout)
		}
	})

	t.Run("no_open_changeset_exits_1", func(t *testing.T) {
		root := newSessionVault(t)
		writeSession(t, root, "committed", "cs-eeee555555555555",
			changesetJSON("cs-eeee555555555555", "2026-09-16T11:00:00Z"),
			srec("2026-09-16T11:00:01Z", "user", "committed question"),
		)

		_, stderr, code := captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr, "lw session list") {
			t.Errorf("stderr = %q, want it to point at lw session list", stderr)
		}
	})

	t.Run("unknown_id_exits_1", func(t *testing.T) {
		root := showFixture(t)

		_, stderr, code := captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root, "cs-zzzz"})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr, "cs-zzzz") {
			t.Errorf("stderr = %q, want it to name the argument", stderr)
		}
	})

	t.Run("ambiguous_prefix_lists_candidates", func(t *testing.T) {
		root := showFixture(t)

		_, stderr, code := captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root, "cs-"})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		open, committed := "cs-dddd444444444444", "cs-eeee555555555555"
		for _, id := range []string{open, committed} {
			if !strings.Contains(stderr, id) {
				t.Errorf("stderr drops candidate %s: %q", id, stderr)
			}
		}
		if i, j := strings.Index(stderr, open), strings.Index(stderr, committed); i > j {
			t.Errorf("candidates not sorted in stderr: %q", stderr)
		}
	})

	t.Run("bare_hex_prefix_resolves", func(t *testing.T) {
		root := newSessionVault(t)
		writeSession(t, root, "committed", "cs-e349000000000001",
			changesetJSON("cs-e349000000000001", "2026-09-16T11:00:00Z"),
			srec("2026-09-16T11:00:01Z", "user", "hi"),
		)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root, "e349"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if !strings.HasPrefix(stdout, "session cs-e349000000000001 · committed · 2026-09-16T11:00:00Z · 1 record\n") {
			t.Errorf("hex prefix resolved to the wrong session:\n%q", stdout)
		}
	})

	t.Run("exact_id_beats_a_longer_match", func(t *testing.T) {
		root := newSessionVault(t)
		writeSession(t, root, "open", "cs-abcdef1",
			changesetJSON("cs-abcdef1", "2026-09-16T12:00:00Z"),
			srec("2026-09-16T12:00:01Z", "user", "legacy id"),
		)
		writeSession(t, root, "committed", "cs-abcdef1234567890",
			changesetJSON("cs-abcdef1234567890", "2026-09-16T11:00:00Z"),
			srec("2026-09-16T11:00:01Z", "user", "long id"),
		)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root, "cs-abcdef1"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if !strings.HasPrefix(stdout, "session cs-abcdef1 · open · 2026-09-16T12:00:00Z") {
			t.Errorf("exact id did not win against the longer id it prefixes:\n%q", stdout)
		}
	})

	t.Run("json_is_the_file_verbatim", func(t *testing.T) {
		root := newSessionVault(t)
		raw := `{"ts":"2026-09-16T08:00:00Z","role":"user","content":"hi","future_field":{"nested":[1,2]}}` + "\n"
		writeSession(t, root, "open", "cs-ffff666666666666",
			changesetJSON("cs-ffff666666666666", "2026-09-16T08:00:00Z"))
		writeSessionNDJSON(t, root, "open", "cs-ffff666666666666", raw)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root, "cs-f", "--json"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if stdout != raw {
			t.Errorf("--json re-encoded the file:\ngot  %q\nwant %q", stdout, raw)
		}
	})

	t.Run("plain_and_json_are_exclusive", func(t *testing.T) {
		root := showFixture(t)

		_, stderr, code := captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root, "--plain", "--json"})
		})
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
		if !strings.Contains(stderr, "--plain and --json are mutually exclusive") {
			t.Errorf("stderr = %q, want the mutual-exclusion message", stderr)
		}
	})

	t.Run("plain_has_no_escapes", func(t *testing.T) {
		root := newSessionVault(t)
		writeSession(t, root, "open", "cs-dddd444444444444",
			changesetJSON("cs-dddd444444444444", "2026-09-16T12:00:00Z"),
			srec("2026-09-16T12:00:01Z", "user", "plain question"),
			srec("2026-09-16T12:00:05Z", "assistant", "## Styled\n\nAn answer with a heading."),
		)
		ttyTerm(t, 60)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if !strings.Contains(stdout, "\x1b[") {
			t.Fatalf("TTY output carries no escape; the --plain assertion below would be vacuous:\n%q", stdout)
		}

		stdout, _, _ = captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root, "--plain"})
		})
		if strings.Contains(stdout, "\x1b") {
			t.Errorf("--plain output carries escapes on a TTY:\n%q", stdout)
		}
	})

	t.Run("answer_matches_render_fragment", func(t *testing.T) {
		root := newSessionVault(t)
		answer := "## Patch review\n\n- keeps the rule style\n- shares the renderer\n\n```go\nfmt.Println(\"shared\")\n```\n"
		writeSession(t, root, "open", "cs-dddd444444444444",
			changesetJSON("cs-dddd444444444444", "2026-09-16T12:00:00Z"),
			srec("2026-09-16T12:00:01Z", "user", "what does the patch buy us?"),
			srec("2026-09-16T12:00:05Z", "assistant", answer),
		)
		ttyTerm(t, 60)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}

		o, err := renderOptions()
		if err != nil {
			t.Fatalf("renderOptions: %v", err)
		}
		want, err := markdown.NewRenderer().RenderFragment([]byte(answer), markdown.Options{
			Width: o.width,
			Style: o.style,
			Plain: !o.tty,
		})
		if err != nil {
			t.Fatalf("RenderFragment: %v", err)
		}

		lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
		start := -1
		for i, l := range lines {
			if strings.HasPrefix(l, "── assistant ") {
				start = i + 2 // the rule, then its blank line
				break
			}
		}
		if start < 0 {
			t.Fatalf("no assistant rule in output:\n%s", stdout)
		}
		if start+len(want) > len(lines) {
			t.Fatalf("output ends before the answer block:\n%s", stdout)
		}
		for i, w := range want {
			if got := lines[start+i]; got != strings.TrimRight(w, " ") {
				t.Errorf("answer line %d = %q, want %q", i, got, strings.TrimRight(w, " "))
			}
		}
	})

	t.Run("thinking_is_folded_by_default", func(t *testing.T) {
		root := newSessionVault(t)
		writeSession(t, root, "open", "cs-dddd444444444444",
			changesetJSON("cs-dddd444444444444", "2026-09-16T12:00:00Z"),
			agentReasoningRecord("2026-09-16T12:00:05Z",
				"alpha thought\nbeta thought\ngamma thought", "The answer."),
		)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if !strings.Contains(stdout, "(thinking: 3 lines hidden; --thinking shows them)") {
			t.Errorf("folded marker missing:\n%s", stdout)
		}
		if strings.Contains(stdout, "alpha thought") {
			t.Errorf("reasoning leaked without --thinking:\n%s", stdout)
		}

		stdout, _, _ = captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root, "--thinking"})
		})
		if !strings.Contains(stdout, "  alpha thought") {
			t.Errorf("--thinking did not unfold the reasoning:\n%s", stdout)
		}
	})

	t.Run("pre005_session_has_no_thinking_marker", func(t *testing.T) {
		root := newSessionVault(t)
		writeSession(t, root, "open", "cs-dddd444444444444",
			changesetJSON("cs-dddd444444444444", "2026-09-16T12:00:00Z"),
			srec("2026-09-16T12:00:01Z", "user", "pre-005 question"),
			srec("2026-09-16T12:00:05Z", "assistant", "pre-005 answer"),
		)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if strings.Contains(stdout, "(thinking:") {
			t.Errorf("a session without reasoning keys grew a thinking marker:\n%s", stdout)
		}
	})

	t.Run("tool_args_and_results_in_full", func(t *testing.T) {
		root := newSessionVault(t)
		big := strings.Repeat("x", 10000)
		writeSession(t, root, "open", "cs-dddd444444444444",
			changesetJSON("cs-dddd444444444444", "2026-09-16T12:00:00Z"),
			srec("2026-09-16T12:00:01Z", "user", "run the tools"),
			stool("2026-09-16T12:00:05Z", "raw.read", `{"path":"raw/notes.md"}`, big),
			stool("2026-09-16T12:00:06Z", "stage.patch_page", `{"body":"first line\nsecond line"}`, "ok"),
			stool("2026-09-16T12:00:07Z", "raw.search", `{"zebra":1,"alpha":2}`, "ok"),
			stool("2026-09-16T12:00:08Z", "stage.patch_page", `{"opts":{"b":2,"a":[1]}}`, "ok"),
			stagedTool("2026-09-16T12:00:09Z", "stage.commit", `{"message":"m"}`, "staged"),
		)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root, "--plain"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if !strings.Contains(stdout, big) {
			t.Errorf("a 10000-byte result was truncated")
		}
		if !strings.Contains(stdout, "  body:\n    first line\n    second line\n") {
			t.Errorf("multi-line string arg not decoded line by line:\n%s", stdout)
		}
		if i, j := strings.Index(stdout, "  zebra: 1"), strings.Index(stdout, "  alpha: 2"); i < 0 || j < 0 || i > j {
			t.Errorf("top-level keys lost source order (zebra before alpha):\n%s", stdout)
		}
		if !strings.Contains(stdout, "  opts: {\"b\":2,\"a\":[1]}") {
			t.Errorf("nested object arg not compacted:\n%s", stdout)
		}
		if !strings.Contains(stdout, "── tool stage.commit · staged ") {
			t.Errorf("staged rule missing:\n%s", stdout)
		}
	})

	t.Run("golden_plain", func(t *testing.T) {
		root := writeGoldenFixtureVault(t)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root, goldenSessionID, "--plain"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		// The transcript's last record block ends with the format's
		// trailing blank line; testutil normalizes a golden file to
		// exactly one trailing newline, so the compared side is
		// normalized the same way.
		testutil.GoldenString(t, "testdata/session/show-plain.golden",
			strings.TrimRight(stdout, "\n")+"\n")
	})

	t.Run("golden_plain_thinking", func(t *testing.T) {
		root := writeGoldenFixtureVault(t)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root, goldenSessionID, "--plain", "--thinking"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		testutil.GoldenString(t, "testdata/session/show-plain-thinking.golden",
			strings.TrimRight(stdout, "\n")+"\n")
	})
}

// agentReasoningRecord builds an assistant record with fixed reasoning and
// content, for the folding tests.
func agentReasoningRecord(ts, reasoning, content string) agent.Record {
	return agent.Record{TS: sessionTS(ts), Role: "assistant", Reasoning: reasoning, Content: content}
}
