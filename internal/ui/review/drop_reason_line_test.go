package review

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
	"github.com/awepo-pro/lw/internal/ui/uitest"
)

// TestDroppedOpLinesAt80And120 pins 069 S2: for every drop actor (human, rollback,
// guard, cascade) and the unmarked case ("reason not recorded"), a dropped op shows
// exactly one fact-first muted line directly under the header in the Detail panel,
// asserted at 80 and 120 columns.
func TestDroppedOpLinesAt80And120(t *testing.T) {
	cases := []struct {
		name     string
		actor    stage.DropActor
		reason   string
		wantLine string
	}{
		{
			name:     "human",
			actor:    stage.DropByHuman,
			reason:   "dropped in review",
			wantLine: "dropped by you · dropped in review",
		},
		{
			name:     "rollback",
			actor:    stage.DropByRollback,
			reason:   "extraction failed on turn 2",
			wantLine: "dropped by rollback · extraction failed on turn 2",
		},
		{
			name:     "guard",
			actor:    stage.DropByGuard,
			reason:   "ask turn tried to stage during a read-only query",
			wantLine: "dropped by guard · ask turn tried to stage during a read-only query",
		},
		{
			name:     "cascade",
			actor:    stage.DropByCascade,
			reason:   "dropped with op1",
			wantLine: "dropped with another op · dropped with op1",
		},
		{
			name:     "not_recorded",
			actor:    "",
			reason:   "",
			wantLine: "dropped · reason not recorded",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, e, _ := newTestDeps(t, "minimal")
			if _, err := e.OpenChangeset("drop line test: "+tc.name, stage.Author{Kind: "agent", Model: "test"}); err != nil {
				t.Fatalf("OpenChangeset: %v", err)
			}
			id := appendTwoHunkPatch(t, e)

			if tc.actor != "" || tc.reason != "" {
				if err := e.DropOpsWith([]string{id}, stage.Drop{By: tc.actor, Reason: tc.reason}); err != nil {
					t.Fatalf("DropOpsWith: %v", err)
				}
			} else {
				if err := e.DropOps([]string{id}); err != nil {
					t.Fatalf("DropOps: %v", err)
				}
			}

			m, ok := initModel(t, d).(*Model)
			if !ok {
				t.Fatalf("pane is %T, want *review.Model", m)
			}

			// Verify Detail panel lines directly at content width 200
			_, _, lines := m.detailContent(200)
			headIdx := -1
			for i, l := range lines {
				if strings.Contains(ansi.Strip(l.text), "kv-cache.md") && strings.Contains(ansi.Strip(l.text), "dropped") {
					headIdx = i
					break
				}
			}
			if headIdx < 0 || headIdx+1 >= len(lines) {
				t.Fatalf("Diff panel missing dropped op header for %s (lines: %d)", id, len(lines))
			}
			gotLine := ansi.Strip(lines[headIdx+1].text)
			if gotLine != tc.wantLine {
				t.Errorf("line under header = %q, want %q", gotLine, tc.wantLine)
			}
			styledWant := m.theme.Muted.Render(tc.wantLine)
			if lines[headIdx+1].text != styledWant {
				t.Errorf("line under header is not Muted styled: got %q, want %q", lines[headIdx+1].text, styledWant)
			}

			// Verify rendered screen at 80 and 120 columns
			for _, sz := range [][2]int{{80, 22}, {120, 38}} {
				w, h := sz[0], sz[1]
				styled, plain := uitest.PaneScreen(m, w, h)
				uitest.AssertGrid(t, plain, w, h)
				if !strings.Contains(plain, tc.wantLine) {
					t.Errorf("%dx%d: plain screen does not contain %q:\n%s", w, h, tc.wantLine, plain)
				}
				if !strings.Contains(styled, styledWant) {
					t.Errorf("%dx%d: styled screen does not contain Muted %q", w, h, tc.wantLine)
				}
			}

			// Narrow content width clipping
			const narrowCW = 24
			_, _, narrowLines := m.detailContent(narrowCW)
			var narrowGot string
			for i, l := range narrowLines {
				if strings.Contains(ansi.Strip(l.text), "kv-cache.md") && i+1 < len(narrowLines) {
					narrowGot = ansi.Strip(narrowLines[i+1].text)
					break
				}
			}
			if !strings.HasSuffix(narrowGot, "…") {
				t.Errorf("narrowCW=%d: clipped line lost trailing ellipsis: %q", narrowCW, narrowGot)
			}
			if w := lipgloss.Width(narrowGot); w > narrowCW {
				t.Errorf("narrowCW=%d: clipped line width = %d, want <= %d", narrowCW, w, narrowCW)
			}
		})
	}
}

