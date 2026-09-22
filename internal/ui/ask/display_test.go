// display_test.go is 027 T2's evidence: the ask pane renders the displayed
// answer — provenance markers and the vault label hidden unless ctrl+p
// revealed them — while the record (entries, the recorded answer, the
// fileability gate) keeps the raw text. The pure transform is table-tested;
// everything else asserts through the real render paths (conversationLines,
// Update, OverlayHelp).
package ask

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/agent"
)

// displayPlain joins rendered lines into one ANSI-stripped string, so the
// render-path assertions can search visible text.
func displayPlain(lines []string) string {
	return ansi.Strip(strings.Join(lines, "\n"))
}

func TestAnswerDisplay(t *testing.T) {
	// transform_table pins displayAnswer itself, rule by rule: marker
	// removal with its one preceding space, the never-inside-code rule
	// (fences and inline spans), the vault label, the showProv identity,
	// and the live-only hides for a marker still arriving and a label
	// still arriving.
	t.Run("transform_table", func(t *testing.T) {
		cases := []struct {
			name     string
			in       string
			showProv bool
			live     bool
			want     string
		}{
			{"marker_with_preceding_space", "a claim ^[raw/x.md] and more", false, false, "a claim and more"},
			{"marker_after_punctuation", "end.^[raw/x.md]", false, false, "end."},
			{"two_markers_one_space_each", "one ^[a] ^[b].", false, false, "one."},
			{"marker_at_line_start", "^[raw/x.md] start", false, false, " start"},
			{"marker_after_tab", "a\t^[raw/x.md]", false, false, "a"},
			{"marker_inside_backtick_fence_stays",
				"before ^[raw/a.md]\n\n```go\ncode ^[raw/in.md]\n```\n\nafter ^[raw/b.md]",
				false, false,
				"before\n\n```go\ncode ^[raw/in.md]\n```\n\nafter"},
			{"marker_inside_tilde_fence_stays",
				"before ^[raw/a.md]\n\n~~~\ncode ^[raw/in.md]\n~~~\n\nafter ^[raw/b.md]",
				false, false,
				"before\n\n~~~\ncode ^[raw/in.md]\n~~~\n\nafter"},
			{"marker_inside_inline_code_stays", "run `x ^[raw/in.md] y` end ^[raw/a.md]", false, false,
				"run `x ^[raw/in.md] y` end"},
			{"label_first_line", "Not from your vault:\n\nParis is the capital.", false, false,
				"Paris is the capital."},
			// Mid-text, only the label line goes: the blank-trim rule is for
			// a text the removal leaves STARTING with blanks.
			{"label_mid_text", "Intro line.\n\nNot from your vault:\n\nParis is the capital.", false, false,
				"Intro line.\n\n\nParis is the capital."},
			{"label_lookalike_without_colon_stays", "Not from your vault", false, false,
				"Not from your vault"},
			{"unterminated_marker_finished_stays", "claim ^[raw/pa", false, false, "claim ^[raw/pa"},
			{"show_prov_true_is_identity",
				"a claim ^[raw/x.md]\n\n```\n^[raw/in.md]\n```\n\nNot from your vault:\n\nbody",
				true, false,
				"a claim ^[raw/x.md]\n\n```\n^[raw/in.md]\n```\n\nNot from your vault:\n\nbody"},
			{"show_prov_true_is_identity_live", "claim ^[raw/pa", true, true, "claim ^[raw/pa"},
			{"live_hides_arriving_marker", "claim ^[raw/pa", false, true, "claim"},
			{"live_keeps_complete_marker", "claim ^[raw/pa] more", false, true, "claim more"},
			{"live_hides_arriving_label_prefix", "Not from yo", false, true, ""},
			{"live_hides_label_without_colon", "Not from your vault", false, true, ""},
			{"live_label_then_body", "Not from your vault:\n\nParis", false, true, "Paris"},
		}
		for _, tc := range cases {
			if got := displayAnswer(tc.in, tc.showProv, tc.live); got != tc.want {
				t.Errorf("%s: displayAnswer(%q, showProv=%v, live=%v) =\n  %q\nwant\n  %q",
					tc.name, tc.in, tc.showProv, tc.live, got, tc.want)
			}
		}
	})

	// hidden_by_default: the default pane hides a finished answer's marker
	// but keeps the answer's own words.
	t.Run("hidden_by_default", func(t *testing.T) {
		m := newRenderModel(t)
		if m.showProvenance {
			t.Fatal("precondition: showProvenance must default to false")
		}
		m.applyEvent(agent.TextDelta{Text: "Speculative decoding drafts tokens. ^[raw/papers/x.md]"})
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})
		lines, _ := m.conversationLines(76)
		got := displayPlain(lines)
		if strings.Contains(got, "[x.md]") || strings.Contains(got, "^[") {
			t.Fatalf("the finished answer still shows its marker:\n%s", got)
		}
		if !strings.Contains(got, "Speculative decoding drafts tokens.") {
			t.Fatalf("the answer's own text was lost with the marker:\n%s", got)
		}
	})

	// label_hidden: the vault label line never reaches the pane by default.
	t.Run("label_hidden", func(t *testing.T) {
		m := newRenderModel(t)
		m.applyEvent(agent.TextDelta{Text: "Not from your vault:\n\nParis is the capital."})
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})
		lines, _ := m.conversationLines(76)
		got := displayPlain(lines)
		if strings.Contains(got, "Not from your vault") {
			t.Fatalf("the vault label still renders:\n%s", got)
		}
		if !strings.Contains(got, "Paris is the capital.") {
			t.Fatalf("the answer under the label was lost:\n%s", got)
		}
	})

	// ctrl_p_reveals: ctrl+p through Update shows the marker exactly as
	// pre-027 did, and ctrl+p again hides it. Pane state only.
	t.Run("ctrl_p_reveals", func(t *testing.T) {
		m := newRenderModel(t)
		m.applyEvent(agent.TextDelta{Text: "Speculative decoding drafts tokens. ^[raw/papers/x.md]"})
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})
		lines, _ := m.conversationLines(76)
		if got := displayPlain(lines); strings.Contains(got, "[x.md]") {
			t.Fatalf("precondition: the marker is visible before ctrl+p:\n%s", got)
		}

		pane, _ := m.Update(specialKey('p', tea.ModCtrl))
		m = pane.(*Model)
		if !m.showProvenance {
			t.Fatal("ctrl+p did not arm showProvenance")
		}
		lines, _ = m.conversationLines(76)
		if got := displayPlain(lines); !strings.Contains(got, "[x.md]") {
			t.Fatalf("ctrl+p did not reveal the marker's [x.md]:\n%s", got)
		}

		pane, _ = m.Update(specialKey('p', tea.ModCtrl))
		m = pane.(*Model)
		if m.showProvenance {
			t.Fatal("the second ctrl+p did not clear showProvenance")
		}
		lines, _ = m.conversationLines(76)
		if got := displayPlain(lines); strings.Contains(got, "[x.md]") {
			t.Fatalf("the second ctrl+p left the marker visible:\n%s", got)
		}
	})

	// record_untouched: the hidden display never reaches the record — the
	// recorded answer keeps its marker (so the fileability gate still sees
	// it and the ctrl+s hint still lands), while the pane shows none.
	t.Run("record_untouched", func(t *testing.T) {
		m := hintTurn(t,
			agent.TextDelta{Text: "Speculative decoding drafts tokens. ^[raw/papers/x.md]"},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		)
		if !strings.Contains(m.last.answer, "^[raw/papers/x.md]") {
			t.Fatalf("the recorded answer lost its raw marker: %q", m.last.answer)
		}
		if n := countStatusEntries(m, fileHint); n != 1 {
			t.Fatalf("scrollback holds %d ctrl+s hints, want exactly 1 (the gate still sees the marker)", n)
		}
		lines, _ := m.conversationLines(76)
		if got := displayPlain(lines); strings.Contains(got, "^[") {
			t.Fatalf("the hidden pane still renders a marker:\n%s", got)
		}
	})

	// live_tail_no_flash: a marker still arriving on the live tail never
	// flashes its half-written bytes.
	t.Run("live_tail_no_flash", func(t *testing.T) {
		m := newRenderModel(t)
		m.applyEvent(agent.TextDelta{Text: "Drafts tokens ^[raw/pap"})
		if !m.turnActive {
			t.Fatal("precondition: the streamed entry is not live")
		}
		lines, _ := m.conversationLines(76)
		got := displayPlain(lines)
		if strings.Contains(got, "^[") {
			t.Fatalf("the live tail flashes the arriving marker:\n%s", got)
		}
		if !strings.Contains(got, "Drafts tokens") {
			t.Fatalf("the live tail lost its plain text:\n%s", got)
		}
	})

	// help_lists_ctrl_p: the ? overlay names the toggle, directly after
	// the ctrl+s entry.
	t.Run("help_lists_ctrl_p", func(t *testing.T) {
		m := newRenderModel(t)
		_, help := m.OverlayHelp()
		pos := -1
		for i, e := range help {
			if e.Key == "ctrl+s" {
				pos = i
			}
			if e.Key == "ctrl+p" && e.Desc == "show/hide sources" && pos == i-1 {
				return
			}
		}
		t.Fatalf("OverlayHelp lacks {ctrl+p show/hide sources} after ctrl+s: %+v", help)
	})
}
