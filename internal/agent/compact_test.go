package agent

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEstimateTokensIsLenOverFour(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"abcd", 1},
		{"abcdefgh", 2},
		{"abc", 0},
	}
	for _, c := range cases {
		if got := EstimateTokens(c.s); got != c.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", c.s, got, c.want)
		}
	}
}

func TestCompactUnderBudgetIsUnchanged(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recs := []Record{
		rec(ts, "user", "short"),
		rec(ts, "assistant", "also short"),
	}
	got := Compact(recs, 1_000_000)
	if !reflect.DeepEqual(got, recs) {
		t.Fatalf("Compact under budget = %+v, want unchanged %+v", got, recs)
	}
}

// TestCompactPreservesStagedRecords is one of the three PASS-by-name tests
// the stage file names. 50 records, 10 of them Staged, laid out as ten
// (4-prose, 1-staged) groups so budget pressure forces real collapsing.
// Every staged record must survive, identified by identity (its unique
// Args marker), not merely by post-compaction count.
func TestCompactPreservesStagedRecords(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	longProse := strings.Repeat("the agent proposed a change and explained its reasoning at length. ", 4) // ~400 chars

	var recs []Record
	var wantStaged []Record
	for group := 0; group < 10; group++ {
		for p := 0; p < 4; p++ {
			role := "user"
			if p%2 == 1 {
				role = "assistant"
			}
			recs = append(recs, Record{
				TS:      ts.Add(time.Duration(len(recs)) * time.Second),
				Role:    role,
				Content: fmt.Sprintf("[group %d turn %d] %s", group, p, longProse),
			})
		}
		staged := Record{
			TS:     ts.Add(time.Duration(len(recs)) * time.Second),
			Role:   "tool",
			Tool:   "stage.create_page",
			Args:   fmt.Sprintf("stage-op-%d", group), // the identity marker
			Result: "proposed",
			Staged: true,
		}
		recs = append(recs, staged)
		wantStaged = append(wantStaged, staged)
	}

	if len(recs) != 50 {
		t.Fatalf("test setup: len(recs) = %d, want 50", len(recs))
	}
	if len(wantStaged) != 10 {
		t.Fatalf("test setup: len(wantStaged) = %d, want 10", len(wantStaged))
	}

	const budget = 500
	got := Compact(recs, budget)

	total := 0
	for _, r := range got {
		total += recordTokens(r)
	}
	if total > budget {
		t.Errorf("Compact left total = %d tokens, want <= %d", total, budget)
	}

	// Identity, not count: every staged record from the input must appear,
	// unmodified, in the output, in its original relative order.
	var gotStaged []Record
	for _, r := range got {
		if r.Staged {
			gotStaged = append(gotStaged, r)
		}
	}
	if !reflect.DeepEqual(gotStaged, wantStaged) {
		t.Fatalf("staged records changed by Compact:\n got  %+v\n want %+v", gotStaged, wantStaged)
	}

	// A count-only assertion would also pass if Compact swapped one staged
	// record for another; guard against that specifically.
	for i, w := range wantStaged {
		if !reflect.DeepEqual(gotStaged[i], w) {
			t.Errorf("staged record %d = %+v, want identical %+v", i, gotStaged[i], w)
		}
	}
}

func TestCompactNeverDropsAStagedRecordEvenOverBudget(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Every record staged: nothing is collapsible, so Compact cannot reach
	// budget without violating the invariant. It must return everything
	// rather than drop one.
	var recs []Record
	for i := 0; i < 5; i++ {
		recs = append(recs, Record{
			TS:     ts.Add(time.Duration(i) * time.Second),
			Role:   "tool",
			Tool:   "stage.create_page",
			Args:   fmt.Sprintf("op-%d", i),
			Result: strings.Repeat("x", 400),
			Staged: true,
		})
	}
	got := Compact(recs, 1) // impossibly small budget
	if !reflect.DeepEqual(got, recs) {
		t.Fatalf("Compact dropped a staged record: got %d records, want %d", len(got), len(recs))
	}
}

func TestCompactPreservesOrder(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	longProse := strings.Repeat("filler prose to force collapsing. ", 10)
	recs := []Record{
		rec(ts, "user", longProse),
		rec(ts.Add(time.Second), "assistant", longProse),
		{TS: ts.Add(2 * time.Second), Role: "tool", Tool: "stage.create_page", Args: "op-1", Result: "ok", Staged: true},
		rec(ts.Add(3*time.Second), "user", longProse),
	}
	got := Compact(recs, 20)

	if len(got) == 0 {
		t.Fatalf("Compact returned nothing")
	}
	lastTS := time.Time{}
	for i, r := range got {
		if r.TS.Before(lastTS) {
			t.Fatalf("record %d out of order: TS %v before previous %v", i, r.TS, lastTS)
		}
		lastTS = r.TS
	}
	// The staged record must still be present, verbatim, wherever it ended up.
	found := false
	for _, r := range got {
		if r.Staged && r.Args == "op-1" {
			found = true
			if r.Result != "ok" {
				t.Errorf("staged record corrupted: %+v", r)
			}
		}
	}
	if !found {
		t.Fatalf("staged record op-1 missing from %+v", got)
	}
}
