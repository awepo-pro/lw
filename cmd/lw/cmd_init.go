package main

// cmdInit scaffolds a new vault in the current directory (backbone §13,
// s6-polish-release.md S6-T2): SCHEMA.md with the domain's real taxonomy,
// index.md, log.md, an empty curator-memory.md (S6-C129: its headings show
// the file's shape; /PLAN.md §6's rule lines are an example of memory the
// agent has already learned, not seed content), the raw/ and wiki/ trees of
// §14, and .llmwiki/.
//
// init is the one command that writes vault files directly. There is no
// vault yet to stage a changeset against and nothing for a reviewer to
// diff, so the review machinery cannot apply; everything after init goes
// through stage.Engine.Commit. In exchange init is strictly additive: it
// never overwrites a file that is already there, so a --force run into an
// occupied directory can only add what is missing.
//
// .llmwiki/ itself is created by stage.OpenEngine — the one component that
// owns that layout — so init cannot drift from it, and no v0.1 code writes
// .llmwiki/config.toml (S6-T5 documented the omission; this keeps it true).

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
)

// cmdInit implements `lw init [--schema <domain>] [--force]`.
func cmdInit(args []string) error {
	return runInit(initOpts{args: args, in: os.Stdin, out: os.Stdout})
}

// initOpts carries runInit's inputs. in and out are parameters rather than
// reads of os.Stdin/os.Stdout so a test can drive the prompts with a
// strings.Reader; now is the injected clock (00-conventions.md §3) — zero
// means the process clock, UTC.
type initOpts struct {
	args []string
	in   io.Reader
	out  io.Writer
	now  time.Time
}

// runInit parses the flags, refuses an occupied directory unless --force,
// collects the vault's answers — prompted, or defaulted under --schema —
// and scaffolds the tree.
func runInit(o initOpts) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	schemaFlag := flags.String("schema", "", "domain name for SCHEMA.md; skips the interactive prompts")
	force := flags.Bool("force", false, "scaffold into a non-empty directory (existing files are never overwritten)")
	if err := flags.Parse(o.args); err != nil {
		return &exitError{code: 2}
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw init: unexpected argument %q\n", flags.Arg(0))
		return &exitError{code: 2}
	}

	// flags.Visit is what distinguishes `--schema ""` from an absent flag: an
	// explicitly empty domain would otherwise fall back to the directory
	// name and silently ignore what the user typed.
	schemaGiven := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "schema" {
			schemaGiven = true
		}
	})
	domainFlag := strings.TrimSpace(*schemaFlag)
	if schemaGiven && domainFlag == "" {
		fmt.Fprintf(os.Stderr, "lw init: --schema needs a domain name, e.g. --schema ml-systems\n")
		return &exitError{code: 2}
	}

	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}

	if err := refuseOccupied(dir, *force); err != nil {
		return err
	}

	answers := defaultInitAnswers(domainFlag)
	answersEndedEarly := false
	if !schemaGiven {
		answers, answersEndedEarly = promptInitAnswers(newPrompter(o.in, o.out), dir)
	}

	now := o.now
	if now.IsZero() {
		now = time.Now()
	}

	res, err := scaffoldVault(dir, answers, now.UTC())
	if err != nil {
		return err
	}

	// Opening the engine creates .llmwiki/ (backbone §14) and — as a side
	// effect this subtask relies on — proves the scaffold parses and
	// indexes before init claims success.
	e, err := stage.OpenEngine(dir)
	if err != nil {
		return fmt.Errorf("open staging engine: %w", err)
	}
	if err := e.Close(); err != nil {
		return fmt.Errorf("close staging engine: %w", err)
	}

	printInitReport(o.out, dir, answers, res, answersEndedEarly)
	return nil
}

