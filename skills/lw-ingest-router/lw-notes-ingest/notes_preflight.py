#!/usr/bin/env python3
"""Read-only preflight for ingesting hand-written Obsidian notes into an lw vault.

For each note it reports what `lw ingest` will see, using the same rules lw's markdown extractor uses
(internal/extract/markdown.go):

  * title    frontmatter `title:` if the frontmatter is closed, else the first line starting with "# "
             outside a ``` fence (ANYWHERE in the body), else the file name. The raw path and every
             asset name are slugged from it.
  * embeds   ![[x]], ![[x|caption]], ![alt](x), <img src="x">. lw resolves x RELATIVE TO THE NOTE'S
             FOLDER and copies only .png .jpg .jpeg .gif .svg .webp into raw/assets/. Anything else
             (not found there, other extension) stays as dead text in the immutable raw file.

It never changes a file. It prints a verdict per note, the exact safe_move.py commands that would put a
misplaced image beside the note that embeds it, and a `FIX  title: <file name>` line when lw would name
the note after a section heading (the agent adds that frontmatter line itself; GUIDE.md Step 3).

A note that is not directly in <vault>/notes/ (at the vault root, in another vault folder, or outside
the vault, e.g. a chat export in ~/Downloads) gets the verdict MOVE with the safe_move.py lines that move it,
and every image it embeds from its own folder, into <vault>/notes/ (GUIDE.md Step 2b). Moving, never
copying: the vault keeps one copy, and cleanup after ingest (Step 7) finds everything in notes/.

A name already taken in notes/ does not block the move. safe_move.py replaces a file holding the same
bytes, and gives a different file the first free `<stem>-2<ext>` name. This script plans that name the
same way: a renamed image gets a `FIX  embed:` line (the moved note must point at the new name), a
renamed note whose title would come from its file name gets `FIX  title: <original name>`.

usage: python3 -I notes_preflight.py <vault> [<note.md> ...]
       (no notes given: every .md directly in <vault>/notes/ and at the vault root, minus lw's own files)
"""
import os
import re
import sys
from urllib.parse import unquote

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)  # this guide's own folder only (python3 -I leaves it off sys.path)
from safe_move import pick_dest  # noqa: E402

MOVE_CMD = "python3 -I " + "'" + os.path.join(HERE, "safe_move.py").replace("'", "'\\''") + "'"

SUPPORTED = {".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp"}
IMAGE_LIKE = SUPPORTED | {".heic", ".heif", ".avif", ".bmp", ".tif", ".tiff"}
LW_ROOT_FILES = {"index.md", "log.md", "SCHEMA.md", "curator-memory.md", "bookkeeping.md", "wikilinks.md"}
DAY_NOTE = re.compile(r"^\d{4}-\d{2}-\d{2}\.md$")
LW_QUICK_NOTE = re.compile(r"^\d{8}-\d{6}-")
SKIP_DIRS = {".git", ".llmwiki", ".obsidian", ".trash", "raw", "wiki", "storage", "ledger"}
STUB_WORDS = 15

EMBED_WIKI = re.compile(r"!\[\[([^\]\n|]+)(?:\|[^\]\n]*)?\]\]")
EMBED_MD = re.compile(r"!\[[^\]\n]*\]\(\s*(<[^>\n]+>|[^)\s]+)")
EMBED_IMG = re.compile(r"<img\b[^>]*\bsrc\s*=\s*[\"']([^\"']+)[\"']", re.I)


def split_frontmatter(text):
    if text.startswith("---\n") or text.startswith("---\r\n"):
        lines = text.splitlines(keepends=True)
        for i in range(1, len(lines)):
            if lines[i].strip() == "---":
                return "".join(lines[1:i]), "".join(lines[i + 1:]), True
    return "", text, False


def lw_title(text, path):
    fm, _, closed = split_frontmatter(text)
    if closed:
        for line in fm.splitlines():
            if line.startswith("title:"):
                t = line[len("title:"):].strip()
                if len(t) >= 2 and t[0] in "\"'" and t[-1] == t[0]:
                    t = t[1:-1]
                if t:
                    return t, "frontmatter"
    in_fence = False
    for line in text.split("\n"):
        if line.strip().startswith("```"):
            in_fence = not in_fence
            continue
        if not in_fence and line.startswith("# "):
            return line[2:].strip(), "first '# ' heading"
    return os.path.splitext(os.path.basename(path))[0], "file name"


def first_content_line(body):
    for line in body.split("\n"):
        if line.strip():
            return line
    return ""


def strip_code(body):
    body = re.sub(r"```.*?(```|\Z)", "", body, flags=re.S)
    return re.sub(r"`[^`\n]*`", "", body)


def embeds(body):
    body = strip_code(body)
    out = []
    for rx in (EMBED_WIKI, EMBED_MD, EMBED_IMG):
        for m in rx.finditer(body):
            dest = m.group(1).strip()
            if dest.startswith("<") and dest.endswith(">"):
                dest = dest[1:-1]
            out.append(dest)
    seen, uniq = set(), []
    for d in out:
        if d not in seen:
            seen.add(d)
            uniq.append(d)
    return uniq


