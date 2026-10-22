package stage

import (
	"fmt"
	"time"
)

// AgeLabel formats a duration as a compact age string for changeset reminders:
// "<1h" for d < 1h, "Nh" for 1h <= d < 48h (floor), and "Nd" for d >= 48h (floor).
func AgeLabel(d time.Duration) string {
	if d < time.Hour {
		return "<1h"
	}
	if d < 48*time.Hour {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}