// TestReviewDropRestoreClearsLine pins 069 S2: pressing 'd' drops the op with
// DropByHuman and displays "dropped by you · dropped in review"; pressing 'u'
// restores the op and clears the drop line completely, returning the diff windows.
func TestReviewDropRestoreClearsLine(t *testing.T) {
	d, e, _ := newTestDeps(t, "minimal")
	if _, err := e.OpenChangeset("drop-restore round trip", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	id := appendTwoHunkPatch(t, e)
	m := initModel(t, d)
	moveCursorTo(t, m, id)

	// First d: arm
	m = send(t, m, keyPress('d'))
	wantStatus := fmt.Sprintf("press d again to drop %s", id)
	if msg, level := statusOf(t, m); msg != wantStatus || level != ui.StatusWarn {
		t.Errorf("Status after first d = (%q, %v), want (%q, StatusWarn)", msg, level, wantStatus)
	}

	// Second d: drop
	m = send(t, m, keyPress('d'))
	wantGood := fmt.Sprintf("dropped %s · u restores", id)
	if msg, level := statusOf(t, m); msg != wantGood || level != ui.StatusGood {
		t.Errorf("Status after second d = (%q, %v), want (%q, StatusGood)", msg, level, wantGood)
	}

	// Engine state check
	op, ok := mustOp(t, e, id)
	if !ok {
		t.Fatalf("changeset lost %s", id)
	}
	if op.State != stage.StateDropped {
		t.Fatalf("%s state = %s, want dropped", id, op.State)
	}
	if op.DroppedBy != stage.DropByHuman {
		t.Errorf("%s DroppedBy = %q, want %q", id, op.DroppedBy, stage.DropByHuman)
	}
	if op.DropReason != "dropped in review" {
		t.Errorf("%s DropReason = %q, want %q", id, op.DropReason, "dropped in review")
	}

	// Detail view check: shows "dropped by you · dropped in review"
	const wantMutedLine = "dropped by you · dropped in review"
	_, plainScreen := uitest.PaneScreen(m, 120, 38)
	if !strings.Contains(plainScreen, wantMutedLine) {
		t.Fatalf("screen after drop does not contain %q:\n%s", wantMutedLine, plainScreen)
	}

	// Press 'u': restore
	m = send(t, m, keyPress('u'))
	wantRestored := fmt.Sprintf("restored %s", id)
	if msg, level := statusOf(t, m); msg != wantRestored || level != ui.StatusGood {
		t.Errorf("Status after u = (%q, %v), want (%q, StatusGood)", msg, level, wantRestored)
	}

	// Engine state restored
	op, ok = mustOp(t, e, id)
	if !ok {
		t.Fatalf("changeset lost %s after restore", id)
	}
	if op.State != stage.StateProposed {
		t.Errorf("%s state after restore = %s, want proposed", id, op.State)
	}
	if op.DroppedBy != "" || op.DropReason != "" {
		t.Errorf("%s still has marks after restore: by=%q reason=%q", id, op.DroppedBy, op.DropReason)
	}

	// Detail view check: line is absent, hunk windows are restored
	_, restoredScreen := uitest.PaneScreen(m, 120, 38)
	if strings.Contains(restoredScreen, "dropped by you") {
		t.Errorf("restored screen still carries 'dropped by you':\n%s", restoredScreen)
	}
	if strings.Contains(restoredScreen, "dropped · reason not recorded") {
		t.Errorf("restored screen still carries 'dropped · reason not recorded':\n%s", restoredScreen)
	}
	if !strings.Contains(restoredScreen, "@@") {
		t.Errorf("restored screen does not show hunk headers (@@):\n%s", restoredScreen)
	}
}

// TestDropArmedDropMarksRootHumanAndDependentsCascade pins 069 S2: dropArmedDrop
// drops the root op with DropByHuman ("dropped in review") and its dependents
// with DropByCascade ("dropped with <rootID>").
func TestDropArmedDropMarksRootHumanAndDependentsCascade(t *testing.T) {
	d, e, _ := newTestDeps(t, "minimal")
	if _, err := e.OpenChangeset("cascade mark test", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	ids, _ := appendChainedKvPatches(t, e, 3) // op1 -> op2 -> op3
	rootID := ids[0]

	m := initModel(t, d)
	moveCursorTo(t, m, rootID)

	// First d arms with 2 dependents
	m = send(t, m, keyPress('d'))
	wantArm := fmt.Sprintf("press d again to drop %s + 2 dependents: %s, %s", rootID, ids[1], ids[2])
	if msg, level := statusOf(t, m); msg != wantArm || level != ui.StatusWarn {
		t.Errorf("arm status = (%q, %v), want (%q, StatusWarn)", msg, level, wantArm)
	}

	// Second d drops root + dependents
	m = send(t, m, keyPress('d'))
	wantDone := fmt.Sprintf("dropped %s + 2 dependents · u restores", rootID)
	if msg, level := statusOf(t, m); msg != wantDone || level != ui.StatusGood {
		t.Errorf("done status = (%q, %v), want (%q, StatusGood)", msg, level, wantDone)
	}

	// Verify root op
	rootOp, ok := mustOp(t, e, rootID)
	if !ok {
		t.Fatalf("missing %s", rootID)
	}
	if rootOp.State != stage.StateDropped {
		t.Errorf("root op state = %s, want dropped", rootOp.State)
	}
	if rootOp.DroppedBy != stage.DropByHuman {
		t.Errorf("root op DroppedBy = %q, want %q", rootOp.DroppedBy, stage.DropByHuman)
	}
	if rootOp.DropReason != "dropped in review" {
		t.Errorf("root op DropReason = %q, want %q", rootOp.DropReason, "dropped in review")
	}

	// Verify dependents
	wantCascadeReason := "dropped with " + rootID
	for _, depID := range ids[1:] {
		depOp, ok := mustOp(t, e, depID)
		if !ok {
			t.Fatalf("missing dependent %s", depID)
		}
		if depOp.State != stage.StateDropped {
			t.Errorf("dep %s state = %s, want dropped", depID, depOp.State)
		}
		if depOp.DroppedBy != stage.DropByCascade {
			t.Errorf("dep %s DroppedBy = %q, want %q", depID, depOp.DroppedBy, stage.DropByCascade)
		}
		if depOp.DropReason != wantCascadeReason {
			t.Errorf("dep %s DropReason = %q, want %q", depID, depOp.DropReason, wantCascadeReason)
		}
	}

	// Verify Detail panel lines
	mm := m.(*Model)
	_, _, lines := mm.detailContent(200)

	// op1 must show "dropped by you · dropped in review"
	// op2 and op3 must show "dropped with another op · dropped with op1"
	var foundRoot, foundDep int
	for _, l := range lines {
		text := ansi.Strip(l.text)
		if text == "dropped by you · dropped in review" {
			foundRoot++
		}
		if text == "dropped with another op · "+wantCascadeReason {
			foundDep++
		}
	}
	if foundRoot != 1 {
		t.Errorf("found %d root drop lines, want 1", foundRoot)
	}
	if foundDep != 2 {
		t.Errorf("found %d cascade drop lines, want 2", foundDep)
	}
}