def is_remote(dest):
    low = dest.lower()
    return low.startswith(("http://", "https://", "data:", "mailto:", "ftp://", "//")) or dest.startswith("/")


def vault_files(vault):
    by_name = {}
    for root, dirs, files in os.walk(vault):
        rel = os.path.relpath(root, vault)
        top = rel.split(os.sep)[0]
        if top in SKIP_DIRS:
            dirs[:] = []
            continue
        dirs[:] = [d for d in dirs if not (rel == "." and d in SKIP_DIRS)]
        for f in files:
            by_name.setdefault(f, []).append(os.path.join(root, f))
    return by_name


def word_count(body):
    body = EMBED_WIKI.sub(" ", strip_code(body))
    body = EMBED_MD.sub(" ", body)
    body = re.sub(r"https?://\S+", " ", body)
    return len(re.findall(r"\w+", body))


def default_notes(vault):
    notes = []
    nd = os.path.join(vault, "notes")
    if os.path.isdir(nd):
        notes += [os.path.join(nd, f) for f in sorted(os.listdir(nd)) if f.endswith(".md")]
    for f in sorted(os.listdir(vault)):
        p = os.path.join(vault, f)
        if f.endswith(".md") and os.path.isfile(p) and f not in LW_ROOT_FILES and not DAY_NOTE.match(f):
            notes.append(p)
    return notes


def all_embedders(vault, notes_text):
    """basename -> set of folders of notes that embed it (to refuse moving a shared image)."""
    users = {}
    for path, text in notes_text.items():
        for d in embeds(split_frontmatter(text)[1]):
            if not is_remote(d):
                users.setdefault(os.path.basename(unquote(d)), set()).add(os.path.dirname(path))
    return users


def plan_move(vault, path, body, scan, problems):
    """(move lines, edit lines, note renamed?) that move a note not directly in notes/, plus the images
    it embeds from its own folder, into <vault>/notes/. A taken name is planned as safe_move.py will
    resolve it (same bytes: replaced; different: <stem>-2<ext>). Appends an ASK to problems (and returns
    nothing) when a move would break a link or an embed, or take an image another file still needs."""
    src_dir = os.path.dirname(path)
    name = os.path.basename(path)
    dst_dir = os.path.join(vault, "notes")
    inside = not os.path.relpath(path, vault).startswith("..")
    before = len(problems)
    note_dst, note_how = pick_dest(path, os.path.join(dst_dir, name))
    edits = []
    # Other markdown files that would lose something: notes in the vault, or siblings outside it.
    others = {p: t for p, t in scan.items() if p != path}
    if not inside:
        for f in sorted(os.listdir(src_dir)):
            p = os.path.join(src_dir, f)
            if f.endswith(".md") and p != path and os.path.isfile(p):
                others[p] = open(p, encoding="utf-8", errors="replace").read()
    enc = name.replace(" ", "%20")
    for p, t in others.items():
        if os.path.dirname(p) == src_dir and any(k in t for k in (f"]({name})", f"](./{name})", f"]({enc})")):
            problems.append(f"ASK  {os.path.basename(p)} links to it by path: moving would break that link")
    lines = [f"{MOVE_CMD} {q(path)} {q(note_dst)}"]
    for dest in embeds(body):
        if is_remote(dest):
            continue
        rp = unquote(dest.split("#")[0].split("?")[0])
        target = os.path.normpath(os.path.join(src_dir, rp))
        if not os.path.isfile(target):
            continue  # unresolved embeds are reported by the main loop
        if "/" in rp:
            problems.append(f"ASK  {dest!r} is a path embed: moving the note to notes/ would break it")
            continue
        img = os.path.basename(rp)
        sharers = [os.path.basename(p) for p, t in others.items()
                   if os.path.dirname(p) == src_dir and img in [os.path.basename(unquote(d)) for d in embeds(split_frontmatter(t)[1])]]
        if sharers:
            problems.append(f"ASK  {img!r} is also embedded by {', '.join(sorted(sharers))}: moving it would break that")
            continue
        img_dst, img_how = pick_dest(target, os.path.join(dst_dir, img))
        line = f"{MOVE_CMD} {q(target)} {q(img_dst)}"
        if line not in lines:
            lines.append(line)
            if img_how == "renamed":
                new = os.path.basename(img_dst)
                edits.append(f"embed: {img} -> {new}   (notes/{img} holds a different image; after the move, change "
                             f"only this note's embed of {img!r} to {new!r})")
    if len(problems) != before:
        return [], [], False
    return lines, edits, note_how == "renamed"


def q(p):
    return "'" + p.replace("'", "'\\''") + "'"


