package score

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

// rawGetCall is the part of a raw.get call's arguments that decides which
// chunk of which source it read. Decoding is as lenient as the tool's own
// (decodeArgs): unknown fields are ignored, and arguments that do not parse
// are a call that read nothing.
type rawGetCall struct {
	Source string `json:"source"`
	Chunk  int    `json:"chunk"`
}

// ChunkCoverage reports how much of one raw source the model read: read is
// the number of distinct chunks of source that raw.get calls asked for across
// turns, total how many chunks raw.get slices body into
// (tools.RawChunkCount — the same count, not a copy of it). A source the
// answer depends on but the model only skimmed is the commonest silent
// failure, and this is what makes it a number.
//
// The calls are read from the responses' tool_calls, which carry the model's
// own arguments — the tool events carry none — and under the WIRE name the
// provider echoed (raw_get), where the tool events carry the canonical one
// (raw.get); both map through tools.CanonicalName. Every tool call in a
// response was dispatched (the loop only records a call after running it, and
// never retries a round that dispatched one), so no attempt needs filtering.
// A missing or 0 chunk is chunk 1, as in raw.get; a chunk outside 1..total
// read nothing (raw.get refused it) and does not count. (037 T2.)
func ChunkCoverage(turns []*trace.Turn, source, body string) (read, total int) {
	total = tools.RawChunkCount(body)
	want := strings.TrimSpace(source)
	seen := map[int]bool{}
	for _, t := range turns {
		if t == nil {
			continue
		}
		for _, a := range t.Attempts {
			if a.Response == nil {
				continue
			}
			for _, tc := range a.Response.ToolCalls {
				if tools.CanonicalName(tc.Name) != "raw.get" {
					continue
				}
				var args rawGetCall
				if json.Unmarshal([]byte(tc.Arguments), &args) != nil {
					continue
				}
				if strings.TrimSpace(args.Source) != want {
					continue
				}
				chunk := args.Chunk
				if chunk == 0 {
					chunk = 1
				}
				if chunk >= 1 && chunk <= total {
					seen[chunk] = true
				}
			}
		}
	}
	return len(seen), total
}

// toolErrorRunes is how much of a failing tool's result text ToolErrors
// keeps: enough to read why (the tools' refusals name the fix in their first
// sentence or two), little enough that a report of a hundred runs stays
// readable.
const toolErrorRunes = 200

// ToolError is one tool call that failed: the round it ran in, the tool's
// canonical name, and the start of the error text the model was given back.
type ToolError struct {
	Round int
	Name  string
	Text  string
}

// ToolErrors lists every tool call of turn t that failed, in trace order,
// with the text the model saw. The trace records THAT a tool failed (the tool
// event's is_error) but never what it said — "the result itself never lands
// in the trace" — so the text is recovered from the next round's request
// body: the role:"tool" message whose tool_call_id is the call's id. The
// latest attempt of round+1 is the one read; a retry resends the identical
// messages, and the latest is the one that survived. Text is the first 200
// runes of that content.
//
// Text is "" — not an error — when the text cannot be had because it was
// never sent: the round was the turn's last (no next request), or the next
// request carries no tool message for the call. A request that exists but
// cannot be read or parsed IS an error: the trace is damaged, and an empty
// Text there would pass for a clean one. dir and id name the turn on disk, as
// trace.Body wants them. (037 T2.)
func ToolErrors(dir, id string, t *trace.Turn) ([]ToolError, error) {
	if t == nil {
		return nil, nil
	}
	results := map[int]map[string]string{} // next round -> tool_call_id -> content, read once
	var out []ToolError
	for _, a := range t.Attempts {
		for _, c := range a.Calls {
			if !c.IsError {
				continue
			}
			next := c.Round + 1
			byID, loaded := results[next]
			if !loaded {
				var err error
				if byID, err = toolResults(dir, id, t, next); err != nil {
					return nil, err
				}
				results[next] = byID
			}
			out = append(out, ToolError{
				Round: c.Round,
				Name:  tools.CanonicalName(c.Name),
				Text:  firstRunes(byID[c.ID], toolErrorRunes),
			})
		}
	}
	return out, nil
}

