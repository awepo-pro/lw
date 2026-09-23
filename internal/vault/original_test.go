package vault

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// TestRawSerializeByteIdentical walks EVERY raw source in the minimal
// fixture and proves Parse -> Serialize is the identity (033 gate rule 6):
// the user's vault carries 11+ committed raws, so any byte drift in
// Serialize would light up src-integrity across their whole vault the
// moment this build touches it. It also pins that the fixture raws — all
// ingested before 033 — carry no original pair.
func TestRawSerializeByteIdentical(t *testing.T) {
	root := testutil.FixtureRoot(t)
	rawDir := filepath.Join(root, "minimal", "raw")

	var paths []string
	if err := filepath.WalkDir(rawDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
			paths = append(paths, p)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", rawDir, err)
	}
	if len(paths) == 0 {
		t.Fatalf("no raw fixtures found under %s", rawDir)
	}

	for _, full := range paths {
		rel, err := filepath.Rel(root, full)
		if err != nil {
			t.Fatalf("rel %s: %v", full, err)
		}
		t.Run(rel, func(t *testing.T) {
			b, err := os.ReadFile(full)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			r, err := ParseRawSource(rel, b)
			if err != nil {
				t.Fatalf("ParseRawSource: %v", err)
			}
			if r.Original != "" || r.OriginalSHA256 != "" {
				t.Errorf("pre-033 fixture parsed an original pair: %q / %q", r.Original, r.OriginalSHA256)
			}
			if got := r.Serialize(); !bytes.Equal(got, b) {
				t.Errorf("Serialize() does not reproduce %s byte for byte:\n got=%q\nwant=%q", rel, got, b)
			}
		})
	}
}

// TestRawSerializeByteIdenticalSynthetic rounds the gate's adversarial
// shapes through Parse -> Serialize: a CRLF body, trailing whitespace, and
// a missing final newline must all come back byte-identical — a vault raw
// is write-once, so even one dropped byte would light src-integrity up
// across every source that has it. (Leading-blank-line stripping and the
// refusal of CRLF delimiters are §2.7 contract, not drift, and are pinned
// by the parser's own tests.)
func TestRawSerializeByteIdenticalSynthetic(t *testing.T) {
	sha := BodySHA256("body\r\nline")
	for name, in := range map[string]string{
		"crlf body":               "---\nsource_url: https://e.test/a\ningested: 2026-09-24\nsha256: " + sha + "\n---\n\n# T\r\nline\r\n",
		"trailing whitespace":     "---\nsource_url: https://e.test/a\ningested: 2026-09-24\nsha256: " + sha + "\n---\n\n# T   \nline\t\n",
		"no final newline":        "---\nsource_url: https://e.test/a\ningested: 2026-09-24\nsha256: " + BodySHA256("no newline at eof") + "\n---\n\nno newline at eof",
		"with the original pair":  "---\nsource_url: https://e.test/a\ningested: 2026-09-24\nsha256: " + sha + "\noriginal: raw/papers/x.pdf\noriginal_sha256: " + strings.Repeat("ab", 32) + "\n---\n\n# T\r\nline\r\n",
		"original pair, no eofnl": "---\nsource_url: https://e.test/a\ningested: 2026-09-24\nsha256: " + BodySHA256("x") + "\noriginal: raw/papers/x.pdf\noriginal_sha256: " + strings.Repeat("ab", 32) + "\n---\n\nx",
	} {
		t.Run(name, func(t *testing.T) {
			r, err := ParseRawSource("raw/papers/x.md", []byte(in))
			if err != nil {
				t.Fatalf("ParseRawSource: %v", err)
			}
			if out := r.Serialize(); !bytes.Equal(out, []byte(in)) {
				t.Errorf("NOT byte-identical:\n in=%q\nout=%q", in, out)
			}
		})
	}
}

