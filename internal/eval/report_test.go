package eval

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// repRun builds one run's result for the hand-built tests: case id, run
// index, and the metrics it holds. The verb follows the group the test wants.
func repRun(verb, id string, index int, m map[string]float64) CaseResult {
	kind := "covered"
	if verb == "ingest" {
		kind = "ingest"
	}
	return CaseResult{Case: id, Index: index, Verb: verb, Kind: kind, Metrics: m}
}

// repSeries builds N runs of one ask case, run i holding metric = values[i].
func repSeries(id, metric string, values ...float64) []CaseResult {
	var out []CaseResult
	for i, v := range values {
		out = append(out, repRun("query", id, i+1, map[string]float64{metric: v}))
	}
	return out
}

func repJoin(parts ...[]CaseResult) []CaseResult {
	var out []CaseResult
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func statFor(t *testing.T, stats []Stat, metric string) Stat {
	t.Helper()
	for _, s := range stats {
		if s.Metric == metric {
			return s
		}
	}
	t.Fatalf("no %s stat in %+v", metric, stats)
	return Stat{}
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// TestAggregateAndSE pins the C5 aggregation on the frozen hand-built case:
// two cases of three runs each, fact_recall 1,1,0 and 1,0,0. Each case has
// mean 2/3 and 1/3 and sample variance (n-1) 1/3, so Value is 0.5 and
// SE = sqrt((1/3)/3 + (1/3)/3) / 2 = 0.2357 (037 T3).
func TestAggregateAndSE(t *testing.T) {
	t.Run("frozen two cases by three runs", func(t *testing.T) {
		rs := repJoin(repSeries("a", "fact_recall", 1, 1, 0), repSeries("b", "fact_recall", 1, 0, 0))
		s := statFor(t, Aggregate(rs), "fact_recall")
		if !near(s.Value, 0.5, 1e-12) {
			t.Errorf("Value = %v, want 0.5", s.Value)
		}
		if !s.HasSE || math.Round(s.SE*1e4)/1e4 != 0.2357 {
			t.Errorf("SE = %v (has %v), want 0.2357", s.SE, s.HasSE)
		}
		if want := math.Sqrt((1.0/3)/3+(1.0/3)/3) / 2; !near(s.SE, want, 1e-12) {
			t.Errorf("SE = %v, want %v", s.SE, want)
		}
		if s.Cases != 2 || s.Runs != 6 {
			t.Errorf("Cases/Runs = %d/%d, want 2/6", s.Cases, s.Runs)
		}
	})

	t.Run("the variance divides by n-1, not n", func(t *testing.T) {
		// One case, values 0 and 1: population variance 0.25, sample 0.5.
		// SE = sqrt(0.5/2)/1 = 0.5 (a population variance would give 0.3536).
		s := statFor(t, Aggregate(repSeries("a", "fact_recall", 0, 1)), "fact_recall")
		if !near(s.SE, 0.5, 1e-12) {
			t.Errorf("SE = %v, want 0.5 (sample variance)", s.SE)
		}
	})

	t.Run("a single run per case has no noise estimate", func(t *testing.T) {
		rs := repJoin(repSeries("a", "fact_recall", 1), repSeries("b", "fact_recall", 0))
		s := statFor(t, Aggregate(rs), "fact_recall")
		if s.HasSE {
			t.Errorf("SE present (%v) with one run per case", s.SE)
		}
		if !near(s.Value, 0.5, 1e-12) || s.Cases != 2 || s.Runs != 2 {
			t.Errorf("stat = %+v, want value 0.5 over 2 cases, 2 runs", s)
		}
	})

	t.Run("a case with one value counts in Value and C but adds no variance", func(t *testing.T) {
		// a: 1,0,0 (mean 1/3, s2 1/3, n 3); b: a single 1. Value = (1/3+1)/2,
		// SE = sqrt((1/3)/3) / 2 with C = 2.
		rs := repJoin(repSeries("a", "fact_recall", 1, 0, 0), repSeries("b", "fact_recall", 1))
		s := statFor(t, Aggregate(rs), "fact_recall")
		if !near(s.Value, 2.0/3, 1e-12) {
			t.Errorf("Value = %v, want 2/3", s.Value)
		}
		if want := math.Sqrt((1.0/3)/3) / 2; !s.HasSE || !near(s.SE, want, 1e-12) {
			t.Errorf("SE = %v (has %v), want %v", s.SE, s.HasSE, want)
		}
		if s.Cases != 2 {
			t.Errorf("Cases = %d, want 2", s.Cases)
		}
	})

	t.Run("identical runs give a present SE of zero", func(t *testing.T) {
		s := statFor(t, Aggregate(repSeries("a", "fact_recall", 1, 1, 1)), "fact_recall")
		if !s.HasSE || s.SE != 0 {
			t.Errorf("SE = %v (has %v), want a present 0", s.SE, s.HasSE)
		}
	})

	t.Run("a case missing the metric is not a case of it", func(t *testing.T) {
		// cite_valid exists only where the answer had a reference.
		rs := repJoin(
			repSeries("a", "cite_valid", 1, 0),
			[]CaseResult{repRun("query", "b", 1, map[string]float64{"fact_recall": 1})},
		)
		s := statFor(t, Aggregate(rs), "cite_valid")
		if s.Cases != 1 || s.Runs != 2 || !near(s.Value, 0.5, 1e-12) {
			t.Errorf("cite_valid = %+v, want 1 case, 2 runs, value 0.5", s)
		}
	})
}

// TestFailedRunsExcluded pins that a failed run adds to the failed count and
// to no metric — even when its record still carries numbers (037 T3).
func TestFailedRunsExcluded(t *testing.T) {
	failed := repRun("query", "a", 2, map[string]float64{"fact_recall": 0, "rounds": 99})
	failed.Failed = true
	rs := []CaseResult{
		repRun("query", "a", 1, map[string]float64{"fact_recall": 1}),
		failed,
		repRun("query", "a", 3, map[string]float64{"fact_recall": 1}),
	}
	res := &Results{Results: rs}

	cards := res.Cards()
	if len(cards) != 1 || cards[0].Group != "ask" {
		t.Fatalf("cards = %+v, want one ask card", cards)
	}
	c := cards[0]
	if c.Failed != 1 || c.Runs != 3 || c.Cases != 1 {
		t.Errorf("card = failed %d, runs %d, cases %d; want 1, 3, 1", c.Failed, c.Runs, c.Cases)
	}
	fr := statFor(t, c.Stats, "fact_recall")
	if fr.Value != 1 || fr.Runs != 2 {
		t.Errorf("fact_recall = %+v, want value 1 over the 2 runs that did not fail", fr)
	}
	for _, s := range c.Stats {
		if s.Metric == "rounds" {
			t.Errorf("a metric that only a failed run held appeared: %+v", s)
		}
	}

	// And in a comparison: counted per side, in no row.
	other := &Results{Results: []CaseResult{
		repRun("query", "a", 1, map[string]float64{"fact_recall": 1}),
		repRun("query", "a", 2, map[string]float64{"fact_recall": 1}),
	}}
	cmp := Compare(res, other)
	if cmp.FailedA != 1 || cmp.FailedB != 0 {
		t.Errorf("failed = A %d, B %d; want 1, 0", cmp.FailedA, cmp.FailedB)
	}
	for _, r := range cmp.Rows {
		if r.Metric == "rounds" {
			t.Errorf("a failed run's metric reached a comparison row: %+v", r)
		}
	}
}

// repSpread builds `cases` ask cases named c1..cN, each three runs of metric
// holding mean-0.1, mean, mean+0.1: every case has sample variance 0.01, so
// with 4 cases SE = sqrt(4 * 0.01/3) / 4 = 0.02887.
func repSpread(metric string, cases int, mean float64) []CaseResult {
	var out []CaseResult
	for c := 1; c <= cases; c++ {
		out = append(out, repSeries("c"+string(rune('0'+c)), metric, mean-0.1, mean, mean+0.1)...)
	}
	return out
}

func rowFor(t *testing.T, c *Comparison, metric string) Row {
	t.Helper()
	for _, r := range c.Rows {
		if r.Metric == metric {
			return r
		}
	}
	t.Fatalf("no %s row in %+v", metric, c.Rows)
	return Row{}
}

// TestCompareVerdicts pins the noise rule: Δ = B - A against a floor of
// 2·sqrt(SE_A² + SE_B²), with the three verdicts REAL, within noise and no
// noise estimate (037 T3).
func TestCompareVerdicts(t *testing.T) {
	t.Run("REAL", func(t *testing.T) {
		a := &Results{Results: repSpread("fact_recall", 4, 0.5)}
		b := &Results{Results: repSpread("fact_recall", 4, 0.9)}
		r := rowFor(t, Compare(a, b), "fact_recall")
		if r.Verdict != VerdictReal {
			t.Errorf("verdict = %q, want %q (row %+v)", r.Verdict, VerdictReal, r)
		}
		if !near(r.Delta, 0.4, 1e-9) {
			t.Errorf("Δ = %v, want 0.4", r.Delta)
		}
		se := math.Sqrt(4*0.01/3) / 4
		if want := 2 * math.Sqrt(2*se*se); !r.HasFloor || !near(r.Floor, want, 1e-9) {
			t.Errorf("floor = %v (has %v), want %v", r.Floor, r.HasFloor, want)
		}
	})

	t.Run("within noise", func(t *testing.T) {
		a := &Results{Results: repSpread("fact_recall", 4, 0.5)}
		b := &Results{Results: repSpread("fact_recall", 4, 0.55)}
		r := rowFor(t, Compare(a, b), "fact_recall")
		if r.Verdict != VerdictNoise {
			t.Errorf("verdict = %q, want %q (row %+v)", r.Verdict, VerdictNoise, r)
		}
		if !near(r.Delta, 0.05, 1e-9) {
			t.Errorf("Δ = %v, want 0.05", r.Delta)
		}
	})

	t.Run("a drop is real too", func(t *testing.T) {
		a := &Results{Results: repSpread("fact_recall", 4, 0.9)}
		b := &Results{Results: repSpread("fact_recall", 4, 0.5)}
		r := rowFor(t, Compare(a, b), "fact_recall")
		if r.Verdict != VerdictReal || !near(r.Delta, -0.4, 1e-9) {
			t.Errorf("row = %+v, want a REAL Δ of -0.4", r)
		}
	})

	t.Run("no noise estimate with one run per case", func(t *testing.T) {
		single := func(v float64) *Results {
			return &Results{Results: repJoin(repSeries("a", "fact_recall", v), repSeries("b", "fact_recall", v))}
		}
		r := rowFor(t, Compare(single(0.2), single(0.9)), "fact_recall")
		if r.Verdict != VerdictNoEstimate || r.HasFloor {
			t.Errorf("row = %+v, want %q with no floor", r, VerdictNoEstimate)
		}
		if !near(r.Delta, 0.7, 1e-9) {
			t.Errorf("Δ = %v, want 0.7 (the delta is still shown)", r.Delta)
		}
	})

	t.Run("one side without an SE is no estimate, however big the delta", func(t *testing.T) {
		a := &Results{Results: repSpread("fact_recall", 4, 0.1)}
		b := &Results{Results: repJoin(repSeries("c1", "fact_recall", 0.9), repSeries("c2", "fact_recall", 0.9),
			repSeries("c3", "fact_recall", 0.9), repSeries("c4", "fact_recall", 0.9))}
		r := rowFor(t, Compare(a, b), "fact_recall")
		if r.Verdict != VerdictNoEstimate {
			t.Errorf("verdict = %q, want %q", r.Verdict, VerdictNoEstimate)
		}
	})

	t.Run("equal to the floor is within noise; zero noise makes any change real", func(t *testing.T) {
		same := &Results{Results: repJoin(repSeries("a", "fact_recall", 1, 1, 1))}
		r := rowFor(t, Compare(same, same), "fact_recall")
		if r.Verdict != VerdictNoise || r.Delta != 0 {
			t.Errorf("identical runs: %+v, want within noise at Δ 0", r)
		}
		other := &Results{Results: repJoin(repSeries("a", "fact_recall", 0.5, 0.5, 0.5))}
		r = rowFor(t, Compare(same, other), "fact_recall")
		if r.Verdict != VerdictReal {
			t.Errorf("zero floor, Δ -0.5: %+v, want REAL", r)
		}
		// Float dust is not a change. Two identical values have an exact mean
		// and no spread, so both noise estimates are exactly 0 — and the
		// 0.1+0.2 that is not quite 0.3 must not read as a REAL difference
		// of 5.6e-17.
		x, y := 0.1, 0.2
		dust := x + y
		dustA := &Results{Results: repSeries("a", "fact_recall", 0.3, 0.3)}
		dustB := &Results{Results: repSeries("a", "fact_recall", dust, dust)}
		if r := rowFor(t, Compare(dustA, dustB), "fact_recall"); r.Verdict != VerdictNoise || r.Delta == 0 {
			t.Errorf("float dust read as %q (Δ %g): %+v", r.Verdict, r.Delta, r)
		}
	})

	t.Run("three verdicts from one pair of runs", func(t *testing.T) {
		// fact_recall: 4 cases x 3 runs, big shift. cite_valid: same spread,
		// small shift. rounds: held by one run per case only.
		var ar, br []CaseResult
		ar = append(ar, repSpread("fact_recall", 4, 0.5)...)
		br = append(br, repSpread("fact_recall", 4, 0.9)...)
		ar = append(ar, repSpread("cite_valid", 4, 0.5)...)
		br = append(br, repSpread("cite_valid", 4, 0.52)...)
		for c := 1; c <= 4; c++ {
			id := "c" + string(rune('0'+c))
			ar = append(ar, repRun("query", id, 4, map[string]float64{"rounds": 3}))
			br = append(br, repRun("query", id, 4, map[string]float64{"rounds": 6}))
		}
		cmp := Compare(&Results{Results: ar}, &Results{Results: br})
		got := map[string]string{}
		for _, r := range cmp.Rows {
			got[r.Metric] = r.Verdict
		}
		want := map[string]string{"fact_recall": "REAL", "cite_valid": "within noise", "rounds": "no noise estimate"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("verdicts = %v, want %v", got, want)
		}
	})

	t.Run("cases in only one run are listed and excluded", func(t *testing.T) {
		// c is wildly different in A and absent from B; d only in B. If either
		// leaked in, A's value would move off 0.5 and B's off 0.9.
		a := &Results{Results: repJoin(
			repSeries("a", "fact_recall", 0.4, 0.5, 0.6),
			repSeries("b", "fact_recall", 0.4, 0.5, 0.6),
			repSeries("c", "fact_recall", 0, 0, 0),
		)}
		b := &Results{Results: repJoin(
			repSeries("a", "fact_recall", 0.8, 0.9, 1.0),
			repSeries("b", "fact_recall", 0.8, 0.9, 1.0),
			repSeries("d", "fact_recall", 0.0, 0.0, 0.0),
		)}
		cmp := Compare(a, b)
		if !reflect.DeepEqual(cmp.OnlyA, []string{"c"}) || !reflect.DeepEqual(cmp.OnlyB, []string{"d"}) {
			t.Errorf("only in A %v, only in B %v; want [c] and [d]", cmp.OnlyA, cmp.OnlyB)
		}
		r := rowFor(t, cmp, "fact_recall")
		if !near(r.A.Value, 0.5, 1e-12) || !near(r.B.Value, 0.9, 1e-12) || r.A.Cases != 2 || r.B.Cases != 2 {
			t.Errorf("row = %+v, want A 0.5 and B 0.9 over the 2 shared cases", r)
		}
		var buf strings.Builder
		if err := WriteComparison(&buf, cmp); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"only in A: c\n", "only in B: d\n"} {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("comparison text lacks %q:\n%s", want, buf.String())
			}
		}
	})

	t.Run("a metric is compared on the cases that have it on both sides", func(t *testing.T) {
		// cite_valid: A has it for a and b, B only for a. b's value in A must
		// not be compared against nothing.
		a := &Results{Results: repJoin(
			repSeries("a", "cite_valid", 0.5, 0.5, 0.5),
			repSeries("b", "cite_valid", 0, 0, 0),
		)}
		b := &Results{Results: repJoin(
			repSeries("a", "cite_valid", 0.5, 0.5, 0.5),
			repSeries("b", "fact_recall", 1, 1, 1),
		)}
		r := rowFor(t, Compare(a, b), "cite_valid")
		if !near(r.A.Value, 0.5, 1e-12) || !near(r.B.Value, 0.5, 1e-12) {
			t.Errorf("cite_valid A %v B %v, want both 0.5 over case a alone", r.A.Value, r.B.Value)
		}
	})
}

