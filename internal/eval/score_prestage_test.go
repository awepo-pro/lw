package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/eval/score"
)

// TestAskScorerResolvesStagedCites pins 067 D5: an ask result citing
// ^[raw/articles/nmap-rtt-timeout-parameters.md] that exists only in staged/
// resolves as cite valid when the case has prestage. The same answer for a case
// without prestage resolves to "no such source" (today's behavior, unchanged).
func TestAskScorerResolvesStagedCites(t *testing.T) {
	set := newRunSet(t)
	scRewriteCases(t, set, `[[ask]]
id = "with-prestage"
kind = "covered"
q = "is there any RTT information?"
facts = [["timeout"]]
cite_any = ["raw/articles/nmap-rtt-timeout-parameters.md"]
prestage = [
    ["ingest", "--prepare", "--agent", "eval", "inputs/paper.txt"]
]

[[ask]]
id = "without-prestage"
kind = "covered"
q = "is there any RTT information?"
facts = [["timeout"]]
cite_any = ["raw/articles/nmap-rtt-timeout-parameters.md"]
`)
	loadedSet, err := LoadSet(set.Dir)
	if err != nil {
		t.Fatalf("LoadSet: %v", err)
	}

	run := scNewRun(t, set, "r1")

	// Raw source content with valid frontmatter and body
	rawContent := `---
source_url: /home/anton/Downloads/nmap-rtt.md
ingested: 2026-10-19
---

# Nmap RTT timeout parameters

Adaptive timing calculation: Timeout Window = SRTT + 4 * RTTVAR.
`
	answer := "The adaptive timeout calculation is detailed in ^[raw/articles/nmap-rtt-timeout-parameters.md].\n"

	for _, id := range []string{"with-prestage", "without-prestage"} {
		scWriteCase(t, run, scAnsweredMeta(id, "query", "covered", 1), map[string]string{
			"stdout.txt": answer,
		})
		stagedRaw := filepath.Join(run, id, "1", "staged", "raw", "articles", "nmap-rtt-timeout-parameters.md")
		if err := os.MkdirAll(filepath.Dir(stagedRaw), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stagedRaw, []byte(rawContent), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	res, err := ScoreSet(run, loadedSet)
	if err != nil {
		t.Fatalf("ScoreSet: %v", err)
	}

	resWith := scResult(t, res, "with-prestage", 1)
	if cv, ok := resWith.Metrics["cite_valid"]; !ok || cv != 1.0 {
		t.Errorf("with-prestage: cite_valid = %v (want 1.0)", cv)
	}

	resWithout := scResult(t, res, "without-prestage", 1)
	if cv, ok := resWithout.Metrics["cite_valid"]; !ok || cv != 0.0 {
		t.Errorf("without-prestage: cite_valid = %v (want 0.0)", cv)
	}

	// Verify the exact failure reason for the case without prestage
	refs := score.Refs(answer)
	if len(refs) != 1 {
		t.Fatalf("len(refs) = %d, want 1", len(refs))
	}
	s := &scorer{set: loadedSet, snap: t.TempDir(), bodies: map[string]string{}}
	checkedWithout := score.CheckRefs(refs, s.resolver(""))
	if checkedWithout[0].Valid {
		t.Error("without prestage: ref was unexpectedly valid")
	}
	if !strings.Contains(checkedWithout[0].Reason, "no such source") {
		t.Errorf("without prestage: reason = %q, want 'no such source'", checkedWithout[0].Reason)
	}

	// And for the case with prestage:
	stagedDir := filepath.Join(run, "with-prestage", "1", "staged")
	checkedWith := score.CheckRefs(refs, s.resolver(stagedDir))
	if !checkedWith[0].Valid {
		t.Errorf("with prestage: ref was invalid: %s", checkedWith[0].Reason)
	}
}
