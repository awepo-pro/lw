package agent

// 027 T1 (A-027-1): the prompt stops narrating. F.P2's no-narration line
// sits immediately after F.P1's rewritten outside-vault rule, in the same
// paragraph block; the old "say so in one sentence" narration bytes are
// gone from the base prompt, while the provenance-mandatory and filing
// paragraphs stay byte-identical.
import (
	"strings"
	"testing"
)

const (
	noNarrationLine = `Answer the question itself. Do not narrate your process: never say which notes, pages or sources you read or searched; apart from the exact "Not from your vault:" line, never say whether the vault covers the topic. The provenance markers are the record of where each claim came from.`

	outsideVaultRule027 = `If neither the wiki nor the raw sources answer a question, answer from your own knowledge under a first line that reads exactly "Not from your vault:"; carry no provenance marker on those claims, and say plainly when the topic may be newer than your training data.`
)

func TestPromptNoNarration(t *testing.T) {
	for _, hasSearch := range []bool{false, true} {
		prompt := systemPromptFor(hasSearch)

		// F.P2 appears exactly once, on the very next line after F.P1 —
		// same paragraph block, one newline between them.
		if got := strings.Count(prompt, noNarrationLine); got != 1 {
			t.Fatalf("systemPromptFor(%v): no-narration line count = %d, want 1", hasSearch, got)
		}
		i := strings.Index(prompt, noNarrationLine)
		j := strings.Index(prompt, outsideVaultRule027)
		if j == -1 {
			t.Fatalf("systemPromptFor(%v): F.P1 outside-vault rule missing", hasSearch)
		}
		if gap := prompt[j+len(outsideVaultRule027) : i]; gap != "\n" {
			t.Errorf("systemPromptFor(%v): no-narration line is not immediately after F.P1 (gap %q)", hasSearch, gap)
		}

		// The old narration instruction is gone (the web-failure one-liner's
		// "say so in one sentence," is split across its wrapped lines, so the
		// exact old bytes cannot match it either).
		if strings.Contains(prompt, "say so in one sentence, then answer") {
			t.Errorf("systemPromptFor(%v): still contains the old narration bytes", hasSearch)
		}

		// The coverage ban carries the label's carve-out: without it the ban
		// contradicts F.P1's mandatory first line one sentence earlier, and a
		// strict model may resolve the conflict by dropping the exact label
		// the filing paragraph and the UI depend on. The byte pin above
		// already holds the carve-out; this assertion states why it must
		// never be reworded away.
		if !strings.Contains(noNarrationLine, `apart from the exact "Not from your vault:" line, never say whether the vault covers the topic`) {
			t.Fatal("no-narration line lost the carve-out that keeps F.P1's mandatory outside-vault label sanctioned")
		}

		// The mandatory paragraphs stay.
		if !strings.Contains(prompt, "Provenance is mandatory") {
			t.Errorf("systemPromptFor(%v): lost the provenance-mandatory paragraph", hasSearch)
		}
		if !strings.Contains(prompt, promptTailHead) {
			t.Errorf("systemPromptFor(%v): lost the filing paragraph", hasSearch)
		}
	}
}
