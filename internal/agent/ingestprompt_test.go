package agent

// ingestprompt_test.go is 063/065's pin of IngestSystemPrompt: `lw ingest
// --prepare` prints the curator rules into the brief an external agent reads,
// and those rules must be the very bytes the in-process ingest turn sends the
// remote model (056: no web paragraphs; 065: trims ask/filing rules, carries
// the raw/assets figure preservation rule), not a copy that can drift. It reuses
// 056's newInwFixture/inwStopRound (ingestnoweb_test.go) and 048's rbCleanStop.
// Permanent regression test (D-10C).

import (
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
)

// TestIngestSystemPromptIsTheIngestTurnPrompt: the exported helper returns the
// dedicated ingest curator prompt, never names web.search, differs from both
// systemPromptFor(true) and systemPromptFor(false), and is byte-for-byte message
// 0 of a real ingest turn over a registry that DOES offer web.search.
func TestIngestSystemPromptIsTheIngestTurnPrompt(t *testing.T) {
	got := IngestSystemPrompt()
	if got == systemPromptFor(false) {
		t.Errorf("IngestSystemPrompt() equals systemPromptFor(false); the ingest prompt must trim ask and filing rules")
	}
	if got == systemPromptFor(true) {
		t.Errorf("IngestSystemPrompt() equals the web-capable prompt; the two must differ or this test proves nothing")
	}
	if strings.Contains(got, "web.search") {
		t.Errorf("IngestSystemPrompt() mentions web.search; an ingest turn is not offered it")
	}

	f := newInwFixture(t, true, [][]llm.Chunk{inwStopRound()})
	events, err := f.send(t, "ingest")
	rbCleanStop(t, events, err)
	reqs := f.fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("Stream called %d times, want 1", len(reqs))
	}
	if m := reqs[0].Messages[0]; m.Role != "system" || m.Content != got {
		t.Fatalf("the ingest turn's message 0 is not IngestSystemPrompt():\n%q", m.Content)
	}
}

// TestIngestSystemPromptTrimsAskAndFilingRules asserts that IngestSystemPrompt()
// trims ask-only rules and the query-page filing rule, while curator prompts
// systemPromptFor(false) and systemPromptFor(true) still carry them.
func TestIngestSystemPromptTrimsAskAndFilingRules(t *testing.T) {
	got := IngestSystemPrompt()

	banned := []struct {
		name    string
		pattern string
	}{
		{"promptOutsideVault", promptOutsideVault},
		{"Not from your vault:", "Not from your vault:"},
		{"promptAnswerVoice", promptAnswerVoice},
		{"promptTailHead", promptTailHead},
		{"When asked to file an answer as a query page", "When asked to file an answer as a query page"},
		{"webSearchRule", webSearchRule},
		{"webInjectionRule", webInjectionRule},
	}
	for _, b := range banned {
		if strings.Contains(got, b.pattern) {
			t.Errorf("IngestSystemPrompt() contains %s; must be trimmed from ingest prompt", b.name)
		}
	}

	for _, p := range []struct {
		name   string
		prompt string
	}{
		{"systemPromptFor(false)", systemPromptFor(false)},
		{"systemPromptFor(true)", systemPromptFor(true)},
	} {
		for _, required := range []struct {
			name    string
			pattern string
		}{
			{"promptOutsideVault", promptOutsideVault},
			{"promptAnswerVoice", promptAnswerVoice},
			{"promptTailHead", promptTailHead},
		} {
			if !strings.Contains(p.prompt, required.pattern) {
				t.Errorf("%s missing %s", p.name, required.name)
			}
		}
	}
}

// TestIngestSystemPromptCarriesAssetRule asserts that IngestSystemPrompt() contains
// ingestAssetRuleParagraph byte for byte as its own paragraph positioned after
// abstractRuleParagraph and before nameHintParagraph, equals the expected
// assembly, and contains no triple newlines.
func TestIngestSystemPromptCarriesAssetRule(t *testing.T) {
	got := IngestSystemPrompt()

	wantExact := promptCuratorCore + "\n" + abstractRuleParagraph + "\n\n" + ingestAssetRuleParagraph + "\n\n" + promptTailRest
	if got != wantExact {
		t.Fatalf("IngestSystemPrompt() does not match expected assembly:\n got %q\nwant %q", got, wantExact)
	}

	if strings.Contains(got, "\n\n\n") {
		t.Errorf("IngestSystemPrompt() contains triple newline; paragraphs must join with exactly one blank line")
	}

	if !strings.Contains(got, "\n\n"+ingestAssetRuleParagraph+"\n\n") {
		t.Errorf("IngestSystemPrompt() does not contain ingestAssetRuleParagraph enclosed in blank lines")
	}

	idxAbstract := strings.Index(got, abstractRuleParagraph)
	idxAsset := strings.Index(got, ingestAssetRuleParagraph)
	idxNameHint := strings.Index(got, nameHintParagraph)

	if idxAbstract < 0 || idxAsset < 0 || idxNameHint < 0 {
		t.Fatalf("missing expected paragraph in IngestSystemPrompt: abstract=%d, asset=%d, nameHint=%d", idxAbstract, idxAsset, idxNameHint)
	}
	if !(idxAbstract < idxAsset && idxAsset < idxNameHint) {
		t.Errorf("paragraphs out of order: abstract at %d, asset at %d, nameHint at %d", idxAbstract, idxAsset, idxNameHint)
	}
}
