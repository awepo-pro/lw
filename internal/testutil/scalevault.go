package testutil

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// scaleSeed seeds the one math/rand.Rand every NewScaleVault call draws from.
// A fixed seed — never the package-global rand, never time.Now — is what
// makes two calls with the same n produce byte-identical trees, which is the
// property the deterministic subtest of TestScaleVault pins down.
const scaleSeed = 20260829

// scaleClockBase is the instant every generated log.md entry is stamped from
// (entry i gets base + i minutes) and scaleDate the created/updated date
// stamped on every generated note's frontmatter. They match FixedClock's
// frozen instant, so a scale vault and a FixedClock-driven test agree on
// "now"; the base is a constant, not a clock read, so nothing here can
// observe the real time.
var scaleClockBase = time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)

// scaleDate is the YYYY-MM-DD form of scaleClockBase, as frontmatter wants it.
const scaleDate = "2026-08-29"

// Shape knobs. scaleLinksPerNote outbound links per note over the strides in
// scaleLinkStrides; body length is uniform over scaleMinBodyLines..
// scaleMaxBodyLines filler lines (a body over 200 lines is a size-split
// info finding by design — a scale vault is meant to give lint something at
// every severity to chew on).
const (
	scaleLinksPerNote = 3
	scaleMinBodyLines = 100
	scaleMaxBodyLines = 300
	scaleParaLines    = 5
)

// scaleLinkStrides selects note i's outbound targets: (i+stride)%n for each
// stride. Stride 1 alone is coprime with every n, so the +1 links by
// themselves form one cycle covering all n notes — every note has an
// outbound link and at least one inbound link, whatever n is (7 and 13 widen
// the graph; for n <= 13 a stride can wrap onto the note itself, which is
// still a resolved link, not a broken one).
var scaleLinkStrides = [scaleLinksPerNote]int{1, 7, 13}

// scaleSchema is the SCHEMA.md every scale vault ships with, modelled on
// spec/fixtures/minimal/SCHEMA.md. ParseSchema (backbone §2.6) requires the
// "## Tags" section, so this block is load-bearing: without it vault.Open
// refuses the whole vault.
const scaleSchema = `# SCHEMA

## Domain

ml-systems — concepts, entities and comparisons related to efficient training
and inference of large language models.

## Tags

- ` + "`inference`" + ` — running a trained model to produce outputs.
- ` + "`decoding`" + ` — strategies for generating tokens one step at a time.
- ` + "`memory`" + ` — memory usage and caching during model execution.
- ` + "`attention`" + ` — attention mechanisms and their computational cost.
- ` + "`kernels`" + ` — low-level, hardware-specific implementations of an operation.
- ` + "`llm`" + ` — large language models as a category of entity.
- ` + "`transformers`" + ` — the transformer architecture and its variants.
- ` + "`quantization`" + ` — reducing numerical precision to save memory or compute.
- ` + "`hardware`" + ` — accelerators and hardware constraints relevant to ML systems.
- ` + "`latency`" + ` — end-to-end or per-token response time.
- ` + "`throughput`" + ` — requests or tokens processed per unit of time.
- ` + "`training`" + ` — fitting model parameters, as opposed to inference.

## Conventions

- Filenames are lowercase-hyphen.md.
- Every page carries at least two outbound [[wikilinks]].
- Claims drawn from a raw source carry a ` + "`^[raw/...]`" + ` provenance marker.
- Pages over 200 lines are split candidates.
`

// scaleCuratorMemory is the standing curator-memory.md, modelled on
// spec/fixtures/minimal/curator-memory.md. It is a root bookkeeping file, not
// a Page (backbone §2.8 loads only wiki/ and raw/), so its content is free
// text.
const scaleCuratorMemory = `## Page thresholds
- Do not create pages for individual benchmark numbers. (2026-08-14, after I rejected 4 such pages.)

## Naming
- Prefer the hyphenated vendor form: ` + "`gpt-4`" + `, not ` + "`gpt4`" + `. (2026-08-29)
`

// scaleTaxonomy mirrors the `## Tags` bullets of scaleSchema, in file order.
// NewScaleVault draws every note's tags from it, so a tag can never drift out
// of the taxonomy without the schema_parses subtest of TestScaleVault
// catching the mismatch against the schema the vault actually ships.
var scaleTaxonomy = []string{
	"inference",
	"decoding",
	"memory",
	"attention",
	"kernels",
	"llm",
	"transformers",
	"quantization",
	"hardware",
	"latency",
	"throughput",
	"training",
}

