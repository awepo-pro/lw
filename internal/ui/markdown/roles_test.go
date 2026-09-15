package markdown

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// roleStyle and roleStyleLight are the built-in adaptive palette with the
// two W5 tokens (contract §3 note 1, eleven tokens), the literals every
// TestRoleColours/TestChromaStylePerPalette/TestMemoKeyWholeStyle subtest
// uses unless it says otherwise.
var roleStyle = Style{
	Dark: true, Fg: "#D8DDE4", Muted: "#8C95A2", Faint: "#5E6672", Border: "#353C47",
	Accent: "#7AB2F2", Good: "#6BC28E", Warn: "#E2B45A", Bad: "#EF7F76",
	Heading: "#C3A0F0", Code: "#6CC7C9",
}

var roleStyleLight = Style{
	Dark: false, Fg: "#1C2128", Muted: "#586270", Faint: "#8A929E", Border: "#C6CDD6",
	Accent: "#1D62C2", Good: "#1D7A4B", Warn: "#93660A", Bad: "#B03A33",
	Heading: "#7A45C2", Code: "#17727A",
}

// roleSGR is the exact x/ansi "set" sequence roleStyle's hex produces for
// one optional attribute suffix ("", "1" bold, "3" italic, "4" underline).
func roleSGR(hex, attr string) string {
	return "\x1b[38;2;" + hexRGB(hex) + attr + "m"
}

// hexRGB converts a "#RRGGBB" test literal to "R;G;B" (decimal). It panics
// on a malformed literal: these are test constants, not input.
func hexRGB(hex string) string {
	var r, g, b int
	if len(hex) != 7 || hex[0] != '#' {
		panic("bad test hex " + hex)
	}
	if _, err := fmt.Sscanf(hex, "#%2x%2x%2x", &r, &g, &b); err != nil {
		panic("bad test hex " + hex + ": " + err.Error())
	}
	return fmt.Sprintf("%d;%d;%d", r, g, b)
}

// activeSGR returns every SGR sequence in force immediately before the
// first occurrence of needle in s: the concatenation of the sequences seen
// since the last reset (a reset clears the run). Non-SGR bytes pass without
// changing the state. "" when the needle is missing or unstyled.
func activeSGR(s, needle string) string {
	return activeSGROccurrence(s, needle, strings.Index)
}

// activeSGRLast is activeSGR at the LAST occurrence of needle — for
// needles whose first textual occurrence may sit inside an escape sequence
// itself (a URL inside its OSC-8 pair, say).
func activeSGRLast(s, needle string) string {
	return activeSGROccurrence(s, needle, strings.LastIndex)
}

func activeSGROccurrence(s, needle string, index func(string, string) int) string {
	idx := index(s, needle)
	if idx < 0 {
		return ""
	}
	active := ""
	for i := 0; i < idx; {
		seq, n, isReset, ok := decodeSGR(s[i:idx])
		if !ok {
			_, size := utf8.DecodeRuneInString(s[i:idx])
			i += size
			continue
		}
		if isReset {
			active = ""
		} else {
			active += seq
		}
		i += n
	}
	return active
}

// renderRoles renders src at width 80 with style and joins the lines.
func renderRoles(t *testing.T, src string, style Style) string {
	t.Helper()
	lines, err := NewRenderer().Render([]byte(src), Options{Width: 80, Style: style})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return strings.Join(lines, "\n")
}

