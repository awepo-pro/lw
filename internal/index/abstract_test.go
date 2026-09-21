package index

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/awepo-pro/lw/internal/vault"
)

// absPageSrc renders a full page file with the given title and body — the
// same frontmatter shape every other index test builds on, so nothing but
// the body (and title, where the test says so) differs between pages.
func absPageSrc(title, body string) string {
	return "---\ntitle: " + title + "\ncreated: 2026-09-01\nupdated: 2026-09-01\ntype: concept\n---\n\n" + body
}

// absParseAt parses src as a full page file at path.
func absParseAt(t *testing.T, path, src string) *vault.Page {
	t.Helper()
	p, err := vault.ParsePage(path, []byte(src))
	if err != nil {
		t.Fatalf("vault.ParsePage(%q): %v", path, err)
	}
	return p
}

// absIndexOver builds an Index over pages given as path -> full file
// contents, entirely in code — no spec/fixtures dependency (014 T-A).
func absIndexOver(t *testing.T, pages map[string]string) *Index {
	t.Helper()
	fsys := fstest.MapFS{
		"SCHEMA.md": &fstest.MapFile{
			Data: []byte("# SCHEMA\n\n## Tags\n\n- `inference` — running a trained model to produce outputs.\n"),
		},
	}
	for path, src := range pages {
		fsys[path] = &fstest.MapFile{Data: []byte(src)}
	}
	v, err := vault.OpenFS(fsys)
	if err != nil {
		t.Fatalf("vault.OpenFS: %v", err)
	}
	return Build(v)
}

// TestAbstractWeightedAboveTitle pins 014's headline change: with both
// pages otherwise identical and their combined BM25F lengths equal, the
// query term occurring in page 1's ## Abstract must outrank the same term
// occurring in page 2's body only.
//
// The lengths are constructed to cancel so weight is the only variable:
// page 1 has 13 body tokens and a 5-token abstract; page 2 has no abstract
// and 13+4*5 = 33 body tokens — equal combinedLen — and one "zebra" each.
// The abstract occurrence counts in both the body and the x4 abstract
// field (the abstract is a body slice), so page 1's weighted term frequency
// is 5 against page 2's 1.
func TestAbstractWeightedAboveTitle(t *testing.T) {
	abstractBody := "## Abstract\n\nzebra one two three four.\n\n## More\n\nfive six seven eight nine ten.\n"

	fillers := make([]string, 0, 31)
	for i := 0; i < 31; i++ {
		fillers = append(fillers, fmt.Sprintf("w%02d", i))
	}
	plainBody := "## More\n\n" + strings.Join(fillers, " ") + " zebra.\n"

	ix := absIndexOver(t, map[string]string{
		"wiki/concepts/with-abstract.md": absPageSrc("Zebra Study", abstractBody),
		"wiki/concepts/plain.md":         absPageSrc("Zebra Study", plainBody),
	})

	// Sanity: the constructed lengths really do cancel, so the ranking
	// below can only come from the weights.
	dAbs := (*ix.docs.Load())["wiki/concepts/with-abstract.md"]
	dPlain := (*ix.docs.Load())["wiki/concepts/plain.md"]
	if combinedLen(dAbs) != combinedLen(dPlain) {
		t.Fatalf("test construction broken: combinedLen %d != %d", combinedLen(dAbs), combinedLen(dPlain))
	}

	hits := ix.Search("zebra", Options{})
	if len(hits) != 2 {
		t.Fatalf("Search(zebra) returned %d hits, want 2: %+v", len(hits), hits)
	}
	if hits[0].Path != "wiki/concepts/with-abstract.md" {
		t.Fatalf("hits[0].Path = %q, want the abstract-bearing page first\nhits: %+v",
			hits[0].Path, hits)
	}
	if hits[1].Path != "wiki/concepts/plain.md" {
		t.Fatalf("hits[1].Path = %q, want the plain page second\nhits: %+v",
			hits[1].Path, hits)
	}
}