// scaleVocab is the word pool filler prose is drawn from. Every word is bare
// lowercase ASCII — no "#", "-", "[[", "`" or fencing — so a filler line can
// never manufacture a heading, a list, a wikilink or a code span, and the
// links a page carries are exactly the scaleLinksPerNote ones the Related
// section puts there.
var scaleVocab = []string{
	"attention", "batch", "cache", "compute", "decode", "embedding", "fused",
	"kernel", "kv", "latency", "layer", "memory", "paged", "precision",
	"prefill", "quantized", "schedule", "sequence", "shard", "sparse",
	"speculative", "stream", "tensor", "throughput", "token", "training",
}

// scaleTitleAdj and scaleTitleNoun compose each note's seeded title.
var scaleTitleAdj = []string{
	"Sparse", "Streaming", "Paged", "Quantized", "Fused",
	"Batched", "Sharded", "Speculative", "Layered", "Cached",
}

var scaleTitleNoun = []string{
	"Attention", "Decoding", "Throughput", "Latency", "Prefill",
	"Kernel", "Memory", "Scheduling", "Inference", "Training",
}

// ScaleNotePath returns the vault-relative path of generated note i:
// wiki/concepts/note-%04d.md. It is exported because later subtasks drive
// stage ops against a scale vault and need to name its pages without
// hard-coding the layout.
func ScaleNotePath(i int) string {
	return fmt.Sprintf("wiki/concepts/note-%04d.md", i)
}

// NewScaleVault writes a deterministic, well-formed lw vault of exactly n
// generated notes into root — creating root and its wiki/concepts directory
// as needed, overwriting anything already there — and fails tb on any I/O
// error. Alongside the n notes it writes the four root bookkeeping files a
// real vault carries (SCHEMA.md, index.md, log.md, curator-memory.md), with
// index.md and log.md listing every generated note, so the result drives
// vault.Open, index.Build, lint.Run and stage ops at realistic scale.
//
// Determinism contract: two calls with the same n produce byte-identical
// trees. All randomness comes from one math/rand.Rand seeded with scaleSeed
// and drawn in a fixed order (titles first, then per note: tags, then body);
// all dates derive from scaleClockBase; there is no time.Now and no global
// rand. A negative n is an error.
func NewScaleVault(tb testing.TB, root string, n int) {
	tb.Helper()

	if err := writeScaleVault(root, n); err != nil {
		tb.Fatalf("testutil: NewScaleVault(root=%s, n=%d): %v", root, n, err)
	}
}

// writeScaleVault is NewScaleVault's error-returning core, unexported so the
// generation logic is testable without needing to observe a *testing.T
// failure.
func writeScaleVault(root string, n int) error {
	if n < 0 {
		return fmt.Errorf("note count is negative: %d", n)
	}

	// Skeleton first: the two static root files. index.md and log.md come
	// last, after the notes, because both list every note.
	skeleton := []struct{ path, body string }{
		{"SCHEMA.md", scaleSchema},
		{"curator-memory.md", scaleCuratorMemory},
	}
	for _, f := range skeleton {
		if err := writeScaleFile(root, f.path, f.body); err != nil {
			return err
		}
	}

	// One seeded source for the whole tree, drawn in a fixed order so the
	// byte-for-byte output is a function of n alone.
	rng := rand.New(rand.NewSource(scaleSeed))
	titles := scaleTitles(rng, n)

	var index, log strings.Builder
	index.WriteString("# Index\n\n## Concepts\n\n")
	log.WriteString("# Log\n\n")

	for i := 0; i < n; i++ {
		body := scaleNote(rng, i, n, titles[i])
		if err := writeScaleFile(root, ScaleNotePath(i), body); err != nil {
			return err
		}
		fmt.Fprintf(&index, "- [[%s]] — %s.\n", scaleNoteName(i), titles[i])
		fmt.Fprintf(&log, "- %s create_page %s\n", scaleStamp(i), ScaleNotePath(i))
	}

	for _, f := range []struct{ path, body string }{
		{"index.md", index.String()},
		{"log.md", log.String()},
	} {
		if err := writeScaleFile(root, f.path, f.body); err != nil {
			return err
		}
	}
	return nil
}

// writeScaleFile creates the parent directories for root/path and writes
// body there, mode 0644. Paths are slash-separated vault-relative paths.
func writeScaleFile(root, path, body string) error {
	target := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	return nil
}