func TestRoleColours(t *testing.T) {
	t.Run("h1_heading_bold", func(t *testing.T) {
		got := renderRoles(t, "# Container groups\n", roleStyle)
		// glamour re-styles per word around a wrap point, so assert the SGR
		// in force at the cells of a word, not a whole-line substring.
		if active := activeSGR(got, "Container"); active != roleSGR(roleStyle.Heading, ";1") {
			t.Errorf("H1 active SGR = %q, want Heading+bold %q: %q", active, roleSGR(roleStyle.Heading, ";1"), got)
		}
		if active := activeSGR(got, "groups"); active != roleSGR(roleStyle.Heading, ";1") {
			t.Errorf("H1 second word active SGR = %q, want Heading+bold %q: %q", active, roleSGR(roleStyle.Heading, ";1"), got)
		}
	})

	t.Run("h2_accent_bold", func(t *testing.T) {
		got := renderRoles(t, "## How routing works\n", roleStyle)
		if active := activeSGR(got, "How"); active != roleSGR(roleStyle.Accent, ";1") {
			t.Errorf("H2 active SGR = %q, want Accent+bold %q: %q", active, roleSGR(roleStyle.Accent, ";1"), got)
		}
	})

	t.Run("h3_heading_not_bold", func(t *testing.T) {
		got := renderRoles(t, "### Deep detail\n", roleStyle)
		if active := activeSGR(got, "Deep"); active != roleSGR(roleStyle.Heading, "") {
			t.Errorf("H3 active SGR = %q, want exactly the Heading colour (no bold): %q", active, got)
		}
	})

	t.Run("code_span_code_token", func(t *testing.T) {
		got := renderRoles(t, "Set `eth0` up.\n", roleStyle)
		if want := roleSGR(roleStyle.Code, "") + "eth0"; !strings.Contains(got, want) {
			t.Errorf("code span not Code: want %q in %q", want, got)
		}
	})

	t.Run("links_accent_underline", func(t *testing.T) {
		src := "Wiki says [[veth-pairs]] and the [spec](https://example.com/a) agrees.\n"
		got := renderRoles(t, src, roleStyle)
		if want := roleSGR(roleStyle.Accent, ";4") + "veth"; !strings.Contains(got, want) {
			t.Errorf("wikilink text not Accent+underline: want %q in %q", want, got)
		}
		if want := roleSGR(roleStyle.Accent, ";4") + "spec"; !strings.Contains(got, want) {
			t.Errorf("real link text not Accent+underline: want %q in %q", want, got)
		}
		// The trailing URL sits inside an OSC-8 hyperlink pair — its first
		// textual occurrence is inside that sequence — so assert at the
		// last occurrence, the printed cells.
		if active := activeSGRLast(got, "https://example.com/a"); active != roleSGR(roleStyle.Muted, "") {
			t.Errorf("real link URL active SGR = %q, want Muted %q: %q", active, roleSGR(roleStyle.Muted, ""), got)
		}
	})

	t.Run("list_marker_accent_item_fg", func(t *testing.T) {
		got := renderRoles(t, "- Namespaces isolate traffic.\n", roleStyle)
		if active := activeSGR(got, "•"); active != roleSGR(roleStyle.Accent, "") {
			t.Errorf("list bullet active SGR = %q, want Accent %q: %q", active, roleSGR(roleStyle.Accent, ""), got)
		}
		if want := roleSGR(roleStyle.Fg, "") + "Namespaces"; !strings.Contains(got, want) {
			t.Errorf("list item text not Fg: want %q in %q", want, got)
		}
	})

	t.Run("table_header_accent_bold_rules_border", func(t *testing.T) {
		src := "| dev | role |\n|---|---|\n| eth0 | up |\n"
		got := renderRoles(t, src, roleStyle)
		lines := strings.Split(got, "\n")
		var header, rule, body string
		for _, l := range lines {
			switch {
			case strings.Contains(ansi.Strip(l), "dev"):
				header = l
			case isRuleLine(strings.TrimPrefix(ansi.Strip(l), "  ")):
				rule = l
			case strings.Contains(ansi.Strip(l), "eth0"):
				body = l
			}
		}
		if header == "" || rule == "" || body == "" {
			t.Fatalf("table lines not found in %q", got)
		}
		if want := roleSGR(roleStyle.Accent, ";1") + "dev"; !strings.Contains(header, want) {
			t.Errorf("header cell not Accent+bold: want %q in %q", want, header)
		}
		if want := roleSGR(roleStyle.Fg, "") + "eth0"; !strings.Contains(body, want) {
			t.Errorf("body cell not Fg: want %q in %q", want, body)
		}
		if active := activeSGR(rule, "─"); active != roleSGR(roleStyle.Border, "") {
			t.Errorf("rule active SGR = %q, want Border %q: %q", active, roleSGR(roleStyle.Border, ""), rule)
		}
	})

	t.Run("blockquote_border_bar_muted_italic", func(t *testing.T) {
		got := renderRoles(t, "> quoted words here\n", roleStyle)
		if active := activeSGR(got, "│"); active != roleSGR(roleStyle.Border, "") {
			t.Errorf("blockquote bar active SGR = %q, want Border %q: %q", active, roleSGR(roleStyle.Border, ""), got)
		}
		if active := activeSGR(got, "quoted"); active != roleSGR(roleStyle.Muted, ";3") {
			t.Errorf("quote text active SGR = %q, want Muted+italic %q: %q", active, roleSGR(roleStyle.Muted, ";3"), got)
		}
	})

	t.Run("fenced_go_token_colours", func(t *testing.T) {
		src := "```go\nfunc f() int { x := \"x\"; return 2 } // c\n```\n"
		got := renderRoles(t, src, roleStyle)
		for _, tc := range []struct {
			needle, want string
			what         string
		}{
			{"func", roleSGR(roleStyle.Heading, ""), "keyword"},
			{"int", roleSGR(roleStyle.Code, ""), "type"},
			{"\"x\"", roleSGR(roleStyle.Good, ""), "string"},
			{"// c", "\x1b[3m" + roleSGR(roleStyle.Faint, ""), "comment"},
		} {
			if active := activeSGR(got, tc.needle); active != tc.want {
				t.Errorf("%s %q active SGR = %q, want %q: %q", tc.what, tc.needle, active, tc.want, got)
			}
		}
		// The number token needs an SGR-anchored needle: a bare "2" also
		// matches inside the first "38;2;" sequence on the line.
		if want := roleSGR(roleStyle.Warn, "") + "2"; !strings.Contains(got, want) {
			t.Errorf("number not Warn: want %q in %q", want, got)
		}
	})

	t.Run("no_background_sgr", func(t *testing.T) {
		src := "---\ntitle: Roles\ntype: note\n---\n" +
			"# H one\n\n## H two\n\n### H three\n\n" +
			"Body **bold** and *italic* and `code` with [[a-link]] and [text](https://e.com/x) " +
			"and ^[raw/p.md] here.\n\n" +
			"- one item\n\n" +
			"| h1 | h2 |\n|---|---|\n| a | b |\n\n" +
			"> quote me\n\n" +
			"```go\nfunc g() { s := \"s\"; n := 1 } // note\n```\n"
		got := renderRoles(t, src, roleStyle)
		if strings.Contains(got, "48;") {
			t.Errorf("output contains a 48; background SGR: %q", got)
		}
		for _, m := range []string{"\x1b[40m", "\x1b[41m", "\x1b[42m", "\x1b[43m", "\x1b[44m", "\x1b[45m", "\x1b[46m", "\x1b[47m",
			"\x1b[100m", "\x1b[101m", "\x1b[102m", "\x1b[103m", "\x1b[104m", "\x1b[105m", "\x1b[106m", "\x1b[107m"} {
			if strings.Contains(got, m) {
				t.Errorf("output contains a classic background SGR %q: %q", m, got)
			}
		}
	})
}

