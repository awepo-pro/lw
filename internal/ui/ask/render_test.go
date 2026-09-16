// render_test.go pins 005 T-B: assistant prose renders through the shared
// markdown renderer (005 contract §5), the Transcript panel's title carries
// the open changeset's short id (§6), and Ask's chrome — the tool rows, the
// you/assistant markers, the turn status — is byte-identical to what it was
// before the renderer plugged in. The scroll-side consequences live in
// scroll_render_test.go.
package ask

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/markdown"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// renderAnswer is a finished answer with the structure a real Ask reply
// has — a heading, a list, an inline code span — so the assertions key on
// what the shared renderer does with structure, not on prose wrapping.
const renderAnswer = "## Where they differ\n\n- static `x-api-key` keys\n- GCP IAM and service accounts"

// newRenderModel builds a dark-theme ask pane on the public fixture vault
// (whose fixture changeset is open), rendered once at the scroll test's
// size — the same construction the transcript and inline tests use, so
// what conversationLines returns here is what the pane draws.
func newRenderModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v := uitest.PublicVault(t, "render-vault")
	m := New(uitest.Deps(v, true, nil)).(*Model)
	_, _ = uitest.PaneScreen(m, scrollW, scrollH)
	return m
}

// renderMDStyle is the test's own palette builder for the shared renderer,
// written from the theme directly so the assertions never depend on the
// production mdStyle helper (a bug in mdStyle must fail these tests, not
// hide inside them).
func renderMDStyle(m *Model) markdown.Style {
	p := m.theme.Palette
	return markdown.Style{
		Dark: m.theme.IsDark, Fg: p.Fg, Muted: p.Muted, Faint: p.Faint,
		Border: p.Border, Accent: p.Accent, Good: p.Good, Warn: p.Warn, Bad: p.Bad,
		Heading: p.Heading, Code: p.Code,
	}
}

// sgrRGBRun returns the first truecolor foreground run ("38;2;r;g;b") in a
// styled probe — the run a token's colour is asserted by.
func sgrRGBRun(styled string) string {
	m := regexp.MustCompile(`38;2;\d+;\d+;\d+`).FindString(styled)
	if m == "" {
		panic("render_test: probe produced no truecolor SGR run: " + styled)
	}
	return m
}

// lineWithStripped returns the first line whose visible text, padding
// trimmed, equals want.
func lineWithStripped(t *testing.T, lines []string, want string) string {
	t.Helper()
	for _, l := range lines {
		if strings.TrimSpace(ansi.Strip(l)) == want {
			return l
		}
	}
	t.Fatalf("no line strips to %q in:\n%s", want, strings.Join(lines, "\n"))
	return ""
}

// answerBlock returns the answer's rendered lines from a conversationLines
// result whose ONLY entries are one assistant entry and — when finished —
// its status line: marker, answer lines…, closing blank [, status]. The
// blank cannot be scanned for (rendered output contains blanks between
// blocks), so the shape is pinned instead of searched.
func answerBlock(t *testing.T, lines []string, finished bool) []string {
	t.Helper()
	strip := func(s string) string { return strings.TrimSpace(ansi.Strip(s)) }
	dump := strings.Join(lines, "\n")
	if len(lines) == 0 || strip(lines[0]) != "assistant" {
		t.Fatalf("the conversation does not open with the assistant marker:\n%s", dump)
	}
	end := len(lines)
	if finished {
		if strip(lines[len(lines)-1]) != "done · 1 rounds" || strip(lines[len(lines)-2]) != "" {
			t.Fatalf("the finished conversation does not end with blank + status:\n%s", dump)
		}
		end -= 2
	} else {
		if strip(lines[len(lines)-1]) != "" {
			t.Fatalf("the live conversation does not end with the closing blank:\n%s", dump)
		}
		end--
	}
	return lines[1:end]
}

