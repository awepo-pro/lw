package main

// ingest_recompile.go holds what `lw ingest --recompile` needs beyond
// cmdIngest's own flow (055): telling a vault path from a file, the
// recompile target and the message that describes it to the agent, and the
// "is this raw cited" question the A-807 skip line asks too.
//
// Why it exists: a source already in the vault is skipped before any turn
// (A-807), which was right for a source whose pages exist and wrong for one
// whose first ingest staged raw only. Three HTTP articles re-ingested on
// 2026-10-08 each printed "skipped …: already in the vault" while the raws
// they landed at were cited by 0 pages; no verb could turn a committed raw
// into pages. A recompile target is such a raw handed to the turn as it is —
// no stage.ingest_source, no new raw file, raw stays immutable.

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/awepo-pro/lw/internal/cite"
	"github.com/awepo-pro/lw/internal/vault"
)

// recompileHead is the turn message of a recompile-only invocation, up to its
// item blocks (055 D3, frozen byte for byte). It asks for what buildIngestMessage
// asks for — every chunk read, only related pages read, pages written as soon
// as they are ready — minus stage.ingest_source, which has no work to do for a
// raw the vault already holds, and plus a nudge toward patching: a raw that
// pages already cite is better served by their patches than by a duplicate.
const recompileHead = "These sources are already in the vault as committed raw files; compile wiki pages from them. The changeset for this work is already open, so do not call stage.open, and do not call stage.ingest_source — each source already has the raw/ path given below. For each source: read it with raw.get, then create or update wiki pages that faithfully reflect it, following the schema and citing that raw path. Read every chunk of each source, requesting several raw.get chunks in one response. Read only the existing pages that wiki.search shows are related to the source — do not survey the whole wiki; when pages already cite a source, patching them usually serves it better than a new page. Write pages as soon as you have what they need, and stage several pages or patches in one response when they do not depend on each other. When you are done, call stage.close to summarize the proposed changeset.\n\n"

// recompileTail follows buildIngestMessage's own text when one invocation
// carries both new sources and recompile targets (055 D3, frozen): the ingest
// paragraph already governs the new sources, so this only says what differs
// for the committed ones.
const recompileTail = "\nThese sources are already in the vault as committed raw files: do not call stage.ingest_source for them. Read each with raw.get and create or update wiki pages from it, citing its raw/ path, under the same rules.\n\n"

// citedByMax is how many citing pages an item block lists before it counts
// the rest.
const citedByMax = 5

// recompileTarget is one committed raw source a recompile turn writes pages
// from. arg is the command-line argument that led to it; byContent says that
// argument was a file whose body hashed to this raw, not the vault path itself.
type recompileTarget struct {
	raw       string // vault-relative path of the committed raw
	arg       string
	byContent bool
	title     string // the raw's Title ("" when it has none)
	chunks    int    // what raw.get reports for the committed body
}

// announce is the line printed on stdout before the turn.
func (t recompileTarget) announce() string {
	if t.byContent {
		return fmt.Sprintf("recompiling %s (same content as %s)", t.raw, t.arg)
	}
	return "recompiling " + t.raw
}

// recompileVaultPath reports whether arg reads as a vault path — a raw in the
// vault rather than a file to read — and the vault-relative slash path it
// names. After filepath.Clean it does when it is relative and starts with
// "raw/", or when it resolves (Abs, then Rel to root) to a path that does. A
// URL never does: a relative "https:/…" resolved under a working directory
// that happens to be the vault's raw/ would otherwise look like one. Whether
// the path names a COMMITTED raw is the caller's question, and what follows
// from the answer depends on --recompile (cmdIngest).
func recompileVaultPath(root, arg string) (string, bool) {
	if isURLSource(arg) {
		return "", false
	}
	if clean := filepath.ToSlash(filepath.Clean(arg)); strings.HasPrefix(clean, "raw/") {
		return clean, true
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "raw/") {
		return rel, true
	}
	return "", false
}

