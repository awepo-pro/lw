package eval

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Set is a loaded eval set: the cases plus where they live. Dir is absolute
// so a Runner started from any working directory resolves the snapshot and
// the ingest inputs the same way.
type Set struct {
	Dir            string
	Version        int
	Snapshot       string // file name inside Dir
	SnapshotSHA256 string
	Ask            []AskCase
	Ingest         []IngestCase
}

// AskCase is one question for `lw query`. Kind says what the vault should
// hold for it (covered, raw-only, absent, multi-hop) — scoring reads it, and
// an absent case, which has no right answer to match, carries no facts.
//
// Facts is a list of facts, each a list of alternatives: a fact hits when
// any one alternative matches the answer (matching is the scorer's, 037 T2;
// the loader only proves every alternative is well-formed).
type AskCase struct {
	ID      string     `toml:"id"`
	Kind    string     `toml:"kind"`
	Q       string     `toml:"q"`
	Facts   [][]string `toml:"facts"`
	CiteAny []string   `toml:"cite_any"`
	Holdout bool       `toml:"holdout"`
}

// IngestCase is one `lw ingest` call. A case names its input as Input (one
// file) or Inputs (several files handed to the SAME call, 049: the failure
// the user hit was three articles in one `lw ingest`, which a single path
// cannot express) — exactly one of the two. Paths are relative to the set's
// directory; Facts are matched against the staged wiki pages.
type IngestCase struct {
	ID      string     `toml:"id"`
	Input   string     `toml:"input"`
	Inputs  []string   `toml:"inputs"`
	Facts   [][]string `toml:"facts"`
	Holdout bool       `toml:"holdout"`
}

// Paths returns the files the case ingests, as written and in order: Inputs
// when it is set, else Input alone. It is nil for a case that names none,
// which LoadSet never lets through. The slice is the case's own — read it,
// do not append to it.
func (c IngestCase) Paths() []string {
	if len(c.Inputs) > 0 {
		return c.Inputs
	}
	if c.Input != "" {
		return []string{c.Input}
	}
	return nil
}

// setFormatVersion is the cases.toml version this loader reads.
const setFormatVersion = 1

// askKinds is the closed set of ask kinds, in the order the refusal names
// them.
var askKinds = []string{"covered", "raw-only", "absent", "multi-hop"}

// caseIDRE is the shape of a case id: it names a directory under a run, a
// scratch vault and a command-line selector, so it is lowercase, short and
// free of anything a shell or a filesystem would treat specially.
var caseIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

// reservedIDs are the words Runner.Only reads as a verb group; a case with
// one of them as its id could never be selected alone.
var reservedIDs = map[string]bool{"ask": true, "ingest": true}

// regexpPrefix marks a fact alternative that is a Go regexp, not a
// substring.
const regexpPrefix = "re:"

// setFile is cases.toml as decoded: the Set's own fields, minus Dir, which
// the file cannot know.
type setFile struct {
	Version        int          `toml:"version"`
	Snapshot       string       `toml:"snapshot"`
	SnapshotSHA256 string       `toml:"snapshot_sha256"`
	Ask            []AskCase    `toml:"ask"`
	Ingest         []IngestCase `toml:"ingest"`
}

// LoadSet reads and validates dir/cases.toml (037 T1, C1).
//
// Everything a later run would trip over is refused here, once, in plain
// words: an eval costs live provider calls, and a typo found at case 17 is
// money spent. That includes checking the snapshot tarball against its
// declared sha256 — a set whose frozen vault has drifted measures a
// different vault than the one the cases were written against. Every error
// starts "cases.toml:" and names the case it is about.
func LoadSet(dir string) (*Set, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("cases.toml: %w", err)
	}
	b, err := os.ReadFile(filepath.Join(abs, "cases.toml"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("cases.toml: not found in %s", abs)
		}
		return nil, fmt.Errorf("cases.toml: %w", err)
	}
	var f setFile
	md, err := toml.Decode(string(b), &f)
	if err != nil {
		return nil, fmt.Errorf("cases.toml: %w", err)
	}
	// A misspelt key would otherwise load as a silent zero value — a "facts"
	// typed "fact" is a case that scores nothing and says nothing.
	if un := md.Undecoded(); len(un) > 0 {
		k := un[0]
		msg := "cases.toml: unknown key " + k[len(k)-1]
		if len(k) > 1 {
			msg += " (in " + strings.Join(k[:len(k)-1], ".") + ")"
		}
		return nil, errors.New(msg)
	}

	if f.Version != setFormatVersion {
		return nil, fmt.Errorf("cases.toml: version %d unsupported (want %d)", f.Version, setFormatVersion)
	}
	if err := checkSnapshotName(f.Snapshot); err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	checkID := func(id string) error {
		if !caseIDRE.MatchString(id) {
			return fmt.Errorf("cases.toml: case id %q must match %s", id, caseIDRE)
		}
		if reservedIDs[id] {
			return fmt.Errorf("cases.toml: case id %q is reserved", id)
		}
		if seen[id] {
			return fmt.Errorf("cases.toml: duplicate case id %q", id)
		}
		seen[id] = true
		return nil
	}
	for _, c := range f.Ask {
		if err := checkID(c.ID); err != nil {
			return nil, err
		}
		if err := checkAsk(c); err != nil {
			return nil, err
		}
	}
	for _, c := range f.Ingest {
		if err := checkID(c.ID); err != nil {
			return nil, err
		}
		if err := checkIngest(abs, c); err != nil {
			return nil, err
		}
	}
	if len(f.Ask)+len(f.Ingest) == 0 {
		return nil, errors.New("cases.toml: no cases")
	}

	// Last: it reads the whole tarball, and a cheap refusal should not wait
	// behind a hash of a vault that may be hundreds of megabytes.
	got, err := fileSHA256(filepath.Join(abs, f.Snapshot))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("cases.toml: snapshot %s: no such file", f.Snapshot)
		}
		return nil, fmt.Errorf("cases.toml: snapshot %s: %w", f.Snapshot, err)
	}
	// A declared hash that is missing, upper-case or not a hash at all can
	// never equal the lowercase hex digest, so it lands here too — and the
	// message prints the file's real hash, which is what a new set's author
	// needs to paste into cases.toml.
	if got != f.SnapshotSHA256 {
		return nil, fmt.Errorf("cases.toml: snapshot %s sha256 mismatch (file %s, cases.toml says %q)", f.Snapshot, got, f.SnapshotSHA256)
	}

	return &Set{
		Dir:            abs,
		Version:        f.Version,
		Snapshot:       f.Snapshot,
		SnapshotSHA256: f.SnapshotSHA256,
		Ask:            f.Ask,
		Ingest:         f.Ingest,
	}, nil
}

