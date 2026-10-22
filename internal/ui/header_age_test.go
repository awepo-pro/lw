package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/stage"
)

// TestHeaderShowsChangesetAge pins 067 D3: when a changeset is open and
// age = Now() − OpenedAt ≥ 1h, the right group shows the age label.
func TestHeaderShowsChangesetAge(t *testing.T) {
	fixedNow := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	csID := "cs-857cb0bbcd3427ae"
	cs9 := ShortID(csID) // "cs-857cb0"

	newAppWithStage := func(opened time.Time, partial bool, w int) *App {
		a := NewApp(Options{
			Now: func() time.Time { return fixedNow },
		})
		a.stageID = csID
		a.stageOps = 19
		a.stageChecks = stage.Checks{Schema: "pass", Lint: "pass"}
		a.hasStage = true
		a.stagePartial = partial
		a.stageOpenedAt = opened
		m, _ := a.Update(tea.WindowSizeMsg{Width: w, Height: 24})
		return m.(*App)
	}

	headerOf := func(a *App) string {
		return ansi.Strip(strings.SplitN(a.View().Content, "\n", 2)[0])
	}

	t.Run("aged_65h12m_width_120", func(t *testing.T) {
		opened := fixedNow.Add(-65*time.Hour - 12*time.Minute)
		a := newAppWithStage(opened, false, 120)
		h := headerOf(a)
		want := cs9 + " · 19 ops · 2d · checks ✓"
		if !strings.Contains(h, want) {
			t.Fatalf("header at width 120 = %q, want it to contain %q", h, want)
		}
	})

	t.Run("opened_30m_earlier", func(t *testing.T) {
		opened := fixedNow.Add(-30 * time.Minute)
		a := newAppWithStage(opened, false, 120)
		h := headerOf(a)
		want := cs9 + " · 19 ops · checks ✓"
		if !strings.Contains(h, want) {
			t.Fatalf("header for <1h = %q, want it to contain %q", h, want)
		}
		if strings.Contains(h, "<1h") {
			t.Fatalf("header for <1h should not display age label, got %q", h)
		}
	})

	t.Run("partial_at_width_120", func(t *testing.T) {
		opened := fixedNow.Add(-65*time.Hour - 12*time.Minute)
		a := newAppWithStage(opened, true, 120)
		h := headerOf(a)
		want := cs9 + " · 19 ops · 2d · partial · checks ✓"
		if !strings.Contains(h, want) {
			t.Fatalf("header partial at width 120 = %q, want it to contain %q", h, want)
		}
	})

	t.Run("width_where_age_does_not_fit", func(t *testing.T) {
		opened := fixedNow.Add(-65*time.Hour - 12*time.Minute)
		// With a 12-char vault name at width 80, the 33-cell age form does not fit,
		// but the 28-cell form without age does fit.
		a := newAppWithStage(opened, false, 80)
		a.vaultName = "longer-vault"
		h := headerOf(a)
		want := cs9 + " · 19 ops · checks ✓"
		if !strings.Contains(h, want) {
			t.Fatalf("header at narrow width = %q, want today's form %q", h, want)
		}
		if strings.Contains(h, "2d") {
			t.Fatalf("header at narrow width should drop age first, got %q", h)
		}
	})
}
