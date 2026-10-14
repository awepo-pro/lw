// commit_partial_test.go pins 061 FIX2 R6 for the TUI's default commit message:
// the commit message is the changeset's intent, and a partial mark in front of
// it ("[partial: provider 502 after 2 op(s)] ") describes the REVIEW state — a
// turn that failed — not the change the reviewer is accepting. The user read the
// diff and pressed C: the commit goes into history as what it is, so the mark
// does not ride into it. (lw commit -m and Engine.Commit take the message they
// are given and are untouched.)
package review

import (
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
)

func TestCommitMessageStripsPartialMark(t *testing.T) {
	cases := []struct {
		name string
		cs   *stage.Changeset
		want string
	}{
		{"partial ingest", &stage.Changeset{ID: "cs-1", Intent: "[partial: provider 502 after 2 op(s)] ingest /x/a.md"}, "ingest /x/a.md"},
		{"partial join", &stage.Changeset{ID: "cs-1", Intent: "[partial: stalled after 1 op(s)] ingest a.md + ingest b.md"}, "ingest a.md + ingest b.md"},
		{"mark and nothing else falls back", &stage.Changeset{ID: "cs-9", Intent: "[partial: provider 502 after 2 op(s)]"}, "review: cs-9"},
		{"not partial is untouched", &stage.Changeset{ID: "cs-1", Intent: "ingest /x/a.md"}, "ingest /x/a.md"},
		{"a mark that is not first is untouched", &stage.Changeset{ID: "cs-1", Intent: "ingest a.md [partial: x]"}, "ingest a.md [partial: x]"},
		{"empty intent falls back as before", &stage.Changeset{ID: "cs-2"}, "review: cs-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &Model{changeset: tc.cs}
			if got := m.commitMessage(); got != tc.want {
				t.Errorf("commitMessage() = %q, want %q", got, tc.want)
			}
		})
	}
	if got := (&Model{}).commitMessage(); got != "review: " {
		t.Errorf("commitMessage() with no changeset = %q, want the existing %q", got, "review: ")
	}
}
