package eval

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// The two groups a scorecard is split into. A run is an ask run (lw query) or
// an ingest run, and the two measure different things, so their numbers are
// never averaged together.
const (
	GroupAsk    = "ask"
	GroupIngest = "ingest"
)

// The verdicts of Compare (C5), as printed.
const (
	VerdictReal       = "REAL"
	VerdictNoise      = "within noise"
	VerdictNoEstimate = "no noise estimate"
)

// verdictTolerance absorbs float rounding in the verdict test. Two runs that
// are the same by construction (three 0.1s against two 0.1s) can differ by
// 1e-17 in their means, and with both noise estimates zero that would
// otherwise read as a REAL change. The scores are ratios and small counts, so
// 1e-9 is far below anything that is a difference.
const verdictTolerance = 1e-9

// boundedMetrics are the metrics bounded in [0,1]: rates and 0/1 outcomes.
// Their per-case variance is floored (A-037-5, see Stat). max_rounds_hit is
// here because a turn either hit the round limit or did not; so are closed (a
// turn reached stage.close or did not) and patched_lossless (a share) (049).
var boundedMetrics = map[string]bool{
	MetricFactRecall: true, MetricAbstainOK: true, MetricCiteValid: true, MetricCiteExpected: true,
	MetricChunkCoverage: true, MetricToolErrorRate: true, MetricMaxRoundsHit: true,
	MetricClosed: true, MetricPatchedLossless: true,
}

// Stat is one metric aggregated over a run's cases (C5).
//
// Each case is repeated N times, and the repeats of one case are what
// measures the noise: a case's own mean μ_c and sample variance s_c² (n-1).
// Value is the mean of the case means — every case counts once, however many
// of its runs have the metric. SE is sqrt(Σ s_c²/n_c) / C: the standard error
// of that mean of means, summed over the cases that have two or more values
// and divided by C, the number of cases that have any. It is absent when no
// case has two values — one run per case has no spread to estimate, and a
// made-up 0 would claim a certainty nobody measured.
//
// A case whose runs all agree has a sample variance of exactly 0 — but three
// 1.0s out of three tries do not prove the case is perfectly steady, and a 0
// noise estimate makes the next 0.05 change read as REAL. So for a metric
// bounded in [0,1] each case's variance is floored at the Laplace-smoothed
// Bernoulli variance: s_c² = max(sample variance, p̃(1-p̃)), p̃ = (Σx+1)/(n+2),
// which for 3 identical runs is 0.16. Unbounded metrics (rounds, tokens,
// seconds) keep the plain sample variance. (A-037-5.)
type Stat struct {
	Metric string
	Value  float64
	SE     float64
	HasSE  bool
	Cases  int // C: cases holding at least one value
	Runs   int // values in all
}

// Card is one group's scorecard. (037 T3.)
type Card struct {
	Group  string
	Cases  int // distinct cases run, including cases whose every run failed
	Runs   int // runs, failed ones included
	Failed int
	Stats  []Stat
}

// groupOf names the group a run belongs to: lw query is "ask", everything
// else (lw ingest) "ingest".
func groupOf(verb string) string {
	if verb == "query" {
		return GroupAsk
	}
	return GroupIngest
}

// series is one metric's values, by case, in run order.
type series map[string][]float64

// collect gathers every metric's values by case. A failed run adds nothing,
// whatever its record holds: failed runs are counted by the caller and never
// enter a metric (C5). A value that is not a finite number is dropped rather
// than poisoning a mean.
func collect(results []CaseResult) map[string]series {
	out := map[string]series{}
	for _, r := range results {
		if r.Failed {
			continue
		}
		for m, v := range r.Metrics {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			if out[m] == nil {
				out[m] = series{}
			}
			out[m][r.Case] = append(out[m][r.Case], v)
		}
	}
	return out
}

