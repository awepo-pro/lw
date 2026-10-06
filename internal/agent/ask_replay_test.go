package agent

// ask_replay_test.go is 054 S1-d: 039 review finding 3. query_history_test.go
// pins the query verb over a session holding staged history; the TUI's ask
// verb is the other half of ask mode, and the only one that may be offered
// web lookup, so its tool set differs. A resumed session replays an earlier
// curator turn's stage_* pair (046) while this turn is offered no stage tool
// that matches it — the chat must stay valid regardless. Permanent regression
// test (D-10C).

import (
	"strings"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

// TestAskTurnReplaysStagePairsWithoutStageTools seeds a session with an
// earlier filing turn's stage.create_page pair, then sends an ask-verb turn
// through Send, over a registry without web lookup and over one with it. The
// request that goes out must replay the pair as an assistant tool_calls plus a
// tool message sharing its id (046's validity), and must advertise no tool the
// pair could be replayed against: no stage_* at all without web, and — since
// an ask turn with web may stage web-ingest sources — still no
// stage_create_page with it.
func TestAskTurnReplaysStagePairsWithoutStageTools(t *testing.T) {
	seed := func(ts time.Time) []Record {
		return []Record{
			{TS: ts, Role: "user", Content: "file that answer as a page"},
			{TS: ts.Add(1 * time.Second), Role: "tool", Tool: "stage.create_page", Staged: true,
				Args:   `{"path":"wiki/concepts/answer.md","title":"Answer","type":"concept","body":"# Answer\n\nA filed answer."}`,
				Result: "proposed op1 (create_page)"},
			{TS: ts.Add(2 * time.Second), Role: "assistant", Content: "Filed."},
		}
	}

	for _, tc := range []struct {
		name string
		web  bool
		want []string
	}{
		{"no_web", false, wantQueryTools},
		{"web", true, wantAskWebTools},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModeFixture(t, tc.web, oneRound())
			for _, r := range seed(time.Date(2026, 10, 6, 1, 0, 0, 0, time.UTC)) {
				if err := m.store.Append(m.csID, r); err != nil {
					t.Fatalf("seed session: %v", err)
				}
			}

			if _, err := m.send(t, trace.VerbAsk); err != nil {
				t.Fatalf("Send: %v", err)
			}
			reqs := m.fake.Requests()
			if len(reqs) != 1 {
				t.Fatalf("Stream called %d times, want 1", len(reqs))
			}
			req := reqs[0]

			// 046's validity: the whole chat, then the pair by hand — a test
			// that found no replayed pair would pass vacuously.
			if err := validChat(req.Messages); err != nil {
				t.Fatalf("an ask turn over a session with staged history is not a valid chat: %v", err)
			}
			announced := map[string]string{} // replayed tool_calls id -> wire name
			answered := map[string]bool{}    // tool messages' tool_call_id
			for _, msg := range req.Messages {
				for _, c := range msg.ToolCalls {
					if strings.HasPrefix(c.ID, "hist_") {
						announced[c.ID] = c.Function.Name
					}
				}
				if msg.Role == "tool" {
					answered[msg.ToolCallID] = true
				}
			}
			if len(announced) != 1 {
				t.Fatalf("history replayed %v, want exactly the one stage_create_page pair", announced)
			}
			for id, name := range announced {
				if name != "stage_create_page" {
					t.Errorf("replayed pair %s is %q, want stage_create_page", id, name)
				}
				if !answered[id] {
					t.Errorf("replayed tool_calls id %s has no matching tool message", id)
				}
			}

			// The tools advertised are the ask set for this registry, in wire
			// spelling and order.
			var got []string
			for _, d := range req.Tools {
				got = append(got, d.Name)
				if d.Name == "stage_create_page" {
					t.Errorf("an ask turn advertises stage_create_page, the tool its history replays")
				}
				if !tc.web && strings.HasPrefix(d.Name, "stage_") {
					t.Errorf("an ask turn without web lookup advertises %s", d.Name)
				}
			}
			var want []string
			for _, c := range tc.want {
				want = append(want, tools.WireName(c))
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("an ask turn's tools (web %v):\n got  %v\n want %v", tc.web, got, want)
			}
		})
	}
}
