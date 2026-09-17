package vault

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync/atomic"
)

// Vault is a loaded wiki: its schema, every parsed page under wiki/, every
// parsed raw source under raw/, and the wikilink graph built over the pages.
//
// The content lives in one immutable snapshot behind an atomic pointer
// (008 A-802): Open and Reload build a complete new snapshot and swap it in
// with a single store, so a reader running concurrently with a Reload sees
// either the whole old state or the whole new one, never a half-written
// structure, and a snapshot is never mutated after it is stored. The
// *Schema, *Page, *RawSource and *Graph values a snapshot holds are
// therefore immutable too: everything handed out from a Vault stays valid
// for exactly as long as the caller keeps it, whatever Reload does.
type Vault struct {
	root string // original argument to Open; "" for an FS-backed vault
	fsys fs.FS
	snap atomic.Pointer[snapshot]
}

// ParseError is one file under wiki/ or raw/ that Open could not parse.
// Open collects these instead of failing; lint's fm-required check reports
// them.
type ParseError struct {
	Path string // vault-relative, slash-separated
	Err  error
}

// Error implements the error interface.
func (e ParseError) Error() string {
	return fmt.Sprintf("vault: parse %s: %v", e.Path, e.Err)
}

// Unwrap returns e.Err, so errors.Is/errors.As see through a ParseError to
// the underlying parse failure.
func (e ParseError) Unwrap() error {
	return e.Err
}

var (
	// ErrNotFound is returned when a requested vault-relative path does not
	// exist.
	ErrNotFound = errors.New("vault: not found")
	// ErrOutsideVault is returned when a path is absolute, contains "..",
	// or otherwise resolves outside the vault root.
	ErrOutsideVault = errors.New("vault: path escapes vault root")
)

// Open loads the vault rooted at root — the directory containing
// SCHEMA.md — reading SCHEMA.md and every *.md under wiki/ and raw/.
//
// Contract (backbone §2.8): a page or raw source that fails to parse is not
// fatal — it is collected in ParseErrors() instead. SCHEMA.md itself must
// parse; a vault with no taxonomy cannot validate anything (backbone §2.6).
//
// Open is a thin wrapper over OpenFS(os.DirFS(root)) that additionally
// records root so Root() can return it. os.DirFS is a live view of the
// directory, not a snapshot, so a later Reload sees anything written to
// root in the meantime.
func Open(root string) (*Vault, error) {
	v, err := OpenFS(os.DirFS(root))
	if err != nil {
		return nil, err
	}
	v.root = root
	return v, nil
}

// OpenFS loads the vault stored in fsys — reading SCHEMA.md and every *.md
// under wiki/ and raw/ — the same as Open, but over any fs.FS rather than
// only the local disk.
//
// Contract (backbone §2.8, MASTER §9 D-AD): this is what lets
// stage.Engine.Append materialize a projected in-memory vault — pages and
// raw sources overridden by an op's post-image content — and lint it
// without writing anything to disk (backbone §5.4). Root() returns "" for
// a vault opened this way, since there is no directory string to report.
func OpenFS(fsys fs.FS) (*Vault, error) {
	v := &Vault{fsys: fsys}
	if err := v.Reload(); err != nil {
		return nil, err
	}
	return v, nil
}

// Root returns the vault's root directory, exactly as passed to Open, or ""
// for a vault loaded with OpenFS.
func (v *Vault) Root() string {
	return v.root
}

// Read returns the raw bytes at the given vault-relative path.
//
// Contract (backbone §2.8): rejects any path that is absolute, contains
// "..", or resolves outside root, returning ErrOutsideVault.
func (v *Vault) Read(path string) ([]byte, error) {
	p, err := v.resolvePath(path)
	if err != nil {
		return nil, err
	}
	b, err := fs.ReadFile(v.fsys, p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("vault: read %s: %w", path, ErrNotFound)
		}
		return nil, fmt.Errorf("vault: read %s: %w", path, err)
	}
	return b, nil
}

// Exists reports whether the given vault-relative path exists. A path that
// would escape the vault root reports false rather than erroring.
func (v *Vault) Exists(path string) bool {
	p, err := v.resolvePath(path)
	if err != nil {
		return false
	}
	_, err = fs.Stat(v.fsys, p)
	return err == nil
}

