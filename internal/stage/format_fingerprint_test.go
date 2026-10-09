// format_fingerprint_test.go is the P2 enforcement of 042 (lw sync): the
// vault's on-disk shape is written down in testdata/format-fingerprint.txt,
// and any change to it fails TestFormatFingerprint until a human has bumped
// stage.FormatVersion, added a migration and regenerated the golden file.
//
// Why it exists. lw sync copies a vault between PCs through git, so a PC
// running yesterday's lw can meet a vault last written by tomorrow's. The
// format gate (format.go) turns that into a clear refusal, but only if the
// version moves whenever the shape does. A rule remembered by people is
// forgotten; a failing test is not. The fingerprint therefore records every
// type lw marshals into a synced path, found by reflection so that a field
// added to Op tomorrow shows up here without anyone remembering this file.
//
// What it covers, and how each part was found (grep for json.Marshal,
// json.NewEncoder, gob and yaml writers in the packages that write under
// .llmwiki/ and raw/; the marshal-site section below re-runs that search on
// every test run):
//
//   - [types]: reflection over stage.Changeset (changeset.json, written by
//     engine_changeset.go) and everything it nests (Op, Hunk, Author,
//     Checks), stage.Event (journal.ndjson, journal.go) and its two Data
//     payloads (commit_end in apply.go, reverted in revert.go), and
//     agent.Record (session.ndjson beside every changeset, agent/session.go).
//   - [enums]: the string vocabularies those types carry (OpKind, OpState,
//     EventKind), read from the source with go/parser — a new op kind is a
//     shape change reflection cannot see.
//   - [snapshot tree]: snapshots/NNNNNN.tree is a text format, so the test
//     writes one through WriteSnapshot and records its bytes.
//   - [frontmatter keys]: raw/ sources (vault.RawSource.Serialize) and wiki
//     pages (vault.Frontmatter.Encode) are YAML emitted by hand; the test
//     serialises a fully populated value and records the keys it emitted.
//   - [layout]: the files lw leaves under .llmwiki/ after a real commit, a
//     rejection and an open changeset, with ids and object names normalised.
//     A new file there is either per-PC state (add it to vaultsync's ignore
//     list) or synced state (it is now part of the shape).
//   - [marshal sites]: how many JSON/YAML marshal calls each of the three
//     packages that write synced state contains. A new call site is the
//     cheapest sign of a new persisted type that nobody added to [types].
//
// Deliberately not covered: .llmwiki/index.gob, cache/, tmp/, logs/, traces/
// and lock are per-PC and never synced; log.md's entry lines and the notes/
// files (cmd/lw) are text written by other packages and are the user's to
// edit; CAS object bytes are zlib streams whose sha is over the UNCOMPRESSED
// bytes precisely so the compressor can change freely.
package stage_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// fingerprintGolden is the recorded shape, relative to this package.
const fingerprintGolden = "testdata/format-fingerprint.txt"

// fingerprintMismatch is the sentence a failing fingerprint opens with. It
// names the remedy because the person reading it has just changed a struct
// and is about to run -update without thinking; the order matters.
const fingerprintMismatch = "the vault's on-disk shape changed: bump stage.FormatVersion and add a migration, then run go test ./internal/stage -run TestFormatFingerprint -update"

// fingerprintLayoutHint is the second line of the failure when only the
// [layout] section moved: a new file under .llmwiki/ is far more often
// per-PC state (a cache, a lock) than a format change, and the fix for that
// is the ignore list, not a version bump (042 D2).
const fingerprintLayoutHint = "if the new path is per-PC state, add it to vaultsync.Ignore instead (042 D2)"

// TestFormatFingerprint compares the vault's current on-disk shape with the
// golden file. -update (the repo-wide testutil flag) rewrites the golden, but
// only through planFingerprintUpdate: a changed shape needs a bumped
// stage.FormatVersion first.
func TestFormatFingerprint(t *testing.T) {
	body := renderFingerprintBody(t)

	if testutil.UpdateEnabled() {
		if err := updateFingerprintGolden(fingerprintGolden, body, stage.FormatVersion); err != nil {
			t.Fatal(err.Error())
		}
		return
	}

	got := fingerprintHeader(stage.FormatVersion) + body
	want, err := os.ReadFile(fingerprintGolden)
	if err != nil {
		t.Fatalf("%s\n(cannot read %s: %v)", fingerprintMismatch, fingerprintGolden, err)
	}
	if string(want) != got {
		t.Fatal(fingerprintFailure(string(want), got))
	}
}

