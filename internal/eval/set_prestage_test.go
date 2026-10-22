package eval

import (
	"reflect"
	"strings"
	"testing"
)

// TestAskCasePrestageParse pins 067 D5: AskCase gains prestage [][]string,
// ordered lw argument lists. Prestage lists parse; an empty inner list, an arg
// list containing --vault, or a prestage on an [[ingest]] case produce a set
// load error naming the case id.
func TestAskCasePrestageParse(t *testing.T) {
	const head = "version = 1\nsnapshot = \"vault-20261005.tar.gz\"\nsnapshot_sha256 = \"{{SHA}}\"\n"
	cases := func(body string) string { return head + body }

	t.Run("prestage lists parse", func(t *testing.T) {
		body := cases(`
[[ask]]
id = "valid-prestage"
kind = "covered"
q = "What is the timeout?"
facts = [["timeout"]]
prestage = [
    ["ingest", "--prepare", "--agent", "eval", "inputs/paper.txt"],
    ["stage", "--from", "inputs/paper.txt"]
]
`)
		dir := setFixture(t, "vault-20261005.tar.gz", body)
		set, err := LoadSet(dir)
		if err != nil {
			t.Fatalf("LoadSet: %v", err)
		}
		if len(set.Ask) != 1 {
			t.Fatalf("len(Ask) = %d, want 1", len(set.Ask))
		}
		c := set.Ask[0]
		want := [][]string{
			{"ingest", "--prepare", "--agent", "eval", "inputs/paper.txt"},
			{"stage", "--from", "inputs/paper.txt"},
		}
		if !reflect.DeepEqual(c.Prestage, want) {
			t.Fatalf("Prestage = %q, want %q", c.Prestage, want)
		}
	})

	t.Run("empty inner list rejected naming case id", func(t *testing.T) {
		body := cases(`
[[ask]]
id = "bad-empty-step"
kind = "covered"
q = "What is the timeout?"
facts = [["timeout"]]
prestage = [[]]
`)
		dir := setFixture(t, "vault-20261005.tar.gz", body)
		_, err := LoadSet(dir)
		if err == nil {
			t.Fatal("LoadSet: want error for empty prestage step, got nil")
		}
		if !strings.Contains(err.Error(), "bad-empty-step") {
			t.Errorf("error %q should name case id %q", err.Error(), "bad-empty-step")
		}
		if !strings.Contains(err.Error(), "empty") {
			t.Errorf("error %q should mention empty step", err.Error())
		}
	})

	t.Run("arg list containing --vault rejected naming case id", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			arg  string
		}{
			{"separate flag", `["ingest", "--vault", "scratch"]`},
			{"equal flag", `["ingest", "--vault=scratch"]`},
			{"single dash flag", `["ingest", "-vault", "scratch"]`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				body := cases(`
[[ask]]
id = "bad-vault-step"
kind = "covered"
q = "What is the timeout?"
facts = [["timeout"]]
prestage = [` + tc.arg + `]
`)
				dir := setFixture(t, "vault-20261005.tar.gz", body)
				_, err := LoadSet(dir)
				if err == nil {
					t.Fatalf("LoadSet: want error for %s, got nil", tc.name)
				}
				if !strings.Contains(err.Error(), "bad-vault-step") {
					t.Errorf("error %q should name case id %q", err.Error(), "bad-vault-step")
				}
				if !strings.Contains(err.Error(), "--vault") {
					t.Errorf("error %q should mention --vault", err.Error())
				}
			})
		}
	})

	t.Run("prestage on ingest case rejected naming case id", func(t *testing.T) {
		body := cases(`
[[ingest]]
id = "bad-ingest-prestage"
input = "inputs/paper.txt"
facts = [["speculative"]]
prestage = [["ingest", "--prepare", "inputs/paper.txt"]]
`)
		dir := setFixture(t, "vault-20261005.tar.gz", body)
		_, err := LoadSet(dir)
		if err == nil {
			t.Fatal("LoadSet: want error for prestage on ingest case, got nil")
		}
		if !strings.Contains(err.Error(), "bad-ingest-prestage") {
			t.Errorf("error %q should name case id %q", err.Error(), "bad-ingest-prestage")
		}
		if !strings.Contains(err.Error(), "prestage") {
			t.Errorf("error %q should mention prestage", err.Error())
		}
	})

	t.Run("real eval set loads with staged cases", func(t *testing.T) {
		const realSetDir = "/home/anton/learning/llm-wiki/.dev-notes/projects/llmwiki/eval"
		set, err := LoadSet(realSetDir)
		if err != nil {
			t.Fatalf("LoadSet(%s): %v", realSetDir, err)
		}
		found := map[string]bool{}
		for _, c := range set.Ask {
			if c.ID == "staged-htb" || c.ID == "staged-rtt" {
				found[c.ID] = true
				if len(c.Prestage) != 2 {
					t.Errorf("case %s: len(Prestage) = %d, want 2", c.ID, len(c.Prestage))
				}
			}
		}
		if !found["staged-htb"] || !found["staged-rtt"] {
			t.Errorf("found staged cases: %v, want both staged-htb and staged-rtt", found)
		}
	})
}