// rawCitations maps each raw path some committed wiki page cites to the paths
// of the pages that do, sorted. A page cites a raw when its sources: list has
// the path or its body has a provenance marker (cite.Scan: code spans and
// fences excluded) whose Source is the path, whatever page suffix the marker
// carries. One scan serves D3's "cited by" lines and D4's skip hint, so the
// two can never disagree about whether a raw is cited.
func rawCitations(v *vault.Vault) map[string][]string {
	sets := make(map[string]map[string]bool)
	note := func(raw, page string) {
		if !strings.HasPrefix(raw, "raw/") {
			return
		}
		if sets[raw] == nil {
			sets[raw] = make(map[string]bool)
		}
		sets[raw][page] = true
	}
	for _, p := range v.Pages() {
		for _, s := range p.FM.Sources {
			note(s, p.Path)
		}
		for _, c := range cite.Scan(p.Body) {
			note(c.Source, p.Path)
		}
	}
	out := make(map[string][]string, len(sets))
	for raw, set := range sets {
		out[raw] = slices.Sorted(maps.Keys(set))
	}
	return out
}

// citedByText renders the "cited by" value of an item block: "no page", or the
// first citedByMax page paths (already sorted) joined ", " with " and N more"
// for the rest.
func citedByText(pages []string) string {
	if len(pages) == 0 {
		return "no page"
	}
	if len(pages) <= citedByMax {
		return strings.Join(pages, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(pages[:citedByMax], ", "), len(pages)-citedByMax)
}

// shellQuoteArg renders arg for pasting into a shell: unchanged when every byte
// is in [A-Za-z0-9_./:@%+=,-], else in single quotes with each embedded single
// quote closed, escaped and reopened (quote, backslash, quote, quote).
// The empty string is quoted too — unquoted it would vanish from the command.
func shellQuoteArg(arg string) string {
	safe := arg != ""
	for i := 0; i < len(arg) && safe; i++ {
		c := arg[i]
		safe = c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("_./:@%+=,-", c) >= 0
	}
	if safe {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// alreadyInVaultLine is the stdout line for a source whose body the vault
// already holds at rawPath and that no --recompile asked to compile (A-807).
// A raw some page cites keeps the line exactly as it always was. One no page
// cites — a first ingest that staged raw only, as in the 2026-10-08 failure —
// also names the command that writes its pages, with src quoted for a shell.
func alreadyInVaultLine(src, rawPath string, cited bool) string {
	line := fmt.Sprintf("skipped %s: already in the vault at %s", src, rawPath)
	if cited {
		return line
	}
	return line + ", cited by no page — run lw ingest --recompile " + shellQuoteArg(src) + " to write pages from it"
}

// recompilePaths is the vault paths of targets, in order — what the agent's
// registry is told to hold to the read-every-chunk rule. nil for none, so an
// ordinary ingest builds its agent exactly as before 055.
func recompilePaths(targets []recompileTarget) []string {
	if len(targets) == 0 {
		return nil
	}
	out := make([]string, len(targets))
	for i, t := range targets {
		out[i] = t.raw
	}
	return out
}

// recompileIntent is the changeset intent of an invocation with at least one
// target: "recompile <raws>" alone, or "ingest <new sources>; recompile
// <raws>" when new sources ride along. An invocation with no target keeps
// ingestIntent untouched.
func recompileIntent(newSources []string, targets []recompileTarget) string {
	raws := "recompile " + strings.Join(recompilePaths(targets), ", ")
	if len(newSources) == 0 {
		return raws
	}
	return ingestIntent(newSources) + "; " + raws
}

// buildRecompileMessage is the first user message of an invocation with at
// least one target (055 D3). Recompile-only: recompileHead, then one item
// block per target. Mixed: buildIngestMessage's text for the new sources,
// unchanged, then recompileTail, then the same blocks. cites is rawCitations
// of the vault. Each block gives the path to read, how many chunks raw.get
// will serve, the raw's title and who already cites it — what the model would
// otherwise spend rounds finding out.
func buildRecompileMessage(items []ingestItem, targets []recompileTarget, cites map[string][]string) string {
	var b strings.Builder
	if len(items) == 0 {
		b.WriteString(recompileHead)
	} else {
		b.WriteString(buildIngestMessage(items))
		b.WriteString(recompileTail)
	}
	for _, t := range targets {
		fmt.Fprintf(&b, "- raw: %s\n  chunks: %d\n  title: %s\n  cited by: %s\n", t.raw, t.chunks, t.title, citedByText(cites[t.raw]))
	}
	return b.String()
}
