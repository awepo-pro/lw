package lint

import (
	"fmt"
	"strings"
)

// sizeSplitCheck is check 11, size-split.
type sizeSplitCheck struct{}

func newSizeSplit() Check { return sizeSplitCheck{} }

func (sizeSplitCheck) ID() string { return "size-split" }
func (sizeSplitCheck) Describe() string {
	return "body exceeds 200 lines -> split candidate"
}
func (sizeSplitCheck) Severity() Severity { return SevInfo }

// sizeSplitMaxLines is the split-candidate threshold (backbone §4, S1
// correction C-8): strictly more than 200 lines fires.
const sizeSplitMaxLines = 200

// Run reports every page whose body has strictly more than 200 lines.
// Page.Body always ends with exactly one "\n" when non-empty (backbone
// §2.3), so counting "\n" bytes counts lines exactly.
func (sizeSplitCheck) Run(ctx *Context) []Finding {
	var findings []Finding
	for _, p := range ctx.Vault.Pages() {
		if strings.Count(p.Body, "\n") <= sizeSplitMaxLines {
			continue
		}
		findings = append(findings, Finding{
			Check:    "size-split",
			Path:     p.Path,
			Severity: SevInfo,
			Message: fmt.Sprintf(
				"body exceeds %d lines; consider splitting into smaller pages", sizeSplitMaxLines),
		})
	}
	return findings
}