// updateFingerprintGolden is -update: it reads the golden at path (a missing
// file is fine), asks planFingerprintUpdate what may be written, and writes
// it. On refusal the file is left exactly as it was.
func updateFingerprintGolden(path, body string, version int) error {
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("update %s: %w", path, err)
	}
	golden, err := planFingerprintUpdate(string(old), body, version)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(golden), 0o644); err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}
	return nil
}

// fingerprintFailure builds TestFormatFingerprint's failure text: the frozen
// sentence, then — when [layout] is the only section that changed — the
// per-PC hint on a second line, then what moved.
func fingerprintFailure(want, got string) string {
	var b strings.Builder
	b.WriteString(fingerprintMismatch)
	b.WriteString("\n")
	if changed := changedSections(want, got); len(changed) == 1 && changed[0] == "layout" {
		b.WriteString(fingerprintLayoutHint)
		b.WriteString("\n")
	}
	b.WriteString(lineDiff(want, got))
	return b.String()
}

// changedSections returns, sorted, the names of the golden sections whose
// text differs between want and got. The text above the first "[section]"
// line — the comment block and the format-version line — is the section
// "header".
func changedSections(want, got string) []string {
	wantSections, gotSections := goldenSections(want), goldenSections(got)
	names := map[string]bool{}
	for n := range wantSections {
		names[n] = true
	}
	for n := range gotSections {
		names[n] = true
	}
	var changed []string
	for n := range names {
		if wantSections[n] != gotSections[n] {
			changed = append(changed, n)
		}
	}
	sort.Strings(changed)
	return changed
}

// sectionLine matches a "[name]" section marker line of the golden file.
var sectionLine = regexp.MustCompile(`^\[(.+)\]$`)

// goldenSections splits golden text into its sections, keyed by name, each
// value the section's lines including its marker.
func goldenSections(text string) map[string]string {
	sections := map[string]string{}
	name := "header"
	for _, line := range strings.SplitAfter(text, "\n") {
		if m := sectionLine.FindStringSubmatch(strings.TrimSuffix(line, "\n")); m != nil {
			name = m[1]
		}
		sections[name] += line
	}
	return sections
}

// fingerprintHeader is the golden file's header: the generated-file notice
// and the FormatVersion the file was generated under. The notice repeats the
// rule planFingerprintUpdate enforces, for the person who opens the file.
func fingerprintHeader(version int) string {
	var b strings.Builder
	b.WriteString("# The vault's on-disk shape (042). Generated by TestFormatFingerprint; do not edit by hand.\n")
	b.WriteString("# Changing anything below means a vault written by one lw may be misread by another:\n")
	b.WriteString("# bump stage.FormatVersion, add a migration, then\n")
	b.WriteString("#   go test ./internal/stage -run TestFormatFingerprint -update\n")
	fmt.Fprintf(&b, "format-version: %d\n", version)
	return b.String()
}

// versionLine is the golden header's format-version line.
var versionLine = regexp.MustCompile(`(?m)^format-version: (.*)$`)

// splitFingerprint splits a golden file into the FormatVersion in its header
// and the body below the header line.
func splitFingerprint(golden string) (version int, body string, err error) {
	loc := versionLine.FindStringSubmatchIndex(golden)
	if loc == nil {
		return 0, "", fmt.Errorf("%s has no format-version line; if it is damaged, delete it and run -update to generate it afresh", fingerprintGolden)
	}
	version, err = strconv.Atoi(strings.TrimSpace(golden[loc[2]:loc[3]]))
	if err != nil || version < 1 {
		return 0, "", fmt.Errorf("%s: the format-version line %q is not a version number", fingerprintGolden, golden[loc[0]:loc[1]])
	}
	// The body starts after the header line's own newline.
	return version, strings.TrimPrefix(golden[loc[1]:], "\n"), nil
}

// planFingerprintUpdate decides what -update may write. oldGolden is the file
// on disk ("" when there is none yet), body the shape rendered now, version
// stage.FormatVersion.
//
// The rule: a changed shape under an unchanged — or lowered — FormatVersion
// is refused. Left to a bare "rewrite the file", -update turned the
// fingerprint into advice: change a struct, run -update, commit, and a vault
// written by the new lw is silently misread by the old one. The golden's own
// header is what makes the rule checkable, since it remembers which version
// the old shape belonged to. An unchanged shape, or a bumped version, writes
// the new golden with the current version in its header.
func planFingerprintUpdate(oldGolden, body string, version int) (string, error) {
	if oldGolden == "" {
		return fingerprintHeader(version) + body, nil
	}
	oldVersion, oldBody, err := splitFingerprint(oldGolden)
	if err != nil {
		return "", err
	}
	if body != oldBody && version <= oldVersion {
		return "", fmt.Errorf("the vault's on-disk shape changed but stage.FormatVersion is still %d: bump it (and add a migration) before running -update", oldVersion)
	}
	return fingerprintHeader(version) + body, nil
}

