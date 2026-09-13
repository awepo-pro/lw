// layout_test.go pins footerBarText itself (C-124/TD-8, gate G6): the
// footer must never again claim a binding no screen or shell code matches.
package ui

import (
	"strings"
	"testing"
)

// TestFooterAdvertisesOnlyRealKeys is TD-8's regression guard: the footer
// used to print "[a]sk · [s]tage · [c]ommit · [l]int · [L]og · [?]" — five
// letters nothing bound and a help hint no screen ever consumed (found live
// at gate G6, 2026-09-14). It must name only keys that do something:
// [tab] to switch screens, [ctrl+r] from Ask to Review, and [q] to quit.
func TestFooterAdvertisesOnlyRealKeys(t *testing.T) {
	for _, dead := range []string{"[a]sk", "[s]tage", "[c]ommit", "[l]int", "[L]og", "[?]"} {
		if strings.Contains(footerBarText, dead) {
			t.Fatalf("footerBarText = %q, still advertises dead key %q", footerBarText, dead)
		}
	}
	for _, want := range []string{"[tab]", "[ctrl+r]", "[q]"} {
		if !strings.Contains(footerBarText, want) {
			t.Fatalf("footerBarText = %q, missing %q", footerBarText, want)
		}
	}
}
