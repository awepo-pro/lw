package agent

// carried_test.go is 009 contract §1's tests for the Record.Carried flag and
// the two filing paragraphs §1.3 adds to the system prompt (012 D-12B: pinned
// on the without-search assembly, systemPromptFor(false)). The store in every
// round-trip is a real NewFileSessions over a temp vault — no fake of the
// store (MASTER §5, T-A assertions).

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// filingParagraph and nameHintParagraph are 009 contract §1.3's two prompt
// paragraphs, byte for byte; promptFilingTest asserts the system prompt
// carries them verbatim.
const (
	filingParagraph   = `When asked to file an answer as a query page, first read the existing query pages you are given and run wiki.search with type "query"; if one already answers the same question, update it with stage.patch_page instead of creating a second page. Otherwise stage.create_page under wiki/queries/ with type: query. Keep every provenance marker from the answer; a claim that carried no marker, or sat under "Not from your vault:", stays out of the page. sources: lists raw paths only: for a claim marked with a wiki page, use that page's own sources.`
	nameHintParagraph = `When a source's title has no Latin letters, pass stage.ingest_source a short English slug in name, e.g. "quaternion-introduction"; it is used only when the title gives no usable file name.`

	// outsideVaultRule is prompt.go's existing A-806 rule, the sentence both
	// new paragraphs must come after.
	outsideVaultRule = `If neither the wiki nor the raw sources answer a question, answer from your own knowledge under a first line that reads exactly "Not from your vault:"; carry no provenance marker on those claims, and say plainly when the topic may be newer than your training data.`
)

func TestCarriedRecord(t *testing.T) {
	t.Run("carried_omitempty_on_normal_records", func(t *testing.T) {
		// omitempty is load-bearing: every session.ndjson written before 009
		// must re-encode byte-identically (contract §1.1), which only holds
		// if a record without Carried marshals without the key at all.
		line, err := json.Marshal(rec(time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC), "user", "hello"))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if bytes.Contains(line, []byte(`"carried"`)) {
			t.Fatalf("a record without Carried marshals with a %q key: %s", "carried", line)
		}
	})

	t.Run("carried_round_trips", func(t *testing.T) {
		store := NewFileSessions(t.TempDir())
		const id = "cs-carried"
		if _, err := store.Create(id); err != nil {
			t.Fatalf("Create: %v", err)
		}

		want := Record{TS: time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC), Role: "user", Content: "Q1", Carried: true}
		if err := store.Append(id, want); err != nil {
			t.Fatalf("Append: %v", err)
		}

		got, err := store.Get(id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(got.Records) != 1 {
			t.Fatalf("Get = %d records, want 1", len(got.Records))
		}
		if !reflect.DeepEqual(got.Records[0], want) {
			t.Fatalf("Append then Get = %+v, want %+v — Carried must survive the round trip", got.Records[0], want)
		}
	})

	t.Run("carried_message_identical", func(t *testing.T) {
		// The wire must not change (contract §1.1): a carried record folds
		// into the very same llm.Message as the same record without the
		// flag — recordToMessage ignores it.
		ts := time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
		for _, base := range []Record{
			rec(ts, "user", "Q1"),
			rec(ts.Add(time.Second), "assistant", "A1"),
		} {
			carried := base
			carried.Carried = true
			got, want := recordToMessage(carried), recordToMessage(base)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("recordToMessage(carried %s) = %+v, without the flag = %+v; Carried must never reach the wire", base.Role, got, want)
			}
		}

		// Positive guard so the equality above cannot pass vacuously: the
		// carried record still folds into its role and content.
		m := recordToMessage(Record{TS: ts, Role: "user", Content: "Q1", Carried: true})
		if m.Role != "user" || m.Content != "Q1" {
			t.Fatalf("recordToMessage(carried user) = %+v, want the user message with content %q", m, "Q1")
		}
	})
}

func TestPromptFiling(t *testing.T) {
	// Both filing paragraphs sit in the prompt's unconditional tail since
	// 012 (D-12B), so the pin uses the without-search assembly and holds on
	// every vault, web-configured or not.
	prompt := systemPromptFor(false)

	// assertOwnParagraph fails unless the paragraph found at index i starts
	// and ends its own paragraph: delimited by newlines on both sides,
	// never glued onto a neighbour.
	assertOwnParagraph := func(t *testing.T, para string, i int) {
		t.Helper()
		if i > 0 && prompt[i-1] != '\n' {
			t.Error("the paragraph does not start its own paragraph")
		}
		if end := i + len(para); end < len(prompt) && prompt[end] != '\n' {
			t.Error("the paragraph does not end its own paragraph")
		}
	}

	t.Run("prompt_contains_filing_paragraph", func(t *testing.T) {
		if !strings.Contains(prompt, filingParagraph) {
			t.Fatalf("systemPromptFor(false) does not contain contract §1.3's filing paragraph byte for byte:\n%s", prompt)
		}
		i := strings.Index(prompt, filingParagraph)
		assertOwnParagraph(t, filingParagraph, i)

		// After the existing "Not from your vault:" sentence, per §1.3.
		j := strings.Index(prompt, outsideVaultRule)
		if j == -1 {
			t.Fatal("precondition: the A-806 out-of-vault rule is missing from systemPromptFor(false)")
		}
		if i < j+len(outsideVaultRule) {
			t.Errorf("the filing paragraph does not come after the Not from your vault: sentence")
		}
	})

	t.Run("prompt_contains_name_hint", func(t *testing.T) {
		if !strings.Contains(prompt, nameHintParagraph) {
			t.Fatalf("systemPromptFor(false) does not contain contract §1.3's name-hint paragraph byte for byte:\n%s", prompt)
		}
		i := strings.Index(prompt, nameHintParagraph)
		assertOwnParagraph(t, nameHintParagraph, i)

		j := strings.Index(prompt, outsideVaultRule)
		if j == -1 {
			t.Fatal("precondition: the A-806 out-of-vault rule is missing from systemPromptFor(false)")
		}
		if i < j+len(outsideVaultRule) {
			t.Errorf("the name-hint paragraph does not come after the Not from your vault: sentence")
		}

		// Contract order: the filing paragraph first, the name hint second.
		k := strings.Index(prompt, filingParagraph)
		if k == -1 {
			t.Fatal("precondition: the filing paragraph is missing from systemPromptFor(false)")
		}
		if i < k {
			t.Errorf("the name-hint paragraph comes before the filing paragraph; contract §1.3 lists the filing paragraph first")
		}
	})
}
