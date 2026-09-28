package trace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/llm"
)

// TestListLoadRoundTrip scripts one full turn — a cut stream retried once,
// two tools (one failing), an elision, and a clean finish — and pins what
// List and Load report for it: token sums across responses, tool errors,
// attempts in event order with the tools on the latest attempt of their
// round, and the request bodies retrievable again through Body.
func TestListLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	id := testID(1)
	_, rec := Start(context.Background(), dir, id, Meta{
		Verb: "ingest", Session: "cs-9", Version: "v0.12.0", Model: "glm-5.3-flash",
	}, 0)
	b1 := []byte(`{"round":1,"attempt":1}`)
	b2 := []byte(`{"round":1,"attempt":2}`)
	b3 := []byte(`{"round":2,"attempt":1}`)

	rec.BeginRequest(1, 1, 5, 18)
	rec.Request(b1)
	rec.Response(Response{Round: 1, Attempt: 1, Finish: "", FirstByteMS: 10, FirstDeltaMS: 20, StreamMS: 30, Text: "partial", Cut: true})
	rec.Retry(1, 2, "llm stream truncated")
	rec.BeginRequest(1, 2, 5, 18)
	rec.Request(b2)
	rec.Tool(Tool{Round: 1, ID: "call-1", Name: "wiki.search", MS: 12, ResultBytes: 340})
	rec.Tool(Tool{Round: 1, ID: "call-2", Name: "stage.propose", IsError: true, MS: 3, ResultBytes: 88})
	rec.Response(Response{
		Round: 1, Attempt: 2, Finish: "tool_calls", FirstByteMS: 11, FirstDeltaMS: 22, StreamMS: 33,
		ToolCalls: []ToolCall{
			{ID: "call-1", Name: "wiki.search", Arguments: "{}"},
			{ID: "call-2", Name: "stage.propose", Arguments: `{"page":"x"}`},
		},
		Usage: &llm.Usage{InputTokens: 100, OutputTokens: 10, CachedTokens: 80, ReasoningTokens: 5},
	})
	rec.Elide(2, 3, 900)
	rec.BeginRequest(2, 1, 5, 18)
	rec.Request(b3)
	rec.Response(Response{Round: 2, Attempt: 1, Finish: "stop", FirstByteMS: 1, FirstDeltaMS: 2, StreamMS: 3, Text: "final", Usage: &llm.Usage{InputTokens: 200, OutputTokens: 20, CachedTokens: 150}})
	rec.Done(Done{Reason: "max_rounds", Rounds: 2, WallMS: 4321})

	sums, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 1 {
		t.Fatalf("List = %d summaries, want 1", len(sums))
	}
	s := sums[0]
	if s.ID != id {
		t.Errorf("Summary.ID = %q, want %q", s.ID, id)
	}
	if s.Verb != "ingest" {
		t.Errorf("Summary.Verb = %q, want ingest", s.Verb)
	}
	if s.Rounds != 2 {
		t.Errorf("Summary.Rounds = %d, want 2", s.Rounds)
	}
	if s.Reason != "max_rounds" {
		t.Errorf("Summary.Reason = %q, want max_rounds", s.Reason)
	}
	if s.InputTokens != 300 || s.CachedTokens != 230 || s.OutputTokens != 30 || s.ReasoningTokens != 5 {
		t.Errorf("Summary tokens = in %d cached %d out %d reasoning %d, want 300/230/30/5",
			s.InputTokens, s.CachedTokens, s.OutputTokens, s.ReasoningTokens)
	}
	if s.ToolErrors != 1 {
		t.Errorf("Summary.ToolErrors = %d, want 1", s.ToolErrors)
	}
	if !s.HasUsage {
		t.Error("Summary.HasUsage = false, want true")
	}
	if s.WallMS != 4321 {
		t.Errorf("Summary.WallMS = %d, want 4321", s.WallMS)
	}
	if s.Bytes <= 0 {
		t.Errorf("Summary.Bytes = %d, want the turn dir's size on disk", s.Bytes)
	}

	tu, err := Load(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if tu.ID != id || tu.Meta.Verb != "ingest" || tu.Meta.Session != "cs-9" || tu.Meta.Version != "v0.12.0" || tu.Meta.Model != "glm-5.3-flash" {
		t.Errorf("Load meta = %+v, want the turn event's meta", tu.Meta)
	}
	if !tu.Started.Equal(s.Started) {
		t.Errorf("Load.Started %v != Summary.Started %v", tu.Started, s.Started)
	}
	if len(tu.Attempts) != 3 {
		t.Fatalf("Load has %d attempts, want 3", len(tu.Attempts))
	}
	for i, want := range [][2]int{{1, 1}, {1, 2}, {2, 1}} {
		if tu.Attempts[i].Round != want[0] || tu.Attempts[i].Attempt != want[1] {
			t.Errorf("attempt %d = (%d,%d), want (%d,%d)", i, tu.Attempts[i].Round, tu.Attempts[i].Attempt, want[0], want[1])
		}
	}
	a1, a2, a3 := tu.Attempts[0], tu.Attempts[1], tu.Attempts[2]
	if a1.Response == nil || !a1.Response.Cut || a1.Response.Text != "partial" || a1.Response.Usage != nil {
		t.Errorf("attempt (1,1) response = %+v, want cut, partial text, no usage", a1.Response)
	}
	if a1.Messages != 5 || a1.ToolDefs != 18 || a1.File != "req-01.json.gz" {
		t.Errorf("attempt (1,1) = %+v", a1)
	}
	if len(a2.Calls) != 2 {
		t.Fatalf("attempt (1,2) carries %d calls, want 2", len(a2.Calls))
	}
	if a2.Calls[0].Name != "wiki.search" || a2.Calls[0].IsError {
		t.Errorf("call 0 = %+v, want wiki.search, not an error", a2.Calls[0])
	}
	if a2.Calls[1].Name != "stage.propose" || !a2.Calls[1].IsError || a2.Calls[1].ResultBytes != 88 {
		t.Errorf("call 1 = %+v, want the failing stage.propose", a2.Calls[1])
	}
	if a2.Response == nil || a2.Response.Usage == nil || *a2.Response.Usage != (llm.Usage{InputTokens: 100, OutputTokens: 10, CachedTokens: 80, ReasoningTokens: 5}) {
		t.Errorf("attempt (1,2) usage = %+v", a2.Response)
	}
	if a3.Response == nil || a3.Response.Usage == nil || a3.Response.Usage.InputTokens != 200 {
		t.Errorf("attempt (2,1) usage = %+v", a3.Response)
	}
	if len(tu.Elisions) != 1 || tu.Elisions[0] != (Elision{Round: 2, Count: 3, Bytes: 900}) {
		t.Errorf("Elisions = %+v, want one {2 3 900}", tu.Elisions)
	}
	if len(tu.Retries) != 1 || tu.Retries[0] != (RetryInfo{Round: 1, Attempt: 2, Reason: "llm stream truncated"}) {
		t.Errorf("Retries = %+v", tu.Retries)
	}
	if tu.Done == nil || tu.Done.Reason != "max_rounds" || tu.Done.Rounds != 2 || tu.Done.Error != "" || tu.Done.WallMS != 4321 {
		t.Errorf("Done = %+v", tu.Done)
	}
	for _, check := range []struct {
		round, attempt int
		want           []byte
	}{
		{1, 1, b1}, {1, 2, b2}, {2, 1, b3},
	} {
		got, err := Body(dir, id, check.round, check.attempt)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(check.want) {
			t.Errorf("Body(%d,%d) = %q, want %q", check.round, check.attempt, got, check.want)
		}
	}
	if _, err := Body(dir, id, 3, 1); err == nil {
		t.Error("Body for a round that was never requested returned no error")
	}
}

