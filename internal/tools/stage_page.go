package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
)

const stageCreatePageSchema = `{
  "type":"object",
  "properties":{
    "path":{"type":"string"},"title":{"type":"string"},"type":{"type":"string"},
    "tags":{"type":"array","items":{"type":"string"}},"sources":{"type":"array","items":{"type":"string"}},
    "confidence":{"type":"string"},"contested":{"type":"boolean"},"body":{"type":"string"},"rationale":{"type":"string"}
  },
  "required":["path","title","type","tags","sources","confidence","contested","body","rationale"],
  "additionalProperties":false
}`

// stagePatchPageSchema carries a description on every property (043 T1):
// the schema text is what tells the model which op to use — replace_text for
// a small edit, replace_section for a whole-body rewrite — and what
// allow_shrink licenses, so the wording is part of the contract, not
// documentation. Since 043 T2 the allow_shrink text says the flag confirms
// a refusal rather than licensing one up front — the live model in R4 set
// it on the first call, which is exactly the reflexive use the two-step
// flow exists to surface.
const stagePatchPageSchema = `{
  "type":"object",
  "properties":{
    "path":{"type":"string","description":"Vault-relative page path, e.g. wiki/concepts/kv-cache.md."},
    "section":{"type":"string","description":"The exact heading line of the target section, e.g. \"## Related\"."},
    "op":{"type":"string","enum":["replace_text","replace_section","append_section","insert_after","insert_before","remove_section"],"description":"replace_text: replace the find text, which must occur exactly once inside the section. replace_section: replace the WHOLE section body below its heading, subsections included. append_section: add content at the end of the section. insert_after / insert_before: add content as a new section next to this one. remove_section: delete the section."},
    "find":{"type":"string","description":"replace_text only: the exact text to replace, copied from the page. It must occur exactly once inside the section."},
    "content":{"type":"string","description":"replace_text: the replacement for find. replace_section: the new body WITHOUT the heading line. append_section: the text to add. insert_after / insert_before: the new section, heading line included. remove_section: send \"\"."},
    "allow_shrink":{"type":"boolean","description":"Confirms a deletion the shrink rule refused. A replace_section or replace_text that drops more than half of a section of 400 bytes or more is refused first; repeating the call with allow_shrink true then applies it. Setting it before a refusal has no effect."},
    "rationale":{"type":"string","description":"One sentence: why this change."}
  },
  "required":["path","section","op","content","rationale"],
  "additionalProperties":false
}`

const stageRenamePageSchema = `{
  "type":"object",
  "properties":{"from":{"type":"string"},"to":{"type":"string"},"rationale":{"type":"string"}},
  "required":["from","to","rationale"],"additionalProperties":false
}`

const stageMergePagesSchema = `{
  "type":"object",
  "properties":{"sources":{"type":"array","items":{"type":"string"},"minItems":2},"into":{"type":"string"},"rationale":{"type":"string"}},
  "required":["sources","into","rationale"],"additionalProperties":false
}`

const stageSplitPageSchema = `{
  "type":"object",
  "properties":{"path":{"type":"string"},"sections":{"type":"array","items":{"type":"string"},"minItems":2},"rationale":{"type":"string"}},
  "required":["path","sections","rationale"],"additionalProperties":false
}`

type stageCreatePageArgs struct {
	Path       string   `json:"path"`
	Title      string   `json:"title"`
	Type       string   `json:"type"`
	Tags       []string `json:"tags"`
	Sources    []string `json:"sources"`
	Confidence string   `json:"confidence"`
	Contested  bool     `json:"contested"`
	Body       string   `json:"body"`
	Rationale  string   `json:"rationale"`
}

func stageCreatePageTool(d Deps) Tool {
	return Tool{Name: "stage.create_page", Description: "Propose a validated wiki page; frontmatter, taxonomy, directory and outbound-link rules are checked before staging.", Schema: json.RawMessage(stageCreatePageSchema), Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
		var a stageCreatePageArgs
		if err := decodeArgs(args, &a); err != nil {
			return badArgs("stage.create_page", err, `{"path":"wiki/concepts/new-page.md","title":"New Page"}`), nil
		}
		now := time.Now().UTC()
		created, _ := vault.ParseDate(now.Format("2006-01-02"))
		fm := vault.Frontmatter{Title: a.Title, Created: created, Updated: created, Type: vault.PageType(a.Type), Tags: a.Tags, Sources: a.Sources, Confidence: vault.Confidence(a.Confidence), Contested: a.Contested}
		page := vault.Page{Path: a.Path, FM: fm, Body: normalizeToolBody(a.Body)}
		return appendStageOp(d, "stage.create_page", stage.Op{Kind: stage.OpCreatePage, Path: a.Path, Content: page.Serialize(), Rationale: a.Rationale, Provenance: append([]string(nil), a.Sources...)})
	}}
}