def main(argv):
    if len(argv) < 2 or argv[1] in ("-h", "--help"):
        print(__doc__)
        return 2
    vault = os.path.abspath(argv[1])
    if not os.path.isfile(os.path.join(vault, "SCHEMA.md")):
        print(f"error: {vault} is not an lw vault (no SCHEMA.md)")
        return 2
    notes = [os.path.abspath(p) for p in argv[2:]] or default_notes(vault)
    scan = {p: open(p, encoding="utf-8", errors="replace").read() for p in default_notes(vault)}
    for p in notes:
        if p not in scan:
            scan[p] = open(p, encoding="utf-8", errors="replace").read()
    users = all_embedders(vault, scan)
    by_name = vault_files(vault)
    totals = {"READY": 0, "MOVE": 0, "FIX": 0, "ASK": 0}

    for path in notes:
        text = scan[path]
        _, body, _ = split_frontmatter(text)
        rel = os.path.relpath(path, vault)
        stem = os.path.splitext(os.path.basename(path))[0]
        title, why = lw_title(text, path)
        problems, moves = [], []

        inside = not rel.startswith("..")
        relocate, relocate_edits, renamed = [], [], False
        if os.path.dirname(rel) != "notes":
            relocate, relocate_edits, renamed = plan_move(vault, path, body, scan, problems)
        # In Obsidian the file name is the title and "# " lines are sections. Only `lw note` quick notes
        # (YYYYMMDD-HHMMSS-<slug>.md) carry their real title in the first heading.
        if why == "first '# ' heading" and title != stem and not LW_QUICK_NOTE.match(stem):
            where = "first line" if first_content_line(body).startswith("# ") else "a mid-note heading"
            has_fm = text.startswith("---\n") or text.startswith("---\r\n")
            if has_fm and not split_frontmatter(text)[2]:
                problems.append(f"ASK  title will be {title!r} ({where}), not the file name {stem!r}, and the "
                                f"frontmatter is never closed, so a `title:` line would be ignored")
            else:
                how = "insert after the first '---'" if has_fm else "prepend a new ---/title/--- block"
                moves.append(f"title: {stem}   ({how}; lw would otherwise use {title!r} from {where})")
        elif renamed and why == "file name":
            has_fm = text.startswith("---\n") or text.startswith("---\r\n")
            how = "insert after the first '---'" if has_fm else "prepend a new ---/title/--- block"
            moves.append(f"title: {stem}   ({how}, after the move; notes/{stem}.md holds a different note, so this "
                         f"one gets a -N suffix and would otherwise be titled after it)")
        words = word_count(body)
        if words < STUB_WORDS:
            problems.append(f"ASK  stub: {words} words; ingest only if the user named this note")

        for dest in embeds(body):
            if is_remote(dest):
                continue
            rp = unquote(dest.split("#")[0].split("?")[0])
            ext = os.path.splitext(rp)[1].lower()
            if ext and ext not in IMAGE_LIKE:
                problems.append(f"ASK  embeds non-image {dest!r}: it stays as plain text in raw")
                continue
            if ext in IMAGE_LIKE - SUPPORTED:
                problems.append(f"ASK  {dest!r}: {ext} is not copied by lw (supported: png jpg jpeg gif svg webp); "
                                f"convert it, or accept losing it")
                continue
            target = os.path.normpath(os.path.join(os.path.dirname(path), rp))
            if os.path.isfile(target) and os.path.getsize(target) > 0:
                continue
            name = os.path.basename(rp)
            found = by_name.get(name, [])
            folders = users.get(name, set())
            if not inside:
                problems.append(f"ASK  {dest!r} is not in the note's folder {os.path.dirname(path)}: "
                                f"put it there (the note is outside the vault, so this script does not search for it)")
            elif len(found) == 1 and folders <= {os.path.dirname(path)} and "/" not in rp:
                moves.append(f"{MOVE_CMD} {q(found[0])} {q(pick_dest(found[0], os.path.join(os.path.dirname(path), name))[0])}")
            elif not found:
                problems.append(f"ASK  {dest!r} is not on this PC (not synced yet?): it would stay dead text in raw")
            elif len(found) > 1:
                problems.append(f"ASK  {dest!r} has {len(found)} copies: "
                                + ", ".join(os.path.relpath(f, vault) for f in found))
            elif "/" in rp:
                problems.append(f"ASK  {dest!r} is a path embed that does not resolve from the note's folder; "
                                f"file is at {os.path.relpath(found[0], vault)}")
            else:
                problems.append(f"ASK  {dest!r} is embedded by notes in several folders: "
                                + ", ".join(sorted(os.path.relpath(f, vault) for f in folders)))

        if relocate and not problems:
            moves = relocate + relocate_edits + moves
        verdict = "ASK" if problems else ("MOVE" if relocate else ("FIX" if moves else "READY"))
        totals[verdict] += 1
        n_img = sum(1 for d in embeds(body) if not is_remote(d))
        print(f"{verdict:5} {rel}")
        print(f"      id={stem!r}  title={title!r} ({why})  words={words}  local embeds={n_img}")
        for m in moves:
            print(f"      {'MOVE' if m in relocate else 'FIX '} {m}")
        for p in problems:
            print(f"      {p}")
    print(f"\nsummary: READY {totals['READY']}  MOVE {totals['MOVE']}  FIX {totals['FIX']}  ASK {totals['ASK']}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
