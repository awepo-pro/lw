package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/awepo-pro/lw/internal/testutil"
)

// F1: normalizeInstruction(s) (string, error)
func TestNormalizeInstruction(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
		errSub  string
	}{
		{
			name:  "trimmed whitespace",
			input: "  keep it short \n",
			want:  "keep it short",
		},
		{
			name:  "crlf to lf",
			input: "a\r\nb",
			want:  "a\nb",
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			input:   "   ",
			wantErr: true,
		},
		{
			name:    "nul byte alone",
			input:   "\x00",
			wantErr: true,
		},
		{
			name:    "nul byte in text",
			input:   "a\x00b",
			wantErr: true,
		},
		{
			name:    "control char x01",
			input:   "a\x01b",
			wantErr: true,
		},
		{
			name:  "tab and newline allowed",
			input: "a\tb\nc",
			want:  "a\tb\nc",
		},
		{
			name:  "exactly 2000 runes",
			input: strings.Repeat("x", 2000),
			want:  strings.Repeat("x", 2000),
		},
		{
			name:    "2001 runes error naming 2000 and 2001",
			input:   strings.Repeat("x", 2001),
			wantErr: true,
			errSub:  "2000",
		},
		{
			name:  "2000 multibyte runes",
			input: strings.Repeat("\u00e9", 2000),
			want:  strings.Repeat("\u00e9", 2000),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeInstruction(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("normalizeInstruction(%q) expected error, got nil", tc.input)
				}
				if tc.name == "2001 runes error naming 2000 and 2001" {
					msg := err.Error()
					if !strings.Contains(msg, "2000") || !strings.Contains(msg, "2001") {
						t.Errorf("error %q must name 2000 and 2001", msg)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeInstruction(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("normalizeInstruction(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// F2: record round-trip
func TestPrepareRecordInstructionRoundTrip(t *testing.T) {
	text := "source: raw/articles/a.md\nline 2 with \"quotes\" and \\backslash"
	p := prepareRecord{
		agent:       "agy",
		instruction: text,
		joined:      true,
		entries: []prepareEntry{
			{kind: "source", path: "raw/articles/x.md", chunks: 2},
		},
	}

	rendered := prepareRecordText(p)
	lines := strings.Split(rendered, "\n")
	if len(lines) < 4 {
		t.Fatalf("expected at least 4 lines, got:\n%s", rendered)
	}
	wantLine2 := "instruction: " + strconv.Quote(text)
	if lines[1] != wantLine2 {
		t.Errorf("line 2 = %q, want %q", lines[1], wantLine2)
	}
	if lines[0] != "lw ingest --prepare (agent: agy)" {
		t.Errorf("line 0 = %q, want head", lines[0])
	}
	if lines[2] != "source: raw/articles/x.md (2 chunk(s))" {
		t.Errorf("line 2 entry = %q", lines[2])
	}
	if lines[3] != "changeset: joined" {
		t.Errorf("last line = %q, want changeset: joined", lines[3])
	}

	parsed, ok := parsePrepareRecord(rendered)
	if !ok {
		t.Fatalf("parsePrepareRecord failed on:\n%s", rendered)
	}
	if parsed.agent != p.agent {
		t.Errorf("agent = %q, want %q", parsed.agent, p.agent)
	}
	if parsed.instruction != p.instruction {
		t.Errorf("instruction = %q, want %q", parsed.instruction, p.instruction)
	}
	if parsed.joined != p.joined {
		t.Errorf("joined = %v, want %v", parsed.joined, p.joined)
	}
	if len(parsed.entries) != len(p.entries) || parsed.entries[0] != p.entries[0] {
		t.Errorf("entries = %+v, want %+v", parsed.entries, p.entries)
	}

	// Record without an instruction renders exactly as base and parses with instruction == ""
	pNoInst := prepareRecord{
		agent:  "agy",
		joined: false,
		entries: []prepareEntry{
			{kind: "source", path: "raw/articles/x.md", chunks: 1},
		},
	}
	renderedNoInst := prepareRecordText(pNoInst)
	wantBase := "lw ingest --prepare (agent: agy)\nsource: raw/articles/x.md (1 chunk(s))"
	if renderedNoInst != wantBase {
		t.Errorf("rendered without instruction:\ngot:  %q\nwant: %q", renderedNoInst, wantBase)
	}
	parsedNoInst, ok := parsePrepareRecord(renderedNoInst)
	if !ok || parsedNoInst.instruction != "" {
		t.Errorf("parse without instruction: ok=%v, instruction=%q", ok, parsedNoInst.instruction)
	}

	// Strictness checks
	t.Run("two_instruction_lines", func(t *testing.T) {
		bad := "lw ingest --prepare (agent: agy)\ninstruction: \"first\"\ninstruction: \"second\"\nsource: raw/x.md (1 chunk(s))"
		if _, ok := parsePrepareRecord(bad); ok {
			t.Errorf("parsePrepareRecord should reject record with two instruction lines")
		}
	})

	t.Run("instruction_after_entry", func(t *testing.T) {
		bad := "lw ingest --prepare (agent: agy)\nsource: raw/x.md (1 chunk(s))\ninstruction: \"late\""
		if _, ok := parsePrepareRecord(bad); ok {
			t.Errorf("parsePrepareRecord should reject record with instruction after entry")
		}
	})

	t.Run("unquoted_instruction", func(t *testing.T) {
		bad := "lw ingest --prepare (agent: agy)\ninstruction: foo\nsource: raw/x.md (1 chunk(s))"
		if _, ok := parsePrepareRecord(bad); ok {
			t.Errorf("parsePrepareRecord should reject record with unquoted instruction")
		}
	})
}

// F3: brief
func TestPrepareBriefInstructionRender(t *testing.T) {
	b := prepareBrief{
		changeset: "cs-1234567812345678",
		joined:    false,
		agent:     "agy",
		sources: []preparedSource{
			{path: "raw/articles/sample.md", chunks: 1, title: "Sample", original: "sample.md"},
		},
		instructions: []string{"only chapter 3; tag it cuda"},
		orientation:  "Vault orientation text.\n",
	}

	out := b.render()

	// Contains heading ## User instruction between ## Sources to compile and ## How to work
	idxSources := strings.Index(out, "## Sources to compile")
	idxUserInst := strings.Index(out, "## User instruction")
	idxHowToWork := strings.Index(out, "## How to work")

	if idxSources < 0 || idxUserInst < 0 || idxHowToWork < 0 {
		t.Fatalf("missing headings in brief:\n%s", out)
	}
	if !(idxSources < idxUserInst && idxUserInst < idxHowToWork) {
		t.Errorf("headings out of order: sources=%d, user instruction=%d, how to work=%d", idxSources, idxUserInst, idxHowToWork)
	}

	// Precedence sentence verbatim
	wantSteers := "It steers WHAT to cover and HOW"
	wantOverrides := "never overrides the Curator rules"
	if !strings.Contains(out, wantSteers) || !strings.Contains(out, wantOverrides) {
		t.Errorf("precedence sentence missing from brief:\n%s", out)
	}

	// Text quoted with >
	if !strings.Contains(out, "> only chapter 3; tag it cuda") {
		t.Errorf("instruction text not quoted with '> ':\n%s", out)
	}

	// Multi-line instruction: every line prefixed
	bMulti := b
	bMulti.instructions = []string{"line 1\nline 2"}
	outMulti := bMulti.render()
	if !strings.Contains(outMulti, "> line 1\n> line 2\n") {
		t.Errorf("multiline instruction lines not each prefixed:\n%s", outMulti)
	}

	// Two instructions (a join) appear in order under one heading, separated by a blank line
	bTwo := b
	bTwo.instructions = []string{"instruction one", "instruction two"}
	outTwo := bTwo.render()
	idxHeading := strings.Index(outTwo, "## User instruction")
	lastHeading := strings.LastIndex(outTwo, "## User instruction")
	if idxHeading != lastHeading {
		t.Errorf("expected exactly one ## User instruction heading, found multiple")
	}
	wantTwoBlock := "> instruction one\n\n> instruction two"
	if !strings.Contains(outTwo, wantTwoBlock) {
		t.Errorf("two instructions not in order separated by blank line:\n%s", outTwo)
	}

	// With no instruction: render equals base render byte for byte
	bNoInst := b
	bNoInst.instructions = nil
	outNoInst := bNoInst.render()
	if strings.Contains(outNoInst, "## User instruction") {
		t.Errorf("brief without instruction should not contain ## User instruction")
	}
}

// F4: CLI
func TestInstructionCLI(t *testing.T) {
	prepHermetic(t)
	prepNoAgent(t)

	// --instruction "x" -> exit 0; stdout has section; lw tool --brief prints it; session show shows instruction: line
	t.Run("instruction_flag_success", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		src := prepSource(t, "src.md", "Source Title", 500)

		stdout, stderr, code := prepareRun(t, root, "--instruction", "only chapter 3; tag it cuda", src)
		if code != 0 {
			t.Fatalf("prepareRun code = %d, want 0; stderr=%q", code, stderr)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want empty", stderr)
		}
		if !strings.Contains(stdout, "## User instruction") || !strings.Contains(stdout, "> only chapter 3; tag it cuda") {
			t.Fatalf("stdout missing user instruction section:\n%s", stdout)
		}

		// lw tool --brief prints it again
		toolOut, toolErr, toolCode := captureRun(t, func() int {
			return run([]string{"tool", "--vault", root, "--brief"})
		})
		if toolCode != 0 {
			t.Fatalf("tool --brief exit %d: %s", toolCode, toolErr)
		}
		if !strings.Contains(toolOut, "## User instruction") || !strings.Contains(toolOut, "> only chapter 3; tag it cuda") {
			t.Fatalf("tool --brief missing section:\n%s", toolOut)
		}

		// lw session show <cs> shows the instruction: record line
		cs := rcOpenChangeset(t, root)
		sessOut, sessErr, sessCode := captureRun(t, func() int {
			return run([]string{"session", "show", "--plain", "--vault", root, cs.ID})
		})
		if sessCode != 0 {
			t.Fatalf("session show exit %d: %s", sessCode, sessErr)
		}
		wantRecLine := "instruction: " + strconv.Quote("only chapter 3; tag it cuda")
		if !strings.Contains(sessOut, wantRecLine) {
			t.Fatalf("session show missing record line %q; got:\n%s", wantRecLine, sessOut)
		}
	})

	// --instruction-file f reads the file; --instruction-file - reads stdin
	t.Run("instruction_file_and_stdin", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		src := prepSource(t, "src.md", "Source Title", 500)
		instPath := filepath.Join(t.TempDir(), "inst.txt")
		if err := os.WriteFile(instPath, []byte("instruction from file"), 0o644); err != nil {
			t.Fatal(err)
		}

		stdout, stderr, code := prepareRun(t, root, "--instruction-file", instPath, src)
		if code != 0 {
			t.Fatalf("prepareRun with --instruction-file code = %d, want 0; stderr=%q", code, stderr)
		}
		if !strings.Contains(stdout, "> instruction from file") {
			t.Fatalf("stdout missing file instruction: %s", stdout)
		}

		// stdin via withStdin seam
		root2 := testutil.CopyFixture(t, "minimal")
		src2 := prepSource(t, "src2.md", "Source Title 2", 500)
		withStdin(t, "instruction from stdin\n")
		stdout2, stderr2, code2 := prepareRun(t, root2, "--instruction-file", "-", src2)
		if code2 != 0 {
			t.Fatalf("prepareRun with stdin code = %d, want 0; stderr=%q", code2, stderr2)
		}
		if !strings.Contains(stdout2, "> instruction from stdin") {
			t.Fatalf("stdout missing stdin instruction: %s", stdout2)
		}
	})

	// Mutually exclusive flags -> exit 2
	t.Run("both_flags_error", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		src := prepSource(t, "src.md", "Source Title", 500)
		instPath := filepath.Join(t.TempDir(), "inst.txt")
		if err := os.WriteFile(instPath, []byte("file text"), 0o644); err != nil {
			t.Fatal(err)
		}

		_, stderr, code := prepareRun(t, root, "--instruction", "direct text", "--instruction-file", instPath, src)
		if code != 2 {
			t.Fatalf("both flags code = %d, want 2", code)
		}
		if !strings.Contains(stderr, "lw: ingest: cannot use --instruction with --instruction-file") {
			t.Fatalf("stderr = %q, want mutually exclusive error", stderr)
		}
	})

	// Refusals and vault untouched
	t.Run("refusals_and_vault_untouched", func(t *testing.T) {
		refusalCases := []struct {
			name       string
			args       func(root, src string) []string
			wantCode   int
			wantErrSub string
		}{
			{
				name: "--instruction without --prepare",
				args: func(root, src string) []string {
					return []string{"ingest", "--vault", root, "--instruction", "foo", src}
				},
				wantCode:   2,
				wantErrSub: "lw: ingest: --instruction needs --prepare",
			},
			{
				name: "--instruction-file without --prepare",
				args: func(root, src string) []string {
					return []string{"ingest", "--vault", root, "--instruction-file", "foo.txt", src}
				},
				wantCode:   2,
				wantErrSub: "lw: ingest: --instruction-file needs --prepare",
			},
			{
				name: "empty instruction",
				args: func(root, src string) []string {
					return []string{"ingest", "--prepare", "--vault", root, "--instruction", "   ", src}
				},
				wantCode:   2,
				wantErrSub: "instruction cannot be empty",
			},
			{
				name: "nul instruction",
				args: func(root, src string) []string {
					return []string{"ingest", "--prepare", "--vault", root, "--instruction", "a\x00b", src}
				},
				wantCode:   2,
				wantErrSub: "control character",
			},
			{
				name: "over-long instruction",
				args: func(root, src string) []string {
					return []string{"ingest", "--prepare", "--vault", root, "--instruction", strings.Repeat("x", 2001), src}
				},
				wantCode:   2,
				wantErrSub: "2000",
			},
			{
				name: "missing file",
				args: func(root, src string) []string {
					return []string{"ingest", "--prepare", "--vault", root, "--instruction-file", filepath.Join(root, "non-existent.txt"), src}
				},
				wantCode:   1,
				wantErrSub: "non-existent.txt",
			},
		}

		for _, rc := range refusalCases {
			t.Run(rc.name, func(t *testing.T) {
				root := testutil.CopyFixture(t, "minimal")
				src := prepSource(t, "src.md", "Source Title", 500)
				dBefore := treeDigest(t, root)

				_, stderr, code := captureRun(t, func() int {
					return run(rc.args(root, src))
				})
				if code != rc.wantCode {
					t.Fatalf("code = %d, want %d; stderr=%q", code, rc.wantCode, stderr)
				}
				if !strings.Contains(stderr, rc.wantErrSub) {
					t.Errorf("stderr = %q, want containing %q", stderr, rc.wantErrSub)
				}

				dAfter := treeDigest(t, root)
				if dBefore != dAfter {
					t.Fatalf("vault was modified during refusal; digest before=%s, after=%s", dBefore, dAfter)
				}
			})
		}
	})

	// Two prepares into one open changeset (second joins) with different instructions:
	// brief of second lists both in order; --brief too.
	t.Run("two_prepares_join", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		src1 := prepSource(t, "src1.md", "Source One", 500)
		src2 := prepSource(t, "src2.md", "Source Two", 500)

		_, _, code1 := prepareRun(t, root, "--instruction", "first instruction", src1)
		if code1 != 0 {
			t.Fatalf("first prepare failed")
		}

		stdout2, stderr2, code2 := prepareRun(t, root, "--instruction", "second instruction", src2)
		if code2 != 0 {
			t.Fatalf("second prepare (join) failed: %d, stderr=%q", code2, stderr2)
		}

		wantJoinSection := "> first instruction\n\n> second instruction"
		if !strings.Contains(stdout2, wantJoinSection) {
			t.Fatalf("joined brief missing instructions in order:\n%s", stdout2)
		}

		// lw tool --brief also prints both in order
		briefOut, _, briefCode := captureRun(t, func() int {
			return run([]string{"tool", "--vault", root, "--brief"})
		})
		if briefCode != 0 {
			t.Fatalf("tool --brief failed: %d", briefCode)
		}
		if !strings.Contains(briefOut, wantJoinSection) {
			t.Fatalf("tool --brief missing joined instructions:\n%s", briefOut)
		}
	})
}

// F5: intent
func TestInstructionIntent(t *testing.T) {
	prepHermetic(t)
	prepNoAgent(t)

	// With instruction: intent = base intent + " \u2014 note: " + first line cut to 60 runes
	t.Run("intent_with_instruction", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		src := prepSource(t, "src.md", "Source Title", 500)

		_, _, code := prepareRun(t, root, "--instruction", "focus on chapter 3\nand ignore chapter 4", src)
		if code != 0 {
			t.Fatalf("prepareRun failed: %d", code)
		}
		cs := rcOpenChangeset(t, root)
		wantIntent := "ingest " + src + " \u2014 note: focus on chapter 3"
		if cs.Intent != wantIntent {
			t.Errorf("intent = %q, want %q", cs.Intent, wantIntent)
		}
	})

	// No instruction: intent equal to base
	t.Run("intent_without_instruction", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		src := prepSource(t, "src.md", "Source Title", 500)

		_, _, code := prepareRun(t, root, src)
		if code != 0 {
			t.Fatalf("prepareRun failed: %d", code)
		}
		cs := rcOpenChangeset(t, root)
		wantIntent := "ingest " + src
		if cs.Intent != wantIntent {
			t.Errorf("intent = %q, want %q", cs.Intent, wantIntent)
		}
	})

	// 300-rune first line: cut to 60 runes ("..." when cut)
	t.Run("long_first_line_and_join_bounds", func(t *testing.T) {
		root := testutil.CopyFixture(t, "minimal")
		src1 := prepSource(t, "src1.md", "Source Title 1", 500)
		src2 := prepSource(t, "src2.md", "Source Title 2", 500)

		longLine := strings.Repeat("z", 300)
		_, _, code1 := prepareRun(t, root, "--instruction", longLine+"\nline 2", src1)
		if code1 != 0 {
			t.Fatalf("prepareRun 1 failed: %d", code1)
		}
		cs1 := rcOpenChangeset(t, root)
		wantTag := " \u2014 note: " + strings.Repeat("z", 60) + "\u2026"
		wantIntent1 := "ingest " + src1 + wantTag
		if cs1.Intent != wantIntent1 {
			t.Errorf("intent = %q, want %q", cs1.Intent, wantIntent1)
		}

		// Join second prepare with another long line into open changeset
		_, _, code2 := prepareRun(t, root, "--instruction", strings.Repeat("y", 300), src2)
		if code2 != 0 {
			t.Fatalf("prepareRun 2 (join) failed: %d", code2)
		}
		csJoined := rcOpenChangeset(t, root)
		// Total intent after join stays within maxIntentRunes (200)
		if n := utf8.RuneCountInString(csJoined.Intent); n > 200 {
			t.Errorf("total intent runes = %d, want <= 200: %q", n, csJoined.Intent)
		}
	})
}

// F6: request bytes
func TestInstructionInProcessUnchanged(t *testing.T) {
	prepHermetic(t)
	root := testutil.CopyFixture(t, "minimal")
	src := prepSource(t, "src.md", "Source Title", 500)

	// 1. --instruction without --prepare is rejected for in-process ingest
	_, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, "--instruction", "focus on x", src})
	})
	if code != 2 {
		t.Fatalf("in-process with --instruction code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "lw: ingest: --instruction needs --prepare") {
		t.Fatalf("stderr = %q, want --instruction needs --prepare", stderr)
	}

	// 2. Normal in-process ingest sends the exact same message bytes
	rec := withRecordingIngestAgent(t, oneIngestOp(), nil)
	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"ingest", "--vault", root, src})
	})
	if code != 0 {
		t.Fatalf("in-process exit %d: stderr=%s\nstdout=%s", code, stderr, stdout)
	}
	if rec.gotMsg == "" {
		t.Fatalf("agent received empty message")
	}
	if strings.Contains(rec.gotMsg, "note:") || strings.Contains(rec.gotMsg, "instruction") {
		t.Fatalf("agent received instruction in message bytes: %q", rec.gotMsg)
	}
}
