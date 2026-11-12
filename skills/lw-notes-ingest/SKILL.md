---
name: lw-notes-ingest
description: >-
  Ingest notes the user wrote by hand in Obsidian (with pasted images) from their lw (llmwiki) vault's notes/ folder
  into the wiki, with YOU as the curator. Before ingesting, it puts each pasted image beside the note that embeds it,
  so `lw ingest` copies every image into raw/assets/ instead of leaving a dead embed in the immutable raw file, and it
  catches wrong titles and stub notes. Use when the user says "ingest my notes", "ingest my Obsidian notes", "compile
  my notes with images", "ingest note X", "ingest <one or more paths to .md files in the vault>", or "my notes have
  images, ingest them". Works in
  any agent with a shell (Claude Code, Codex, Antigravity).
---

# lw-notes-ingest: hand-written Obsidian notes (with images) into the wiki

The user writes notes in Obsidian and pastes screenshots into them. Obsidian saves each paste as
`Pasted image <timestamp>.png` and embeds it as `![[Pasted image <timestamp>.png]]`. lw already handles these:
`lw ingest` copies every embedded image into **`raw/assets/<note-slug>-<image-slug>.png`** (the images folder inside
`raw/`), rewrites the embed to `![…](../../raw/assets/…)`, and stages it all in one reviewable changeset.

**You never move or copy anything into `raw/` yourself.** `raw/` is immutable and only lw writes it, inside the
review gate. Your job is the part lw cannot do: make sure each note is in a state where lw's copy works, then curate.

lw's extractor has two silent traps that this skill exists to catch:

1. **It looks for an image only in the note's own folder.** Obsidian finds `![[x.png]]` anywhere in the vault, so a
   note can look fine in Obsidian while its image is in another folder. lw then leaves `![[x.png]]` as dead text in the
   raw file, copies nothing, and the raw file can never be edited. Re-ingesting the same note text is refused as a
   duplicate, so the image is lost for good. Only `.png .jpg .jpeg .gif .svg .webp` are copied; `.heic .avif .bmp`
   are dropped the same way.
2. **The title is the first `# ` line anywhere in the note**, unless the frontmatter has a `title:`. In Obsidian the
   file name is the title and `#` lines are sections, so `footprint smb.md` whose first heading is `# tools` becomes
   `raw/notes/tools.md` with assets named `tools-pasted-image-….png`.

## Rules you must not break

1. **Never write under `raw/`, `wiki/`, `.llmwiki/`, `storage/`, `index.md`, `log.md`, `SCHEMA.md`.** Every wiki change
   is a `lw tool stage.*` call.
