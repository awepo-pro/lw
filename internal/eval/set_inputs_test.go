package eval

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestLoadSetIngestInputs pins 049's multi-file ingest case: `inputs` is a
// list of files read by ONE `lw ingest`, in the order written, and a case sets
// exactly one of `input` and `inputs`. Every element is checked as `input`
// always was, and every refusal names the case (049).
func TestLoadSetIngestInputs(t *testing.T) {
	const head = "version = 1\nsnapshot = \"vault-20261005.tar.gz\"\nsnapshot_sha256 = \"{{SHA}}\"\n"
	ingest := func(body string) string { return head + "\n[[ingest]]\nid = \"i\"\n" + body }

	// fixture builds a set with inputs/paper.txt (from setFixture) plus a
	// second file and a directory, so every row can name real and unreal paths.
	fixture := func(t *testing.T, cases string) string {
		t.Helper()
		dir := setFixture(t, "vault-20261005.tar.gz", cases)
		if err := os.WriteFile(filepath.Join(dir, "inputs", "second.txt"), []byte("Second article.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "inputs", "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	t.Run("two files load in the order written", func(t *testing.T) {
		for _, order := range [][]string{
			{"inputs/paper.txt", "inputs/second.txt"},
			{"inputs/second.txt", "inputs/paper.txt"},
		} {
			dir := fixture(t, ingest(`inputs = ["`+order[0]+`", "`+order[1]+`"]`+"\nfacts = [[\"x\"]]\n"))
			set, err := LoadSet(dir)
			if err != nil {
				t.Fatalf("LoadSet: %v", err)
			}
			c := set.Ingest[0]
			if got := c.Paths(); !reflect.DeepEqual(got, order) {
				t.Errorf("Paths() = %q, want %q", got, order)
			}
			if c.Input != "" || !reflect.DeepEqual(c.Inputs, order) {
				t.Errorf("Input %q, Inputs %q; want the list as written and no single input", c.Input, c.Inputs)
			}
		}
	})

	t.Run("a single input is a one-element path list", func(t *testing.T) {
		dir := fixture(t, ingest("input = \"inputs/paper.txt\"\nfacts = [[\"x\"]]\n"))
		set, err := LoadSet(dir)
		if err != nil {
			t.Fatalf("LoadSet: %v", err)
		}
		if got := set.Ingest[0].Paths(); !reflect.DeepEqual(got, []string{"inputs/paper.txt"}) {
			t.Errorf("Paths() = %q, want [inputs/paper.txt]", got)
		}
		if got := (IngestCase{}).Paths(); len(got) != 0 {
			t.Errorf("Paths() of an empty case = %q, want none", got)
		}
	})

	tests := []struct {
		name  string
		cases string
		want  string
	}{
		{"input and inputs together",
			ingest("input = \"inputs/paper.txt\"\ninputs = [\"inputs/second.txt\"]\n"),
			`cases.toml: ingest "i": set input or inputs, not both`},
		{"an empty inputs list",
			ingest("inputs = []\nfacts = [[\"x\"]]\n"),
			`cases.toml: ingest "i": input is empty`},
		{"neither key",
			ingest("facts = [[\"x\"]]\n"),
			`cases.toml: ingest "i": input is empty`},
		{"a repeated element",
			ingest("inputs = [\"inputs/paper.txt\", \"inputs/second.txt\", \"inputs/paper.txt\"]\n"),
			`cases.toml: ingest "i": input inputs/paper.txt is listed twice`},
		{"a repeated element spelt another way",
			ingest("inputs = [\"inputs/paper.txt\", \"./inputs/paper.txt\"]\n"),
			`cases.toml: ingest "i": input ./inputs/paper.txt is listed twice`},
		{"a missing element",
			ingest("inputs = [\"inputs/paper.txt\", \"inputs/nope.txt\"]\n"),
			`cases.toml: ingest "i": input inputs/nope.txt: no such file`},
		{"an element that escapes the set",
			ingest("inputs = [\"inputs/paper.txt\", \"../paper.txt\"]\n"),
			`cases.toml: ingest "i": input ../paper.txt must be a path inside the set`},
		{"an element that is a directory",
			ingest("inputs = [\"inputs/sub\"]\n"),
			`cases.toml: ingest "i": input inputs/sub is not a regular file`},
		{"an empty element",
			ingest("inputs = [\"inputs/paper.txt\", \"\"]\n"),
			`cases.toml: ingest "i": input is empty`},
		{"a bad fact in a multi-file case",
			ingest("inputs = [\"inputs/paper.txt\", \"inputs/second.txt\"]\nfacts = [[\"ok\"], [\"re:[\"]]\n"),
			`cases.toml: ingest "i": fact 2: bad regexp`},
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

// TestLoadSetRecompileCase pins 055 D6's set half: an ingest case with
// `recompile = true` names VAULT paths — raw/….md files the snapshot already
// holds — not files in the set directory, so it loads without any such file
// existing beside cases.toml; a path that is not a clean raw/….md vault path
// is refused in the frozen words, naming the case and the path. Everything
// else a case is checked for (one of input/inputs, no repeats, facts) still
// applies, and a case without the flag is checked as it always was.
func TestLoadSetRecompileCase(t *testing.T) {
	const head = "version = 1\nsnapshot = \"vault-20261005.tar.gz\"\nsnapshot_sha256 = \"{{SHA}}\"\n"
	ingest := func(body string) string { return head + "\n[[ingest]]\nid = \"i\"\n" + body }

	t.Run("a recompile case loads", func(t *testing.T) {
		dir := setFixture(t, "vault-20261005.tar.gz", ingest("recompile = true\ninput = \"raw/articles/llm-wiki.md\"\nfacts = [[\"memex\"]]\n"))
		set, err := LoadSet(dir)
		if err != nil {
			t.Fatalf("LoadSet: %v", err)
		}
		c := set.Ingest[0]
		if !c.Recompile || !reflect.DeepEqual(c.Paths(), []string{"raw/articles/llm-wiki.md"}) {
			t.Errorf("case = %+v, want recompile on with Paths() [raw/articles/llm-wiki.md]", c)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "raw")); statErr == nil {
			t.Error("the test set holds a raw/ directory; the case must load without one")
		}
	})

	t.Run("several raws load in order", func(t *testing.T) {
		dir := setFixture(t, "vault-20261005.tar.gz", ingest("recompile = true\ninputs = [\"raw/papers/b.md\", \"raw/articles/a.md\"]\n"))
		set, err := LoadSet(dir)
		if err != nil {
			t.Fatalf("LoadSet: %v", err)
		}
		if got := set.Ingest[0].Paths(); !reflect.DeepEqual(got, []string{"raw/papers/b.md", "raw/articles/a.md"}) {
			t.Errorf("Paths() = %q, want the list as written", got)
		}
	})

	t.Run("a case without the flag is not a recompile case", func(t *testing.T) {
		dir := setFixture(t, "vault-20261005.tar.gz", ingest("input = \"inputs/paper.txt\"\n"))
		set, err := LoadSet(dir)
		if err != nil {
			t.Fatalf("LoadSet: %v", err)
		}
		if set.Ingest[0].Recompile {
			t.Error("Recompile is on for a case that never set it")
		}
	})

	tests := []struct {
		name  string
		cases string
		want  string
	}{
		{"no raw/ prefix",
			ingest("recompile = true\ninput = \"articles/x.md\"\n"),
			`cases.toml: ingest "i": recompile input articles/x.md must be a vault path raw/….md`},
		{"not .md",
			ingest("recompile = true\ninput = \"raw/articles/x.txt\"\n"),
			`cases.toml: ingest "i": recompile input raw/articles/x.txt must be a vault path raw/….md`},
		{"escapes with ../",
			ingest("recompile = true\ninput = \"../raw/x.md\"\n"),
			`cases.toml: ingest "i": recompile input ../raw/x.md must be a vault path raw/….md`},
		{"escapes after raw/",
			ingest("recompile = true\ninput = \"raw/../../x.md\"\n"),
			`cases.toml: ingest "i": recompile input raw/../../x.md must be a vault path raw/….md`},
		{"leaves raw/ and lands elsewhere",
			ingest("recompile = true\ninput = \"raw/../wiki/x.md\"\n"),
			`cases.toml: ingest "i": recompile input raw/../wiki/x.md must be a vault path raw/….md`},
		{"absolute",
			ingest("recompile = true\ninput = \"/raw/x.md\"\n"),
			`cases.toml: ingest "i": recompile input /raw/x.md must be a vault path raw/….md`},
		{"a set-directory file is not a vault path",
			ingest("recompile = true\ninput = \"inputs/paper.txt\"\n"),
			`cases.toml: ingest "i": recompile input inputs/paper.txt must be a vault path raw/….md`},
		{"one bad element among good ones",
			ingest("recompile = true\ninputs = [\"raw/articles/a.md\", \"raw/articles/b.html\"]\n"),
			`cases.toml: ingest "i": recompile input raw/articles/b.html must be a vault path raw/….md`},
		{"a repeated raw",
			ingest("recompile = true\ninputs = [\"raw/articles/a.md\", \"raw/articles/a.md\"]\n"),
			`cases.toml: ingest "i": input raw/articles/a.md is listed twice`},
		{"no input at all",
			ingest("recompile = true\nfacts = [[\"x\"]]\n"),
			`cases.toml: ingest "i": input is empty`},
		{"a bad fact still counts",
			ingest("recompile = true\ninput = \"raw/articles/a.md\"\nfacts = [[\"re:[\"]]\n"),
			`cases.toml: ingest "i": fact 1: bad regexp`},
		{"the flag off keeps the set-directory check",
			ingest("input = \"raw/articles/a.md\"\n"),
			`cases.toml: ingest "i": input raw/articles/a.md: no such file`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LoadSet(setFixture(t, "vault-20261005.tar.gz", tc.cases))
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
