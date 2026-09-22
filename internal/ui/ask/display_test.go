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

// flatten normalizes a rendered block for comparison across the live and
// finished paths: ANSI-stripped lines, whitespace runs collapsed, blanks
// dropped — the visible words and their order, nothing else.
func flatten(lines []string) string {
	var parts []string
	for _, l := range lines {
		l = strings.Join(strings.Fields(ansi.Strip(l)), " ")
		if l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, "\n")
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

			// ---- 027 T2 fresh-context review pins (decisions, RED first) ----
			// The label rule is a whole-line rule on PROSE lines only: the
			// same words inside a fenced code block are the answer's own
			// code and stay, backtick or tilde fence alike.
			{"label_inside_backtick_fence_stays",
				"Answer.\n\n```\nNot from your vault:\necho hi\n```\n",
				false, false,
				"Answer.\n\n```\nNot from your vault:\necho hi\n```\n"},
			{"label_inside_tilde_fence_stays",
				"~~~\nNot from your vault:\n~~~",
				false, false,
				"~~~\nNot from your vault:\n~~~"},
			{"label_in_inline_code_stays", "saying `Not from your vault:` out loud", false, false,
				"saying `Not from your vault:` out loud"},
			{"label_with_trailing_spaces_removed", "Not from your vault:  \n\nbody", false, false,
				"body"},
			// The arriving label can sit mid-answer — after a vault-backed
			// part — so the live prefix rule guards the LAST line too, never
			// inside an open fence, and never an empty last line.
			{"live_hides_arriving_mid_text_label", "Intro.\n\nNot from yo", false, true,
				"Intro.\n"},
			{"live_hides_mid_text_label_without_colon", "Intro.\n\nNot from your vault", false, true,
				"Intro.\n"},
			{"live_keeps_fenced_label_prefix", "Intro.\n\n```\nNot from yo", false, true,
				"Intro.\n\n```\nNot from yo"},
			{"live_keeps_text_after_newline_caret", "Answer.\n", false, true, "Answer.\n"},
			// A bare trailing ^ is a marker one byte before its `[`: hidden
			// live like any other arriving marker, kept once final — the
			// final bytes are the record, and ctrl+p shows them regardless.
			{"live_hides_trailing_lone_caret", "claim ^", false, true, "claim"},
			{"live_keeps_mid_line_caret", "x^2 = 4", false, true, "x^2 = 4"},
			{"live_keeps_trailing_caret_in_inline_code", "run `x^", false, true, "run `x^"},
			{"live_keeps_trailing_caret_in_fence", "```\nx^", false, true, "```\nx^"},
			{"finished_trailing_caret_stays", "claim ^", false, false, "claim ^"},
			// A list item whose only content was a citation leaves no empty
			// bullet behind; an item that was already bare stays as it was.
			{"marker_only_list_item_dropped", "Intro.\n\n- ^[raw/a.md]\n- real item", false, false,
				"Intro.\n\n- real item"},
			{"marker_only_ordered_item_dropped", "Intro.\n\n1. ^[raw/a.md]\n2. real item", false, false,
				"Intro.\n\n2. real item"},
			{"pre_existing_empty_item_stays", "Intro.\n\n-\n- real item", false, false,
				"Intro.\n\n-\n- real item"},
			{"marker_only_item_dropped_live_tail", "Intro.\n\n- ^[raw/a.md]", false, true,
				"Intro.\n"},
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

	// fenced_label_kept_real_label_hidden: through conversationLines, the
	// agent's label line vanishes while the same words inside a fenced
	// block — the answer's own code — render.
	t.Run("fenced_label_kept_real_label_hidden", func(t *testing.T) {
		m := newRenderModel(t)
		m.applyEvent(agent.TextDelta{Text: "Not from your vault:\n\nAnswer body.\n\n```\nNot from your vault:\necho hi\n```\n"})
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})
		lines, _ := m.conversationLines(76)
		got := displayPlain(lines)
		if n := strings.Count(got, "Not from your vault"); n != 1 {
			t.Fatalf("want exactly the fenced label kept, found %d:\n%s", n, got)
		}
		if !strings.Contains(got, "Answer body.") {
			t.Fatalf("the answer body was lost:\n%s", got)
		}
	})

	// live_mid_answer_label_no_flash: frame by frame through the real
	// render path, an arriving mid-answer label never shows — not even the
	// frame its prefix sits on a line below the first.
	t.Run("live_mid_answer_label_no_flash", func(t *testing.T) {
		m := newRenderModel(t)
		deltas := []string{"Vault claim. ^[raw/a.md]\n\n", "Not from yo", "ur vault:\n\n", "Outside knowledge."}
		for i, d := range deltas {
			m.applyEvent(agent.TextDelta{Text: d})
			lines, _ := m.conversationLines(76)
			if got := displayPlain(lines); strings.Contains(got, "Not from") {
				t.Fatalf("frame %d flashes the arriving label:\n%s", i, got)
			}
		}
		lines, _ := m.conversationLines(76)
		if got := displayPlain(lines); !strings.Contains(got, "Outside knowledge.") {
			t.Fatalf("the completed outside part was lost:\n%s", got)
		}
	})

	// live_caret_split_no_flash: with the delta boundary exactly between
	// the marker's ^ and its [, no frame shows a caret.
	t.Run("live_caret_split_no_flash", func(t *testing.T) {
		m := newRenderModel(t)
		for i, d := range []string{"claim ", "^", "[raw/x.md] more"} {
			m.applyEvent(agent.TextDelta{Text: d})
			lines, _ := m.conversationLines(76)
			if got := displayPlain(lines); strings.Contains(got, "^") {
				t.Fatalf("frame %d shows a caret:\n%s", i, got)
			}
		}
	})

	// first_line_not_lookalike_shows: the first-line prefix hide is a
	// transient rule — a real answer opening with "Not" appears the moment
	// its line diverges from the label, and when finished.
	t.Run("first_line_not_lookalike_shows", func(t *testing.T) {
		m := newRenderModel(t)
		m.applyEvent(agent.TextDelta{Text: "Not"})
		lines, _ := m.conversationLines(76)
		if got := displayPlain(lines); strings.Contains(got, "Not") {
			t.Fatalf("the arriving label's own prefix flashed:\n%s", got)
		}
		m.applyEvent(agent.TextDelta{Text: " many people know this."})
		lines, _ = m.conversationLines(76)
		if got := displayPlain(lines); !strings.Contains(got, "Not many people know this.") {
			t.Fatalf("the diverging first line did not appear:\n%s", got)
		}
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})
		lines, _ = m.conversationLines(76)
		if got := displayPlain(lines); !strings.Contains(got, "Not many people know this.") {
			t.Fatalf("the finished first line was lost:\n%s", got)
		}
	})

	// marker_only_list_item_no_stray_bullet: the citation-only item's
	// bullet never renders; the list's other items do.
	t.Run("marker_only_list_item_no_stray_bullet", func(t *testing.T) {
		m := newRenderModel(t)
		m.applyEvent(agent.TextDelta{Text: "- ^[raw/a.md]\n- real item"})
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})
		lines, _ := m.conversationLines(76)
		got := displayPlain(lines)
		for _, l := range strings.Split(got, "\n") {
			if s := strings.TrimSpace(l); s == "•" || s == "-" {
				t.Fatalf("a stray empty list bullet rendered (%q):\n%s", l, got)
			}
		}
		if !strings.Contains(got, "real item") {
			t.Fatalf("the list's real item was lost:\n%s", got)
		}
	})

	// double_space_between_markers_invisible: stripping two markers two
	// spaces apart leaves two spaces in the transformed bytes, but neither
	// renderer lets them show — the live tail's wrapper and the markdown
	// renderer both collapse word gaps. Pinned so a renderer change that
	// exposes the double space fails here.
	t.Run("double_space_between_markers_invisible", func(t *testing.T) {
		m := newRenderModel(t)
		m.applyEvent(agent.TextDelta{Text: "one ^[a]  ^[b]."})
		lines, _ := m.conversationLines(76)
		if got := displayPlain(lines); strings.Contains(got, "one  ") {
			t.Fatalf("the live tail renders a double space:\n%s", got)
		}
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})
		lines, _ = m.conversationLines(76)
		if got := displayPlain(lines); strings.Contains(got, "one  ") {
			t.Fatalf("the settled render shows a double space:\n%s", got)
		}
	})

	// marker_own_line_live_matches_finished: a marker alone on a line
	// strips to a blank, which is a block boundary for the live
	// settled/tail split too — the finished render and the last live frame
	// agree on the visible text (whitespace runs normalized).
	t.Run("marker_own_line_live_matches_finished", func(t *testing.T) {
		m := newRenderModel(t)
		m.applyEvent(agent.TextDelta{Text: "para one.\n^[raw/x.md]\npara two."})
		live, _ := m.conversationLines(76)
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})
		done, _ := m.conversationLines(76)
		// the finished conversation carries the turn status and the file
		// hint after the answer; the live frame does not. Cut them.
		for n := len(done); n > 0; n-- {
			s := strings.TrimSpace(ansi.Strip(done[n-1]))
			if s == "" || strings.HasPrefix(s, "done ·") || strings.HasPrefix(s, "ctrl+s") {
				done = done[:n-1]
				continue
			}
			break
		}
		if flatten(live) != flatten(done) {
			t.Fatalf("the live frame did not settle into the finished render:\nLIVE:\n%s\nDONE:\n%s",
				displayPlain(live), displayPlain(done))
		}
	})

	// ctrl_p_midstream: toggling mid-stream reveals the raw arriving
	// bytes and hides them again — pane state only.
	t.Run("ctrl_p_midstream", func(t *testing.T) {
		m := newRenderModel(t)
		m.applyEvent(agent.TextDelta{Text: "claim ^[raw/pa"})
		lines, _ := m.conversationLines(76)
		if got := displayPlain(lines); strings.Contains(got, "^[") {
			t.Fatalf("the arriving marker showed before ctrl+p:\n%s", got)
		}
		pane, _ := m.Update(specialKey('p', tea.ModCtrl))
		m = pane.(*Model)
		lines, _ = m.conversationLines(76)
		if got := displayPlain(lines); !strings.Contains(got, "^[raw/pa") {
			t.Fatalf("ctrl+p did not reveal the arriving bytes:\n%s", got)
		}
		pane, _ = m.Update(specialKey('p', tea.ModCtrl))
		m = pane.(*Model)
		lines, _ = m.conversationLines(76)
		if got := displayPlain(lines); strings.Contains(got, "^[") {
			t.Fatalf("the second ctrl+p left the arriving marker visible:\n%s", got)
		}
	})

	// ctrl_p_then_file_keeps_raw: with provenance shown, ctrl+s still
	// files the raw marked answer — the filing turn's message embeds the
	// record, never the displayed text.
	t.Run("ctrl_p_then_file_keeps_raw", func(t *testing.T) {
		m := hintTurn(t,
			agent.TextDelta{Text: "It is the attention cache.^[raw/articles/kv-cache-explained.md]"},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		)
		fa, ok := m.deps.Agent.(*fakeTurnAgent)
		if !ok {
			t.Fatalf("agent is %T, want *fakeTurnAgent", m.deps.Agent)
		}
		pane, _ := m.Update(specialKey('p', tea.ModCtrl))
		m = pane.(*Model)
		fa.script = []agent.Event{
			agent.TextDelta{Text: "Filed. See ^[wiki/queries/kv-cache.md]"},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		}
		pane, cmd := m.Update(specialKey('s', tea.ModCtrl))
		m = pane.(*Model)
		if cmd == nil {
			t.Fatal("ctrl+s produced no command")
		}
		var seen []tea.Msg
		m = runCmd(t, m, cmd, &seen).(*Model)
		msgs := fa.sentMsgs()
		if len(msgs) < 2 {
			t.Fatalf("the filing turn sent %d messages, want the question + the filing message", len(msgs))
		}
		if last := msgs[len(msgs)-1]; !strings.Contains(last, "^[raw/articles/kv-cache-explained.md]") {
			t.Fatalf("the filing message lost the raw marked answer:\n%s", last)
		}
	})

	// hidden_render_then_file_keeps_raw: the mainline ordering — a finished
	// turn renders hidden (the default), THEN ctrl+s files it. The filing
	// message embeds the raw record: every marker and the vault label too
	// (the filing agent needs both — the markers make the query page
	// attributable, the label tells it part of the answer was outside
	// knowledge). Tier-2 027: this ordering had no pin — only the ctrl+p
	// (shown) → ctrl+s one did.
	t.Run("hidden_render_then_file_keeps_raw", func(t *testing.T) {
		m := hintTurn(t,
			agent.TextDelta{Text: "Not from your vault:\n\nParis is the capital. But attention is the cache.^[raw/articles/kv-cache-explained.md]"},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		)
		if lines, _ := m.conversationLines(76); strings.Contains(displayPlain(lines), "Not from your vault") ||
			strings.Contains(displayPlain(lines), "^[") {
			t.Fatalf("precondition: the pane did not render hidden:\n%s", displayPlain(lines))
		}
		if !strings.Contains(m.last.answer, "Not from your vault:") {
			t.Fatalf("the recorded answer lost the label: %q", m.last.answer)
		}
		if !m.lastAnswerFileable() {
			t.Fatalf("precondition: the labelled, marked answer is not fileable: %q", m.last.answer)
		}
		fa, ok := m.deps.Agent.(*fakeTurnAgent)
		if !ok {
			t.Fatalf("agent is %T, want *fakeTurnAgent", m.deps.Agent)
		}
		fa.script = []agent.Event{
			agent.TextDelta{Text: "Filed. See ^[wiki/queries/paris.md]"},
			agent.DoneEv{Reason: "stop", Rounds: 1},
		}
		pane, cmd := m.Update(specialKey('s', tea.ModCtrl))
		m = pane.(*Model)
		if cmd == nil {
			t.Fatal("ctrl+s produced no command")
		}
		var seen []tea.Msg
		runCmd(t, m, cmd, &seen)
		msgs := fa.sentMsgs()
		if len(msgs) < 2 {
			t.Fatalf("the filing turn sent %d messages, want the question + the filing message", len(msgs))
		}
		last := msgs[len(msgs)-1]
		if !strings.Contains(last, "^[raw/articles/kv-cache-explained.md]") {
			t.Fatalf("the filing message lost the raw marker:\n%s", last)
		}
		if !strings.Contains(last, "Not from your vault:") {
			t.Fatalf("the filing message lost the raw label:\n%s", last)
		}
	})

	// ctrl_p_does_not_move_scroll: the 022-class interaction — a toggle
	// must not move the scroll offset behind the user. ctrl+p changes the
	// rendered line count (markers and labels shorten answers), and back is
	// a count-from-the-bottom, so both directions of the toggle must leave
	// it exactly where it was.
	t.Run("ctrl_p_does_not_move_scroll", func(t *testing.T) {
		m := newRenderModel(t)
		m.applyEvent(agent.TextDelta{Text: "first ^[raw/a.md]\n\n" + strings.Repeat("filler sentence. ", 60)})
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})
		m.applyEvent(agent.TextDelta{Text: "second ^[raw/b.md]"})
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})
		m.scrollBy(5)
		if m.back != 5 {
			t.Fatalf("precondition: scrollBy(5) left back=%d", m.back)
		}
		pane, _ := m.Update(specialKey('p', tea.ModCtrl))
		m = pane.(*Model)
		if m.back != 5 {
			t.Fatalf("ctrl+p moved the scroll offset: back=%d, want 5", m.back)
		}
		pane, _ = m.Update(specialKey('p', tea.ModCtrl))
		m = pane.(*Model)
		if m.back != 5 {
			t.Fatalf("the second ctrl+p moved the scroll offset: back=%d, want 5", m.back)
		}
		if m.back > m.maxBack() || m.back < 0 {
			t.Fatalf("back left the clamped range: %d (max %d)", m.back, m.maxBack())
		}
	})

	// ctrl_t_chrome_ignores_provenance: the reasoning view reads no answer
	// text — byte-identical with provenance hidden and shown, on a turn
	// carrying both reasoning and a marked, labelled live answer; and the
	// busy tail mount never moves with the toggle.
	t.Run("ctrl_t_chrome_ignores_provenance", func(t *testing.T) {
		build := func(show bool) *Model {
			m := newRenderModel(t)
			m.SetShowProvenance(show)
			m.applyEvent(agent.ReasoningDelta{Text: "weighing ^[raw/never.md] against the label"})
			m.applyEvent(agent.TextDelta{Text: "Not from your vault:\n\nclaim ^[raw/x.md]"})
			return m
		}
		hidden, shown := build(false), build(true)
		h := strings.Join(hidden.reasoningViewLines(76), "\x00")
		s := strings.Join(shown.reasoningViewLines(76), "\x00")
		if h != s {
			t.Fatalf("the ctrl+t view differs between hidden and shown:\n%q\n%q", h, s)
		}
		if hidden.mountedTailLines() != shown.mountedTailLines() {
			t.Fatalf("the busy tail mount moved: %d vs %d",
				hidden.mountedTailLines(), shown.mountedTailLines())
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