// toolResults reads the tool-result messages out of the latest attempt of
// round: tool_call_id to content. A turn with no request for the round has
// none, which is nil and no error.
func toolResults(dir, id string, t *trace.Turn, round int) (map[string]string, error) {
	attempt := 0
	for _, a := range t.Attempts {
		if a.Round == round {
			attempt = a.Attempt // trace order: the last one listed is the latest
		}
	}
	if attempt == 0 {
		return nil, nil
	}
	b, err := trace.Body(dir, id, round, attempt)
	if err != nil {
		return nil, fmt.Errorf("score: turn %s round %d request: %w", id, round, err)
	}
	var req struct {
		Messages []struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
			Content    string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(b, &req); err != nil {
		return nil, fmt.Errorf("score: turn %s round %d request body: %w", id, round, err)
	}
	byID := map[string]string{}
	for _, m := range req.Messages {
		if m.Role != "tool" || m.ToolCallID == "" {
			continue
		}
		if _, dup := byID[m.ToolCallID]; !dup {
			byID[m.ToolCallID] = m.Content
		}
	}
	return byID, nil
}

// firstRunes returns the first n runes of s, whole when it is shorter —
// runes, not bytes, so a multi-byte character is never cut in half.
func firstRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// Process is what a run cost to produce, summed over its turns: the loop's
// rounds, the tool calls it made and how many failed, the provider's token
// counts, and the wall time. Two runs of the same task are comparable on
// these only through their spread, which is why the harness records them
// beside the accuracy numbers (037 T2).
type Process struct {
	Rounds, MaxRoundsHit, ToolCalls, ToolErrs                int
	InputTokens, CachedTokens, OutputTokens, ReasoningTokens int
	WallMS                                                   int64
	HasUsage                                                 bool
}

// ProcessOf sums the process numbers of turns. Rounds counts distinct rounds
// that got a response, per turn; MaxRoundsHit the turns whose done reason is
// "max_rounds"; ToolCalls and ToolErrs come from the tool events; WallMS is
// the sum of the done events' wall times. Tokens are summed over the LAST
// attempt of each round, and only when that attempt's response carried a
// usage object: a retried round's cut first attempt is the loop's discarded
// work, and counting both would double a round that happened once. HasUsage
// is true when ANY response carried usage — "the provider reports tokens at
// all" — so a run whose every response omitted usage reads as unmeasured
// rather than as zero tokens. A nil turn, and a turn with no done event, add
// nothing for what they lack. (037 T2.)
func ProcessOf(turns []*trace.Turn) Process {
	var p Process
	for _, t := range turns {
		if t == nil {
			continue
		}
		lastOf := map[int]int{} // round -> index of its last attempt
		for i, a := range t.Attempts {
			lastOf[a.Round] = i
		}
		answered := map[int]bool{}
		for i, a := range t.Attempts {
			for _, c := range a.Calls {
				p.ToolCalls++
				if c.IsError {
					p.ToolErrs++
				}
			}
			r := a.Response
			if r == nil {
				continue
			}
			answered[a.Round] = true
			if r.Usage == nil {
				continue
			}
			p.HasUsage = true
			if lastOf[a.Round] == i {
				p.InputTokens += r.Usage.InputTokens
				p.CachedTokens += r.Usage.CachedTokens
				p.OutputTokens += r.Usage.OutputTokens
				p.ReasoningTokens += r.Usage.ReasoningTokens
			}
		}
		p.Rounds += len(answered)
		if t.Done != nil {
			if t.Done.Reason == "max_rounds" {
				p.MaxRoundsHit++
			}
			p.WallMS += t.Done.WallMS
		}
	}
	return p
}
