package tools

import (
	"strings"
	"testing"
)

// TestRawChunkCount pins RawChunkCount to exactly what raw.get itself
// slices — len(chunkText(body, rawChunkRunes)) — at every boundary that
// matters: the empty body (still one empty chunk), one rune, a body of
// exactly rawChunkRunes runes (one chunk, no empty tail), one rune more (two),
// and a multi-byte body counted in runes, not bytes (037 T2). The eval
// harness reads a source's chunk total from this function, so a divergence
// from raw.get would score a model's coverage against the wrong denominator.
func TestRawChunkCount(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"empty body is one empty chunk", "", 1},
		{"one rune", "a", 1},
		{"exactly rawChunkRunes runes", strings.Repeat("a", rawChunkRunes), 1},
		{"rawChunkRunes plus one rune", strings.Repeat("a", rawChunkRunes+1), 2},
		{"40000 CJK runes (120000 bytes)", strings.Repeat("语", 40000), 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RawChunkCount(tc.body)
			if got != tc.want {
				t.Errorf("RawChunkCount = %d, want %d", got, tc.want)
			}
			if direct := len(chunkText(tc.body, rawChunkRunes)); got != direct {
				t.Errorf("RawChunkCount = %d, but raw.get's own chunkText yields %d", got, direct)
			}
		})
	}
}