// checkSnapshotName validates the snapshot key's shape; whether the file
// exists and matches its hash is checked last, once the cheap checks pass.
func checkSnapshotName(name string) error {
	if name == "" {
		return errors.New("cases.toml: snapshot is empty")
	}
	if name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsRune(name, '\\') {
		return fmt.Errorf("cases.toml: snapshot %s must be a file name inside the set", name)
	}
	return nil
}

// checkAsk validates one ask case. An absent case is the vault's "I have
// nothing on that" probe, so there is nothing to find in its answer; every
// other kind needs at least one fact, or the case could never fail.
func checkAsk(c AskCase) error {
	who := fmt.Sprintf("ask %q", c.ID)
	if !containsString(askKinds, c.Kind) {
		return fmt.Errorf("cases.toml: %s: kind %q (want covered, raw-only, absent or multi-hop)", who, c.Kind)
	}
	if strings.TrimSpace(c.Q) == "" {
		return fmt.Errorf("cases.toml: %s: q is empty", who)
	}
	for i, p := range c.CiteAny {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("cases.toml: %s: cite_any entry %d is empty", who, i+1)
		}
	}
	if c.Kind == "absent" {
		if len(c.Facts) > 0 || len(c.CiteAny) > 0 {
			return fmt.Errorf("cases.toml: %s: an absent case takes no facts or cite_any", who)
		}
		return nil
	}
	if len(c.Facts) == 0 {
		return fmt.Errorf("cases.toml: %s: needs at least one fact", who)
	}
	return checkFacts(who, c.Facts)
}

// checkIngest validates one ingest case. Its facts are optional: the case
// still yields the staged pages and their lint report, which is a result
// even when nobody wrote a fact to look for. Every file it names, whether as
// input or as an element of inputs, gets the same checks, and the same
// words, as a lone input always did; a file listed twice is refused because
// the run would hand lw the same source twice and score a different case
// than the one written.
func checkIngest(setDir string, c IngestCase) error {
	who := fmt.Sprintf("ingest %q", c.ID)
	switch {
	case c.Input != "" && len(c.Inputs) > 0:
		return fmt.Errorf("cases.toml: %s: set input or inputs, not both", who)
	case c.Input == "" && len(c.Inputs) == 0:
		return fmt.Errorf("cases.toml: %s: input is empty", who)
	}
	seen := map[string]bool{}
	for _, p := range c.Paths() {
		if err := checkInput(setDir, who, p); err != nil {
			return err
		}
		key := filepath.Clean(filepath.FromSlash(p))
		if seen[key] {
			return fmt.Errorf("cases.toml: %s: input %s is listed twice", who, p)
		}
		seen[key] = true
	}
	return checkFacts(who, c.Facts)
}

// checkInput validates one input path of an ingest case.
func checkInput(setDir, who, p string) error {
	if p == "" {
		return fmt.Errorf("cases.toml: %s: input is empty", who)
	}
	// The input is read by the real lw, with the set's directory as the only
	// thing the case is allowed to name; a path out of it would make the set
	// depend on files nobody froze.
	if !filepath.IsLocal(filepath.FromSlash(p)) {
		return fmt.Errorf("cases.toml: %s: input %s must be a path inside the set", who, p)
	}
	info, err := os.Stat(filepath.Join(setDir, filepath.FromSlash(p)))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("cases.toml: %s: input %s: no such file", who, p)
	case err != nil:
		return fmt.Errorf("cases.toml: %s: input %s: %w", who, p, err)
	case !info.Mode().IsRegular():
		return fmt.Errorf("cases.toml: %s: input %s is not a regular file", who, p)
	}
	return nil
}

// checkFacts validates a case's facts: no empty fact (a fact with no
// alternative can never hit, one with a blank alternative always would), and
// every "re:" alternative a compilable Go regexp under the same (?i) prefix
// the scorer compiles it with.
func checkFacts(who string, facts [][]string) error {
	for i, fact := range facts {
		if len(fact) == 0 {
			return fmt.Errorf("cases.toml: %s: fact %d is empty", who, i+1)
		}
		for _, alt := range fact {
			if strings.TrimSpace(alt) == "" {
				return fmt.Errorf("cases.toml: %s: fact %d is empty", who, i+1)
			}
			if pat, ok := strings.CutPrefix(alt, regexpPrefix); ok {
				if strings.TrimSpace(pat) == "" {
					return fmt.Errorf("cases.toml: %s: fact %d: bad regexp %q: empty pattern", who, i+1, alt)
				}
				if _, err := regexp.Compile("(?i)" + pat); err != nil {
					return fmt.Errorf("cases.toml: %s: fact %d: bad regexp %q: %v", who, i+1, alt, err)
				}
			}
		}
	}
	return nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