// lineDiff says what changed between the golden file and the current shape.
// A line present on both sides under the same "Name: " key (a type's field
// list, a package's marshal sites) is reported as the words removed and added,
// because a type's line is one very long record and "-old +new" of the whole
// thing hides the one field that moved; any other line only one side has is
// reported whole, "-" for the golden file's and "+" for the current shape's.
func lineDiff(want, got string) string {
	split := func(s string) []string { return strings.Split(strings.TrimSuffix(s, "\n"), "\n") }
	key := func(l string) (string, bool) {
		k, _, ok := strings.Cut(l, ": ")
		return k, ok
	}
	wantLines, gotLines := split(want), split(got)
	inWant, inGot := map[string]bool{}, map[string]bool{}
	wantByKey, gotByKey := map[string]string{}, map[string]string{}
	for _, l := range wantLines {
		inWant[l] = true
		if k, ok := key(l); ok {
			wantByKey[k] = l
		}
	}
	for _, l := range gotLines {
		inGot[l] = true
		if k, ok := key(l); ok {
			gotByKey[k] = l
		}
	}
	words := func(l string) map[string]bool {
		m := map[string]bool{}
		for _, w := range strings.Fields(l) {
			m[w] = true
		}
		return m
	}

	var b strings.Builder
	for _, l := range wantLines {
		if inGot[l] {
			continue
		}
		if k, ok := key(l); ok && gotByKey[k] != "" {
			continue // reported once, from the paired line below
		}
		fmt.Fprintf(&b, "- %s\n", l)
	}
	for _, l := range gotLines {
		if inWant[l] {
			continue
		}
		k, ok := key(l)
		if !ok || wantByKey[k] == "" {
			fmt.Fprintf(&b, "+ %s\n", l)
			continue
		}
		oldWords, newWords := words(wantByKey[k]), words(l)
		fmt.Fprintf(&b, "~ %s:", k)
		for _, w := range strings.Fields(wantByKey[k]) {
			if !newWords[w] {
				fmt.Fprintf(&b, " -%s", w)
			}
		}
		for _, w := range strings.Fields(l) {
			if !oldWords[w] {
				fmt.Fprintf(&b, " +%s", w)
			}
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return "(same lines, different order or whitespace)"
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// renderFingerprintBody renders everything in the golden file below its
// header. Every section is sorted or emitted in a fixed order, so the same
// code always renders the same bytes.
func renderFingerprintBody(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("\n[types]\n")
	b.WriteString(renderTypes())

	b.WriteString("\n[enums]\n")
	b.WriteString(renderEnums(t))

	b.WriteString("\n[snapshot tree]\n")
	b.WriteString(renderSnapshotTree(t))

	b.WriteString("\n[frontmatter keys]\n")
	b.WriteString(renderFrontmatterKeys(t))

	b.WriteString("\n[layout]\n")
	b.WriteString(renderLayout(t))

	b.WriteString("\n[marshal sites]\n")
	b.WriteString(renderMarshalSites(t))
	return b.String()
}

// --- [types] --------------------------------------------------------------

// fingerprintRoots are the types lw marshals directly into a synced path.
// Everything they nest is found by renderTypes itself.
func fingerprintRoots() []reflect.Type {
	roots := []reflect.Type{
		reflect.TypeOf(stage.Changeset{}), // changesets/{committed,rejected}/<id>/changeset.json
		reflect.TypeOf(stage.Event{}),     // journal.ndjson
		reflect.TypeOf(agent.Record{}),    // changesets/{committed,rejected}/<id>/session.ndjson
	}
	for _, p := range stage.PersistedPayloadTypes { // journal Event.Data payloads
		roots = append(roots, reflect.TypeOf(p))
	}
	return roots
}

var (
	timeType = reflect.TypeOf(time.Time{})
	rawType  = reflect.TypeOf(json.RawMessage(nil))
)

// renderTypes renders every struct reachable from the roots, one line each:
//
//	pkg.TypeName: Field(json-name,kind) Field(json-name,kind) …
//
// sorted by type name and, within a line, by json name. Fields tagged
// json:"-" are not persisted and are left out; a field newly persisted
// therefore appears as an addition. omitempty is not recorded: it changes
// how zero values are written, never how any lw reads them.
func renderTypes() string {
	seen := map[reflect.Type]bool{}
	var order []reflect.Type
	var visit func(t reflect.Type)
	visit = func(t reflect.Type) {
		t = unwrap(t)
		if t.Kind() != reflect.Struct || t == timeType || seen[t] {
			return
		}
		seen[t] = true
		order = append(order, t)
		for i := 0; i < t.NumField(); i++ {
			if f := t.Field(i); f.IsExported() && f.Tag.Get("json") != "-" {
				visit(f.Type)
			}
		}
	}
	for _, r := range fingerprintRoots() {
		visit(r)
	}
	sort.Slice(order, func(i, j int) bool { return order[i].String() < order[j].String() })

	var b strings.Builder
	for _, t := range order {
		type field struct{ json, text string }
		var fields []field
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			if f.Anonymous && tag == "" {
				panic(fmt.Sprintf("fingerprint: %s embeds %s; encoding/json flattens embedded structs — teach renderTypes before adding one", t, f.Type))
			}
			name, _, _ := strings.Cut(tag, ",")
			if name == "" {
				name = f.Name
			}
			fields = append(fields, field{name, fmt.Sprintf("%s(%s,%s)", f.Name, name, renderKind(f.Type))})
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].json < fields[j].json })
		parts := make([]string, len(fields))
		for i, f := range fields {
			parts[i] = f.text
		}
		fmt.Fprintf(&b, "%s: %s\n", t, strings.Join(parts, " "))
	}
	return b.String()
}

