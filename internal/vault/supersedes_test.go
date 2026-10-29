package vault

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/awepo-pro/lw/internal/testutil"
)

// V1 Serialize/Parse.
func TestV1_SerializeParse(t *testing.T) {
	d, err := ParseDate("2026-10-29")
	if err != nil {
		t.Fatalf("ParseDate: %v", err)
	}

	r := &RawSource{
		SourceURL:  "/n/a.md",
		Ingested:   d,
		SHA256:     "S",
		Supersedes: "raw/articles/a.md",
		Body:       "# T\n",
	}

	got := string(r.Serialize())
	want := "---\nsource_url: /n/a.md\ningested: 2026-10-29\nsha256: S\nsupersedes: raw/articles/a.md\n---\n\n# T\n"
	if got != want {
		t.Fatalf("Serialize mismatch:\ngot:\n%q\nwant:\n%q", got, want)
	}

	parsed, err := ParseRawSource("raw/articles/a-2.md", []byte(got))
	if err != nil {
		t.Fatalf("ParseRawSource: %v", err)
	}
	if parsed.Supersedes != "raw/articles/a.md" {
		t.Errorf("parsed.Supersedes = %q, want %q", parsed.Supersedes, "raw/articles/a.md")
	}
	if parsed.SourceURL != "/n/a.md" {
		t.Errorf("parsed.SourceURL = %q, want %q", parsed.SourceURL, "/n/a.md")
	}
	if parsed.Ingested != d {
		t.Errorf("parsed.Ingested = %v, want %v", parsed.Ingested, d)
	}
	if parsed.SHA256 != "S" {
		t.Errorf("parsed.SHA256 = %q, want %q", parsed.SHA256, "S")
	}
	if parsed.Body != "# T\n" {
		t.Errorf("parsed.Body = %q, want %q", parsed.Body, "# T\n")
	}

	// With Supersedes:"" the bytes equal today's output (fixture raw round-trips byte-identically).
	root := testutil.FixtureRoot(t)
	fixturePath := filepath.Join(root, "minimal", "raw", "papers", "leviathan-2023.md")
	fixtureBytes, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("ReadFile fixture: %v", err)
	}
	fixtureParsed, err := ParseRawSource("raw/papers/leviathan-2023.md", fixtureBytes)
	if err != nil {
		t.Fatalf("ParseRawSource fixture: %v", err)
	}
	if fixtureParsed.Supersedes != "" {
		t.Errorf("fixtureParsed.Supersedes = %q, want empty", fixtureParsed.Supersedes)
	}
	reserialized := fixtureParsed.Serialize()
	if !bytes.Equal(reserialized, fixtureBytes) {
		t.Errorf("Serialize() with empty Supersedes does not match original bytes byte-identically")
	}

	// A raw that has both supersedes and the 033 original pair round-trips.
	rWithOrig := &RawSource{
		SourceURL:      "/n/a.md",
		Ingested:       d,
		SHA256:         "S",
		Supersedes:     "raw/articles/a.md",
		Original:       "raw/articles/a-2.pdf",
		OriginalSHA256: "P",
		Body:           "# T\n",
	}
	origBytes := rWithOrig.Serialize()
	wantWithOrig := "---\nsource_url: /n/a.md\ningested: 2026-10-29\nsha256: S\nsupersedes: raw/articles/a.md\noriginal: raw/articles/a-2.pdf\noriginal_sha256: P\n---\n\n# T\n"
	if string(origBytes) != wantWithOrig {
		t.Fatalf("Serialize with original mismatch:\ngot:\n%q\nwant:\n%q", string(origBytes), wantWithOrig)
	}
	parsedWithOrig, err := ParseRawSource("raw/articles/a-2.md", origBytes)
	if err != nil {
		t.Fatalf("ParseRawSource with original: %v", err)
	}
	if parsedWithOrig.Supersedes != "raw/articles/a.md" {
		t.Errorf("parsedWithOrig.Supersedes = %q, want %q", parsedWithOrig.Supersedes, "raw/articles/a.md")
	}
	if parsedWithOrig.Original != "raw/articles/a-2.pdf" {
		t.Errorf("parsedWithOrig.Original = %q, want %q", parsedWithOrig.Original, "raw/articles/a-2.pdf")
	}
	if parsedWithOrig.OriginalSHA256 != "P" {
		t.Errorf("parsedWithOrig.OriginalSHA256 = %q, want %q", parsedWithOrig.OriginalSHA256, "P")
	}
	if !bytes.Equal(parsedWithOrig.Serialize(), origBytes) {
		t.Errorf("roundtrip with original did not produce identical bytes")
	}

	// An old-shape raw (no key) parses with Supersedes == "".
	oldShape := []byte("---\nsource_url: /n/old.md\ningested: 2026-10-29\nsha256: S\n---\n\n# Old\n")
	parsedOld, err := ParseRawSource("raw/articles/old.md", oldShape)
	if err != nil {
		t.Fatalf("ParseRawSource old shape: %v", err)
	}
	if parsedOld.Supersedes != "" {
		t.Errorf("parsedOld.Supersedes = %q, want empty", parsedOld.Supersedes)
	}
}

