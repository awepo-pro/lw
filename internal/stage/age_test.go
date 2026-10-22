package stage_test

import (
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
)

func TestAgeLabel(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "<1h"},
		{59*time.Minute + 59*time.Second, "<1h"},
		{1 * time.Hour, "1h"},
		{47*time.Hour + 59*time.Minute, "47h"},
		{48 * time.Hour, "2d"},
		{65*time.Hour + 12*time.Minute, "2d"},
		{720 * time.Hour, "30d"},
	}

	for _, tt := range tests {
		got := stage.AgeLabel(tt.d)
		if got != tt.want {
			t.Errorf("AgeLabel(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
