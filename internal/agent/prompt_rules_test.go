package agent

// prompt_rules_test.go is 012 contract §1's matrix over the two web-lookup
// paragraphs. with_search keeps 010 §5's assertions: both paragraphs present
// byte for byte, the injection rule after the search rule, both after the
// "Not from your vault:" rule they qualify. without_search is 012's new half:
// no registry-offered web.search, no paragraph — and not even the substring
// web.search anywhere in the prompt. Permanent per D-10C, amended per 012
// D-12B and per 017 TS-17A (the search rule's bytes became 017 §5's
// auto-search + quota-fallback text; the assertions kept their shape). The
// expectations are prompt.go's own consts, never restated
// literals.

import (
	"strings"
	"testing"
)

func TestPromptWebRules(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hasSearch bool
	}{
		{"with_search", true},
		{"without_search", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prompt := systemPromptFor(tc.hasSearch)
			search := strings.Index(prompt, webSearchRule)
			injection := strings.Index(prompt, webInjectionRule)

			if !tc.hasSearch {
				if search >= 0 || injection >= 0 {
					t.Fatalf("without_search still carries the web paragraphs (search at %d, injection at %d)", search, injection)
				}
				if strings.Contains(prompt, "web.search") {
					t.Fatalf("without_search prompt still mentions web.search anywhere:\n%s", prompt)
				}
				// The text on both sides of the removed block is unchanged
				// and still separated by exactly one blank line: the
				// outside-vault rule closes promptBase (027 added the
				// no-narration line to that paragraph), the query-page
				// filing rule opens promptTail.
				label := strings.Index(prompt, outsideVaultRule)
				if label < 0 {
					t.Fatal("without_search lost the outside-vault rule")
				}
				filing := strings.Index(prompt, filingParagraph)
				if filing < 0 {
					t.Fatal("without_search lost the query-page filing rule")
				}
				if filing != label+len(outsideVaultRule)+1+len(noNarrationLine)+2 {
					t.Fatalf("base and tail are not joined by one blank line: outside-vault rule ends at %d, filing paragraph starts at %d", label+len(outsideVaultRule), filing)
				}
				return
			}

			if search < 0 {
				t.Fatalf("systemPromptFor(true) does not carry the 010 §5 search rule byte for byte\nwant: %s", webSearchRule)
			}
			if injection < 0 {
				t.Fatalf("systemPromptFor(true) does not carry the 010 §5 injection rule byte for byte\nwant: %s", webInjectionRule)
			}
			if injection < search {
				t.Fatalf("injection rule (at %d) precedes the search rule (at %d); §5 puts it after", injection, search)
			}
			label := strings.Index(prompt, outsideVaultRule)
			if label < 0 {
				t.Fatal("systemPromptFor(true) lost the outside-vault rule the web rules qualify")
			}
			if search < label || injection < label {
				t.Fatalf("web rules (search %d, injection %d) must both sit after the \"Not from your vault:\" rule (at %d)", search, injection, label)
			}
		})
	}
}

// TestSystemPromptHasNoOrientRitual is 022's pin: the orientation ritual
// left the system prompt. The digest itself is ContextBuilder.Build's job —
// injected as its own system message once per session, refreshed on an
// index.md change (context.go, §11.3 item 3) — and vault.orient stays
// registered for on-demand calls, so neither the ritual's name nor the
// tool's belongs in the prompt on either assembly.
func TestSystemPromptHasNoOrientRitual(t *testing.T) {
	for _, hasSearch := range []bool{false, true} {
		prompt := systemPromptFor(hasSearch)
		for _, banned := range []string{"Orientation ritual", "vault.orient"} {
			if strings.Contains(prompt, banned) {
				t.Errorf("systemPromptFor(%v) still carries %q:\n%s", hasSearch, banned, prompt)
			}
		}
	}
}