// refuseOccupied returns the refusal error when dir holds anything at all
// and force is false. Hidden entries count: a directory holding only .git
// is not empty to this check, and the message names what was found so
// passing --force is an informed choice.
func refuseOccupied(dir string, force bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	if len(entries) == 0 || force {
		return nil
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	shown := names
	if len(names) > 5 {
		// A fresh slice, so the "… and N more" marker cannot overwrite
		// names[5] through append's reuse of the backing array.
		shown = append([]string(nil), names[:5]...)
		shown = append(shown, fmt.Sprintf("… and %d more", len(names)-5))
	}
	return fmt.Errorf(
		"%s is not empty (found %s); pass --force to scaffold into it anyway — existing files are never overwritten",
		dir, strings.Join(shown, ", "))
}

// initTag is one starter-taxonomy entry: the tag and the gloss written
// beside it in SCHEMA.md.
type initTag struct {
	name  string
	gloss string
}

// initDefaultTags is the taxonomy an Enter-through prompt or a --schema run
// produces: ten domain-neutral tags. A domain-specific taxonomy is the
// curator's to edit once the vault exists; init only has to leave a usable
// one behind.
var initDefaultTags = []initTag{
	{"basics", "foundational ideas the rest of the wiki builds on"},
	{"methods", "procedures, techniques and how-to knowledge"},
	{"tools", "software, libraries and equipment used in the domain"},
	{"benchmarks", "measurements, baselines and comparison numbers"},
	{"pitfalls", "known failure modes and how to avoid them"},
	{"glossary", "short definitions of domain vocabulary"},
	{"reference", "stable facts worth citing, with a raw source"},
	{"open-questions", "things the wiki does not yet answer"},
	{"contested", "claims that disagree between sources"},
	{"example", "worked instances of an idea"},
}

// initPageType pairs a vault.PageType — whose Dir() names the directory, so
// the scaffold cannot drift from the type mapping — with the convention
// line describing what belongs in it.
type initPageType struct {
	typ vault.PageType
	def string
}

// initPageTypes is the prompt order, which is also the order SCHEMA.md's
// "## Page types" section lists them in. `summary` is omitted: the v0.1
// layout scaffolds no wiki/summaries directory.
var initPageTypes = []initPageType{
	{vault.TypeEntity, "a named thing in the domain: a model, product, team, dataset"},
	{vault.TypeConcept, "one idea explained in depth, with at least two outbound links"},
	{vault.TypeComparison, "two or more entities or concepts compared side by side"},
	{vault.TypeQuery, "a standing question the wiki answers as evidence arrives"},
}

// initConventions are /PLAN.md §6's mechanically enforced rules, written
// into every SCHEMA.md so they are visible to the human and to the agent.
var initConventions = []string{
	"Filenames are lowercase-hyphen.md.",
	"Every page carries at least two outbound [[wikilinks]].",
	"Claims drawn from a raw source carry a `^[raw/...]` provenance marker.",
	"Pages over 200 lines are split candidates.",
}

// initIndexMD is the empty catalog: one section per scaffolded page type,
// no lines under them yet. index-sync stays satisfied because a page-less
// vault is trivially 1:1 with an index that links nowhere.
const initIndexMD = `# Index

## Entities

## Concepts

## Comparisons

## Queries
`

// initCuratorMemory is a fresh vault's curator-memory.md: the "## Page
// thresholds" / "## Naming" headings show the file's shape, but it seeds no
// rule under either. /PLAN.md §6's benchmark/gpt-4 lines are an EXAMPLE of
// memory the agent has already learned from a past review, not seed content
// a brand-new vault ships with — a live URL ingest surfaced that a verbatim
// copy taught the curator a preference no reviewer had actually stated yet
// (S6-C129). The agent still edits this file only through stage.patch_page.
const initCuratorMemory = `<!-- curator-memory.md: the curator agent's standing editorial preferences.
It starts with no rules. When your reviews teach the agent a preference, it
adds a dated rule under a heading here through stage.patch_page, and the
change reaches you as a diff like any other page. -->

## Page thresholds

## Naming
`

// initAnswers is everything the prompts — or their defaults — decided.
type initAnswers struct {
	Domain    string
	Tags      []string // normalized, deduped, sorted
	PageTypes []string // convention text, parallel to initPageTypes
}

// defaultInitAnswers is the vault `lw init --schema <domain>` builds, and
// the starting point every prompt edits.
func defaultInitAnswers(domain string) initAnswers {
	types := make([]string, 0, len(initPageTypes))
	for _, p := range initPageTypes {
		types = append(types, p.def)
	}
	return initAnswers{Domain: domain, Tags: defaultInitTagNames(), PageTypes: types}
}

// defaultInitTagNames returns just the starter tags, sorted.
func defaultInitTagNames() []string {
	tags := make([]string, 0, len(initDefaultTags))
	for _, t := range initDefaultTags {
		tags = append(tags, t.name)
	}
	return tags
}

// prompter reads one answer at a time from in, echoing each question to out.
// A blank answer takes the default. An exhausted reader — a pipe that has
// run out, or ^D at a terminal — takes the default for this and every later
// question, so piping a partial transcript still yields a valid vault.
type prompter struct {
	in   *bufio.Reader
	out  io.Writer
	done bool // input exhausted; every answer is now its default
}

// newPrompter wraps in for line-at-a-time reading.
func newPrompter(in io.Reader, out io.Writer) *prompter {
	return &prompter{in: bufio.NewReader(in), out: out}
}

// ask asks one question and returns the answer, or def when the answer is
// blank or the input has run out.
func (p *prompter) ask(label, def string) string {
	if p.done {
		return def
	}
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}

	line, err := p.in.ReadString('\n')
	switch {
	case err == nil:
		// whole line
	case errors.Is(err, io.EOF) && line != "":
		// last line without its newline — still an answer
	default:
		p.done = true
		return def
	}

	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// promptInitAnswers walks the three questions S6-T2 names — domain, tags,
// page-type conventions — in that order. It also reports whether the input
// ran out before every question was asked, so the report can say the rest
// were defaulted rather than leaving the curator to guess.
func promptInitAnswers(p *prompter, dir string) (initAnswers, bool) {
	a := defaultInitAnswers(defaultInitDomain(dir))

	fmt.Fprintf(p.out, "scaffolding a new vault in %s (press Enter to accept a [default])\n", dir)
	a.Domain = p.ask("Domain (a short lowercase name for the wiki)", a.Domain)
	joined := p.ask("Tags, comma-separated (10-20 recommended)", strings.Join(a.Tags, ", "))
	a.Tags = normalizeInitTags(strings.Split(joined, ","))
	if len(a.Tags) == 0 {
		// An empty taxonomy would fail every future page's fm-taxonomy
		// check, so an answer that normalizes away to nothing gets the
		// same starter taxonomy a bare Enter would have taken.
		a.Tags = defaultInitTagNames()
		fmt.Fprintln(p.out, "no usable tags in that answer; using the starter taxonomy")
	}
	for i, pt := range initPageTypes {
		a.PageTypes[i] = p.ask(fmt.Sprintf("%s pages — what belongs in %s", pt.typ, pt.typ.Dir()), a.PageTypes[i])
	}
	return a, p.done
}

// defaultInitDomain derives the domain a bare Enter accepts: the directory's
// own name, slugified. Falling back to the literal below covers "/" and
// other names that slugify away entirely.
func defaultInitDomain(dir string) string {
	if d := slugify(filepath.Base(dir)); d != "" {
		return d
	}
	return "my-domain"
}

// tagSlugRE collapses every run of characters that is not a lowercase ASCII
// letter or digit into a single hyphen, for normalizing typed tags.
var tagSlugRE = regexp.MustCompile(`[^a-z0-9]+`)

// slugify lowercases s and reduces it to lowercase-hyphen form.
func slugify(s string) string {
	return strings.Trim(tagSlugRE.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// normalizeInitTags lowercases, slugs, dedupes and sorts the tags a user
// typed. Rewriting rather than rejecting is deliberate: a taxonomy entry is
// a word, not a path, and the printed report shows exactly what was kept.
func normalizeInitTags(raw []string) []string {
	seen := make(map[string]bool, len(raw))
	var tags []string
	for _, r := range raw {
		t := slugify(r)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		tags = append(tags, t)
	}
	if len(tags) == 0 {
		return nil
	}
	return tags
}

// initDirs lists the empty directories §14 scaffolds, in /PLAN.md §6's own
// order: raw first, then wiki. The wiki half comes from PageType.Dir(), so
// it cannot drift from the type mapping lint's path-convention checks
// against.
func initDirs() []string {
	dirs := []string{"raw/articles", "raw/papers", "raw/transcripts", "raw/assets"}
	for _, pt := range initPageTypes {
		dirs = append(dirs, pt.typ.Dir())
	}
	return dirs
}

// scaffoldResult records what init did to the tree, for its report.
type scaffoldResult struct {
	createdFiles []string
	skippedFiles []string // already present; left byte-for-byte alone
	createdDirs  int      // of the §14 directories initDirs names
}

// scaffoldVault creates the raw/ and wiki/ directories and writes the four
// root markdown files. A file that already exists is reported as skipped and
// left byte-for-byte alone — see this file's package comment.
func scaffoldVault(dir string, a initAnswers, now time.Time) (scaffoldResult, error) {
	var res scaffoldResult

	for _, d := range initDirs() {
		full := filepath.Join(dir, filepath.FromSlash(d))
		if _, err := os.Stat(full); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return res, fmt.Errorf("stat %s: %w", d, err)
		}
		if err := os.MkdirAll(full, 0o755); err != nil {
			return res, fmt.Errorf("create %s: %w", d, err)
		}
		res.createdDirs++
	}

	files := []struct {
		path string
		body []byte
	}{
		{"SCHEMA.md", renderSchema(a)},
		{"index.md", []byte(initIndexMD)},
		{"log.md", renderLog(a.Domain, now)},
		{"curator-memory.md", []byte(initCuratorMemory)},
	}
	for _, f := range files {
		full := filepath.Join(dir, filepath.FromSlash(f.path))
		if _, err := os.Stat(full); err == nil {
			res.skippedFiles = append(res.skippedFiles, f.path)
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return res, fmt.Errorf("stat %s: %w", f.path, err)
		}
		if err := os.WriteFile(full, f.body, 0o644); err != nil {
			return res, fmt.Errorf("write %s: %w", f.path, err)
		}
		res.createdFiles = append(res.createdFiles, f.path)
	}

	return res, nil
}

// renderSchema writes SCHEMA.md: the domain, the taxonomy (sorted — it is
// what fm-taxonomy validates against), what each page type is for, and the
// mechanically enforced conventions. ParseSchema reads "## Domain",
// "## Tags" and "## Conventions"; "## Page types" is for the human and the
// agent, and no code keys on it.
func renderSchema(a initAnswers) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, `# SCHEMA

## Domain

%s — concepts, entities and comparisons for %s, collected from the sources in
raw/ and synthesized into reviewable wiki pages.

## Tags

`, a.Domain, a.Domain)

	for _, t := range a.Tags {
		if gloss, ok := initTagGloss(t); ok {
			fmt.Fprintf(&b, "- `%s` — %s\n", t, gloss)
		} else {
			fmt.Fprintf(&b, "- `%s`\n", t)
		}
	}

	b.WriteString("\n## Page types\n\n")
	for i, pt := range initPageTypes {
		fmt.Fprintf(&b, "- `%s` — %s (%s).\n", pt.typ, a.PageTypes[i], pt.typ.Dir())
	}

	b.WriteString("\n## Conventions\n\n")
	for _, c := range initConventions {
		fmt.Fprintf(&b, "- %s\n", c)
	}

	return []byte(b.String())
}

