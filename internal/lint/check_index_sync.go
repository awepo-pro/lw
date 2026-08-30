package lint

import (
	"fmt"
	"sort"

	"github.com/awepo-pro/lw/internal/vault"
)

// indexSyncCheck is check 10, index-sync.
type indexSyncCheck struct{}

func newIndexSync() Check { return indexSyncCheck{} }

func (indexSyncCheck) ID() string { return "index-sync" }
func (indexSyncCheck) Describe() string {
	return "index.md and the wiki/ page set are not 1:1"
}
func (indexSyncCheck) Severity() Severity { return SevError }

// Run reports both directions of an index.md / wiki/ page-set mismatch.
// index.md is not a Page (backbone §2.8 loads only wiki/ and raw/), so it
// is read via Context.Vault.Read and its [[links]] parsed directly with
// vault.ParseWikilinks; Line is therefore 1-based in index.md as read
// (backbone §4, S1 correction C-6), with no frontmatter to offset from —
// the missing-entry case carries Line 0, the points-nowhere case carries
// the wikilink's real line. A file in Context.Vault.ParseErrors is not
// part of the page set (backbone §2.8, MASTER §9 D-W), so a page that
// failed to parse cannot generate a spurious "missing from index.md"
// finding here.
func (indexSyncCheck) Run(ctx *Context) []Finding {
	b, err := ctx.Vault.Read("index.md")
	if err != nil {
		return nil
	}
	content := string(b)
	links := vault.ParseWikilinks(content)

	var findings []Finding
	linked := map[string]bool{}
	for _, w := range links {
		target, ok := vault.Resolve(ctx.Vault, w.Target)
		if !ok {
			findings = append(findings, Finding{
				Check:    "index-sync",
				Path:     "index.md",
				Line:     w.Line,
				Severity: SevError,
				Message: fmt.Sprintf(
					"entry [[%s]] points to a page that does not exist; remove it or create the page",
					content[w.Start+2:w.End-2]),
			})
			continue
		}
		linked[target] = true
	}

	for _, p := range ctx.Vault.Pages() {
		if linked[p.Path] {
			continue
		}
		findings = append(findings, Finding{
			Check:    "index-sync",
			Path:     "index.md",
			Severity: SevError,
			Message:  fmt.Sprintf("%s has no line in index.md; add one", p.Path),
		})
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Message < findings[j].Message
	})
	return findings
}