type stagePatchPageArgs struct {
	Path        string `json:"path"`
	Section     string `json:"section"`
	Op          string `json:"op"`
	Find        string `json:"find"`
	Content     string `json:"content"`
	AllowShrink bool   `json:"allow_shrink"`
	Rationale   string `json:"rationale"`
}

func stagePatchPageTool(d Deps) Tool {
	// 043 T2: the pending shrink confirmations, keyed path+"\n"+section.
	// They live on the tool instance — one per registry — because a
	// confirmation is conversational state, not changeset state: it must not
	// survive a restart (a fresh process re-asks, costing one round), and it
	// must not be visible to another changeset. The mutex covers MCP, where
	// calls arrive on many goroutines.
	var (
		armMu      sync.Mutex
		shrinkArms = map[string]bool{}
	)
	return Tool{Name: "stage.patch_page", Description: "Propose a section-level page patch. For a small edit — add a link, fix a sentence — use op replace_text: it changes only the text you quote. replace_section rewrites the whole section body, subsections included. When the open changeset already stages the page, the patch composes against that staged state, not the committed bytes.", Schema: json.RawMessage(stagePatchPageSchema), Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
		var a stagePatchPageArgs
		if err := decodeArgs(args, &a); err != nil {
			return badArgs("stage.patch_page", err, `{"path":"wiki/concepts/kv-cache.md","section":"## Related","op":"append_section","content":"- [[new-page]]","rationale":"add a related page"}`), nil
		}
		if d.Vault == nil {
			return Result{IsError: true, Content: "no vault configured"}, nil
		}
		page, onFail, ok, err := stagedPatchBase(d, a.Path)
		if err != nil {
			return Result{}, fmt.Errorf("tools: stage.patch_page: %w", err)
		}
		if !ok {
			return onFail, nil
		}
		sec, ok := page.Section(a.Section)
		if !ok {
			return Result{IsError: true, Content: patchSectionNotFound(a.Section, a.Path, page)}, nil
		}
		var body string
		var armConsumed bool // 043 T2: the check consumed the key's arm…
		armKey := shrinkArmKey(a.Path, a.Section)
		switch a.Op {
		case "replace_section", "replace_text":
			armMu.Lock()
			confirmed := a.AllowShrink && shrinkArms[armKey]
			if confirmed {
				// The arm is consumed atomically with the check (043 T2r):
				// check-then-act across the mutex would let two concurrent
				// flagged calls both read the one arm and both apply.
				delete(shrinkArms, armKey)
			}
			armMu.Unlock()
			newBody, refuse, shrinkFired := guardedReplace(page, sec, a, confirmed)
			if refuse != nil {
				armMu.Lock()
				if confirmed || shrinkFired {
					// confirmed: the arm was consumed above but this
					// refusal means the shrink never landed, so it goes
					// back; shrinkFired: the refusal arms the key for its
					// repeat. A non-shrink refusal (nested loss, find
					// problems) without the flag touches nothing.
					shrinkArms[armKey] = true
				}
				armMu.Unlock()
				// The call set the flag but no refusal of this
				// path+section had armed the key (043 T2), so the last
				// line — which would have it repeat blindly — is swapped
				// for the one that says the flag only counts now.
				if shrinkFired && a.AllowShrink {
					if i := strings.LastIndexByte(refuse.Content, '\n'); i >= 0 {
						refuse.Content = refuse.Content[:i+1] + shrinkConfirmLine
					}
				}
				return *refuse, nil
			}
			body = newBody
			armConsumed = confirmed // …whether this call shrinks or not; 043 T3 decides the arm by the staging outcome below
		case "append_section":
			body = vault.AppendToSection(page.Body, sec, sectionAppendText(a.Content))
		case "insert_after":
			body = vault.InsertAfterSection(page.Body, sec, a.Content)
		case "insert_before":
			body = vault.InsertBeforeSection(page.Body, sec, a.Content)
		case "remove_section":
			body = vault.RemoveSection(page.Body, sec) // Content ignored
		default:
			return Result{IsError: true, Content: fmt.Sprintf("op %q is invalid; use replace_text, replace_section, append_section, insert_after, insert_before or remove_section", a.Op)}, nil
		}
		updated := vault.Page{Path: page.Path, FM: page.FM, Body: body}
		hunks := stage.ComputeHunks(string(page.Serialize()), string(updated.Serialize()))
		for i := range hunks {
			hunks[i].Path = a.Path
			hunks[i].Section = a.Section
		}
		res, err := appendStageOp(d, "stage.patch_page", stage.Op{Kind: stage.OpPatchPage, Path: a.Path, Section: a.Section, Before: page.SHA256(), Content: updated.Serialize(), Hunks: hunks, Rationale: a.Rationale})
		armMu.Lock()
		if err != nil || res.IsError {
			if armConsumed {
				// The check consumed the arm (043 T2r) but this call never
				// staged, so the confirmation goes back — a staging refusal
				// must not silently eat it. A call whose arm was not
				// consumed touches nothing here.
				shrinkArms[armKey] = true
			}
		} else {
			// 043 T3: ANY staged edit on path+section disarms the key —
			// every op, shrink or not, flagged or not. A confirmation must
			// directly follow its refusal: letting the arm survive a
			// successful edit would admit a much later pre-emptive
			// allow_shrink on a stale refusal.
			delete(shrinkArms, armKey)
		}
		armMu.Unlock()
		return res, err
	}}
}