func TestChromaStylePerPalette(t *testing.T) {
	t.Run("dark_then_light_code_colours_differ", func(t *testing.T) {
		src := "```go\nfunc f() int { return 2 }\n```\n"
		dark := renderRoles(t, src, roleStyle)
		light := renderRoles(t, src, roleStyleLight)

		if want := roleSGR(roleStyle.Heading, ""); !strings.Contains(dark, want+"func") {
			t.Errorf("dark fence: func not Heading %q: %q", want, dark)
		}
		if want := roleSGR(roleStyleLight.Heading, ""); !strings.Contains(light, want+"func") {
			t.Errorf("light fence: func not the light Heading %q: %q", want, light)
		}
	})

	t.Run("truecolor_formatter_no_256_codes", func(t *testing.T) {
		src := "```go\nfunc f() int { return 2 } // c\n```\n"
		got := renderRoles(t, src, roleStyle)
		if strings.Contains(got, "38;5;") {
			t.Errorf("fenced output contains 38;5; (256-colour quantisation): %q", got)
		}
	})
}

func TestMemoKeyWholeStyle(t *testing.T) {
	t.Run("changed_token_misses_cache", func(t *testing.T) {
		a := roleStyle
		b := roleStyle
		b.Heading = roleStyleLight.Heading // the only token that differs

		src := []byte("# Titled page\n")
		r := NewRenderer()
		gotA, err := r.Render(src, Options{Width: 80, Style: a})
		if err != nil {
			t.Fatalf("Render A: %v", err)
		}
		gotB, err := r.Render(src, Options{Width: 80, Style: b})
		if err != nil {
			t.Fatalf("Render B: %v", err)
		}

		wantA := roleSGR(a.Heading, ";1")
		wantB := roleSGR(b.Heading, ";1")
		joinedA, joinedB := strings.Join(gotA, "\n"), strings.Join(gotB, "\n")
		if active := activeSGR(joinedA, "Titled"); active != wantA {
			t.Errorf("style A H1 active SGR = %q, want %q: %q", active, wantA, joinedA)
		}
		if active := activeSGR(joinedB, "Titled"); active != wantB {
			t.Errorf("style B H1 active SGR = %q, want %q (cache key likely ignored the token): %q", active, wantB, joinedB)
		}
		if joinedA == joinedB {
			t.Errorf("two Styles differing only in Heading rendered identically through one Renderer")
		}
	})
}
