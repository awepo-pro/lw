package agent

// abstract_rule_test.go is 014 §5's pin (TS-14A): the abstract rule sits in
// the unconditional tail, between the query-page filing paragraph and the
// name hint, so the pin runs on both assemblies — the paragraph must hold
// whether or not the registry offers web.search. The expectation is
// prompt.go's own const, never a restated literal (the carried_test.go
// pattern).

import (
	"strings"
	"testing"
)

func TestPromptAbstractRule(t *testing.T) {
	for _, hasSearch := range []bool{false, true} {
		prompt := systemPromptFor(hasSearch)

		if !strings.Contains(prompt, abstractRuleParagraph) {
			t.Fatalf("systemPromptFor(%v) does not carry the 014 §5 abstract rule byte for byte:\n%s", hasSearch, prompt)
		}
		i := strings.Index(prompt, abstractRuleParagraph)

		// The abstract rule is its own paragraph, joined to both neighbours
		// by exactly one blank line, per the tail's existing style.
		if i < 2 || prompt[i-2:i] != "\n\n" {
			t.Errorf("the abstract rule does not start its own paragraph: no one-blank-line join before it")
		}
		end := i + len(abstractRuleParagraph)
		if end+2 > len(prompt) || prompt[end:end+2] != "\n\n" {
			t.Errorf("the abstract rule does not end its own paragraph: no one-blank-line join after it")
		}

		// Ordering per TS-14A: after the filing paragraph, before the name
		// hint.
		filing := strings.Index(prompt, filingParagraph)
		if filing < 0 {
			t.Fatal("precondition: the query-page filing paragraph is missing from the prompt")
		}
		if i < filing+len(filingParagraph) {
			t.Errorf("the abstract rule does not come after the query-page filing paragraph")
		}
		hint := strings.Index(prompt, nameHintParagraph)
		if hint < 0 {
			t.Fatal("precondition: the name-hint paragraph is missing from the prompt")
		}
		if i > hint {
			t.Errorf("the abstract rule does not come before the name-hint paragraph")
		}
	}

	// The insertion did not disturb the neighbours: the filing paragraph
	// still opens the tail right after the outside-vault rule, joined by
	// exactly one blank line (the shape TestPromptWebRules pins), and the
	// name hint still runs into the lint paragraph.
	prompt := systemPromptFor(false)
	label := strings.Index(prompt, outsideVaultRule)
	if label < 0 {
		t.Fatal("precondition: the outside-vault rule is missing from systemPromptFor(false)")
	}
	filing := strings.Index(prompt, filingParagraph)
	if filing < 0 {
		t.Fatal("precondition: the query-page filing paragraph is missing from systemPromptFor(false)")
	}
	if filing != label+len(outsideVaultRule)+2 {
		t.Errorf("the filing paragraph no longer follows the outside-vault rule by one blank line (outside-vault ends at %d, filing starts at %d)", label+len(outsideVaultRule), filing)
	}
	hint := strings.Index(prompt, nameHintParagraph)
	if hint < 0 {
		t.Fatal("precondition: the name-hint paragraph is missing from systemPromptFor(false)")
	}
	if !strings.HasPrefix(prompt[hint+len(nameHintParagraph):], "\n\nLint is the engine's job") {
		t.Errorf("the name-hint paragraph is no longer followed by the lint paragraph across one blank line")
	}
}