// Guard constants for 043 T1. shrinkFloorBytes is the section size below
// which a whole-body rewrite is small enough that Review shows it whole —
// about two sentences — so the shrink guard stays out of the way under it.
// maxDeletedParas caps the deleted-text list a refusal carries, and
// paraCutRunes truncates each listed paragraph to a nameable line.
const (
	shrinkFloorBytes = 400
	maxDeletedParas  = 5
	paraCutRunes     = 80
)

// shrinkConfirmLine replaces a shrink refusal's last line when the
// refused call itself carried allow_shrink (043 T2): the model answered the
// schema's invitation before any refusal existed, so the reply must not
// tell it to repeat as if the flag had been missing — it must say the flag
// only counts as a confirmation after THIS refusal. The test file pins the
// same text byte for byte.
const shrinkConfirmLine = `"allow_shrink" counts only as a confirmation after this refusal. If deleting this text is intended, repeat the same call now.`

// shrinkArmKey keys a pending shrink confirmation by path and section
// (043 T2): a confirmation licenses deleting most of THAT section and
// nothing else, and the op — replace_section or replace_text — is beside
// the point, since both run the same shrink measurement.
func shrinkArmKey(path, section string) string {
	return path + "\n" + section
}

// guardedReplace computes the new page body for a replace_section or
// replace_text op behind 043 T1's two guards, returning a refusal Result
// instead of a body when one fires. confirmed is the caller's resolved
// allow_shrink (043 T2): true only when the call set the flag AND a prior
// shrink refusal of the same path+section armed the key. The third return
// reports whether the shrink guard measured a shrink at all, refusal or
// not — the caller arms the key on a fired refusal, while every other
// refusal (nested loss, find problems) leaves the key untouched. Disarming
// is not this function's call: since 043 T3 any staged edit on the key
// disarms it, shrink or not.
//
// replace_section refuses first through the nested-section guard
// (refuseNestedLoss): rewriting a section that contains subsections without
// repeating their heading lines deletes them, and neither allow_shrink nor
// an armed key overrides that — deleting a subsection has its own op,
// remove_section.
//
// Both ops then run the shrink guard (refuseShrink): a replacement keeping
// under half of a 400+-byte section is the op8 failure shape this subtask
// exists for — a live model twice sent one paragraph as the whole body of a
// four-paragraph section and silently erased the rest — so it is refused
// with the deleted text named, and the model either redoes the edit as
// replace_text, quoting only the text it means to change, or repeats the
// call with allow_shrink, which counts once the refusal armed the key
// (043 T2).
//
// replace_text replaces find — which must occur exactly once inside the
// section — byte for byte, with no newline normalization: the whole point
// of the quoted-edit shape is that nothing the model did not quote moves.
func guardedReplace(page *vault.Page, sec vault.Section, a stagePatchPageArgs, confirmed bool) (newBody string, refuse *Result, shrinkFired bool) {
	if a.Op == "replace_text" {
		if a.Find == "" {
			return "", &Result{IsError: true, Content: "replace_text needs find: the exact text to replace, copied from the page"}, false
		}
		if a.Find == a.Content {
			return "", &Result{IsError: true, Content: "find and content are identical; nothing to change"}, false
		}
		switch n := strings.Count(page.Body[sec.Body:sec.End], a.Find); {
		case n == 0:
			return "", &Result{IsError: true, Content: fmt.Sprintf("find text was not found in section %q of %s; copy it exactly from wiki.get (whitespace and punctuation included)", a.Section, a.Path)}, false
		case n > 1:
			return "", &Result{IsError: true, Content: fmt.Sprintf("find text occurs %d times in section %q of %s; include more surrounding text so it matches exactly once", n, a.Section, a.Path)}, false
		}
		newBody, _ := vault.ReplaceTextInSection(page.Body, sec, a.Find, a.Content)
		if res := refuseShrink(page.Body, sec, strings.Replace(page.Body[sec.Body:sec.End], a.Find, a.Content, 1), a); res != nil {
			if confirmed {
				return newBody, nil, true
			}
			return "", res, true
		}
		return newBody, nil, false
	}
	if res := refuseNestedLoss(page, sec, normalizeToolBody(a.Content)); res != nil {
		return "", res, false
	}
	newRegion := normalizeToolBody(a.Content)
	if res := refuseShrink(page.Body, sec, newRegion, a); res != nil {
		if confirmed {
			return vault.ReplaceSection(page.Body, sec, newRegion), nil, true
		}
		return "", res, true
	}
	return vault.ReplaceSection(page.Body, sec, newRegion), nil, false
}

