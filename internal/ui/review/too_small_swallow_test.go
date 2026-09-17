// too_small_swallow_test.go is the fix-review F-3 evidence (MASTER §5
// R-803), driven through a real ui.App with the real Review pane: a key the
// shell swallows because the frame is below 80×24 must reach the pane as
// ui.ShellKeyMsg, exactly like every other shell-consumed key — a raw-only
// confirmation armed before the shrink must not survive it.
package review

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/ui"
)

func TestTooSmallSwallowDisarms(t *testing.T) {
	t.Run("too_small_key_between_Cs_disarms", func(t *testing.T) {
		a, pane, _, root := newRawOnlyApp(t)

		pressC(t, a) // arms
		if msg, level := statusOf(t, pane); msg != wantRawOnlyWarn || level != ui.StatusWarn {
			t.Fatalf("setup: first C = (%q, %v), want the raw-only warning", msg, level)
		}

		// Shrink below the 80×24 minimum: every key but quit is swallowed
		// from here on.
		m, cmd := a.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
		a = m.(*ui.App)
		feedShell(t, a, cmd, 64)

		// j is swallowed by the too-small branch — and must still disarm the
		// confirmation the way every other shell-consumed key does.
		m, cmd = a.Update(keyPress('j'))
		a = m.(*ui.App)
		feedShell(t, a, cmd, 64)

		// Grow back to exactly the minimum, then C: the confirmation was
		// disarmed, so this C warns and re-arms instead of committing.
		m, cmd = a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		a = m.(*ui.App)
		feedShell(t, a, cmd, 64)

		pressC(t, a)

		if journalHasCommitBegin(t, root) {
			t.Fatal("C, shrink, j, grow, C committed — the swallowed key did not disarm the confirmation")
		}
		if msg, level := statusOf(t, pane); msg != wantRawOnlyWarn || level != ui.StatusWarn {
			t.Fatalf("status after the swallowed key = (%q, %v), want the warning shown again", msg, level)
		}

		// The re-armed confirmation is live: the next C commits.
		pressC(t, a)
		if msg, _ := statusOf(t, pane); !strings.HasPrefix(msg, "committed ") {
			t.Errorf("status after the next C = %q, want the commit status", msg)
		}
	})
}
