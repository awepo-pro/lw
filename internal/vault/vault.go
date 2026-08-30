package vault

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Vault is a loaded wiki: its schema, every parsed page under wiki/, every
// parsed raw source under raw/, and the wikilink graph built over the pages.
type Vault struct {
	root        string
	schema      *Schema
	pages       map[string]*Page
	rawSources  map[string]*RawSource
	parseErrors []ParseError
	graph       *Graph
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
func Open(root string) (*Vault, error) {
	v := &Vault{root: root}
	if err := v.Reload(); err != nil {
		return nil, err
	}
	return v, nil
}

// Root returns the vault's root directory, exactly as passed to Open.
func (v *Vault) Root() string {
	return v.root
}

// Schema returns the vault's parsed SCHEMA.md.
func (v *Vault) Schema() *Schema {
	return v.schema
}

// Pages returns every successfully parsed wiki page, sorted by Path.
func (v *Vault) Pages() []*Page {
	paths := sortedKeys(v.pages)
	out := make([]*Page, 0, len(paths))
	for _, p := range paths {
		out = append(out, v.pages[p])
	}
	return out
}

// Page returns the page at the given vault-relative path, and whether it
// was found.
func (v *Vault) Page(path string) (*Page, bool) {
	p, ok := v.pages[path]
	return p, ok
}

// RawSources returns every successfully parsed raw source, sorted by Path.
func (v *Vault) RawSources() []*RawSource {
	paths := sortedKeys(v.rawSources)
	out := make([]*RawSource, 0, len(paths))
	for _, p := range paths {
		out = append(out, v.rawSources[p])
	}
	return out
}

// RawSource returns the raw source at the given vault-relative path, and
// whether it was found.
func (v *Vault) RawSource(path string) (*RawSource, bool) {
	r, ok := v.rawSources[path]
	return r, ok
}

// ParseErrors returns one entry per file under wiki/ or raw/ that Open
// could not parse, sorted by Path. A file in ParseErrors is in neither
// Pages() nor RawSources() and contributes no graph edges (backbone §2.8,
// MASTER §9 D-W).
func (v *Vault) ParseErrors() []ParseError {
	return append([]ParseError(nil), v.parseErrors...)
}

// Graph returns the vault's wikilink graph, built from Pages() only.
func (v *Vault) Graph() *Graph {
	return v.graph
}

// Read returns the raw bytes at the given vault-relative path.
//
// Contract (backbone §2.8): rejects any path that is absolute, contains
// "..", or resolves outside root, returning ErrOutsideVault.
func (v *Vault) Read(path string) ([]byte, error) {
	abs, err := v.resolvePath(path)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("vault: read %s: %w", path, ErrNotFound)
		}
		return nil, fmt.Errorf("vault: read %s: %w", path, err)
	}
	return b, nil
}

// Exists reports whether the given vault-relative path exists on disk. A
// path that would escape the vault root reports false rather than erroring.
func (v *Vault) Exists(path string) bool {
	abs, err := v.resolvePath(path)
	if err != nil {
		return false
	}
	_, err = os.Stat(abs)
	return err == nil
}

// Reload re-reads SCHEMA.md and every *.md under wiki/ and raw/ from disk,
// replacing the vault's in-memory state.
func (v *Vault) Reload() error {
	schemaBytes, err := os.ReadFile(filepath.Join(v.root, "SCHEMA.md"))
	if err != nil {
		return fmt.Errorf("vault: read SCHEMA.md: %w", err)
	}
	schema, err := ParseSchema(schemaBytes)
	if err != nil {
		return fmt.Errorf("vault: parse SCHEMA.md: %w", err)
	}

	pages, pageErrs, err := loadPages(v.root)
	if err != nil {
		return err
	}
	rawSources, rawErrs, err := loadRawSources(v.root)
	if err != nil {
		return err
	}

	parseErrors := append(pageErrs, rawErrs...)
	sort.Slice(parseErrors, func(i, j int) bool { return parseErrors[i].Path < parseErrors[j].Path })

	v.schema = schema
	v.pages = pages
	v.rawSources = rawSources
	v.parseErrors = parseErrors
	v.graph = BuildGraph(v)
	return nil
}

// loadPages walks root/wiki for *.md files and parses each as a Page,
// collecting failures as ParseErrors rather than aborting.
func loadPages(root string) (map[string]*Page, []ParseError, error) {
	rels, err := walkMarkdown(root, "wiki")
	if err != nil {
		return nil, nil, err
	}

	pages := make(map[string]*Page, len(rels))
	var errs []ParseError
	for _, rel := range rels {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
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

// loadRawSources walks root/raw for *.md files and parses each as a
// RawSource, collecting failures as ParseErrors rather than aborting.
func loadRawSources(root string) (map[string]*RawSource, []ParseError, error) {
	rels, err := walkMarkdown(root, "raw")
	if err != nil {
		return nil, nil, err
	}

	sources := make(map[string]*RawSource, len(rels))
	var errs []ParseError
	for _, rel := range rels {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
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
// *.md file under root/subdir, sorted. A missing subdir yields no paths and
// no error — a fresh vault may not have any raw sources yet.
func walkMarkdown(root, subdir string) ([]string, error) {
	dir := filepath.Join(root, subdir)
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("vault: stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("vault: %s is not a directory", dir)
	}

	var rels []string
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("vault: rel %s: %w", path, err)
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("vault: walk %s: %w", dir, err)
	}
	sort.Strings(rels)
	return rels, nil
}

// resolvePath validates path per Read's contract and returns its absolute
// filesystem location.
func (v *Vault) resolvePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("vault: empty path: %w", ErrOutsideVault)
	}
	if filepath.IsAbs(path) || strings.HasPrefix(filepath.ToSlash(path), "/") {
		return "", fmt.Errorf("vault: path %q is absolute: %w", path, ErrOutsideVault)
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return "", fmt.Errorf("vault: path %q contains \"..\": %w", path, ErrOutsideVault)
		}
	}

	root, err := filepath.Abs(v.root)
	if err != nil {
		return "", fmt.Errorf("vault: resolve root: %w", err)
	}
	abs := filepath.Join(root, filepath.FromSlash(path))

	// filepath.Join already cleans ".." segments; this is defense in depth
	// in case a future path form slips past the split-based check above.
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", fmt.Errorf("vault: path %q escapes vault root: %w", path, ErrOutsideVault)
	}
	return abs, nil
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