// refuseNestedLoss is replace_section's nested-section guard (043 T1): a
// nested heading is any heading whose Start lies strictly inside
// (sec.Start, sec.End), and every one of them must survive as a line of the
// new content — compared after TrimRight " \t", the only normalization a
// heading line ever carries (buildSection). The session-35 H1 shape —
// replace the whole page with its intro paragraph — took every subsection
// with it; this refusal names each heading it would delete, in document
// order, and points at either the targeted subsection call or the
// keep-the-headings rewrite. It runs before refuseShrink and ignores
// allow_shrink: a lost subsection is structural damage no byte count
// licenses.
func refuseNestedLoss(page *vault.Page, sec vault.Section, newBody string) *Result {
	var missing []string
	for _, s := range page.Sections {
		if s.Start <= sec.Start || s.Start >= sec.End {
			continue
		}
		if !hasTrimmedLine(newBody, s.Heading) {
			missing = append(missing, fmt.Sprintf("%q", s.Heading))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &Result{IsError: true, Content: fmt.Sprintf("replace_section %q would delete its subsections %s; target the subsection you mean, or keep every subsection heading line in content.", sec.Heading, strings.Join(missing, ", "))}
}

// hasTrimmedLine reports whether line appears in s as a line of its own,
// with trailing spaces and tabs trimmed — the only normalization
// buildSection applies to a heading line, so the only one the comparison
// may apply. It is a whole-line comparison: a heading that occurs only as
// the substring of a longer line does not count as kept.
func hasTrimmedLine(s, line string) bool {
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimRight(l, " \t") == line {
			return true
		}
	}
	return false
}

// refuseShrink is 043 T1's shrink guard, shared by replace_section and
// replace_text: when the section body before is 400+ bytes and the body
// after would keep under half of it — op8 kept 19% — the replacement is
// returned as a refusal. Since 043 T2 this is a pure measurement: whether
// the shrink may land is the caller's call, resolved from the armed
// confirmation, so an allow_shrink that arrives before any refusal changes
// nothing here. A legitimate condense seldom halves a section without
// meaning to, so the refusal names the ratio and lists every old paragraph
// (a blank-line-separated block, trimmed) the new body no longer contains,
// at most maxDeletedParas of them, each cut to paraCutRunes runes: the
// model sees exactly what it was about to erase. newRegion is the section
// body after the edit — the replacement content for replace_section, the
// section with find swapped for content for replace_text.
func refuseShrink(body string, sec vault.Section, newRegion string, a stagePatchPageArgs) *Result {
	old := strings.TrimSpace(body[sec.Body:sec.End])
	new := strings.TrimSpace(newRegion)
	if len(old) < shrinkFloorBytes || 2*len(new) >= len(old) {
		return nil
	}
	var entries []string
	more := 0
	for _, para := range sectionParagraphs(old) {
		if strings.Contains(new, para) {
			continue
		}
		if len(entries) < maxDeletedParas {
			entries = append(entries, fmt.Sprintf("- %q", cutRunes(para, paraCutRunes)))
		} else {
			more++
		}
	}
	msg := fmt.Sprintf("%s %q keeps %d of %d bytes (%d%%) of the section; it would delete:", a.Op, sec.Heading, len(new), len(old), len(new)*100/len(old))
	if len(entries) > 0 {
		msg += "\n" + strings.Join(entries, "\n")
	}
	if more > 0 {
		msg += fmt.Sprintf("\n- … and %d more", more)
	}
	return &Result{IsError: true, Content: msg + "\nFor a small edit (a link, a sentence) use op replace_text. If deleting this text is intended, repeat the call with \"allow_shrink\": true."}
}

// sectionParagraphs splits s into its blank-line-separated blocks, each the
// paragraph unit the shrink refusal lists deleted text in. s arrives
// TrimSpace'd, so no block is empty; each block is trimmed itself — the
// contract's "blank-line-separated block of old, trimmed" — so an indented
// first or last line is compared by the block's text, not its indentation.
func sectionParagraphs(s string) []string {
	var paras []string
	var cur []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			if len(cur) > 0 {
				paras = append(paras, strings.TrimSpace(strings.Join(cur, "\n")))
				cur = nil
			}
			continue
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		paras = append(paras, strings.TrimSpace(strings.Join(cur, "\n")))
	}
	return paras
}