// V2 round trip through attach.
func TestV2_RoundTripThroughAttach(t *testing.T) {
	d, _ := ParseDate("2026-10-29")
	raw := &RawSource{
		Path:       "raw/articles/a-2.md",
		SourceURL:  "/n/a.md",
		Ingested:   d,
		SHA256:     "S",
		Supersedes: "raw/articles/a.md",
		Body:       "# T\n",
	}

	initialBytes := raw.Serialize()
	parsed, err := ParseRawSource("raw/articles/a-2.md", initialBytes)
	if err != nil {
		t.Fatalf("ParseRawSource: %v", err)
	}
	if parsed.Supersedes != "raw/articles/a.md" {
		t.Fatalf("parsed.Supersedes = %q, want raw/articles/a.md", parsed.Supersedes)
	}

	// Simulate cmd/lw attach: updated := *parsed; updated.Original = ...; updated.Serialize()
	updated := *parsed
	updated.Original = "raw/articles/a-2.pdf"
	updated.OriginalSHA256 = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"

	updatedBytes := updated.Serialize()
	reparsed, err := ParseRawSource("raw/articles/a-2.md", updatedBytes)
	if err != nil {
		t.Fatalf("ParseRawSource after attach: %v", err)
	}
	if reparsed.Supersedes != "raw/articles/a.md" {
		t.Errorf("reparsed.Supersedes = %q, want raw/articles/a.md", reparsed.Supersedes)
	}
	if reparsed.Original != updated.Original {
		t.Errorf("reparsed.Original = %q, want %q", reparsed.Original, updated.Original)
	}
	if reparsed.OriginalSHA256 != updated.OriginalSHA256 {
		t.Errorf("reparsed.OriginalSHA256 = %q, want %q", reparsed.OriginalSHA256, updated.OriginalSHA256)
	}
}