// TestCompareText pins the rendered comparison: the columns, the verdict
// words, the failed line (037 T3).
func TestCompareText(t *testing.T) {
	a := &Results{
		Run:     RunInfo{ID: "run-a", LWVersion: "lw v1"},
		Results: repSpread("fact_recall", 4, 0.5),
	}
	b := &Results{
		Run:     RunInfo{ID: "run-b", LWVersion: "lw v2", Note: "thinking=on"},
		Results: repSpread("fact_recall", 4, 0.9),
	}
	b.Results[0].Failed = true
	var buf strings.Builder
	if err := WriteComparison(&buf, Compare(a, b)); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{
		"A: run-a",
		"B: run-b",
		"ask\n",
		"metric", "floor", "verdict",
		"fact_recall",
		"REAL",
		"failed: A 0 · B 1\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("comparison text lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "only in") {
		t.Errorf("an exact-same case list printed an only-in line:\n%s", got)
	}
}

// TestCompareWarnsOnDifferentSnapshots pins that comparing runs of two
// different vaults says so: the numbers are then not a measurement of the
// model alone (037 T3).
func TestCompareWarnsOnDifferentSnapshots(t *testing.T) {
	a := &Results{Run: RunInfo{SnapshotSHA256: "aaaa"}, Results: repSpread("fact_recall", 2, 0.5)}
	b := &Results{Run: RunInfo{SnapshotSHA256: "bbbb"}, Results: repSpread("fact_recall", 2, 0.5)}
	cmp := Compare(a, b)
	if len(cmp.Warnings) != 1 || !strings.Contains(cmp.Warnings[0], "snapshot") {
		t.Fatalf("warnings = %q, want one naming the snapshot", cmp.Warnings)
	}
	var buf strings.Builder
	if err := WriteComparison(&buf, cmp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "warning: ") {
		t.Errorf("the warning is not printed:\n%s", buf.String())
	}
	if w := Compare(a, a).Warnings; len(w) != 0 {
		t.Errorf("same snapshot warned: %q", w)
	}
}