// cutRunes shortens s to at most n runes, appending "…" only when it cut.
func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// frontmatterSectionClause follows a patch refusal's heading list when the
// section asked for was the frontmatter or the text above the first heading.
// Neither is a section — buildSection opens one at each heading line — so
// no spelling of the argument can ever reach them, and listing the headings
// alone sends the model round the same loop (live: "frontmatter", "---",
// "preamble" and "" were each tried). The clause names the two ways out: a
// replace_text on the title heading for text under the page title, and
// stage.create_page or the user for frontmatter, which no patch op writes.
const frontmatterSectionClause = `. Frontmatter and the text above the first heading are not sections: to change text under the page title use op replace_text on the title heading (e.g. "# Title"); frontmatter changes go through stage.create_page on a new page or the user`

// patchSectionNotFound is stage.patch_page's refusal for a section its page
// does not have (036 D2). It is wiki.get's message — the section as given,
// then the page's real headings — computed from the same page the patch
// would have run against, so a page whose staged state dropped a section
// lists the staged headings, not the committed ones. A page without any
// heading says "(none)" rather than ending on a bare "are: ". A section
// argument that is, trimmed and lowercased, "frontmatter", "---",
// "preamble" or empty gains frontmatterSectionClause.
func patchSectionNotFound(section, pagePath string, p *vault.Page) string {
	list := "(none)"
	if heads := sectionHeadings(p); len(heads) > 0 {
		list = strings.Join(heads, ", ")
	}
	msg := fmt.Sprintf("section %q was not found on %s; valid headings are: %s", section, pagePath, list)
	switch strings.ToLower(strings.TrimSpace(section)) {
	case "frontmatter", "---", "preamble", "":
		msg += frontmatterSectionClause
	}
	return msg
}

// stagedPatchBase resolves the base page stage.patch_page computes against
// (020 T-B): when the currently open changeset already holds a live
// content op for path, its staged bytes are the base — the section lookup,
// the old side of the hunks and Before all read from them, so a second
// patch on a staged page composes with the first (its Before is the staged
// sha) instead of proposing against content the engine has already moved
// past. Only when nothing staged targets path does the committed vault
// answer, which keeps every changeset without a prior op on the path on
// byte-for-byte its pre-T-B path. Staged bytes parse through
// vault.ParsePage — the same parser a committed page goes through — so the
// sha the tool proposes is the sha the engine's own projection recomputes.
//
// A staged parse failure for a path the committed vault carries as a Page
// is an internal-invariant violation returned as err — the turn aborts,
// mirroring rawSourceBody (020 FIX-1): Append only stages bytes it
// validated, so unparseable staged content for a committed page means the
// changeset or the CAS is damaged, and editing on top of it would hide the
// damage. A cascade sub-op may also target a VAULT-ROOT file
// (curator-memory.md, index.md), whose staged bytes legitimately are not
// page-structured (op.go's buildCascadeOp raw branch): for such a path —
// or any path the committed vault does not carry as a Page — the staged
// bytes are not consumed, and the committed lookup below answers, whose
// miss is the not-found IsError. ok is false with onFail set when
// path resolves through neither the changeset nor the vault.
//
// 036 D1: that not-found message names up to three pages that resemble path
// ("; closest pages: a, b") — committed ones and ones the open changeset
// creates — because the live miss was the right name under the wrong
// directory (wiki/concepts/tilelang.md for wiki/entities/tilelang.md). A path
// nothing resembles keeps the bare message.
func stagedPatchBase(d Deps, path string) (page *vault.Page, onFail Result, ok bool, err error) {
	if d.Engine != nil {
		b, staged, err := d.Engine.StagedFile(path)
		if err != nil {
			return nil, Result{}, false, err
		}
		if staged {
			p, perr := vault.ParsePage(path, b)
			if perr == nil {
				return p, Result{}, true, nil
			}
			if _, isPage := d.Vault.Page(path); isPage {
				// A committed page's staged bytes should parse: failing
				// that is damage, not input the model gets to argue with.
				return nil, Result{}, false, fmt.Errorf("parse staged page %s: %w", path, perr)
			}
			// Root-file cascade bytes (or an unknown path): keep the
			// committed lookup below.
		}
	}
	p, found := d.Vault.Page(path)
	if !found {
		return nil, Result{IsError: true, Content: fmt.Sprintf("page %q was not found%s", path, closestPagesClause(d, path))}, false, nil
	}
	return p, Result{}, true, nil
}

