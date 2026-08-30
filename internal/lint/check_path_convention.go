package lint

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// pathConventionCheck is check 4, path-convention.
type pathConventionCheck struct{}

func newPathConvention() Check { return pathConventionCheck{} }

func (pathConventionCheck) ID() string { return "path-convention" }
func (pathConventionCheck) Describe() string {
	return "filename is not lowercase-hyphen.md, or the directory does not match type"
}
func (pathConventionCheck) Severity() Severity { return SevWarn }

// pathConventionFilenameRE matches a filename in canonical lowercase-hyphen
// form: one or more lowercase-alphanumeric segments joined by single
// hyphens, then ".md".
var pathConventionFilenameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*\.md$`)

// pathConventionSlugRE collapses every run of characters that are not a
// lowercase ASCII letter or digit into a single hyphen, for suggesting a
// renamed filename.
var pathConventionSlugRE = regexp.MustCompile(`[^a-z0-9]+`)

// Run reports a page whose filename is not lowercase-hyphen.md, or — for a
// page whose filename is already conventional — whose directory does not
// match its declared type. Each page gets at most one finding: a bad
// filename is reported ahead of a directory mismatch, since fixing the
// filename is usually the smaller edit and the two causes are rarely both
// present in the same page.
func (pathConventionCheck) Run(ctx *Context) []Finding {
	var findings []Finding
	for _, p := range ctx.Vault.Pages() {
		base := path.Base(p.Path)
		if !pathConventionFilenameRE.MatchString(base) {
			findings = append(findings, Finding{
				Check:    "path-convention",
				Path:     p.Path,
				Severity: SevWarn,
				Message:  fmt.Sprintf("filename is not lowercase-hyphen.md; rename to %s", slugifyFilename(base)),
				Fixable:  true,
			})
			continue
		}

		dir := path.Dir(p.Path)
		wantDir := p.FM.Type.Dir()
		if wantDir != "" && dir != wantDir {
			findings = append(findings, Finding{
				Check:    "path-convention",
				Path:     p.Path,
				Severity: SevWarn,
				Message: fmt.Sprintf(
					"page is under %s but type %s belongs under %s; move the file or fix the type",
					dir, p.FM.Type, wantDir),
				Fixable: true,
			})
		}
	}
	return findings
}

// slugifyFilename lowercases base's stem, collapses every run of
// non-alphanumeric characters into a single hyphen, trims leading and
// trailing hyphens, and re-appends the original extension (".md" if base
// had none) — the rename suggested in path-convention's Message.
func slugifyFilename(base string) string {
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	slug := pathConventionSlugRE.ReplaceAllString(strings.ToLower(stem), "-")
	slug = strings.Trim(slug, "-")
	if ext == "" {
		ext = ".md"
	}
	return slug + ext
}
