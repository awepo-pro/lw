package main

import (
	"os"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
)

// carriedRec builds a user/assistant record the Ask pane copied forward
// from the previous conversation of the same pane (009 contract §1):
// srec plus Carried.
func carriedRec(ts, role, content string) agent.Record {
	r := srec(ts, role, content)
	r.Carried = true
	return r
}

// sessionRuleTitles collects the rule line titles of a rendered session,
// in order — the text between the rule's dash prefix and its dash run.
func sessionRuleTitles(t *testing.T, out string) []string {
	t.Helper()
	var titles []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "── ") {
			continue
		}
		body := strings.TrimRight(strings.TrimPrefix(line, "── "), "─")
		titles = append(titles, strings.TrimRight(body, " "))
	}
	return titles
}

func TestSessionShowCarried(t *testing.T) {
	t.Run("carried_rules_named", func(t *testing.T) {
		root := newSessionVault(t)
		writeSession(t, root, "open", "cs-cccc333333333333",
			changesetJSON("cs-cccc333333333333", "2026-09-16T13:00:00Z"),
			carriedRec("2026-09-16T12:00:01Z", "user", "earlier question"),
			carriedRec("2026-09-16T12:00:05Z", "assistant", "earlier answer"),
			srec("2026-09-16T13:00:01Z", "user", "follow-up question"),
			srec("2026-09-16T13:00:05Z", "assistant", "follow-up answer"),
		)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root, "--plain"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}

		want := []string{"you · carried", "assistant · carried", "you", "assistant"}
		got := sessionRuleTitles(t, stdout)
		if len(got) != len(want) {
			t.Fatalf("rule titles = %q, want %q", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("rule title %d = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("uncarried_session_unchanged", func(t *testing.T) {
		root := writeGoldenFixtureVault(t)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "show", "--vault", root, goldenSessionID, "--plain"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		golden, err := os.ReadFile("testdata/session/show-plain.golden")
		if err != nil {
			t.Fatalf("read golden: %v", err)
		}
		// testutil normalizes a golden to exactly one trailing newline;
		// the compared side is normalized the same way (as golden_plain
		// does), so any added or altered byte anywhere fails here.
		want := strings.TrimRight(string(golden), "\n") + "\n"
		if got := strings.TrimRight(stdout, "\n") + "\n"; got != want {
			t.Errorf("uncarried session changed:\ngot  %q\nwant %q", got, want)
		}
	})
}