// scaleTitles draws n titles from rng, one per note, in note order. It runs
// before any note is generated so index.md and the Related sections can name
// a note's title without a second draw sequence.
func scaleTitles(rng *rand.Rand, n int) []string {
	titles := make([]string, n)
	for i := range titles {
		titles[i] = fmt.Sprintf("Note %04d %s %s",
			i, scaleTitleAdj[rng.Intn(len(scaleTitleAdj))], scaleTitleNoun[rng.Intn(len(scaleTitleNoun))])
	}
	return titles
}

// scaleNote renders note i of n: frontmatter carrying the schema-required
// keys, a seeded title, scaleMinBodyLines..scaleMaxBodyLines seeded filler
// lines, and scaleLinksPerNote wikilinks to notes (i+stride)%n. The body
// ends with exactly one "\n" (backbone §2.3), so the file is already in
// canonical form and a later lint --fix has nothing to rewrite.
func scaleNote(rng *rand.Rand, i, n int, title string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", title)
	fmt.Fprintf(&b, "created: %s\n", scaleDate)
	fmt.Fprintf(&b, "updated: %s\n", scaleDate)
	b.WriteString("type: concept\n")
	fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(scaleTags(rng), ", "))
	b.WriteString("confidence: high\n")
	b.WriteString("---\n\n")

	fmt.Fprintf(&b, "# %s\n\n", title)
	b.WriteString(scaleSentence(rng, 12, 20))
	b.WriteString("\n\n## Notes\n\n")
	b.WriteString(scaleParagraphs(scaleFiller(rng)))
	b.WriteString("## Related\n\n")
	b.WriteString(scaleRelated(rng, i, n))
	return b.String()
}

// scaleTags draws 2 or 3 distinct tags from scaleTaxonomy and returns them
// sorted, so the emitted order is a function of the chosen set alone.
func scaleTags(rng *rand.Rand) []string {
	count := 2 + rng.Intn(2)

	chosen := make(map[int]bool, count)
	tags := make([]string, 0, count)
	for len(tags) < count {
		idx := rng.Intn(len(scaleTaxonomy))
		if chosen[idx] {
			continue
		}
		chosen[idx] = true
		tags = append(tags, scaleTaxonomy[idx])
	}
	sort.Strings(tags)
	return tags
}

// scaleFiller returns scaleMinBodyLines..scaleMaxBodyLines filler sentences,
// one per line, each built from scaleVocab so it can never introduce markup.
func scaleFiller(rng *rand.Rand) []string {
	count := scaleMinBodyLines + rng.Intn(scaleMaxBodyLines-scaleMinBodyLines+1)

	lines := make([]string, count)
	for i := range lines {
		lines[i] = scaleSentence(rng, 8, 15)
	}
	return lines
}

// scaleSentence returns one filler sentence of minWords..maxWords vocabulary
// words, first word capitalized, ending in a period.
func scaleSentence(rng *rand.Rand, minWords, maxWords int) string {
	words := make([]string, minWords+rng.Intn(maxWords-minWords+1))
	for i := range words {
		words[i] = scaleVocab[rng.Intn(len(scaleVocab))]
	}
	words[0] = strings.ToUpper(words[0][:1]) + words[0][1:]
	return strings.Join(words, " ") + "."
}

// scaleParagraphs joins lines into paragraphs of scaleParaLines lines, each
// paragraph followed by one blank line, so the last paragraph's trailing
// blank line sets up the heading that follows.
func scaleParagraphs(lines []string) string {
	var b strings.Builder
	for start := 0; start < len(lines); start += scaleParaLines {
		end := min(start+scaleParaLines, len(lines))
		b.WriteString(strings.Join(lines[start:end], "\n"))
		b.WriteString("\n\n")
	}
	return b.String()
}

// scaleRelated renders note i's Related section: one resolved wikilink per
// stride, each glossed with a seeded vocabulary word.
func scaleRelated(rng *rand.Rand, i, n int) string {
	var b strings.Builder
	for _, stride := range scaleLinkStrides {
		target := (i + stride) % n
		fmt.Fprintf(&b, "- [[%s]] — companion page on %s.\n",
			scaleNoteName(target), scaleVocab[rng.Intn(len(scaleVocab))])
	}
	return b.String()
}

// scaleNoteName returns the linkable stem of note i — note-0007 — the form
// every [[wikilink]] between generated notes uses.
func scaleNoteName(i int) string {
	return fmt.Sprintf("note-%04d", i)
}

// scaleStamp returns the log.md timestamp for note i: scaleClockBase plus i
// minutes, so entries are ordered and distinct while never reading the real
// clock.
func scaleStamp(i int) string {
	return scaleClockBase.Add(time.Duration(i) * time.Minute).UTC().Format(time.RFC3339)
}