// initTagGloss returns the gloss for one of the starter tags, and whether it
// has one: a tag the curator typed carries none, rather than a placeholder
// sentence pretending to describe it.
func initTagGloss(tag string) (string, bool) {
	for _, t := range initDefaultTags {
		if t.name == tag {
			return t.gloss, true
		}
	}
	return "", false
}

// renderLog writes log.md with init's own entry, in the shape Engine.Commit
// appends: "- <RFC-3339 UTC> <verb> <object>".
func renderLog(domain string, now time.Time) []byte {
	return []byte(fmt.Sprintf("# Log\n\n- %s init %s\n", now.UTC().Format(time.RFC3339), domain))
}

// printInitReport writes init's summary: what the vault is about, which
// files were written and which were left alone, and the next command. ended
// is true when the prompt input ran out before every question was asked.
func printInitReport(w io.Writer, dir string, a initAnswers, res scaffoldResult, ended bool) {
	fmt.Fprintf(w, "initialized vault in %s\n", dir)
	fmt.Fprintf(w, "  domain           %s\n", a.Domain)
	fmt.Fprintf(w, "  tags             %d (10-20 recommended)\n", len(a.Tags))
	if len(res.createdFiles) > 0 {
		fmt.Fprintf(w, "  files            %s\n", strings.Join(res.createdFiles, ", "))
	}
	if len(res.skippedFiles) > 0 {
		fmt.Fprintf(w, "  skipped          %s (already present; left untouched)\n", strings.Join(res.skippedFiles, ", "))
	}
	fmt.Fprintf(w, "  directories      %s (%d of %d created)\n", initDirsSummary(), res.createdDirs, len(initDirs()))
	fmt.Fprintf(w, "  engine           .llmwiki/ (staging engine state)\n")
	if ended {
		fmt.Fprintf(w, "  note             input ended before every question was answered; the rest took their defaults\n")
	}
	fmt.Fprintf(w, "next: lw config — point lw at a provider, then lw ingest <url> to add the first source\n")
}

// initDirsSummary renders initDirs in the brace form the stage file uses,
// e.g. "raw/{articles,papers,transcripts,assets}, wiki/{comparisons,…}",
// grouped by top-level directory in sorted order.
func initDirsSummary() string {
	grouped := make(map[string][]string, 2)
	for _, d := range initDirs() {
		top, rest, _ := strings.Cut(d, "/")
		grouped[top] = append(grouped[top], rest)
	}

	tops := make([]string, 0, len(grouped))
	for top := range grouped {
		tops = append(tops, top)
	}
	sort.Strings(tops)

	parts := make([]string, 0, len(tops))
	for _, top := range tops {
		parts = append(parts, top+"/{"+strings.Join(grouped[top], ",")+"}")
	}
	return strings.Join(parts, ", ")
}
