package lint

import (
	"fmt"
	"strings"

	"github.com/awepo-pro/lw/internal/cite"
)

// citeSupersededCheck is check 19, cite-superseded (070 S2).
// Reports wiki pages whose body cites a raw that a newer revision supersedes (L1),
// and raw sources whose supersedes: frontmatter key names themselves or a path
// that is not a committed raw in the vault (L2).
type citeSupersededCheck struct{}

func newCiteSuperseded() Check { return citeSupersededCheck{} }

func (citeSupersededCheck) ID() string { return "cite-superseded" }
func (citeSupersededCheck) Describe() string {
	return "a page body cites a superseded raw source, or a raw names itself or a missing or non-raw path in supersedes:"
}
func (citeSupersededCheck) Severity() Severity { return SevWarn }

// supersededTips maps every superseded raw to the tip of its chain: the last raw
// reached by following supersedes: successors, with a cycle guard. One pass over
// the raws, so a vault with many pages and citations pays O(raws) once instead of
// a slice copy per citation. A raw that names itself supersedes nothing.
func supersededTips(ctx *Context) map[string]string {
	next := make(map[string]string)
	for _, r := range ctx.Vault.RawSources() {
		if r.Supersedes == "" || r.Supersedes == r.Path {
			continue
		}
		if _, dup := next[r.Supersedes]; !dup {
			next[r.Supersedes] = r.Path
		}
	}
	tips := make(map[string]string, len(next))
	for old := range next {
		tip, seen := next[old], map[string]bool{old: true}
		for {
			n, ok := next[tip]
			if !ok || seen[tip] {
				break
			}
			seen[tip] = true
			tip = n
		}
		tips[old] = tip
	}
	return tips
}

// Run reports:
// L1: one finding per superseded raw that a page's BODY cites (a ^[raw/...] marker),
// naming the tip of the chain. Only body citations: the agent can edit those, but
// stage.patch_page only ever adds to sources: (050 D2), so a stale sources: entry
// could never be cleared and would be permanent noise (A-070-6); src-provenance
// already covers a sources: entry with no marker.
// L2: one finding per raw whose supersedes: names itself, a missing path or a non-raw path.
func (citeSupersededCheck) Run(ctx *Context) []Finding {
	if ctx == nil || ctx.Vault == nil {
		return nil
	}
	tips := supersededTips(ctx)

	var findings []Finding
	for _, p := range ctx.Vault.Pages() {
		seen := make(map[string]bool)
		for _, c := range cite.Scan(p.Body) {
			if seen[c.Source] || !strings.HasPrefix(c.Source, "raw/") {
				continue
			}
			tip, ok := tips[c.Source]
			if !ok {
				continue
			}
			seen[c.Source] = true
			findings = append(findings, Finding{
				Check:    "cite-superseded",
				Path:     p.Path,
				Severity: SevWarn,
				Message: fmt.Sprintf(
					"cites %s in the body, superseded by %s; re-point it to the new raw",
					c.Source, tip),
			})
		}
	}

	for _, r := range ctx.Vault.RawSources() {
		if r.Supersedes == "" {
			continue
		}
		if r.Supersedes == r.Path {
			findings = append(findings, Finding{
				Check:    "cite-superseded",
				Path:     r.Path,
				Severity: SevWarn,
				Message:  "supersedes names itself",
			})
			continue
		}
		if _, ok := ctx.Vault.RawSource(r.Supersedes); !ok {
			findings = append(findings, Finding{
				Check:    "cite-superseded",
				Path:     r.Path,
				Severity: SevWarn,
				Message: fmt.Sprintf(
					"supersedes names %s, which is not a committed raw",
					r.Supersedes),
			})
		}
	}

	return findings
}
