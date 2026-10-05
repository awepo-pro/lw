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