// V3 SameSource table.
func TestV3_SameSource(t *testing.T) {
	table := []struct {
		a    string
		b    string
		want bool
	}{
		{"https://Example.com/a/b/#frag", "https://example.com/a/b", true},
		{"https://example.com/a?x=1", "https://example.com/a", false},
		{"https://example.com/a", "http://example.com/a", false},
		{"/home/u/n.md", "/home/u/./x/../n.md", true},
		{"/home/u/n.md", "/home/u/n.markdown", false},
		{"n.md", "n.md", false},
		{"n.md", "/home/u/n.md", false},
		{"", "", false},
		{"https://example.com/a", "/home/u/a", false},
	}

	for _, tc := range table {
		got := SameSource(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("SameSource(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		// Symmetry
		gotRev := SameSource(tc.b, tc.a)
		if gotRev != tc.want {
			t.Errorf("SameSource(%q, %q) [rev] = %v, want %v", tc.b, tc.a, gotRev, tc.want)
		}
	}
}

// V4 RevisionTip and SupersededBy.
func TestV4_RevisionTip(t *testing.T) {
	schemaContent := "# SCHEMA\n\n## Domain\nTest domain.\n\n## Tags\n- `test`\n"

	// Subtest 1: R1 (url U), R2 (url U, supersedes: R1) -> tip for U = R2, SupersededBy(R1) = R2
	t.Run("supersedes link", func(t *testing.T) {
		fsys := fstest.MapFS{
			"SCHEMA.md": &fstest.MapFile{Data: []byte(schemaContent)},
			"raw/articles/r1.md": &fstest.MapFile{Data: []byte(
				"---\nsource_url: https://example.com/doc\ningested: 2026-10-01\nsha256: 1111\n---\n\n# R1\n",
			)},
			"raw/articles/r2.md": &fstest.MapFile{Data: []byte(
				"---\nsource_url: https://example.com/doc\ningested: 2026-10-02\nsha256: 2222\nsupersedes: raw/articles/r1.md\n---\n\n# R2\n",
			)},
		}
		v, err := OpenFS(fsys)
		if err != nil {
			t.Fatalf("OpenFS: %v", err)
		}

		tip, ok := v.RevisionTip("https://example.com/doc")
		if !ok || tip == nil {
			t.Fatalf("RevisionTip ok = %v, tip = %v, want R2", ok, tip)
		}
		if tip.Path != "raw/articles/r2.md" {
			t.Errorf("tip.Path = %q, want raw/articles/r2.md", tip.Path)
		}

		succ, ok := v.SupersededBy("raw/articles/r1.md")
		if !ok || succ != "raw/articles/r2.md" {
			t.Errorf("SupersededBy(r1) = (%q, %v), want (raw/articles/r2.md, true)", succ, ok)
		}

		succ2, ok2 := v.SupersededBy("raw/articles/r2.md")
		if ok2 || succ2 != "" {
			t.Errorf("SupersededBy(r2) = (%q, %v), want (\"\", false)", succ2, ok2)
		}
	})

	// Subtest 2: Vault with R1, R3 (both url U, no links, R3 ingested later): tip = R3.
	t.Run("no links latest ingested wins", func(t *testing.T) {
		fsys := fstest.MapFS{
			"SCHEMA.md": &fstest.MapFile{Data: []byte(schemaContent)},
			"raw/articles/r1.md": &fstest.MapFile{Data: []byte(
				"---\nsource_url: https://example.com/doc\ningested: 2026-10-01\nsha256: 1111\n---\n\n# R1\n",
			)},
			"raw/articles/r3.md": &fstest.MapFile{Data: []byte(
				"---\nsource_url: https://example.com/doc\ningested: 2026-10-05\nsha256: 3333\n---\n\n# R3\n",
			)},
		}
		v, err := OpenFS(fsys)
		if err != nil {
			t.Fatalf("OpenFS: %v", err)
		}

		tip, ok := v.RevisionTip("https://example.com/doc")
		if !ok || tip == nil {
			t.Fatalf("RevisionTip ok = %v, tip = %v, want R3", ok, tip)
		}
		if tip.Path != "raw/articles/r3.md" {
			t.Errorf("tip.Path = %q, want raw/articles/r3.md", tip.Path)
		}
	})

	// Subtest 3: Same Ingested: greatest path wins.
	t.Run("same ingested greatest path wins", func(t *testing.T) {
		fsys := fstest.MapFS{
			"SCHEMA.md": &fstest.MapFile{Data: []byte(schemaContent)},
			"raw/articles/a.md": &fstest.MapFile{Data: []byte(
				"---\nsource_url: https://example.com/doc\ningested: 2026-10-01\nsha256: 1111\n---\n\n# A\n",
			)},
			"raw/articles/b.md": &fstest.MapFile{Data: []byte(
				"---\nsource_url: https://example.com/doc\ningested: 2026-10-01\nsha256: 2222\n---\n\n# B\n",
			)},
		}
		v, err := OpenFS(fsys)
		if err != nil {
			t.Fatalf("OpenFS: %v", err)
		}

		tip, ok := v.RevisionTip("https://example.com/doc")
		if !ok || tip == nil {
			t.Fatalf("RevisionTip ok = %v, tip = %v, want b.md", ok, tip)
		}
		if tip.Path != "raw/articles/b.md" {
			t.Errorf("tip.Path = %q, want raw/articles/b.md", tip.Path)
		}
	})

	// Subtest 4: No raw for U: ok=false.
	t.Run("no raw for U", func(t *testing.T) {
		fsys := fstest.MapFS{
			"SCHEMA.md": &fstest.MapFile{Data: []byte(schemaContent)},
		}
		v, err := OpenFS(fsys)
		if err != nil {
			t.Fatalf("OpenFS: %v", err)
		}

		tip, ok := v.RevisionTip("https://example.com/missing")
		if ok || tip != nil {
			t.Errorf("RevisionTip for missing = (%v, %v), want (nil, false)", tip, ok)
		}
	})

	// Subtest 5: A supersedes: naming a path that is not a committed raw does not panic and links nothing.
	// SupersededBy of that path still returns the claimant.
	t.Run("claimant of non-raw path", func(t *testing.T) {
		fsys := fstest.MapFS{
			"SCHEMA.md": &fstest.MapFile{Data: []byte(schemaContent)},
			"raw/articles/claimant.md": &fstest.MapFile{Data: []byte(
				"---\nsource_url: https://example.com/other\ningested: 2026-10-01\nsha256: 9999\nsupersedes: raw/missing.md\n---\n\n# Claimant\n",
			)},
		}
		v, err := OpenFS(fsys)
		if err != nil {
			t.Fatalf("OpenFS: %v", err)
		}

		// SupersededBy of missing path returns the claimant
		succ, ok := v.SupersededBy("raw/missing.md")
		if !ok || succ != "raw/articles/claimant.md" {
			t.Errorf("SupersededBy(raw/missing.md) = (%q, %v), want (raw/articles/claimant.md, true)", succ, ok)
		}

		// RevisionTip for missing raw does not panic and returns false
		tip, ok := v.RevisionTip("https://example.com/missing")
		if ok || tip != nil {
			t.Errorf("RevisionTip = (%v, %v), want (nil, false)", tip, ok)
		}
	})
}