// unwrap strips pointers, slices, arrays and map values down to the element
// type a struct walk should follow.
func unwrap(t reflect.Type) reflect.Type {
	for {
		switch t.Kind() {
		case reflect.Ptr, reflect.Slice, reflect.Array, reflect.Map:
			if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
				return t // []byte, json.RawMessage: a leaf
			}
			t = t.Elem()
		default:
			return t
		}
	}
}

// renderKind names a field's type the way a reader of the golden file needs
// it: basic kinds by reflect.Kind, nested structs by name, containers with
// their element, and the two byte-slice leaves as "bytes" and "raw".
func renderKind(t reflect.Type) string {
	switch {
	case t == timeType:
		return "time"
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8:
		if t == rawType {
			return "raw"
		}
		return "bytes"
	}
	switch t.Kind() {
	case reflect.Ptr:
		return "*" + renderKind(t.Elem())
	case reflect.Slice:
		return "[]" + renderKind(t.Elem())
	case reflect.Array:
		return fmt.Sprintf("[%d]%s", t.Len(), renderKind(t.Elem()))
	case reflect.Map:
		return "map[" + renderKind(t.Key()) + "]" + renderKind(t.Elem())
	case reflect.Struct:
		if t.Name() == "" {
			return "struct"
		}
		return t.Name()
	case reflect.Interface:
		return "any"
	}
	return t.Kind().String()
}

// --- [enums] --------------------------------------------------------------

// renderEnums lists the string values of every constant declared with one of
// the vocabulary types, read from this package's source. Reflection cannot
// see constants, and a new OpKind is exactly the change an older lw would
// choke on.
func renderEnums(t *testing.T) string {
	t.Helper()
	wanted := map[string]bool{"OpKind": true, "OpState": true, "EventKind": true}
	values := map[string][]string{}

	fset := token.NewFileSet()
	for _, name := range goSourceFiles(t, ".") {
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				id, ok := vs.Type.(*ast.Ident)
				if !ok || !wanted[id.Name] {
					continue
				}
				for _, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Fatalf("%s: constant of type %s is not a string literal", fset.Position(v.Pos()), id.Name)
					}
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("%s: %v", fset.Position(v.Pos()), err)
					}
					values[id.Name] = append(values[id.Name], s)
				}
			}
		}
	}

	var b strings.Builder
	for _, name := range []string{"EventKind", "OpKind", "OpState"} {
		vs := values[name]
		if len(vs) == 0 {
			t.Fatalf("found no constants of type %s in the stage sources", name)
		}
		sort.Strings(vs)
		fmt.Fprintf(&b, "stage.%s: %s\n", name, strings.Join(vs, " "))
	}
	return b.String()
}

// goSourceFiles returns the non-test .go file names in dir, sorted.
func goSourceFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// --- [snapshot tree] ------------------------------------------------------

