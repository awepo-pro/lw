package agent

// prompt_page_cite_test.go is 034 T4's pin: the prompt teaches the page
// citation rule (034 TN) — when raw.get's header names pages, claims cite
// "^[src p.N]" / "^[src p.N-M]", exactly as the header's anchors define
// pages. The paragraph sits directly after the provenance-mandatory
// paragraph it qualifies, on both assemblies. The example markers it carries
// must parse clean through internal/cite, so the prompt's grammar and the
// consumer grammar can never drift apart.

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/cite"
)

// pageCiteRule is the frozen paragraph, byte for byte (034 T4).
const pageCiteRule = `When raw.get's header names pages, the source has a PDF original and its
text carries "<!-- page N -->" lines: cite the page the claim comes from,
e.g. "^[raw/papers/x.md p.12]", or "p.12-13" for a claim that crosses a
page break. A claim's page is the nearest "<!-- page N -->" line above it;
text before the chunk's first such line is on the header's first page.
Write the page exactly as "p.N" — one space after the path, no "pp.", no
"page". A source whose header names no pages is cited without a page.`

func TestPromptPageCitationRule(t *testing.T) {
	for _, hasSearch := range []bool{false, true} {
		prompt := systemPromptFor(hasSearch)

		if !strings.Contains(prompt, pageCiteRule) {
			t.Fatalf("systemPromptFor(%v) does not carry the 034 page-citation rule byte for byte:\n%s", hasSearch, prompt)
		}
		i := strings.Index(prompt, pageCiteRule)

		// The rule qualifies the provenance-mandatory paragraph, so it sits
		// directly after it, joined by exactly one blank line — per the
		// base prompt's one-blank-line paragraph style.
		prov := "Provenance is mandatory. Every synthesized claim drawn from a raw source\ncarries a marker naming that source, e.g. \"^[raw/papers/x.md]\". An\nunmarked claim is indistinguishable from something invented, and a\nreviewer cannot tell the difference from the diff alone — mark as you\nwrite, not as an afterthought."
		j := strings.Index(prompt, prov)
		if j < 0 {
			t.Fatal("precondition: the provenance-mandatory paragraph is missing")
		}
		if i != j+len(prov)+2 {
			t.Errorf("the page-citation rule does not sit directly after the provenance paragraph across one blank line (provenance ends at %d, rule starts at %d)", j+len(prov), i)
		}
	}
}

// TestPromptPageCitationExamplesParse pins that both markers the rule holds
// up as examples are clean cite.Parse results — the prompt must never teach
// a shape internal/cite rejects (or silently accepts with an Err).
func TestPromptPageCitationExamplesParse(t *testing.T) {
	single := cite.Parse("raw/papers/x.md p.12")
	if single.Err != "" || single.Source != "raw/papers/x.md" || single.From != 12 || single.To != 12 {
		t.Fatalf("cite.Parse of the rule's single-page example = %+v, want a clean p.12", single)
	}
	rng := cite.Parse("raw/papers/x.md p.12-13")
	if rng.Err != "" || rng.From != 12 || rng.To != 13 {
		t.Fatalf("cite.Parse of the rule's range example = %+v, want a clean p.12-13", rng)
	}
}
