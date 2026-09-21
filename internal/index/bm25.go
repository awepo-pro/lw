package index

import "math"

// BM25 tuning constants, fixed by backbone §3.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// Field weights, fixed by backbone §3: a term matching the title scores
// x3, a term matching a tag x2, and the body field itself is weight 1. They
// are combined into one pseudo-field ("BM25F-lite") by scaling each field's
// term frequency and field length by its weight before running a single
// BM25 computation — an occurrence in the title counts as three body
// occurrences worth of evidence, which is what "scores x3" means in
// practice.
//
// weights extended by 014 (backbone §3 amendment — 014 workflow §9): the
// page's ## Abstract joins as a fourth field at x4, above title, so a page
// is found by the summary it leads with. A page with no abstract section
// contributes exactly zero from the field. The abstract is a slice of the
// body, so its terms also count ×1 in the body field: an abstract hit
// weighs effectively ×5 (4+1), not ×4 — and the 4-vs-3 ordering above
// title holds a fortiori.
const (
	bodyWeight     = 1
	tagWeight      = 2
	titleWeight    = 3
	abstractWeight = 4
)

// combinedFreq returns d's weighted term frequency for term across the
// four fields.
func combinedFreq(d *docEntry, term string) int {
	return bodyWeight*d.BodyTermFreq[term] + tagWeight*d.TagTermFreq[term] + titleWeight*d.TitleTermFreq[term] + abstractWeight*d.AbstractTermFreq[term]
}

// combinedLen returns d's weighted document length across the four
// fields, on the same scale as combinedFreq, for the BM25 length
// normalization term.
func combinedLen(d *docEntry) int {
	return bodyWeight*d.BodyLen + tagWeight*d.TagLen + titleWeight*d.TitleLen + abstractWeight*d.AbstractLen
}

// idf is the standard BM25 inverse document frequency, using the "+1"
// variant that stays positive for every document frequency from 1 to n.
func idf(n, df int) float64 {
	return math.Log(1 + (float64(n)-float64(df)+0.5)/(float64(df)+0.5))
}

// termScore is one query term's BM25 contribution to d's score, given the
// term's document frequency df among the n candidate documents and the
// candidate set's average combined document length.
func termScore(d *docEntry, term string, n, df int, avgLen float64) float64 {
	f := float64(combinedFreq(d, term))
	if f == 0 {
		return 0
	}
	dl := float64(combinedLen(d))
	return idf(n, df) * (f * (bm25K1 + 1)) / (f + bm25K1*(1-bm25B+bm25B*dl/avgLen))
}
