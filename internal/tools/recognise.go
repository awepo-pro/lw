package tools

// recognise.go reads a recorded tool result back into what it meant (063, plan
// R5). An external agent drives ingest one `lw tool` process per call, and each
// process rebuilds the 040 read log and the 043 shrink arms from the
// changeset's session.ndjson (Registry.Replay). A session record is an
// agent.Record: it carries the result's text but no IsError, and adding the
// field would change a persisted struct and trip the format fingerprint. So the
// meaning has to be recovered from the text, and the only place that can do it
// safely is this package, which writes the text: the recognisers sit beside the
// producers' wording (the refusal constants in readlog.go and stage_page.go,
// the header format in read_raw.go), take no engine and read no file, and are
// tested by feeding each real handler's real output back in. cmd/lw never
// string-matches a tool result.

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/awepo-pro/lw/internal/stage"
)

// UnreadRefusalKey reports whether result is the refusal stage.close returns
// for an unread set (040, unreadRefusal), and if so the key that refusal is
// remembered under: the unread set it names, "<src> chunks <a, b> of <n>
// unread" per source — exactly what readLog.closeVerdict compares the next
// close's set against, so restoring it makes a repeat close over the same set
// go through. The head and tail are the producer's own constants and the key is
// what lies between them; text that merely contains the head, or a refusal with
// no clauses, is not one.
func UnreadRefusalKey(result string) (key string, ok bool) {
	rest, ok := strings.CutPrefix(result, unreadRefusalHead)
	if !ok {
		return "", false
	}
	key, ok = strings.CutSuffix(rest, unreadRefusalTail)
	if !ok || key == "" {
		return "", false
	}
	return key, true
}

// shrinkFirstLineRe is the opening line of every 043 shrink refusal
// (refuseShrink): the op, the section's heading as %q wrote it, and the ratio.
// The refusals that are not shrinks open differently — the nested-loss refusal
// "would delete its subsections", the echo-only one "stage.patch_page refused",
// the find problems with their own sentences — so this line separates them.
var shrinkFirstLineRe = regexp.MustCompile(`^(?:replace_section|replace_text) ".*" keeps [0-9]+ of [0-9]+ bytes \([0-9]+%\) of the section; it would delete:$`)

// ShrinkRefusal reports whether result is a 043 shrink refusal from
// stage.patch_page — a replace_section or replace_text that would drop more
// than half of a section of 400 bytes or more. It is true for both forms of the
// refusal: the plain one, whose last line is shrinkAdviceLine, and the one a
// call that carried allow_shrink before any refusal gets, whose last line is
// shrinkConfirmLine (043 T2) — both arm the key. It is false for the
// nested-loss refusal, the echo-only refusal, find-not-found, find-not-unique
// and every other result. The opening line and the closing line must both
// match: the deleted-paragraph lines between them quote page text, which may
// say anything.
func ShrinkRefusal(result string) bool {
	first, _, _ := strings.Cut(result, "\n")
	if !shrinkFirstLineRe.MatchString(first) {
		return false
	}
	last := result[strings.LastIndexByte(result, '\n')+1:]
	return last == shrinkAdviceLine || last == shrinkConfirmLine
}

// chunkHeaderLineRe is the header line raw.get puts ahead of a served chunk
// (chunkHeader): "chunk i of n", plus " · page N" or " · pages a-b" when the
// body carries PDF page anchors (034 T4).
var chunkHeaderLineRe = regexp.MustCompile(`^chunk [0-9]+ of [0-9]+(?: · pages? [0-9]+(?:-[0-9]+)?)?$`)

// RawGetServed reports whether result is a raw.get that served a chunk: the
// optional staged-source marker, then the header line, a blank line and the
// chunk text — whatever that text is, an empty chunk and a chunk that quotes a
// refusal included. It is false for every raw.get refusal (a source that was
// not found or was not named, a chunk out of range, arguments that do not
// parse): none of them opens with the header, and a refused call is not a read.
func RawGetServed(result string) bool {
	res := strings.TrimPrefix(result, stagedCitableMarker)
	res = strings.TrimPrefix(res, stagedSourceMarker)
	head, rest, ok := strings.Cut(res, "\n")
	return ok && chunkHeaderLineRe.MatchString(head) && strings.HasPrefix(rest, "\n")
}

// stagedPatchRe is the first line of a stage.patch_page result that staged:
// appendStageOp's "proposed <op id> (patch_page)", with any notes (050) on the
// lines after it.
var stagedPatchRe = regexp.MustCompile(`^proposed [^ ]+ \(` + regexp.QuoteMeta(string(stage.OpPatchPage)) + `\)$`)

// patchStaged reports whether result is a stage.patch_page that staged an op.
// Replay needs the success side of the shrink-arm story (043 T3: any staged
// edit of the path+section disarms the key), and a PastCall has no IsError to
// say so.
func patchStaged(result string) bool {
	first, _, _ := strings.Cut(result, "\n")
	return stagedPatchRe.MatchString(first)
}

// closeWentThrough reports whether result is the summary of a stage.close that
// got past the 040 guard (stageSummary: "changeset <id>", "intent: …", …), and
// whether that summary ends on the unread-chunks line — a close that went
// through over a non-empty unread set. A refusal, an engine failure and bad
// arguments are not summaries and report false: they never reached
// closeVerdict's pass, so they must not clear what it remembers.
func closeWentThrough(result string) (through, overUnread bool) {
	lines := strings.SplitN(result, "\n", 3)
	if len(lines) < 3 || !strings.HasPrefix(lines[0], "changeset ") || !strings.HasPrefix(lines[1], "intent: ") {
		return false, false
	}
	last := result[strings.LastIndexByte(result, '\n')+1:]
	return true, strings.HasPrefix(last, "unread chunks: ")
}

// ingestedReadHint is what a stage.ingest_source that staged names right after
// its chunk count: the raw.get call that reads the source back, with the staged
// path as a quoted JSON string ("…; read it with raw.get {"source":%q,…").
const ingestedReadHint = `; read it with raw.get {"source":`

// stagedSourcePath returns the staged raw path a stage.ingest_source result
// names, and whether result is one that staged (063, C-063-5). The path is the
// raw.get hint's own quoted string, read back the way %q wrote it, not
// scraped from the prose around it: the " named X because …" suffix and a
// source title in the path cannot confuse it. A refusal opens differently and
// carries no hint.
func stagedSourcePath(result string) (string, bool) {
	first, _, _ := strings.Cut(result, "\n")
	if !strings.HasPrefix(first, "proposed ") {
		return "", false
	}
	_, rest, ok := strings.Cut(first, ingestedReadHint)
	if !ok {
		return "", false
	}
	quoted, err := strconv.QuotedPrefix(rest)
	if err != nil {
		return "", false
	}
	path, err := strconv.Unquote(quoted)
	if err != nil || path == "" {
		return "", false
	}
	return path, true
}