// TestAbstractSnippetPreferred pins the snippet rule: when a term occurs at
// the body's start AND inside the ## Abstract, the snippet centres on the
// abstract occurrence. The lead is long enough that a window centred on the
// body occurrence cannot reach the abstract one, so the two rules produce
// visibly different snippets.
func TestAbstractSnippetPreferred(t *testing.T) {
	lead := []string{"quokka"}
	for i := 0; i < 60; i++ {
		lead = append(lead, fmt.Sprintf("pad%03d", i))
	}
	body := strings.Join(lead, " ") + ".\n\n## Abstract\n\nthe quokka giraffe sentinel study continues here.\n"

	// Directly: the abstract occurrence wins over the body occurrence.
	abstract := "\nthe quokka giraffe sentinel study continues here.\n"
	if got := buildSnippet(abstract, body, []string{"quokka"}); !strings.Contains(got, "giraffe") {
		t.Fatalf("buildSnippet = %q, want it centred on the abstract occurrence", got)
	}

	// End to end, through Search.
	ix := absIndexOver(t, map[string]string{
		"wiki/concepts/quokka.md": absPageSrc("Quokka", body),
	})
	hits := ix.Search("quokka", Options{})
	if len(hits) != 1 {
		t.Fatalf("Search(quokka) returned %d hits, want 1: %+v", len(hits), hits)
	}
	snip := hits[0].Snippet
	if !strings.Contains(snip, "giraffe") {
		t.Fatalf("snippet %q does not centre on the abstract occurrence", snip)
	}
	if strings.Contains(snip, "pad000") {
		t.Fatalf("snippet %q is centred on the body occurrence, not the abstract", snip)
	}
}

// TestAbstractSnippetFallsBackToBody pins the empty-abstract promise: with
// no abstract the snippet is byte-for-byte today's rule — centred on the
// first term occurrence in the body, falling back to the body's start when
// no term occurs there literally.
func TestAbstractSnippetFallsBackToBody(t *testing.T) {
	body := "lead prose.\n\n## Notes\n\nmiddle marmot occurrence sits here in the notes body.\n\ntrailing filler sentence.\n"
	noHitBody := "nothing matching lives here.\n"

	// Today's rule, restated inline: centre on the first term in the body,
	// else 0.
	todaysSnippet := func(b string, terms []string) string {
		lower := strings.ToLower(b)
		center := 0
		for _, term := range terms {
			if idx := strings.Index(lower, term); idx >= 0 {
				center = idx
				break
			}
		}
		return runeWindow(b, center)
	}

	if got := buildSnippet("", body, []string{"marmot"}); got != todaysSnippet(body, []string{"marmot"}) {
		t.Fatalf("buildSnippet empty-abstract = %q, want today's body-centred %q",
			got, todaysSnippet(body, []string{"marmot"}))
	}
	if got := buildSnippet("", noHitBody, []string{"marmot"}); got != todaysSnippet(noHitBody, []string{"marmot"}) {
		t.Fatalf("buildSnippet empty-abstract no-hit = %q, want today's body-start fallback %q",
			got, todaysSnippet(noHitBody, []string{"marmot"}))
	}

	// End to end: the search path goes through the same rule.
	ix := absIndexOver(t, map[string]string{
		"wiki/concepts/marmot.md": absPageSrc("Marmot", body),
	})
	hits := ix.Search("marmot", Options{})
	if len(hits) != 1 {
		t.Fatalf("Search(marmot) returned %d hits, want 1: %+v", len(hits), hits)
	}
	if want := todaysSnippet(body, []string{"marmot"}); hits[0].Snippet != want {
		t.Fatalf("Search snippet = %q, want today's body-centred %q", hits[0].Snippet, want)
	}
}

