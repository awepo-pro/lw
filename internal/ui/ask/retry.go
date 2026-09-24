// retry.go is the ask pane's half of 035's stream-integrity handling: the
// one fold a RetryEv applies to the scrollback. The loop side (sibling T2)
// emits RetryEv when a round's stream ended early with no tool call
// dispatched and the identical request is being sent again; the text the
// cut round already streamed into the transcript must not survive it, or
// the retried round's answer would be appended to a half-sentence the
// provider abandoned.
package ask

// retryStatusLine is the kindStatus line a RetryEv leaves behind (035):
// U+00B7, matching the pane's other mid-turn notices (`· sending…`,
// `· thinking…`). It names the cut without blaming the curator — the
// provider ended the stream before [DONE] or a finish_reason, and the
// round is going out again unchanged.
const retryStatusLine = "provider stream cut · retrying the round"

// retryRound folds one agent.RetryEv into the scrollback (035): the
// assistant entries the cut round streamed since the turn's last
// ToolCallEv/ToolResEv — or since the turn's start, when it had none —
// are removed, the round's reasoning flags reset, and the retry status
// line appended. The scan runs backwards from the tail and stops at the
// first user, tool or error entry, which is exactly the round boundary: a
// tool call's entry sits between this round's text and every earlier
// round's, and the echoed user entry sits at the turn's start. Tool
// entries are never removed, so toolIndex's entry indexes stay valid; and
// runs inside applyEvent's mutateEntries, so the scroll accounting sees
// the removal and the appended status line as one mutation.
//
// A kindStatus entry inside the span is kept, not treated as a boundary:
// pane-local notices (a submit refused mid-turn, a failed Close) land at
// the round's tail while it streams, so the round's partial text can sit
// on BOTH sides of one — stopping there would strand the text below the
// notice above the retry line. The drop itself only runs while the turn
// is active: beginTurn set turnActive before the loop could emit anything,
// and a RetryEv outside a turn — a protocol-violating fake, never the
// real Loop, whose cut rounds only ever surface mid-turn — must not be
// able to reach back into a finished turn's answer.
func (m *Model) retryRound() {
	if m.turnActive {
		start := len(m.entries)
		for start > 0 && m.entries[start-1].kind != kindUser &&
			m.entries[start-1].kind != kindTool && m.entries[start-1].kind != kindError {
			start--
		}
		kept := m.entries[start:]
		n := 0
		for _, e := range kept {
			if e.kind != kindAssistant {
				kept[n] = e
				n++
			}
		}
		m.entries = append(m.entries[:start], kept[:n]...)
	}
	m.resetReasoningRound()
	m.entries = append(m.entries, entry{kind: kindStatus, text: retryStatusLine})
}