// statOf aggregates one metric's series. Cases are walked in sorted order so
// the floating-point sums — and with them the last digit of every printed
// number — never depend on map order.
func statOf(metric string, s series) Stat {
	ids := make([]string, 0, len(s))
	for id := range s {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	st := Stat{Metric: metric, Cases: len(ids)}
	var sumMean, sumVar float64
	for _, id := range ids {
		v := s[id]
		n := float64(len(v))
		mean := sum(v) / n
		sumMean += mean
		st.Runs += len(v)
		if len(v) >= 2 {
			var ss float64
			for _, x := range v {
				ss += (x - mean) * (x - mean)
			}
			s2 := ss / (n - 1)
			if boundedMetrics[metric] {
				p := (sum(v) + 1) / (n + 2)
				if floor := p * (1 - p); floor > s2 {
					s2 = floor
				}
			}
			sumVar += s2 / n // s_c² / n_c
			st.HasSE = true
		}
	}
	if st.Cases > 0 {
		st.Value = sumMean / float64(st.Cases)
		if st.HasSE {
			st.SE = math.Sqrt(sumVar) / float64(st.Cases)
		}
	}
	return st
}

func sum(v []float64) float64 {
	var t float64
	for _, x := range v {
		t += x
	}
	return t
}

// orderedMetrics lists the metrics of m in print order: the known ones in
// metricOrder, then any other by name.
func orderedMetrics[T any](m map[string]T) []string {
	var out []string
	known := map[string]bool{}
	for _, name := range metricOrder {
		known[name] = true
		if _, ok := m[name]; ok {
			out = append(out, name)
		}
	}
	var extra []string
	for name := range m {
		if !known[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

// Aggregate aggregates every metric present in results (C5), in print order.
// The caller chooses what results span — Cards splits by group. (037 T3.)
func Aggregate(results []CaseResult) []Stat {
	by := collect(results)
	var out []Stat
	for _, name := range orderedMetrics(by) {
		out = append(out, statOf(name, by[name]))
	}
	return out
}

// Cards splits the results into the ask and ingest scorecards, in that
// order, leaving out a group with no runs. Holdout cases are in a run only
// when it was made with --holdout (the runner leaves them out otherwise), so
// a scorecard is over exactly the cases its run contains. (037 T3.)
func (r *Results) Cards() []Card {
	var cards []Card
	for _, g := range []string{GroupAsk, GroupIngest} {
		var rs []CaseResult
		cases := map[string]bool{}
		failed := 0
		for _, x := range r.Results {
			if groupOf(x.Verb) != g {
				continue
			}
			rs = append(rs, x)
			cases[x.Case] = true
			if x.Failed {
				failed++
			}
		}
		if len(rs) == 0 {
			continue
		}
		cards = append(cards, Card{Group: g, Cases: len(cases), Runs: len(rs), Failed: failed, Stats: Aggregate(rs)})
	}
	return cards
}

// Row is one metric of a comparison. (037 T3.)
//
// A and B are the metric over the cases both runs have a value for — a
// metric such as cite_valid exists only where an answer carried a reference,
// and comparing A's mean over ten cases with B's over nine would put the
// difference in which cases were counted into Δ, where it would read as the
// model changing. When the two runs share no case with the metric, each side
// shows its own cases and there is no Δ. A nil side has no value at all.
type Row struct {
	Group, Metric string
	A, B          *Stat
	Delta         float64 // B - A
	HasDelta      bool
	Floor         float64 // 2·sqrt(SE_A² + SE_B²)
	HasFloor      bool    // both runs have a noise estimate
	Verdict       string
}

// Comparison is the A → B report: one row per metric, the failed runs per
// side, the cases only one run has, and anything that makes the numbers less
// comparable than they look.
type Comparison struct {
	A, B             RunInfo
	Rows             []Row
	FailedA, FailedB int
	OnlyA, OnlyB     []string
	Warnings         []string
}

// Compare compares run A with run B on the cases both have (037 T3, C5).
//
// Δ = B - A. It is called REAL only when both runs have a noise estimate and
// |Δ| is above the floor, 2·sqrt(SE_A² + SE_B²): two standard errors of the
// difference, the least that separates a change from the run-to-run spread
// the repeats measured. At or under it the verdict is "within noise"; with no
// estimate on a side (one run per case) it is "no noise estimate" and the
// delta is shown but not judged. Cases only one run has are listed and
// excluded, and failed runs among the compared cases are counted per side and
// enter no metric.
func Compare(a, b *Results) *Comparison {
	verbsA, verbsB := caseVerbs(a.Results), caseVerbs(b.Results)
	c := &Comparison{A: a.Run, B: b.Run}
	common := map[string]bool{}
	for id := range verbsA {
		if _, ok := verbsB[id]; ok {
			common[id] = true
		} else {
			c.OnlyA = append(c.OnlyA, id)
		}
	}
	for id := range verbsB {
		if !common[id] {
			c.OnlyB = append(c.OnlyB, id)
		}
	}
	sort.Strings(c.OnlyA)
	sort.Strings(c.OnlyB)
	c.Warnings = differingFields(a, b)

	var ra, rb []CaseResult
	for _, r := range a.Results {
		if common[r.Case] {
			ra = append(ra, r)
			if r.Failed {
				c.FailedA++
			}
		}
	}
	for _, r := range b.Results {
		if common[r.Case] {
			rb = append(rb, r)
			if r.Failed {
				c.FailedB++
			}
		}
	}

	for _, g := range []string{GroupAsk, GroupIngest} {
		inGroup := func(rs []CaseResult) []CaseResult {
			var out []CaseResult
			for _, r := range rs {
				if groupOf(r.Verb) == g {
					out = append(out, r)
				}
			}
			return out
		}
		sa, sb := collect(inGroup(ra)), collect(inGroup(rb))
		union := map[string]bool{}
		for m := range sa {
			union[m] = true
		}
		for m := range sb {
			union[m] = true
		}
		for _, m := range orderedMetrics(union) {
			c.Rows = append(c.Rows, compareRow(g, m, sa[m], sb[m]))
		}
	}
	return c
}

// differingFields returns one warning line for each of the fields below that
// differs between the two runs, in this order. Every one changes what the
// numbers are numbers of — a comparison across them is still printed, but it
// is not the comparison its header suggests. lw_version differs in any
// comparison of two builds; the line is there so that is a fact on the page,
// not an assumption. (A-037-8.)
func differingFields(a, b *Results) []string {
	short := func(s string) string {
		if len(s) > 12 {
			return s[:12]
		}
		return s
	}
	show := func(s string) string {
		if s == "" {
			return "—"
		}
		return s
	}
	fields := []struct{ name, a, b, why string }{
		{"set_sha256", short(a.SetSHA256), short(b.SetSHA256), "cases.toml changed between the two scorings, so cases, facts or cite_any may differ"},
		{"snapshot", short(a.Run.SnapshotSHA256), short(b.Run.SnapshotSHA256), "the numbers compare two vaults, not just two lw configurations"},
		{"lw_version", a.Run.LWVersion, b.Run.LWVersion, "the runs used different lw builds"},
		{"n", fmt.Sprint(a.Run.N), fmt.Sprint(b.Run.N), "the noise estimates rest on different numbers of repeats"},
		{"holdout", fmt.Sprint(a.Run.Holdout), fmt.Sprint(b.Run.Holdout), "only one run included the holdout cases"},
		{"only", fmt.Sprintf("%q", a.Run.Only), fmt.Sprintf("%q", b.Run.Only), "the runs covered different parts of the set"},
	}
	var out []string
	for _, f := range fields {
		if f.a != f.b {
			out = append(out, fmt.Sprintf("%s differs (A %s · B %s): %s", f.name, show(f.a), show(f.b), f.why))
		}
	}
	return out
}

// caseVerbs maps each case id in results to its verb.
func caseVerbs(results []CaseResult) map[string]string {
	out := map[string]string{}
	for _, r := range results {
		out[r.Case] = r.Verb
	}
	return out
}

// compareRow builds one metric's row from each side's series.
func compareRow(group, metric string, sa, sb series) Row {
	row := Row{Group: group, Metric: metric}
	shared := series{}
	sharedB := series{}
	for id, v := range sa {
		if w, ok := sb[id]; ok {
			shared[id], sharedB[id] = v, w
		}
	}
	if len(shared) == 0 {
		// No common ground: show what each side has, judge nothing.
		if len(sa) > 0 {
			st := statOf(metric, sa)
			row.A = &st
		}
		if len(sb) > 0 {
			st := statOf(metric, sb)
			row.B = &st
		}
		row.Verdict = VerdictNoEstimate
		return row
	}
	a, b := statOf(metric, shared), statOf(metric, sharedB)
	row.A, row.B = &a, &b
	row.Delta, row.HasDelta = b.Value-a.Value, true
	if a.HasSE && b.HasSE {
		row.Floor, row.HasFloor = 2*math.Sqrt(a.SE*a.SE+b.SE*b.SE), true
	}
	switch {
	case !row.HasFloor:
		row.Verdict = VerdictNoEstimate
	case math.Abs(row.Delta) > row.Floor+verdictTolerance:
		row.Verdict = VerdictReal
	default:
		row.Verdict = VerdictNoise
	}
	return row
}

// plural renders "1 case" / "2 cases".
func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// fmt3 prints a number to three decimals, never as a negative zero.
func fmt3(v float64) string {
	s := fmt.Sprintf("%.3f", v)
	if s == "-0.000" {
		return "0.000"
	}
	return s
}

// fmtDelta prints a signed difference to three decimals.
func fmtDelta(v float64) string {
	s := fmt.Sprintf("%+.3f", v)
	if s == "-0.000" {
		return "+0.000"
	}
	return s
}

func fmtSE(s *Stat) string {
	if s == nil || !s.HasSE {
		return "—"
	}
	return "±" + fmt3(s.SE)
}

func fmtStat(s *Stat) string {
	if s == nil {
		return "—"
	}
	return fmt3(s.Value)
}

// writeTable writes header and rows as aligned columns, two spaces apart.
// left marks the columns that are left-aligned; the rest are right-aligned.
// A left-aligned last column is not padded, so no line ends in spaces.
func writeTable(b *strings.Builder, header []string, rows [][]string, left []bool) {
	width := make([]int, len(header))
	all := append([][]string{header}, rows...)
	for _, r := range all {
		for i, cell := range r {
			if n := utf8.RuneCountInString(cell); n > width[i] {
				width[i] = n
			}
		}
	}
	for _, r := range all {
		for i, cell := range r {
			pad := strings.Repeat(" ", width[i]-utf8.RuneCountInString(cell))
			if i > 0 {
				b.WriteString("  ")
			}
			switch {
			case left[i] && i == len(r)-1:
				b.WriteString(cell)
			case left[i]:
				b.WriteString(cell + pad)
			default:
				b.WriteString(pad + cell)
			}
		}
		b.WriteByte('\n')
	}
}

// WriteScorecard prints the run's header and one table per group — ask, then
// ingest — with the columns metric, value, ±SE, cases, runs. Numbers are to
// three decimals; an SE that does not exist is an em dash. (037 T3.)
func (r *Results) WriteScorecard(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "run:   %s\n", r.Run.ID)
	fmt.Fprintf(&b, "lw:    %s\n", r.Run.LWVersion)
	n := fmt.Sprintf("%d per case · parallel %d", r.Run.N, r.Run.Parallel)
	if r.Run.Holdout {
		n += " · holdout"
	}
	if r.Run.Only != "" {
		n += " · only " + r.Run.Only
	}
	fmt.Fprintf(&b, "n:     %s\n", n)
	if r.Run.Note != "" {
		fmt.Fprintf(&b, "note:  %s\n", r.Run.Note)
	}
	for _, c := range r.Cards() {
		fmt.Fprintf(&b, "\n%s · %s · %s · %d failed\n", c.Group, plural(c.Cases, "case"), plural(c.Runs, "run"), c.Failed)
		var rows [][]string
		for i := range c.Stats {
			s := &c.Stats[i]
			rows = append(rows, []string{s.Metric, fmt3(s.Value), fmtSE(s), fmt.Sprint(s.Cases), fmt.Sprint(s.Runs)})
		}
		if len(rows) == 0 {
			b.WriteString("no metrics: every run failed\n")
			continue
		}
		writeTable(&b, []string{"metric", "value", "±SE", "cases", "runs"}, rows, []bool{true, false, false, false, false})
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// label names a run in the comparison header: its id, lw version and note.
func label(info RunInfo) string {
	var parts []string
	for _, p := range []string{info.ID, info.LWVersion, info.Note} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}

// WriteComparison prints a Comparison: the two runs, any warning, one table
// per group with the columns metric, A, B, Δ, floor and verdict, then the
// failed counts and the cases only one run has. (037 T3.)
func WriteComparison(w io.Writer, c *Comparison) error {
	var b strings.Builder
	fmt.Fprintf(&b, "A: %s\nB: %s\n", label(c.A), label(c.B))
	for _, warn := range c.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", warn)
	}
	for _, g := range []string{GroupAsk, GroupIngest} {
		var rows [][]string
		for _, r := range c.Rows {
			if r.Group != g {
				continue
			}
			delta, floor := "—", "—"
			if r.HasDelta {
				delta = fmtDelta(r.Delta)
			}
			if r.HasFloor {
				floor = fmt3(r.Floor)
			}
			rows = append(rows, []string{r.Metric, fmtStat(r.A), fmtStat(r.B), delta, floor, r.Verdict})
		}
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s\n", g)
		writeTable(&b, []string{"metric", "A", "B", "Δ", "floor", "verdict"}, rows, []bool{true, false, false, false, false, true})
	}
	if len(c.Rows) == 0 {
		b.WriteString("\nno cases in common\n")
	}
	fmt.Fprintf(&b, "\nfailed: A %d · B %d\n", c.FailedA, c.FailedB)
	if len(c.OnlyA) > 0 {
		fmt.Fprintf(&b, "only in A: %s\n", strings.Join(c.OnlyA, ", "))
	}
	if len(c.OnlyB) > 0 {
		fmt.Fprintf(&b, "only in B: %s\n", strings.Join(c.OnlyB, ", "))
	}
	_, err := io.WriteString(w, b.String())
	return err
}