// TestListIncompleteTurn pins the crash-tolerant read: a turn with no done
// event and a final line cut in half by a mid-write crash still lists and
// loads, with Reason "" and no error.
func TestListIncompleteTurn(t *testing.T) {
	dir := t.TempDir()
	id := testID(2)
	_, rec := Start(context.Background(), dir, id, Meta{}, 0)
	rec.BeginRequest(1, 1, 1, 0)
	rec.Request([]byte(`{}`))
	rec.Response(Response{Round: 1, Attempt: 1, Finish: "stop", FirstByteMS: 1, FirstDeltaMS: 1, StreamMS: 1, Text: "half"})
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	// A crash mid-write: the final event's first half, no trailing newline.
	f, err := os.OpenFile(filepath.Join(dir, id, "events.ndjson"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"turn":"` + id + `","kind`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	sums, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sums) != 1 {
		t.Fatalf("List = %d summaries, want 1", len(sums))
	}
	if sums[0].Reason != "" {
		t.Errorf("Summary.Reason = %q, want empty without a done event", sums[0].Reason)
	}
	if sums[0].HasUsage {
		t.Error("Summary.HasUsage = true, want false")
	}
	tu, err := Load(dir, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if tu.Done != nil {
		t.Errorf("Turn.Done = %+v, want nil", tu.Done)
	}
	if len(tu.Attempts) != 1 {
		t.Errorf("Turn has %d attempts, want 1 — the partial line must be ignored", len(tu.Attempts))
	}
	if !strings.HasPrefix(sums[0].ID, "2026") {
		t.Errorf("Summary.ID = %q", sums[0].ID)
	}
}

// TestResolve pins ref resolution and the exact error texts: last, a full
// id, a unique prefix, an ambiguous prefix, no match, and last on an empty
// dir.
func TestResolve(t *testing.T) {
	dir := t.TempDir()
	id1, id2 := testID(1), testID(2)
	for _, id := range []string{id1, id2} {
		if _, rec := Start(context.Background(), dir, id, Meta{}, 0); rec == nil {
			t.Fatalf("Start %s failed", id)
		}
	}
	if got, err := Resolve(dir, "last"); err != nil || got != id2 {
		t.Errorf(`Resolve(last) = %q, %v; want %q`, got, err, id2)
	}
	if got, err := Resolve(dir, id1); err != nil || got != id1 {
		t.Errorf("Resolve(full id) = %q, %v; want %q", got, err, id1)
	}
	prefix := id1[:15] // 20260101T000001: only the first turn matches
	if got, err := Resolve(dir, prefix); err != nil || got != id1 {
		t.Errorf("Resolve(unique prefix) = %q, %v; want %q", got, err, id1)
	}
	_, err := Resolve(dir, id1[:12])
	if want := `"` + id1[:12] + `" matches 2 turns; give more of the id`; err == nil || err.Error() != want {
		t.Errorf("Resolve(ambiguous) = %v, want %q", err, want)
	}
	_, err = Resolve(dir, "zzzz")
	if want := `no turn matches "zzzz"`; err == nil || err.Error() != want {
		t.Errorf("Resolve(no match) = %v, want %q", err, want)
	}
	_, err = Resolve(t.TempDir(), "last")
	if want := "no traces yet"; err == nil || err.Error() != want {
		t.Errorf("Resolve(last, empty dir) = %v, want %q", err, want)
	}
}
