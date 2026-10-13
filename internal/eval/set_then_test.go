package eval

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestLoadSetThen pins 060's set half: an ingest case may name `then`, the
// inputs of a SECOND `lw ingest` run in the same scratch vault right after the
// first, so it joins the open changeset (019). The eval never ran a joined second
// turn, so the 10-08 failure — a joined recompile turn at max_rounds after six
// not-found wiki.get on its own staged pages — was invisible to it.
//
// `then` paths resolve exactly like `inputs`: set-directory files, or vault
// paths for a recompile case. Every element is checked as an input always was,
// every refusal names the case and the path, and Paths() — the first call —
// does not move.
func TestLoadSetThen(t *testing.T) {
	const head = "version = 1\nsnapshot = \"vault-20261005.tar.gz\"\nsnapshot_sha256 = \"{{SHA}}\"\n"
	ingest := func(body string) string { return head + "\n[[ingest]]\nid = \"i\"\n" + body }

	// fixture builds a set with inputs/paper.txt (from setFixture) plus two
	// more files and a directory, so every row can name real and unreal paths.
	fixture := func(t *testing.T, cases string) string {
		t.Helper()
		dir := setFixture(t, "vault-20261005.tar.gz", cases)
		for _, name := range []string{"second.txt", "third.txt"} {
			if err := os.WriteFile(filepath.Join(dir, "inputs", name), []byte("Article "+name+".\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(filepath.Join(dir, "inputs", "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	t.Run("then loads in the order written and Paths is still the first call", func(t *testing.T) {
		for _, order := range [][]string{
			{"inputs/second.txt", "inputs/third.txt"},
			{"inputs/third.txt", "inputs/second.txt"},
			{"inputs/second.txt"},
		} {
			quoted := make([]string, len(order))
			for i, p := range order {
				quoted[i] = `"` + p + `"`
			}
			dir := fixture(t, ingest("inputs = [\"inputs/paper.txt\"]\nthen = ["+strings.Join(quoted, ", ")+"]\nfacts = [[\"x\"]]\n"))
			set, err := LoadSet(dir)
			if err != nil {
				t.Fatalf("LoadSet: %v", err)
			}
			c := set.Ingest[0]
			if !reflect.DeepEqual(c.Then, order) {
				t.Errorf("Then = %q, want %q", c.Then, order)
			}
			if got := c.Paths(); !reflect.DeepEqual(got, []string{"inputs/paper.txt"}) {
				t.Errorf("Paths() = %q, want just the first call's [inputs/paper.txt]", got)
			}
		}
	})

	t.Run("a single input can have a then", func(t *testing.T) {
		dir := fixture(t, ingest("input = \"inputs/paper.txt\"\nthen = [\"inputs/second.txt\"]\n"))
		set, err := LoadSet(dir)
		if err != nil {
			t.Fatalf("LoadSet: %v", err)
		}
		c := set.Ingest[0]
		if c.Input != "inputs/paper.txt" || !reflect.DeepEqual(c.Then, []string{"inputs/second.txt"}) {
			t.Errorf("case = %+v, want input inputs/paper.txt and then [inputs/second.txt]", c)
		}
	})

	t.Run("a case without then has none", func(t *testing.T) {
		dir := fixture(t, ingest("input = \"inputs/paper.txt\"\nfacts = [[\"x\"]]\n"))
		set, err := LoadSet(dir)
		if err != nil {
			t.Fatalf("LoadSet: %v", err)
		}
		if got := set.Ingest[0].Then; len(got) != 0 {
			t.Errorf("Then = %q, want none for a case that names none", got)
		}
	})

	t.Run("an empty then is no second call", func(t *testing.T) {
		dir := fixture(t, ingest("input = \"inputs/paper.txt\"\nthen = []\n"))
		set, err := LoadSet(dir)
		if err != nil {
			t.Fatalf("LoadSet: %v", err)
		}
		if got := set.Ingest[0].Then; len(got) != 0 {
			t.Errorf("Then = %q, want none for then = []", got)
		}
	})

	t.Run("a recompile case takes vault paths in then", func(t *testing.T) {
		dir := fixture(t, ingest("recompile = true\ninputs = [\"raw/papers/a.md\", \"raw/articles/b.md\"]\nthen = [\"raw/articles/c.md\"]\nfacts = [[\"x\"]]\n"))
		set, err := LoadSet(dir)
		if err != nil {
			t.Fatalf("LoadSet: %v", err)
		}
		c := set.Ingest[0]
		if !c.Recompile || !reflect.DeepEqual(c.Then, []string{"raw/articles/c.md"}) {
			t.Errorf("case = %+v, want recompile on with Then [raw/articles/c.md]", c)
		}
		if got := c.Paths(); !reflect.DeepEqual(got, []string{"raw/papers/a.md", "raw/articles/b.md"}) {
			t.Errorf("Paths() = %q, want the first call's raws only", got)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "raw")); statErr == nil {
			t.Error("the test set holds a raw/ directory; the case must load without one")
		}
	})

	tests := []struct {
		name  string
		cases string
		want  string
	}{
		{"a missing then file",
			ingest("input = \"inputs/paper.txt\"\nthen = [\"inputs/nope.txt\"]\n"),
			`cases.toml: ingest "i" then: input inputs/nope.txt: no such file`},
		{"a missing file after a good one",
			ingest("input = \"inputs/paper.txt\"\nthen = [\"inputs/second.txt\", \"inputs/nope.txt\"]\n"),
			`cases.toml: ingest "i" then: input inputs/nope.txt: no such file`},
		{"a then file that escapes the set",
			ingest("input = \"inputs/paper.txt\"\nthen = [\"../paper.txt\"]\n"),
			`cases.toml: ingest "i" then: input ../paper.txt must be a path inside the set`},
		{"a then file that is a directory",
			ingest("input = \"inputs/paper.txt\"\nthen = [\"inputs/sub\"]\n"),
			`cases.toml: ingest "i" then: input inputs/sub is not a regular file`},
		{"an empty then element",
			ingest("input = \"inputs/paper.txt\"\nthen = [\"inputs/second.txt\", \"\"]\n"),
			`cases.toml: ingest "i" then: input is empty`},
		{"a repeated then element",
			ingest("input = \"inputs/paper.txt\"\nthen = [\"inputs/second.txt\", \"./inputs/second.txt\"]\n"),
			`cases.toml: ingest "i" then: input ./inputs/second.txt is listed twice`},
		{"a then file that is also a first-call input",
			ingest("inputs = [\"inputs/paper.txt\", \"inputs/second.txt\"]\nthen = [\"inputs/second.txt\"]\n"),
			`cases.toml: ingest "i" then: input inputs/second.txt is listed twice`},
		{"a then file that is the single input",
			ingest("input = \"inputs/paper.txt\"\nthen = [\"inputs/paper.txt\"]\n"),
			`cases.toml: ingest "i" then: input inputs/paper.txt is listed twice`},
		{"recompile: not a vault path",
			ingest("recompile = true\ninput = \"raw/articles/a.md\"\nthen = [\"articles/x.md\"]\n"),
			`cases.toml: ingest "i" then: recompile input articles/x.md must be a vault path raw/….md`},
		{"recompile: not .md",
			ingest("recompile = true\ninput = \"raw/articles/a.md\"\nthen = [\"raw/articles/x.txt\"]\n"),
			`cases.toml: ingest "i" then: recompile input raw/articles/x.txt must be a vault path raw/….md`},
		{"recompile: escapes raw/",
			ingest("recompile = true\ninput = \"raw/articles/a.md\"\nthen = [\"raw/../wiki/x.md\"]\n"),
			`cases.toml: ingest "i" then: recompile input raw/../wiki/x.md must be a vault path raw/….md`},
		{"recompile: a set-directory file is not a vault path",
			ingest("recompile = true\ninput = \"raw/articles/a.md\"\nthen = [\"inputs/second.txt\"]\n"),
			`cases.toml: ingest "i" then: recompile input inputs/second.txt must be a vault path raw/….md`},
		{"recompile: the same raw in both calls",
			ingest("recompile = true\ninput = \"raw/articles/a.md\"\nthen = [\"raw/articles/a.md\"]\n"),
			`cases.toml: ingest "i" then: input raw/articles/a.md is listed twice`},
		{"without the flag a raw/ path is looked up in the set",
			ingest("input = \"inputs/paper.txt\"\nthen = [\"raw/articles/a.md\"]\n"),
			`cases.toml: ingest "i" then: input raw/articles/a.md: no such file`},
		{"then with no first call",
			ingest("then = [\"inputs/second.txt\"]\n"),
			`cases.toml: ingest "i": input is empty`},
		{"a bad fact still counts",
			ingest("input = \"inputs/paper.txt\"\nthen = [\"inputs/second.txt\"]\nfacts = [[\"re:[\"]]\n"),
			`cases.toml: ingest "i": fact 1: bad regexp`},
		{"then is not a key of an ask case",
			head + "\n[[ask]]\nid = \"q\"\nkind = \"covered\"\nq = \"?\"\nfacts = [[\"x\"]]\nthen = [\"inputs/second.txt\"]\n",
			`cases.toml: unknown key then (in ask)`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LoadSet(fixture(t, tc.cases))
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
