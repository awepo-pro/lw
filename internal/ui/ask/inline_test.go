// inline_test.go pins the inline renderer's colours (W5 F3/D-3W): a code
// span carries the theme's Code token, a wikilink Accent + underline, and
// a tea.ColorProfileMsg retheming reaches the selected tool-call row's
// cursor tint. Bold, italic and provenance are unchanged, so they are not
// re-pinned here.
package ask

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// sgrParamsWith returns the SGR parameter lists of the styled render that
// carry run (e.g. "38;2;122;178;242") as a contiguous parameter run.
// Colour assertions key on parameter runs, not on one byte layout —
// lipgloss is free to interleave attributes around the colour.
func sgrParamsWith(styled, run string) []string {
	re := regexp.MustCompile(`\x1b\[([0-9;]*)m`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(styled, -1) {
		if strings.Contains(m[1], run) {
			out = append(out, m[1])
		}
	}
	return out
}

// hasSGR reports whether any SGR sequence in styled carries run.
func hasSGR(styled, run string) bool {
	return len(sgrParamsWith(styled, run)) > 0
}

func TestAskInlineColours(t *testing.T) {
	// code_uses_code_token: a code span renders in the Code token —
	// #6CC7C9 = 38;2;108;199;201 on the dark default theme — with the
	// backticks consumed as before.
	t.Run("code_uses_code_token", func(t *testing.T) {
		m := newInlineModel(t)
		styled := strings.Join(m.inlineWrap("runs `x` now", 40), "\n")
		if !hasSGR(styled, "38;2;108;199;201") {
			t.Fatalf("code span lost the Code token (want an SGR carrying 38;2;108;199;201): %q", styled)
		}
		if plain := ansi.Strip(styled); plain != "runs x now" {
			t.Fatalf("code span render = %q, want the backticks consumed: %q", plain, "runs x now")
		}
	})

	// wikilink_accent_underline: a wikilink renders in Accent —
	// #7AB2F2 = 38;2;122;178;242 — with underline (SGR 4), brackets gone.
	t.Run("wikilink_accent_underline", func(t *testing.T) {
		m := newInlineModel(t)
		styled := strings.Join(m.inlineWrap("see [[vertex-ai]] ok", 40), "\n")
		if !hasSGR(styled, "38;2;122;178;242") {
			t.Fatalf("wikilink lost Accent (want an SGR carrying 38;2;122;178;242): %q", styled)
		}
		underlined := false
		for _, params := range sgrParamsWith(styled, "38;2;122;178;242") {
			for _, f := range strings.Split(params, ";") {
				if f == "4" {
					underlined = true
				}
			}
		}
		if !underlined {
			t.Fatalf("wikilink's Accent run carries no underline (SGR 4): %q", styled)
		}
		if plain := ansi.Strip(styled); plain != "see vertex-ai ok" {
			t.Fatalf("wikilink render = %q, want the target with brackets consumed: %q", plain, "see vertex-ai ok")
		}
	})

	// profile_msg_rethemes: after tea.ColorProfileMsg the pane's theme copy
	// re-resolves (frame note 8, like WithDark on BackgroundColorMsg), so a
	// selected tool-call row's tint moves from the truecolor cursor colour
	// to the profile's grey 236.
	t.Run("profile_msg_rethemes", func(t *testing.T) {
		m := newInlineModel(t)
		m.applyEvent(agent.ToolCallEv{ID: "t1", Name: "wiki.search", Args: `{"q":"kv cache"}`})
		m.applyEvent(agent.ToolResEv{ID: "t1", Name: "wiki.search", Content: "a memoization technique", IsError: false})
		if _, cmd := m.Update(specialKey(tea.KeyUp, 0)); cmd != nil {
			t.Fatalf("up produced a command (%v), want nil", cmd)
		}

		styled, _ := uitest.PaneScreen(m, scrollW, scrollH)
		if !hasSGR(styled, "48;2;26;35;49") {
			t.Fatal("the selected tool call's row carries no truecolor cursor tint before ColorProfileMsg")
		}

		if _, cmd := m.Update(tea.ColorProfileMsg{Profile: colorprofile.ANSI256}); cmd != nil {
			t.Fatalf("ColorProfileMsg produced a command (%v), want nil", cmd)
		}

		styled, _ = uitest.PaneScreen(m, scrollW, scrollH)
		if !hasSGR(styled, "48;5;236") {
			t.Fatalf("after ColorProfileMsg{ANSI256} the selected tool call's row carries no 48;5;236 tint: %q", styled)
		}
		if hasSGR(styled, "48;2;26;35;49") {
			t.Fatal("the truecolor cursor tint survived ColorProfileMsg{ANSI256}")
		}
	})
}

// newInlineModel is a dark-theme ask pane on the fixture vault, rendered
// once at the scroll test's size — the same construction the transcript
// uses, so the inline renderer's output here is its output in the pane.
func newInlineModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := uitest.PublicVault(t, "inline-vault")
	m := New(uitest.Deps(v, true, nil)).(*Model)
	_, _ = uitest.PaneScreen(m, scrollW, scrollH)
	return m
}