// Reload re-reads SCHEMA.md and every *.md under wiki/ and raw/ from v's
// underlying fs.FS, replacing the vault's in-memory state. For a
// disk-backed vault (opened via Open), that FS is a live view of the
// directory, not a snapshot, so Reload sees anything written there since
// the last load.
//
// The new state is built completely — pages, raw sources, parse errors and
// the graph over the NEW pages — before a single store swaps it in, so a
// reader concurrent with Reload always observes one whole state (008
// A-802). A Reload that fails leaves the previous snapshot standing, and
// slices or pages taken before it stay exactly as they were.
func (v *Vault) Reload() error {
	schemaBytes, err := fs.ReadFile(v.fsys, "SCHEMA.md")
	if err != nil {
		return fmt.Errorf("vault: read SCHEMA.md: %w", err)
	}
	schema, err := ParseSchema(schemaBytes)
	if err != nil {
		return fmt.Errorf("vault: parse SCHEMA.md: %w", err)
	}

	pages, pageErrs, err := loadPages(v.fsys)
	if err != nil {
		return err
	}
	rawSources, rawErrs, err := loadRawSources(v.fsys)
	if err != nil {
		return err
	}

	parseErrors := append(pageErrs, rawErrs...)
	sort.Slice(parseErrors, func(i, j int) bool { return parseErrors[i].Path < parseErrors[j].Path })

	s := &snapshot{
		schema:      schema,
		pages:       pages,
		rawSources:  rawSources,
		parseErrors: parseErrors,
	}
	s.pagesSorted = sortedValues(pages)
	s.rawSorted = sortedValues(rawSources)
	// The graph resolves against the new pages only — this snapshot's own —
	// so it can never mix the old and new states of one reload.
	s.graph = s.buildGraph()
	v.snap.Store(s)
	return nil
}

// loadPages walks fsys's wiki/ for *.md files and parses each as a Page,
// collecting failures as ParseErrors rather than aborting.
func loadPages(fsys fs.FS) (map[string]*Page, []ParseError, error) {
	rels, err := walkMarkdown(fsys, "wiki")
	if err != nil {
		return nil, nil, err
	}

	pages := make(map[string]*Page, len(rels))
	var errs []ParseError
	for _, rel := range rels {
		b, err := fs.ReadFile(fsys, rel)
		if err != nil {
			return nil, nil, fmt.Errorf("vault: read %s: %w", rel, err)
		}
		p, err := ParsePage(rel, b)
		if err != nil {
			errs = append(errs, ParseError{Path: rel, Err: err})
			continue
		}
		pages[rel] = p
	}
	return pages, errs, nil
}

// loadRawSources walks fsys's raw/ for *.md files and parses each as a
// RawSource, collecting failures as ParseErrors rather than aborting.
func loadRawSources(fsys fs.FS) (map[string]*RawSource, []ParseError, error) {
	rels, err := walkMarkdown(fsys, "raw")
	if err != nil {
		return nil, nil, err
	}

	sources := make(map[string]*RawSource, len(rels))
	var errs []ParseError
	for _, rel := range rels {
		b, err := fs.ReadFile(fsys, rel)
		if err != nil {
			return nil, nil, fmt.Errorf("vault: read %s: %w", rel, err)
		}
		r, err := ParseRawSource(rel, b)
		if err != nil {
			errs = append(errs, ParseError{Path: rel, Err: err})
			continue
		}
		sources[rel] = r
	}
	return sources, errs, nil
}

// walkMarkdown returns the vault-relative, slash-separated paths of every
// *.md file under fsys's subdir, sorted. A missing subdir yields no paths
// and no error — a fresh vault may not have any raw sources yet.
func walkMarkdown(fsys fs.FS, subdir string) ([]string, error) {
	info, err := fs.Stat(fsys, subdir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("vault: stat %s: %w", subdir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("vault: %s is not a directory", subdir)
	}

	var rels []string
	err = fs.WalkDir(fsys, subdir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rels = append(rels, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("vault: walk %s: %w", subdir, err)
	}
	sort.Strings(rels)
	return rels, nil
}

// resolvePath validates p per Read's contract and returns the fs.FS-relative
// path to use against v.fsys.
//
// Contract (backbone §2.8, MASTER §9 D-AP): the rejection conditions are
// exactly the three frozen ones — empty, absolute, or containing a ".."
// segment — each carrying ErrOutsideVault. There is no fourth. A path that is
// merely non-canonical without escaping ("./x", "a/./b", "a//b", "a/") is
// NORMALIZED with path.Clean, not refused: that is what the pre-fs.FS
// implementation did via filepath.Join, and §2.8 promises no other reason to
// reject. Strictness about canonical spelling belongs in §5.5 ValidateOp,
// which governs the paths an agent proposes; the vault's own read path stays
// permissive, exactly as it was before OpenFS.
//
// Cleaning after the ".." check (never before) keeps "a/../../b" rejected on
// the literal segment rather than resolved into something that merely looks
// in-bounds. path.Clean of a non-empty, non-absolute, ".."-free path always
// satisfies fs.ValidPath, so v.fsys never sees a malformed name; the final
// check is defence in depth and is expected to be unreachable.
func (v *Vault) resolvePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("vault: empty path: %w", ErrOutsideVault)
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("vault: path %q is absolute: %w", p, ErrOutsideVault)
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", fmt.Errorf("vault: path %q contains \"..\": %w", p, ErrOutsideVault)
		}
	}
	clean := path.Clean(p)
	if !fs.ValidPath(clean) {
		return "", fmt.Errorf("vault: path %q is not a valid vault path: %w", p, ErrOutsideVault)
	}
	return clean, nil
}

// sortedKeys returns m's keys in ascending order, for deterministic
// iteration over a path-keyed map.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedValues returns m's values ordered by their keys' ascending order —
// the order Pages() and RawSources() hand out.
func sortedValues[V any](m map[string]V) []V {
	out := make([]V, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, m[k])
	}
	return out
}