// renderSnapshotTree writes a two-entry snapshot through the real writer and
// records the file's exact bytes (quoted, so the two spaces and the newline
// are visible). The second path holds a space on purpose: the format's path
// field runs to the end of the line.
func renderSnapshotTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	snap := stage.Snapshot{
		"wiki/a page.md": strings.Repeat("ab", 32),
		"index.md":       strings.Repeat("01", 32),
	}
	if err := stage.WriteSnapshot(dir, "000007", snap); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	// The file's name is part of the format too (nextCommitID scans for it),
	// so it is read back from the directory rather than assumed.
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("WriteSnapshot left %d entries in %s, want exactly one file (err %v)", len(entries), dir, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	name := strings.Replace(entries[0].Name(), "000007", "<NNNNNN>", 1)
	return fmt.Sprintf("snapshots/%s: %q\n", name, string(b))
}

// --- [frontmatter keys] ---------------------------------------------------

// renderFrontmatterKeys serialises a fully populated raw source and wiki
// page and lists the YAML keys each emitted, in emit order. Emit order is
// itself part of the canonical form (byte-stable round trips), so it is
// recorded rather than sorted.
func renderFrontmatterKeys(t *testing.T) string {
	t.Helper()
	d, err := vault.ParseDate("2026-01-02")
	if err != nil {
		t.Fatal(err)
	}

	raw := (&vault.RawSource{
		Path:           "raw/papers/x.md",
		SourceURL:      "https://example.test/x",
		Ingested:       d,
		SHA256:         strings.Repeat("ab", 32),
		Body:           "# X\n",
		Original:       "raw/papers/x.pdf",
		OriginalSHA256: strings.Repeat("cd", 32),
	}).Serialize()

	// A sentinel extra key stands for "unknown keys, preserved, sorted" so
	// the golden does not fossilise the sentinel's name.
	const extraKey = "zz-extra"
	page := vault.Frontmatter{
		Title:      "T",
		Created:    d,
		Updated:    d,
		Type:       "concept",
		Tags:       []string{"a"},
		Sources:    []string{"raw/papers/x.md"},
		Confidence: "high",
		Contested:  true,
		Extra:      map[string]string{extraKey: "v"},
	}.Encode()

	pageKeys := frontmatterKeys(t, page)
	for i, k := range pageKeys {
		if k == extraKey {
			pageKeys[i] = "<extra keys, sorted>"
		}
	}
	return fmt.Sprintf("raw source: %s\nwiki page: %s\n",
		strings.Join(frontmatterKeys(t, raw), " "), strings.Join(pageKeys, " "))
}

// frontmatterKeys returns the key of every "key: value" line between the
// first two "---" lines of doc, in order.
func frontmatterKeys(t *testing.T, doc []byte) []string {
	t.Helper()
	lines := strings.Split(string(doc), "\n")
	if len(lines) == 0 || lines[0] != "---" {
		t.Fatalf("document does not open with a frontmatter fence:\n%s", doc)
	}
	var keys []string
	for _, l := range lines[1:] {
		if l == "---" {
			return keys
		}
		k, _, ok := strings.Cut(l, ":")
		if !ok {
			t.Fatalf("frontmatter line %q has no key", l)
		}
		keys = append(keys, k)
	}
	t.Fatalf("frontmatter has no closing fence:\n%s", doc)
	return nil
}

// --- [layout] -------------------------------------------------------------

var (
	changesetDirPattern = regexp.MustCompile(`^changesets/(open|committed|rejected)/cs-[0-9a-f]+/`)
	objectPathPattern   = regexp.MustCompile(`^objects/[0-9a-f]{2}/[0-9a-f]+$`)
)

// layoutContent is a page that validates against the minimal fixture's
// schema (two outbound wikilinks to pages that exist there).
const layoutContent = "---\n" +
	"title: Layout Probe\n" +
	"created: 2026-08-29\n" +
	"updated: 2026-08-29\n" +
	"type: concept\n" +
	"tags: [inference]\n" +
	"confidence: medium\n" +
	"---\n" +
	"\n" +
	"# Layout Probe\n" +
	"\n" +
	"See [[kv-cache]] and [[gpt-4]] for background.\n"