func TestAssistantRendersMarkdown(t *testing.T) {
	// finished_entry_renders_whole_buffer (contract §5 note 2): once the
	// turn has ended the whole buffer renders through the shared renderer —
	// the heading carries the page's Accent+bold, the list markers become
	// the renderer's `•`, and the block is exactly the lines RenderFragment
	// produces for the same buffer.
	t.Run("finished_entry_renders_whole_buffer", func(t *testing.T) {
		m := newRenderModel(t)
		m.applyEvent(agent.TextDelta{Text: renderAnswer})
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})

		lines, _ := m.conversationLines(76)
		block := answerBlock(t, lines, true)

		frag, err := m.md.RenderFragment([]byte(renderAnswer), markdown.Options{Width: 76, Style: m.mdStyle()})
		if err != nil {
			t.Fatalf("RenderFragment: %v", err)
		}
		if len(block) != len(frag) {
			t.Fatalf("the finished answer renders %d lines, want the whole-buffer fragment's %d:\n%s",
				len(block), len(frag), strings.Join(block, "\n"))
		}

		// The heading renders as a page heading: Accent+bold, `##` consumed.
		accent := sgrRGBRun(m.theme.Accent.Bold(true).Render("x"))
		head := lineWithStripped(t, block, "Where they differ")
		runs := sgrParamsWith(head, accent)
		if len(runs) == 0 {
			t.Fatalf("the rendered heading carries no Accent SGR (want a run with %s):\n%q", accent, head)
		}
		bold := false
		for _, params := range runs {
			for _, f := range strings.Split(params, ";") {
				if f == "1" {
					bold = true
				}
			}
		}
		if !bold {
			t.Fatalf("the rendered heading's Accent run carries no bold (SGR 1): %q", head)
		}

		// The list renders with the renderer's marker, not the source's.
		for _, l := range block {
			if s := ansi.Strip(l); strings.HasPrefix(s, "- ") {
				t.Fatalf("a list item kept its source marker: %q", s)
			}
		}
		if !strings.Contains(ansi.Strip(strings.Join(block, "\n")), "• static x-api-key keys") {
			t.Fatalf("the list did not render with the renderer's `•` marker:\n%s",
				ansi.Strip(strings.Join(block, "\n")))
		}
	})

	// live_entry_renders_settled_and_plain_tail (D-5A): mid-turn, the
	// settled blocks carry the renderer's SGR while the half-written tail
	// stays plain — and the live shape is exactly the shape the same buffer
	// settles into when the turn ends, so the display does not jump at Done.
	t.Run("live_entry_renders_settled_and_plain_tail", func(t *testing.T) {
		const buf = renderAnswer + "\n\nhalf-written tail"
		m := newRenderModel(t)
		m.applyEvent(agent.TextDelta{Text: buf})
		if !m.turnActive {
			t.Fatal("precondition: the streamed entry is not live")
		}
		liveLines, _ := m.conversationLines(76)
		liveBlock := answerBlock(t, liveLines, false)

		// The settled heading carries the renderer's Accent+bold…
		accent := sgrRGBRun(m.theme.Accent.Bold(true).Render("x"))
		if head := lineWithStripped(t, liveBlock, "Where they differ"); len(sgrParamsWith(head, accent)) == 0 {
			t.Fatalf("the settled heading carries no Accent SGR (want a run with %s):\n%q", accent, head)
		}
		// …and the settled list renders its marker…
		if !strings.Contains(ansi.Strip(strings.Join(liveBlock, "\n")), "• static x-api-key keys") {
			t.Fatalf("the settled list did not render:\n%s", ansi.Strip(strings.Join(liveBlock, "\n")))
		}
		// …while the tail stays plain: the marker-free tail line carries no
		// escapes at all — inlineWrap's unstyled base.
		tail := lineWithStripped(t, liveBlock, "half-written tail")
		if strings.Contains(tail, "\x1b[") {
			t.Fatalf("the live tail carries escapes, want plain unstyled text: %q", tail)
		}

		// Finishing the turn must not reflow: the same buffer rendered
		// finished is the same lines (settled + tail, not a second render).
		done := newRenderModel(t)
		done.applyEvent(agent.TextDelta{Text: buf})
		done.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})
		doneLines, _ := done.conversationLines(76)
		doneBlock := answerBlock(t, doneLines, true)
		if len(doneBlock) != len(liveBlock) {
			t.Fatalf("finishing the turn reflowed the answer: live %d lines, finished %d\nlive:\n%s\nfinished:\n%s",
				len(liveBlock), len(doneBlock), strings.Join(liveBlock, "\n"), strings.Join(doneBlock, "\n"))
		}
	})

	// heading_matches_a_page_render: the user's complaint, asserted
	// directly — the same `## H2` through Ask and through the page renderer
	// carries the IDENTICAL Accent SGR.
	t.Run("heading_matches_a_page_render", func(t *testing.T) {
		m := newRenderModel(t)
		m.applyEvent(agent.TextDelta{Text: "## H2"})
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 1})
		lines, _ := m.conversationLines(76)
		askHead := lineWithStripped(t, lines, "H2")

		page, err := markdown.NewRenderer().Render([]byte("## H2"),
			markdown.Options{Width: 76, Style: renderMDStyle(m)})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		pageHead := lineWithStripped(t, page, "H2")

		accent := sgrRGBRun(m.theme.Accent.Render("x"))
		askRuns := sgrParamsWith(askHead, accent)
		pageRuns := sgrParamsWith(pageHead, accent)
		if len(askRuns) == 0 || len(pageRuns) == 0 {
			t.Fatalf("the heading lost Accent somewhere: ask %q page %q (want a run with %s)",
				askHead, pageHead, accent)
		}
		if strings.Join(askRuns, "\x1b") != strings.Join(pageRuns, "\x1b") {
			t.Fatalf("the same heading renders with different SGR:\nask  %q\npage %q", askRuns, pageRuns)
		}
	})

	// tool_rows_are_unchanged: the ▸ rows, the you/assistant markers and
	// the turn status are chrome, not prose — byte-identical to the
	// pre-renderer shapes their frozen lines pin.
	t.Run("tool_rows_are_unchanged", func(t *testing.T) {
		m := newRenderModel(t)
		m.echoUser("How does calling Claude through Vertex AI differ from the Anthropic API?")
		m.applyEvent(agent.ToolCallEv{ID: "c1", Name: "wiki_search", Args: `{"query":"claude vertex ai"}`})
		m.applyEvent(agent.ToolResEv{ID: "c1", Name: "wiki_search",
			Content: "4 pages: anthropic-api-vs-vertex-ai, claude"})
		m.applyEvent(agent.TextDelta{Text: "Short answer."})
		m.applyEvent(agent.DoneEv{Reason: "stop", Rounds: 2})

		lines, _ := m.conversationLines(200)
		var got []string
		for _, l := range lines {
			got = append(got, strings.TrimSpace(ansi.Strip(l)))
		}
		want := []string{
			"you",
			"How does calling Claude through Vertex AI differ from the Anthropic API?",
			"",
			`▸ wiki_search {"query":"claude vertex ai"}  → 4 pages: anthropic-api-vs-vertex-ai, claude`,
			"",
			"assistant",
			"Short answer.",
			"",
			"done · 2 rounds",
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("the chrome moved:\ngot:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}

		// The markers' styled bytes are exactly the theme tokens they always
		// rendered with, and the tool row keeps its faint ▸ / muted name.
		for i, l := range lines {
			switch got[i] {
			case "you", "assistant":
				if want := m.theme.Bold.Render(got[i]); l != want {
					t.Fatalf("%s marker = %q, want the bold token's %q", got[i], l, want)
				}
			case "done · 2 rounds":
				if want := m.theme.Faint.Render(got[i]); l != want {
					t.Fatalf("status = %q, want the faint token's %q", l, want)
				}
			}
		}
		tool := lineWithStripped(t, lines,
			`▸ wiki_search {"query":"claude vertex ai"}  → 4 pages: anthropic-api-vs-vertex-ai, claude`)
		if !hasSGR(tool, sgrRGBRun(m.theme.Muted.Render("x"))) {
			t.Fatalf("the tool row's name lost the Muted token: %q", tool)
		}
	})

	// provenance_is_muted (A-5-1/D-5C): the live tail's ^[path] marker —
	// inline.go's only provenance call site — renders in Muted, not Faint.
	t.Run("provenance_is_muted", func(t *testing.T) {
		m := newRenderModel(t)
		m.applyEvent(agent.TextDelta{Text: "read ^[wiki/some/page.md] next"})
		lines, _ := m.conversationLines(76)
		prov := lineWithStripped(t, lines, "read [page.md] next")

		muted := sgrRGBRun(m.theme.Muted.Render("x"))
		faint := sgrRGBRun(m.theme.Faint.Render("x"))
		if !hasSGR(prov, muted) {
			t.Fatalf("the provenance marker carries no Muted SGR (want a run with %s): %q", muted, prov)
		}
		if hasSGR(prov, faint) {
			t.Fatalf("the provenance marker still carries the Faint SGR (%s): %q", faint, prov)
		}
	})
}

func TestTranscriptTitle(t *testing.T) {
	// open_changeset_shows_the_short_id (contract §6): the top border reads
	// `Transcript — <9 chars>`, and those 9 characters are the open
	// changeset's id through ui.ShortID — the same helper the frame header
	// uses, so the two cannot drift.
	t.Run("open_changeset_shows_the_short_id", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		v := uitest.PublicVault(t, "title-vault")
		m := New(uitest.Deps(v, true, nil)).(*Model)
		cs, err := v.Engine.Current()
		if err != nil {
			t.Fatalf("precondition: the fixture changeset is not open: %v", err)
		}

		styled, plain := uitest.PaneScreen(m, 80, 22)
		want := "Transcript — " + ui.ShortID(cs.ID)
		if !strings.Contains(strings.Split(plain, "\n")[0], want) {
			t.Fatalf("the top border reads %q, want it to contain %q", ansi.Strip(strings.Split(plain, "\n")[0]), want)
		}
		if !strings.Contains(ansi.Strip(strings.Split(styled, "\n")[0]), want) {
			t.Fatalf("the styled top border does not carry %q", want)
		}
	})

	// no_changeset_is_plain_transcript: with no open changeset — and,
	// separately, with no engine at all, both supported states — the title
	// is exactly `Transcript`, never a partial id.
	t.Run("no_changeset_is_plain_transcript", func(t *testing.T) {
		assertPlain := func(name string, m *Model) {
			t.Helper()
			_, plain := uitest.PaneScreen(m, 80, 22)
			border := strings.Split(plain, "\n")[0]
			if !strings.Contains(border, "Transcript") {
				t.Fatalf("%s: the top border %q lost the title", name, border)
			}
			if strings.Contains(border, "Transcript —") {
				t.Fatalf("%s: the top border %q shows an id with no changeset", name, border)
			}
		}

		// No engine: newTestDeps builds Deps with a nil Engine.
		assertPlain("nil engine", New(newTestDeps(t)).(*Model))

		// An engine, but nothing open.
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		root := testutil.CopyFixture(t, "minimal")
		engine, err := stage.OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		t.Cleanup(func() { engine.Close() })
		d := newTestDeps(t)
		d.Engine = engine
		assertPlain("no open changeset", New(d).(*Model))
	})
}
