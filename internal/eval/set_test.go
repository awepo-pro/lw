package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// validSet is the checked-in synthetic set: a tiny vault tarball, one input
// file and a cases.toml whose snapshot_sha256 is the tarball's real hash.
const validSet = "testdata/valid"

// validSnapshotSHA is what testdata/valid/cases.toml declares for its
// tarball — repeated here so TestLoadSetValid compares against the file's
// text, not against a value LoadSet computed.
const validSnapshotSHA = "b3f304ee142d3bcaccbcc0f61203d038778104d281156a1acc7f7ce93127e3ba"

// setFixture builds a set directory under t.TempDir() from the checked-in
// pieces: the tarball (copied as snapshotName; "" copies none), the one
// input file, and cases.toml with every {{SHA}} replaced by the tarball's
// real sha256 — so a refusal test states only the defect it is about.
func setFixture(t *testing.T, snapshotName, cases string) string {
	t.Helper()
	dir := t.TempDir()
	tarBytes, err := os.ReadFile(filepath.Join(validSet, "vault-20261005.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if snapshotName != "" {
		if err := os.WriteFile(filepath.Join(dir, snapshotName), tarBytes, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	paper, err := os.ReadFile(filepath.Join(validSet, "inputs", "paper.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "inputs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inputs", "paper.txt"), paper, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(tarBytes)
	cases = strings.ReplaceAll(cases, "{{SHA}}", hex.EncodeToString(sum[:]))
	if err := os.WriteFile(filepath.Join(dir, "cases.toml"), []byte(cases), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestLoadSetValid pins 037 T1 C1/C2: a set with two ask cases (one absent
// with no facts) and one ingest case loads with every field equal to the
// file, and Dir is the absolute directory.
func TestLoadSetValid(t *testing.T) {
	got, err := LoadSet(validSet)
	if err != nil {
		t.Fatalf("LoadSet: %v", err)
	}
	abs, err := filepath.Abs(validSet)
	if err != nil {
		t.Fatal(err)
	}
	want := &Set{
		Dir:            abs,
		Version:        1,
		Snapshot:       "vault-20261005.tar.gz",
		SnapshotSHA256: validSnapshotSHA,
		Ask: []AskCase{
			{
				ID:      "kv-cache-basics",
				Kind:    "covered",
				Q:       "What does the KV cache store during decoding?",
				Facts:   [][]string{{"keys"}, {"values", "re:value(s)?\\b"}},
				CiteAny: []string{"wiki/concepts/kv-cache.md"},
				Holdout: false,
			},
			{
				ID:      "no-such-topic",
				Kind:    "absent",
				Q:       "What is the capital of Atlantis?",
				Holdout: true,
			},
		},
		Ingest: []IngestCase{
			{
				ID:      "paper-text",
				Input:   "inputs/paper.txt",
				Facts:   [][]string{{"speculative"}},
				Holdout: false,
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadSet mismatch\n got %#v\nwant %#v", got, want)
	}
	if !filepath.IsAbs(got.Dir) {
		t.Fatalf("Dir %q is not absolute", got.Dir)
	}
}

// TestLoadSetRefusals pins the C1 refusals: each defect is named by the
// frozen text, and a set with a defect never loads.
func TestLoadSetRefusals(t *testing.T) {
	const head = "version = 1\nsnapshot = \"vault-20261005.tar.gz\"\nsnapshot_sha256 = \"{{SHA}}\"\n"
	const okAsk = "\n[[ask]]\nid = \"a\"\nkind = \"covered\"\nq = \"What?\"\nfacts = [[\"x\"]]\n"
	ask := func(body string) string { return head + "\n[[ask]]\nid = \"a\"\n" + body }
	ingest := func(body string) string {
		return head + "\n[[ingest]]\nid = \"i\"\n" + body
	}
	zeros := strings.Repeat("0", 64)

	tests := []struct {
		name     string
		snapshot string // file name copied into the set; "" copies none
		cases    string
		want     string
	}{
		{"version 2", "vault-20261005.tar.gz",
			"version = 2\nsnapshot = \"vault-20261005.tar.gz\"\nsnapshot_sha256 = \"{{SHA}}\"\n" + okAsk,
			`cases.toml: version 2 unsupported (want 1)`},
		{"duplicate id across ask and ingest", "vault-20261005.tar.gz",
			head + "\n[[ask]]\nid = \"x\"\nkind = \"covered\"\nq = \"What?\"\nfacts = [[\"x\"]]\n" +
				"\n[[ingest]]\nid = \"x\"\ninput = \"inputs/paper.txt\"\nfacts = [[\"y\"]]\n",
			`cases.toml: duplicate case id "x"`},
		{"id with a capital and an underscore", "vault-20261005.tar.gz",
			head + "\n[[ask]]\nid = \"X_1\"\nkind = \"covered\"\nq = \"What?\"\nfacts = [[\"x\"]]\n",
			`cases.toml: case id "X_1" must match ^[a-z0-9][a-z0-9-]{0,47}$`},
		{"id longer than 48", "vault-20261005.tar.gz",
			head + "\n[[ask]]\nid = \"" + strings.Repeat("a", 49) + "\"\nkind = \"covered\"\nq = \"What?\"\nfacts = [[\"x\"]]\n",
			`must match ^[a-z0-9][a-z0-9-]{0,47}$`},
		{"id that is a run-selector word", "vault-20261005.tar.gz",
			head + "\n[[ask]]\nid = \"ask\"\nkind = \"covered\"\nq = \"What?\"\nfacts = [[\"x\"]]\n",
			`cases.toml: case id "ask" is reserved`},
		{"unknown kind", "vault-20261005.tar.gz",
			ask("kind = \"easy\"\nq = \"What?\"\nfacts = [[\"x\"]]\n"),
			`cases.toml: ask "a": kind "easy" (want covered, raw-only, absent or multi-hop)`},
		{"empty question", "vault-20261005.tar.gz",
			ask("kind = \"covered\"\nq = \"\"\nfacts = [[\"x\"]]\n"),
			`cases.toml: ask "a": q is empty`},
		{"covered with no facts", "vault-20261005.tar.gz",
			ask("kind = \"covered\"\nq = \"What?\"\n"),
			`cases.toml: ask "a": needs at least one fact`},
		{"multi-hop with no facts", "vault-20261005.tar.gz",
			ask("kind = \"multi-hop\"\nq = \"What?\"\nfacts = []\n"),
			`cases.toml: ask "a": needs at least one fact`},
		{"a fact that is an empty list", "vault-20261005.tar.gz",
			ask("kind = \"covered\"\nq = \"What?\"\nfacts = [[]]\n"),
			`cases.toml: ask "a": fact 1 is empty`},
		{"a fact holding an empty alternative", "vault-20261005.tar.gz",
			ask("kind = \"covered\"\nq = \"What?\"\nfacts = [[\"\"]]\n"),
			`cases.toml: ask "a": fact 1 is empty`},
		{"the second fact is the empty one", "vault-20261005.tar.gz",
			ask("kind = \"covered\"\nq = \"What?\"\nfacts = [[\"x\"], [\"y\", \"\"]]\n"),
			`cases.toml: ask "a": fact 2 is empty`},
		{"a whitespace-only alternative", "vault-20261005.tar.gz",
			ask("kind = \"covered\"\nq = \"What?\"\nfacts = [[\"  \"]]\n"),
			`cases.toml: ask "a": fact 1 is empty`},
		{"absent with facts", "vault-20261005.tar.gz",
			ask("kind = \"absent\"\nq = \"What?\"\nfacts = [[\"x\"]]\n"),
			`cases.toml: ask "a": an absent case takes no facts or cite_any`},
		{"absent with cite_any", "vault-20261005.tar.gz",
			ask("kind = \"absent\"\nq = \"What?\"\ncite_any = [\"wiki/x.md\"]\n"),
			`cases.toml: ask "a": an absent case takes no facts or cite_any`},
		{"bad regexp in an ask", "vault-20261005.tar.gz",
			ask("kind = \"covered\"\nq = \"What?\"\nfacts = [[\"re:(\"]]\n"),
			`cases.toml: ask "a": fact 1: bad regexp`},
		{"bad regexp in an ingest", "vault-20261005.tar.gz",
			ingest("input = \"inputs/paper.txt\"\nfacts = [[\"ok\"], [\"re:[\"]]\n"),
			`cases.toml: ingest "i": fact 2: bad regexp`},
		{"ingest input missing", "vault-20261005.tar.gz",
			ingest("input = \"inputs/nope.pdf\"\nfacts = [[\"x\"]]\n"),
			`cases.toml: ingest "i": input inputs/nope.pdf: no such file`},
		{"ingest input escapes the set", "vault-20261005.tar.gz",
			ingest("input = \"../paper.txt\"\nfacts = [[\"x\"]]\n"),
			`cases.toml: ingest "i": input ../paper.txt must be a path inside the set`},
		{"ingest input is a directory", "vault-20261005.tar.gz",
			ingest("input = \"inputs\"\nfacts = [[\"x\"]]\n"),
			`cases.toml: ingest "i": input inputs is not a regular file`},
		{"snapshot sha mismatch", "vault-x.tar.gz",
			"version = 1\nsnapshot = \"vault-x.tar.gz\"\nsnapshot_sha256 = \"" + zeros + "\"\n" + okAsk,
			`cases.toml: snapshot vault-x.tar.gz sha256 mismatch`},
		{"snapshot sha is not a lowercase hex digest", "vault-20261005.tar.gz",
			"version = 1\nsnapshot = \"vault-20261005.tar.gz\"\nsnapshot_sha256 = \"ABC\"\n" + okAsk,
			`cases.toml: snapshot vault-20261005.tar.gz sha256 mismatch`},
		{"snapshot sha omitted", "vault-20261005.tar.gz",
			"version = 1\nsnapshot = \"vault-20261005.tar.gz\"\n" + okAsk,
			`cases.toml: snapshot vault-20261005.tar.gz sha256 mismatch`},
		{"snapshot file missing", "",
			head + okAsk,
			`cases.toml: snapshot vault-20261005.tar.gz: no such file`},
		{"snapshot named outside the set", "vault-20261005.tar.gz",
			"version = 1\nsnapshot = \"../vault.tar.gz\"\nsnapshot_sha256 = \"" + zeros + "\"\n" + okAsk,
			`cases.toml: snapshot ../vault.tar.gz must be a file name inside the set`},
		{"no cases at all", "vault-20261005.tar.gz",
			head,
			`cases.toml: no cases`},
		{"unknown top-level key", "vault-20261005.tar.gz",
			head + "qq = \"stray\"\n" + okAsk,
			`cases.toml: unknown key qq`},
		{"unknown key inside an ask", "vault-20261005.tar.gz",
			ask("kind = \"covered\"\nq = \"What?\"\nfacts = [[\"x\"]]\nqq = \"stray\"\n"),
			`cases.toml: unknown key qq`},
		{"malformed toml", "vault-20261005.tar.gz",
			head + "[[ask\n",
			`cases.toml:`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := setFixture(t, tc.snapshot, tc.cases)
			got, err := LoadSet(dir)
			if err == nil {
				t.Fatalf("LoadSet succeeded (%#v); want an error containing %q", got, tc.want)
			}
			if got != nil {
				t.Errorf("LoadSet returned a Set alongside its error: %#v", got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// TestLoadSetMissingCases pins that a directory with no cases.toml is a
// plain error, not a panic or an empty Set.
func TestLoadSetMissingCases(t *testing.T) {
	_, err := LoadSet(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "cases.toml") {
		t.Fatalf("LoadSet(empty dir) = %v; want an error naming cases.toml", err)
	}
}

// TestValidSetSnapshotExtracts pins that the checked-in synthetic snapshot
// is a tarball Extract accepts and that it holds a vault: later stages build
// their fixtures on it, and a snapshot Extract refuses would only be found
// there.
func TestValidSetSnapshotExtracts(t *testing.T) {
	set, err := LoadSet(validSet)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "scratch")
	if err := Extract(filepath.Join(set.Dir, set.Snapshot), dst); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, p := range []string{"SCHEMA.md", "index.md", "wiki/concepts/kv-cache.md"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(p))); err != nil {
			t.Errorf("extracted snapshot lacks %s: %v", p, err)
		}
	}
}