type stageRenamePageArgs struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Rationale string `json:"rationale"`
}

func stageRenamePageTool(d Deps) Tool {
	return Tool{Name: "stage.rename_page", Description: "Propose a page rename. The engine computes every inbound backlink cascade from the graph.", Schema: json.RawMessage(stageRenamePageSchema), Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
		var a stageRenamePageArgs
		if err := decodeArgs(args, &a); err != nil {
			return badArgs("stage.rename_page", err, `{"from":"wiki/concepts/old-page.md","to":"wiki/concepts/new-page.md","rationale":"canonical name"}`), nil
		}
		return appendStageOp(d, "stage.rename_page", stage.Op{Kind: stage.OpRenamePage, From: a.From, To: a.To, Rationale: a.Rationale})
	}}
}

type stageMergePagesArgs struct {
	Sources   []string `json:"sources"`
	Into      string   `json:"into"`
	Rationale string   `json:"rationale"`
}

func stageMergePagesTool(d Deps) Tool {
	return Tool{Name: "stage.merge_pages", Description: "Propose merging source pages into one destination; backlink rewrites are computed by the engine.", Schema: json.RawMessage(stageMergePagesSchema), Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
		var a stageMergePagesArgs
		if err := decodeArgs(args, &a); err != nil {
			return badArgs("stage.merge_pages", err, `{"sources":["wiki/concepts/a.md","wiki/concepts/b.md"],"into":"wiki/concepts/merged.md","rationale":"combine duplicates"}`), nil
		}
		return appendStageOp(d, "stage.merge_pages", stage.Op{Kind: stage.OpMergePages, Sources: a.Sources, To: a.Into, Rationale: a.Rationale})
	}}
}

type stageSplitPageArgs struct {
	Path      string   `json:"path"`
	Sections  []string `json:"sections"`
	Rationale string   `json:"rationale"`
}

func stageSplitPageTool(d Deps) Tool {
	return Tool{Name: "stage.split_page", Description: "Propose splitting a page into the listed resulting page paths; the source becomes a reviewable stub.", Schema: json.RawMessage(stageSplitPageSchema), Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
		var a stageSplitPageArgs
		if err := decodeArgs(args, &a); err != nil {
			return badArgs("stage.split_page", err, `{"path":"wiki/concepts/old-page.md","sections":["wiki/concepts/part-a.md","wiki/concepts/part-b.md"],"rationale":"separate concerns"}`), nil
		}
		return appendStageOp(d, "stage.split_page", stage.Op{Kind: stage.OpSplitPage, Path: a.Path, Sources: a.Sections, Rationale: a.Rationale})
	}}
}

func normalizeToolBody(body string) string {
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return ""
	}
	return body + "\n"
}

func sectionAppendText(content string) string {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return ""
	}
	// AppendToSection is a byte-offset primitive. Its caller must supply the
	// blank-line separator that keeps a middle section distinct from the next
	// heading (backbone §2.4 / 00-conventions §5.6). The section's existing
	// content already ends at a line boundary, so only the trailing blank line
	// belongs in the inserted text.
	return content + "\n\n"
}

func pageBasename(p string) string {
	return strings.TrimSuffix(path.Base(p), ".md")
}
