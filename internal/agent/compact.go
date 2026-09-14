package agent

// compact.go implements backbone §9's Compact and EstimateTokens: the
// mechanical (never model-driven) rule that trims session history to fit a
// token budget without ever touching a Record whose Staged flag is true —
// those are the audit trail behind an open changeset (/.dev-notes/PLAN-v1.md §11.3).

import "fmt"

// EstimateTokens estimates how many LLM tokens s costs. It is an estimate,
// not a measurement: real token counts depend on the provider's tokenizer,
// which this package never calls. Good enough to budget context; never
// good enough to bill by.
func EstimateTokens(s string) int {
	return len(s) / 4
}

// recordText is the text Compact and EstimateTokens measure a Record by —
// the same fields recordToMessage (context.go) turns into an llm.Message.
func recordText(r Record) string {
	return r.Content + r.Tool + r.Args + r.Result
}

func recordTokens(r Record) int {
	return EstimateTokens(recordText(r))
}

// isProse reports whether r is an ordinary conversational turn: no tool
// call or result attached, and not the audit trail behind a staged op.
// Compact's only reduction mechanism — collapsing a run of these — never
// applies to anything else.
func isProse(r Record) bool {
	return !r.Staged && r.Tool == ""
}

// Compact trims recs to fit budget (an EstimateTokens total), mechanically,
// without ever consulting a model.
//
// Contract (backbone §9, /.dev-notes/PLAN-v1.md §11.3): records with Staged == true are
// NEVER dropped or summarized — they are the audit trail behind the
// changeset, and losing one silently detaches a staged op from the tool
// call that produced it. Tool calls and results are already small by
// construction (backbone §6's own result-size discipline — "context
// discipline is enforced [in internal/tools], not in the prompt"), so the
// only thing Compact ever reduces is a maximal run of consecutive,
// non-staged, tool-free "prose" turns: each such run still needed to fit
// budget is replaced by one placeholder record naming how many turns it
// stands for. Relative order is preserved throughout. If every collapsible
// run has already been collapsed and recs is still over budget, Compact
// returns what it has rather than touching a staged or tool record to hit
// a number.
func Compact(recs []Record, budget int) []Record {
	total := 0
	for _, r := range recs {
		total += recordTokens(r)
	}
	if total <= budget {
		out := make([]Record, len(recs))
		copy(out, recs)
		return out
	}

	type span struct{ start, end int } // [start, end) over recs, a prose run
	var runs []span
	for i := 0; i < len(recs); {
		if !isProse(recs[i]) {
			i++
			continue
		}
		j := i
		for j < len(recs) && isProse(recs[j]) {
			j++
		}
		runs = append(runs, span{i, j})
		i = j
	}

	collapsed := make(map[int]Record, len(runs)) // run start -> placeholder
	absorbed := make(map[int]bool, len(recs))    // indices folded into a placeholder
	for _, r := range runs {
		if total <= budget {
			break
		}

		group := recs[r.start:r.end]
		before := 0
		for _, g := range group {
			before += recordTokens(g)
		}

		placeholder := collapsePlaceholder(group)
		after := recordTokens(placeholder)
		if after >= before {
			continue // nothing gained by collapsing this run; leave it be
		}

		collapsed[r.start] = placeholder
		for i := r.start + 1; i < r.end; i++ {
			absorbed[i] = true
		}
		total = total - before + after
	}

	out := make([]Record, 0, len(recs))
	for i, r := range recs {
		if absorbed[i] {
			continue
		}
		if ph, ok := collapsed[i]; ok {
			out = append(out, ph)
			continue
		}
		out = append(out, r)
	}
	return out
}

// collapsePlaceholder mechanically stands in for a run of prose turns: no
// model call, just a count. TS is the run's earliest timestamp, so the
// placeholder still sorts where the run it replaces was.
func collapsePlaceholder(group []Record) Record {
	return Record{
		TS:      group[0].TS,
		Role:    "system",
		Content: fmt.Sprintf("[%d earlier message(s) omitted to fit the context budget]", len(group)),
	}
}