// TestRawOriginalFrontmatterRoundTrip pins the 033 frontmatter extension:
// a RawSource carrying an original serializes the two keys in the exact
// position — directly after sha256, only when set — and parses back to the
// same values, and a RawSource without one serializes byte-identically to
// the pre-033 three-key shape.
func TestRawOriginalFrontmatterRoundTrip(t *testing.T) {
	ingested, err := ParseDate("2026-09-24")
	if err != nil {
		t.Fatalf("ParseDate: %v", err)
	}
	body := "# Attention Is Boring\n\nBody.\n"

	t.Run("keys emitted after sha256 only when set", func(t *testing.T) {
		src := &RawSource{
			Path:           "raw/papers/attention-is-boring.md",
			SourceURL:      "https://example.test/attention",
			Ingested:       ingested,
			SHA256:         BodySHA256(body),
			Original:       "raw/papers/attention-is-boring.pdf",
			OriginalSHA256: strings.Repeat("ab", 32),
			Body:           body,
		}
		want := "---\n" +
			"source_url: https://example.test/attention\n" +
			"ingested: 2026-09-24\n" +
			"sha256: " + BodySHA256(body) + "\n" +
			"original: raw/papers/attention-is-boring.pdf\n" +
			"original_sha256: " + strings.Repeat("ab", 32) + "\n" +
			"---\n" +
			"\n" +
			body
		if got := src.Serialize(); string(got) != want {
			t.Errorf("Serialize() =\n%q\nwant\n%q", got, want)
		}

		back, err := ParseRawSource(src.Path, src.Serialize())
		if err != nil {
			t.Fatalf("ParseRawSource: %v", err)
		}
		if back.Original != src.Original {
			t.Errorf("Original = %q, want %q", back.Original, src.Original)
		}
		if back.OriginalSHA256 != src.OriginalSHA256 {
			t.Errorf("OriginalSHA256 = %q, want %q", back.OriginalSHA256, src.OriginalSHA256)
		}
		if again := back.Serialize(); !bytes.Equal(again, src.Serialize()) {
			t.Errorf("re-serialize is not a fixed point:\n%q\nvs\n%q", again, src.Serialize())
		}
	})

	t.Run("half-set pair still round-trips", func(t *testing.T) {
		src := &RawSource{
			Path:      "raw/papers/half.md",
			SourceURL: "https://example.test/half",
			Ingested:  ingested,
			SHA256:    BodySHA256(body),
			Original:  "raw/papers/half.pdf",
			Body:      body,
		}
		back, err := ParseRawSource(src.Path, src.Serialize())
		if err != nil {
			t.Fatalf("ParseRawSource: %v", err)
		}
		if back.Original != "raw/papers/half.pdf" || back.OriginalSHA256 != "" {
			t.Errorf("parsed back = %q / %q, want the path set and the sha empty", back.Original, back.OriginalSHA256)
		}
	})
}

// TestAttachmentUnderRawExistsButIsNotParsed pins the 033 attachment rule:
// a non-.md file under raw/ answers Exists and Read (src-integrity checks
// it, Obsidian opens it) but is never parsed as a RawSource, and the .md
// loader stays exactly as strict as before.
func TestAttachmentUnderRawExistsButIsNotParsed(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")

	blob := []byte("%PDF-1.6 fake bytes for the attachment test\n")
	abs := filepath.Join(dir, "raw", "papers", "leviathan-2023.pdf")
	if err := os.WriteFile(abs, blob, 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	v, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !v.Exists("raw/papers/leviathan-2023.pdf") {
		t.Errorf("Exists(raw/papers/leviathan-2023.pdf) = false, want true for a raw/ attachment")
	}
	got, err := v.Read("raw/papers/leviathan-2023.pdf")
	if err != nil {
		t.Fatalf("Read attachment: %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Errorf("Read attachment = %q, want the written bytes %q", got, blob)
	}
	if n := len(v.RawSources()); n != 2 {
		t.Errorf("RawSources() = %d, want the fixture's 2 — the attachment must not be parsed as a source", n)
	}
	if _, ok := v.RawSource("raw/papers/leviathan-2023.pdf"); ok {
		t.Errorf("RawSource() found the attachment, want it to stay unparsed")
	}
}