// renderLayout drives a real engine through a commit, a rejection and a
// left-open changeset — each with a session, as the agent writes them — and
// lists every file the vault's .llmwiki/ then holds. Changeset ids and CAS
// object names are normalised; everything else is literal.
func renderLayout(t *testing.T) string {
	t.Helper()
	dir := testutil.CopyFixture(t, "minimal")
	e, err := stage.OpenEngine(dir, stage.WithClock(testutil.FixedClock()))
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	sessions := agent.NewFileSessions(dir)
	author := stage.Author{Kind: "agent", Model: "fingerprint"}
	ts := testutil.FixedClock()()

	openWithSession := func(intent string) *stage.Changeset {
		t.Helper()
		cs, err := e.OpenChangeset(intent, author)
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		if _, err := sessions.Create(cs.ID); err != nil {
			t.Fatalf("Create session: %v", err)
		}
		if err := sessions.Append(cs.ID, agent.Record{TS: ts, Role: "user", Content: "probe"}); err != nil {
			t.Fatalf("Append session record: %v", err)
		}
		return cs
	}

	openWithSession("layout: committed")
	if _, err := e.Append(stage.Op{
		Kind:       stage.OpCreatePage,
		Path:       "wiki/concepts/layout-probe.md",
		Content:    []byte(layoutContent),
		Rationale:  "layout probe",
		Provenance: []string{"raw/papers/leviathan-2023.md"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := e.Commit("layout probe"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	openWithSession("layout: rejected")
	if err := e.Reject("layout probe"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	openWithSession("layout: open")

	root := filepath.Join(dir, ".llmwiki")
	set := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		rel = changesetDirPattern.ReplaceAllString(rel, "changesets/$1/<changeset>/")
		if objectPathPattern.MatchString(rel) {
			rel = "objects/<sha[0:2]>/<sha[2:]>"
		}
		set[".llmwiki/"+rel] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	var lines []string
	for l := range set {
		lines = append(lines, l)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

// --- [marshal sites] ------------------------------------------------------

// marshalSitePackages are the packages that write synced state. The per-PC
// writers (index, trace, extract/cache, logging) are left out on purpose:
// their output is in vaultsync's ignore list and never travels.
var marshalSitePackages = []string{"../stage", "../agent", "../vault"}

// marshalSiteNotes say what each known call site marshals. They are
// documentation for whoever reads the golden file, not an allowlist: an
// unlisted site still appears, un-annotated, and still fails the comparison.
var marshalSiteNotes = map[string]string{
	"stage/apply.go":            "persisted: commit_end Event.Data (commitEndPayload)",
	"stage/engine_changeset.go": "persisted: changeset.json (Changeset)",
	"stage/journal.go":          "persisted: journal.ndjson (Event)",
	"stage/revert.go":           "persisted: reverted Event.Data (revertedPayload)",
	"agent/budget.go":           "wire only: provider request bodies",
	"agent/context.go":          "wire only: provider request bodies",
	"agent/session.go":          "persisted: session.ndjson (Record)",
	"vault/attachments.go":      "per-PC: .llmwiki/cache/attachments.json, ignored by lw sync",
}

// renderMarshalSites counts json/yaml Marshal, MarshalIndent and NewEncoder
// calls per source file, found with go/parser rather than grep so comments
// and strings cannot match.
func renderMarshalSites(t *testing.T) string {
	t.Helper()
	fset := token.NewFileSet()
	var lines []string
	for _, pkgDir := range marshalSitePackages {
		for _, name := range goSourceFiles(t, pkgDir) {
			f, err := parser.ParseFile(fset, filepath.Join(pkgDir, name), nil, 0)
			if err != nil {
				t.Fatalf("parse %s/%s: %v", pkgDir, name, err)
			}
			counts := map[string]int{}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || (pkg.Name != "json" && pkg.Name != "yaml") {
					return true
				}
				switch sel.Sel.Name {
				case "Marshal", "MarshalIndent", "NewEncoder":
					counts[pkg.Name+"."+sel.Sel.Name]++
				}
				return true
			})
			if len(counts) == 0 {
				continue
			}
			var calls []string
			for c, n := range counts {
				calls = append(calls, fmt.Sprintf("%s x%d", c, n))
			}
			sort.Strings(calls)
			label := strings.TrimPrefix(filepath.ToSlash(filepath.Join(pkgDir, name)), "../")
			line := label + ": " + strings.Join(calls, ", ")
			if note := marshalSiteNotes[label]; note != "" {
				line += " — " + note
			}
			lines = append(lines, line)
		}
	}
	sort.Strings(lines)
	var buf bytes.Buffer
	for _, l := range lines {
		buf.WriteString(l + "\n")
	}
	return buf.String()
}
