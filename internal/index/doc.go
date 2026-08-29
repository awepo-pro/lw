// Package index is the in-memory inverted word index over a vault: tokenizing
// page bodies, titles and tags, scoring matches with BM25, and persisting the
// result to disk for reuse across runs. It will hold index.go, tokenize.go,
// bm25.go and persist.go.
package index