// TestGobRoundTripKeepsAbstract pins the persist lockstep: gobDoc mirrors
// every new docEntry field, or Save/Load silently drops it. The loaded
// doc's abstract text, term frequencies and length must equal the original's,
// and the loaded index must still answer an abstract-only query.
func TestGobRoundTripKeepsAbstract(t *testing.T) {
	const body = "## Abstract\n\nthe tapir summary mentions foraging and habitat.\n\n## Notes\n\nmore tapir notes.\n"
	original := absIndexOver(t, map[string]string{
		"wiki/concepts/tapir.md": absPageSrc("Tapir", body),
	})

	path := filepath.Join(t.TempDir(), "index.gob")
	if err := original.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	before := (*original.docs.Load())["wiki/concepts/tapir.md"]
	after := (*loaded.docs.Load())["wiki/concepts/tapir.md"]

	// Vacuous-input guard: the page really does carry an abstract.
	if before.Abstract == "" || before.AbstractLen == 0 || len(before.AbstractTermFreq) == 0 {
		t.Fatalf("constructed page produced no abstract: %+v", before)
	}

	if after.Path != "wiki/concepts/tapir.md" {
		t.Fatalf("loaded Path = %q, want %q", after.Path, "wiki/concepts/tapir.md")
	}
	if want := "\nthe tapir summary mentions foraging and habitat.\n\n"; after.Abstract != want {
		t.Fatalf("loaded Abstract = %q, want %q", after.Abstract, want)
	}
	wantFreq := map[string]int{"tapir": 1, "summary": 1, "mentions": 1, "foraging": 1, "habitat": 1}
	if !reflect.DeepEqual(after.AbstractTermFreq, wantFreq) {
		t.Fatalf("loaded AbstractTermFreq = %#v, want %#v", after.AbstractTermFreq, wantFreq)
	}
	if after.AbstractLen != 5 {
		t.Fatalf("loaded AbstractLen = %d, want 5", after.AbstractLen)
	}

	hits := loaded.Search("foraging", Options{})
	if len(hits) != 1 || hits[0].Path != "wiki/concepts/tapir.md" {
		t.Fatalf("loaded index Search(foraging) = %+v, want tapir.md", hits)
	}
}

// TestNoAbstractBehavesAsBefore pins the invariant 014 promises for pages
// without a ## Abstract section: the abstract field contributes exactly
// zero to every weighted quantity, and snippets are the pre-change rule.
func TestNoAbstractBehavesAsBefore(t *testing.T) {
	body := "## Notes\n\nmarmot again with more text about marmot things.\n"
	p := absParseAt(t, "wiki/concepts/no-abs.md", absPageSrc("Marmot Study", body))
	d := buildDocEntry(p)

	if d.Abstract != "" || d.AbstractLen != 0 || len(d.AbstractTermFreq) != 0 {
		t.Fatalf("page without ## Abstract built a non-empty abstract: %+v", d)
	}
	if got, want := combinedLen(d), bodyWeight*d.BodyLen+tagWeight*d.TagLen+titleWeight*d.TitleLen; got != want {
		t.Fatalf("combinedLen = %d, want the three-field value %d (abstract must contribute zero)", got, want)
	}
	for term := range d.BodyTermFreq {
		want := bodyWeight*d.BodyTermFreq[term] + tagWeight*d.TagTermFreq[term] + titleWeight*d.TitleTermFreq[term]
		if got := combinedFreq(d, term); got != want {
			t.Fatalf("combinedFreq(%q) = %d, want the three-field value %d", term, got, want)
		}
	}

	// Snippets: empty abstract reproduces today's rule byte for byte.
	lower := strings.ToLower(body)
	center := 0
	if idx := strings.Index(lower, "marmot"); idx >= 0 {
		center = idx
	}
	if got, want := buildSnippet("", body, []string{"marmot"}), runeWindow(body, center); got != want {
		t.Fatalf("buildSnippet = %q, want pre-change %q", got, want)
	}
}
