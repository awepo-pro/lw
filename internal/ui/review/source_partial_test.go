// source_partial_test.go pins 061 FIX1 A-061-1's effect on the Changeset
// panel's source row: the partial mark now LEADS the intent
// (`[partial: provider 502 after 2 op(s)] ingest /x/a.md`), so the row's
// "ingest " prefix test — which finds the source's base name — must look past
// it. Without that the row printed the whole intent, mark and absolute path,
// where it printed `a.md`. The mark itself is the shell header's badge.
package review

import (
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
)

func TestChangesetSourceRowPastPartialMark(t *testing.T) {
	cases := []struct {
		name   string
		intent string
		want   string
	}{
		{"ingest", "[partial: provider 502 after 2 op(s)] ingest /home/anton/notes/a.md", "source  a.md"},
		{"ingest after a join", "[partial: stalled after 1 op(s)] ingest /x/a.md + ingest /x/b.md", "source  b.md"},
		{"not an ingest", "[partial: provider 429 after 3 op(s)] rework the entities pages", "source  rework the entities pages"},
		{"mark only", "[partial: provider 502 after 2 op(s)]", "source  [partial: provider 502 after 2 op(s)]"},
		{"not partial is untouched", "ingest /home/anton/notes/a.md", "source  a.md"},
		{"a bracket that is not the mark", "[wip] ingest /x/a.md", "source  [wip] ingest /x/a.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &Model{theme: testTheme(t), changeset: &stage.Changeset{ID: "cs-1", Intent: tc.intent}}
			rows := m.changesetRows()
			if got := plain(rows[0]); got != tc.want {
				t.Errorf("source row = %q, want %q", got, tc.want)
			}
		})
	}
}
