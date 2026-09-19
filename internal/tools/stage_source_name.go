// stage_source_name.go holds the raw-source naming rule 008 contract §4.1
// pins for stage.ingest_source, extended by 009 (idea 011): the staged
// file's name comes from the extracted title, else from the tool's
// optional name hint (for a source whose title has no Latin letters —
// slug.Make keeps ASCII only, so 四元數簡介 used to land at
// untitled.md), else from the uri's basename with one trailing extension
// stripped, else "untitled" — and a candidate path already taken by a
// DIFFERENT source gets the first free "-2", "-3", … suffix instead of a
// refusal. Before 008 the basename fallback kept the extension as "-md"
// ("notes.md" -> "notes-md.md"), and a taken candidate was a dead end:
// the agent has no filesystem verbs, so the source was simply never
// ingested (008 U6).
package tools

import (
	"fmt"
	"path"
	"strings"

	"github.com/awepo-pro/lw/internal/slug"
	"github.com/awepo-pro/lw/internal/stage"
)

// rawSourceExtensions are the trailing extensions stripped from a uri's
// basename before slugging, compared case-insensitively (008 §4.1). One
// extension only: "notes.tar.md" slugs to "notes-tar", not "notes".
var rawSourceExtensions = [...]string{".md", ".markdown", ".txt", ".html", ".htm"}

// sourceNameForDoc derives the source filename — no directory, no
// extension — for one extracted document: the slugged title when non-empty,
// else the slugged name hint (009's optional argument, empty when the
// caller passed none), else the slugged basename minus one trailing
// rawSourceExtensions entry, else "untitled". The result is never empty.
func sourceNameForDoc(title, name, uri string) string {
	if slug := slugSourceName(title); slug != "" {
		return slug
	}
	if slug := slugSourceName(name); slug != "" {
		return slug
	}
	if slug := slugSourceName(stripRawSourceExtension(path.Base(uri))); slug != "" {
		return slug
	}
	return "untitled"
}

// stripRawSourceExtension removes one trailing rawSourceExtensions entry
// from base, case-insensitively; a base that is only an extension (".md")
// strips to "".
func stripRawSourceExtension(base string) string {
	lower := strings.ToLower(base)
	for _, ext := range rawSourceExtensions {
		if strings.HasSuffix(lower, ext) {
			return base[:len(base)-len(ext)]
		}
	}
	return base
}

// resolveSourcePath returns the candidate raw path for name under kindDir
// and the path to actually stage at: the candidate itself when free, else
// the first free raw/<kindDir>/<name>-<n>.md for n = 2, 3, …, with suffixed
// true. A path counts as taken when a committed raw source exists there
// (Vault.Exists) or a live ingest_source op in the open changeset already
// claims it. Same-body occupants never reach this decision — the handler's
// dedupe checks refuse them first, with the existing errors.
func resolveSourcePath(d Deps, kindDir, name string) (candidate, final string, suffixed bool) {
	candidate = "raw/" + kindDir + "/" + name + ".md"
	final = candidate
	for n := 2; sourcePathTaken(d, final); n++ {
		final = fmt.Sprintf("raw/%s/%s-%d.md", kindDir, name, n)
		suffixed = true
	}
	return candidate, final, suffixed
}

// sourcePathTaken reports whether path already holds a raw source: a
// committed one in the vault, or one a live ingest_source op of the open
// changeset would write.
func sourcePathTaken(d Deps, path string) bool {
	if d.Vault != nil && d.Vault.Exists(path) {
		return true
	}
	if d.Engine != nil {
		if cs, err := d.Engine.Current(); err == nil {
			for _, op := range cs.Live() {
				if op.Kind == stage.OpIngestSource && op.Path == path {
					return true
				}
			}
		}
	}
	return false
}

// slugSourceName slugs a title or basename into the raw source's file-name
// fragment: internal/slug.Make, the one slug rule the whole tree shares
// (A-805) and the exact lowercase-ASCII shape the engine's validator
// accepts. "" when nothing survives; §4.1's order — title, basename,
// "untitled" — owns the fallback. Before A-805 this kept every Unicode
// letter rune, so a CJK title staged a path the validator rejected and
// the ingest died (the user's G5b quaternion clipping).
var slugSourceName = slug.Make