2. **The only file changes you may make are:** `mv -n` of an image into the folder of the note that embeds it (the
   exact commands the preflight prints as `FIX`), `mv -n` of a root note into `notes/` (as in the `lw-notes-sync`
   skill's Step 2a), and, **only after the user agrees**, adding one `title:` line to a note's frontmatter. Never fix
   typos, rename files, rewrite links or delete anything.
3. **Never ingest a note the preflight marks `ASK` or `FIX`.** Fix it or get the user's decision first. A bad ingest
   cannot be repaired by re-ingesting.
4. **Never run `lw commit`, `lw sync`, `lw revert`, `lw doctor --discard-changeset`, or `lw config set`.** The human
   reviews and commits.
5. **At most 10 notes per ingest** (lw's limit). More than that: batches, as in Step 2.

## Step 0: find lw, the vault, and this skill's folder (once)

```bash
for c in "$(command -v lw)" "$HOME/go/bin/lw" "$HOME/.local/bin/lw" /usr/local/bin/lw; do
  [ -x "$c" ] && echo "$(readlink -f "$c"): $("$c" version)"; done; true
```

Use the newest one; it must be `lw v2.61.0` or later (`--note`, `note list --pending`, image assets). Below, `LW` is its
**absolute path**. Then:

```bash
LW config | grep -E '^vault\.path'
```

`V` below is that path written out absolutely (or the vault the user named; then pass `--vault V` to every `LW`
command). `SKILL_DIR` is the folder holding this SKILL.md (resolve the symlink: `readlink -f` of it), where
`notes_preflight.py` lives.

## Step 1: make sure no other changeset is open

```bash
LW status --vault V
```

If it prints `open changeset cs-…` you did not open in this session, **stop and ask**. `lw ingest --prepare` joins an
open changeset, so your pages would be reviewed and committed together with someone else's unrelated work. Tell the
user what it is (the text after the id) and the two ways out: review and commit it first (`lw tui` → Review), or
discard it themselves (`lw doctor --discard-changeset`). Do not choose for them.

## Step 2: pick the notes

Build one list of **absolute note paths**. Everything below works on paths: `lw ingest --prepare <path>…` stores a
note under `raw/notes/` and clears it from `--pending` exactly like `--note <id>` does, while `--note` rejects a path.

- **The user gave paths** (`ingest /home/…/notes/footprint FTP.md, /home/…/notes/footprint nfs.md, …`): split on commas
  and new lines, not on spaces (file names contain spaces), expand `~`, and check each exists with `ls -l "<path>"`.
  A path that does not exist: ask, do not guess a nearby name. A `…` or "and the rest" in the request: ask which notes.
- **The user gave names** ("footprint FTP and DoS"): the path is `V/notes/<name>.md`; check it exists. Use
  `LW note list --vault V` to find the exact name if it does not; more than one candidate: ask.
- **"All my notes" / "my pending notes":** `LW note list --pending --vault V` prints one note per line as
  `<id>  <age>  <first line>`; the path is `V/notes/<id>.md`.
- A note at the vault root or in another vault folder: move it into `notes/` with the `lw-notes-sync` skill's Step 2a
  rules (same name, `mv -n`, not if a markdown link points at it), then use its new path.
- A note **outside the vault** (e.g. `~/Downloads/x.md`): it can be ingested by path too, but its images must sit in
  its own folder; never move files into or out of a folder outside the vault without asking.

More than 10 notes: split them into batches of at most 10 and run Steps 3 to 5 once per batch. Tell the user they get
one changeset per batch and should commit each one before you start the next (Step 1 stops on an open changeset).

## Step 3: preflight (read-only)

```bash
python3 -I SKILL_DIR/notes_preflight.py V "V/notes/<note 1>.md" "V/notes/<note 2>.md" ...
```

(No note arguments = every note in `notes/` and at the root.) Each note gets a verdict:

| Verdict / line | Meaning | Do |
|---|---|---|
| `READY` | every local embed resolves from the note's folder to a supported image, the title is right | ingest it |
| `FIX  mv -n …` | an image is elsewhere in the vault, unique by name, and only notes in this folder embed it | run that exact `mv -n` line, then rerun the preflight |
| `ASK  title will be …` | lw would name the raw file and its assets after a section heading | ask the user (once, for all such notes): add `title: <file name>` to the frontmatter? |
| `ASK  stub: N words` | a near-empty note (`Testing`, `dlsite`) | skip it unless the user named it; a stub makes no wiki page worth reviewing |
| `ASK  … not on this PC` | the image is still on the device that pasted it | skip the note until it syncs (`lw sync`, or the `lw-notes-sync` skill on that device) |
| `ASK  … .heic` (etc.) | lw does not copy that format | ask: the user converts it to PNG/JPG in place, or accepts losing that image |
| `ASK  … several folders` / `copies` / path embed / non-image | moving would break another note, or lw cannot resolve it | report it; skip the note |

**Adding the title, after the user agrees.** If the note starts with a `---` frontmatter block, insert
`title: <file name without .md>` as the line right after the first `---`. If it has no frontmatter, prepend:

```
---
title: <file name without .md>
---
```

Change nothing else. Then rerun the preflight: the line must read `title='<file name>' (frontmatter)`. (Tell the user
this edits the note; if the phone edits the same note before the next `lw sync`, that note can come back as a sync
conflict.)

Do not continue until every note you are about to ingest is `READY`.

## Step 4: prepare

```bash
LW ingest --prepare --agent <you>:<model> --vault V "V/notes/<note 1>.md" "V/notes/<note 2>.md" ...
```

Pass exactly the paths that came out of Step 3 as `READY`, each in double quotes (they contain spaces). Pass any
extra wording from the user verbatim with `--instruction "…"`. A `skipped …: already in the vault` line means that
exact note text was ingested before; report it and move on.

Then **verify the images made it**, for every raw the brief lists under *Sources to compile*:

```bash
LW tool raw.get '{"source":"raw/notes/<slug>.md","chunk":1}' | grep -nE '!\[\[|<img' ; true
LW tool raw.get '{"source":"raw/notes/<slug>.md","chunk":1}' | grep -c 'raw/assets/'
```

The first must print nothing (no Obsidian embed survived), and the second must count at least the note's
`local embeds` from the preflight (an image embedded twice appears twice). A raw slug that is not the note's file name
slugged means the title step was skipped. On any mismatch: **stop**, close nothing, and tell the user the changeset id
and what is wrong; they discard it with `lw doctor --discard-changeset`.

## Step 5: curate, close, report

Follow the **`lw-ingest` skill from its Step 2 to its Step 5** (read every chunk, search the wiki, stage pages,
`stage.close`, report). Its SKILL.md is next to this one: `SKILL_DIR/../lw-ingest/SKILL.md`; read it now if it is not
already loaded. Two extras for notes:

- Keep the relevant `![alt](../../raw/assets/<name>)` image links in the pages you stage. A screenshot of a command's
  output is often the only record of it: describe what it shows in the text too, so the page is useful without it.
- A hand-written note is one person's learning log, not a reference. Set `confidence` to match (`low` or `medium`
  unless the note cites a source), and do not present guesses in the note as settled fact.

Add to the `lw-ingest` report:

- **Moved:** each `mv -n` you ran (`from -> to`), and each `title:` you added.
- **Skipped:** each note left out and why (stub, image not on this PC, unsupported format, open question).
- **Images:** how many images went to `raw/assets/` per note. The originals stay in `notes/` beside their note
  (the note still embeds them); nothing was deleted.