// TestScorecardText pins the scorecard layout on a tiny result: the header,
// one table per group (ask then ingest), numbers to 3 decimals, the SE as
// ±x.xxx or an em dash, the counts (037 T3).
func TestScorecardText(t *testing.T) {
	res := &Results{
		Run: RunInfo{ID: "r1", LWVersion: "lw v1", N: 2, Parallel: 2, Note: "n"},
		Results: []CaseResult{
			repRun("query", "a", 1, map[string]float64{"fact_recall": 1}),
			repRun("query", "a", 2, map[string]float64{"fact_recall": 0}),
			repRun("ingest", "i", 1, map[string]float64{"ops": 4}),
		},
	}
	var buf strings.Builder
	if err := res.WriteScorecard(&buf); err != nil {
		t.Fatal(err)
	}
	want := "run:   r1\n" +
		"lw:    lw v1\n" +
		"n:     2 per case · parallel 2\n" +
		"note:  n\n" +
		"\n" +
		"ask · 1 case · 2 runs · 0 failed\n" +
		"metric       value     ±SE  cases  runs\n" +
		"fact_recall  0.500  ±0.500      1     2\n" +
		"\n" +
		"ingest · 1 case · 1 run · 0 failed\n" +
		"metric  value  ±SE  cases  runs\n" +
		"ops     4.000    —      1     1\n"
	if buf.String() != want {
		t.Errorf("scorecard\n got:\n%s\nwant:\n%s", buf.String(), want)
	}
}
