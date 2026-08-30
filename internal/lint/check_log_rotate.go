package lint

import (
	"fmt"
	"strings"
)

// logRotateCheck is check 14, log-rotate.
type logRotateCheck struct{}

func newLogRotate() Check { return logRotateCheck{} }

func (logRotateCheck) ID() string         { return "log-rotate" }
func (logRotateCheck) Describe() string   { return "log.md exceeds 500 entries" }
func (logRotateCheck) Severity() Severity { return SevInfo }

// logRotateThreshold is the rotation threshold (backbone §4, Hermes:
// "When log.md exceeds 500 entries, rotate it"). §5.4 Engine.Commit step 9
// already rotates log.md at 500 lines by itself, so this check only fires
// on a vault whose log was hand-edited or predates the engine — info,
// never error, for that reason.
const logRotateThreshold = 500

// Run reports when log.md carries more than 500 entries — lines
// beginning with "- ", not file lines (backbone §4, S1 correction C-9:
// dirty/log.md is 507 lines but 505 entries, and EXPECTED-LINT.md's
// message says 505). log.md is not a Page (backbone §2.8 loads only
// wiki/ and raw/), so it is read via Context.Vault.Read. The rotation
// target's year is read off the first entry's own timestamp, never
// time.Now() (backbone §4's determinism contract) — log.md's entries are
// appended in chronological order, so the oldest entry's year is the year
// being rotated out.
func (logRotateCheck) Run(ctx *Context) []Finding {
	b, err := ctx.Vault.Read("log.md")
	if err != nil {
		return nil
	}

	var entries int
	var year string
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		entries++
		if year == "" && len(line) >= 6 {
			year = line[2:6]
		}
	}
	if entries <= logRotateThreshold {
		return nil
	}

	return []Finding{{
		Check:    "log-rotate",
		Path:     "log.md",
		Severity: SevInfo,
		Message: fmt.Sprintf(
			"%d entries exceeds the %d-entry rotation threshold; rotate to log-%s.md",
			entries, logRotateThreshold, year),
	}}
}
