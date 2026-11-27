> Part of the **lw-ingest-router** skill (`../SKILL.md` decides when this guide applies). Route: the
> user's own notes; this guide commits by itself and tidies the originals away.

# lw-notes-ingest: hand-written Obsidian notes (with images) into the wiki

The user writes notes in Obsidian and pastes screenshots into them. Obsidian saves each paste as
`Pasted image <timestamp>.png` and embeds it as `![[Pasted image <timestamp>.png]]`. lw already handles these:
`lw ingest` copies every embedded image into **`raw/assets/<note-slug>-<image-slug>.png`** (the images folder inside
`raw/`), rewrites the embed to `![…](../../raw/assets/…)`, and stages it all in one reviewable changeset.

**You never move or copy anything into `raw/` yourself.** `raw/` is immutable and only lw writes it, inside the
changeset. Your job is the part lw cannot do: make sure each note is in a state where lw's copy works, curate the
wiki pages, commit, and then clear the originals out of `notes/` so the vault does not hold every note twice.

The whole run, end to end:

1. **Move in** (Step 2b): a note the user named that is not in `notes/` yet, from anywhere, is moved there with its
   images, never copied. An `.html` file is instead stored as it is with `lw storage add` and its original moved to
   `V/.trash/` (Step 2c).
   A chat export the note links to stays where it is: it is the note's **companion** (Step 2d), not a note.
2. **Preflight** (Step 3): fix images and titles so lw's copy is complete.
3. **Prepare** (Step 4): companions first, into `raw/articles/`; then a link to each one goes into the note beside
   its chat URL; then `lw ingest --prepare` copies each note to `raw/notes/` and its images to `raw/assets/`, in the
   same changeset.
4. **Curate** (Step 5): stage the wiki pages and close the changeset.
5. **Commit** (Step 6): `lw commit`. From here the raw copies are permanent.
6. **Clean up** (Step 7): move each ingested note, and the images only it embeds, from `notes/` to `V/.trash/`.
7. **Summarize** (Step 8): write one HTML revision page, one section per topic.
8. **Report** (Step 9).

lw's extractor has two silent traps that this guide exists to catch:

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
   is a `lw tool stage.*` call. The one exception is `storage/`, and only through `LW storage add` (Step 2c).
2. **The only file changes you may make are:** moving an image into the folder of the note that embeds it (the
   exact commands the preflight prints as `FIX`), moving a note the user named and its images into `notes/` (the
   exact commands the preflight prints as `MOVE`, Step 2b; never `cp`), adding the one `title:` line the preflight
   prints as `FIX  title: …`, changing the one embed the preflight prints as `FIX  embed: <old> -> <new>`,
   `LW storage add` of an `.html` file the user named followed by moving its original into `V/.trash/` (Step 2c),
   appending ` ([[<raw path without .md>|saved chat]])` right after a companion's chat URL in the note (Step 4b),
   and, in Step 7 only, moving a companion's original into `V/.trash/`, and moving an ingested note and its images into `V/.trash/`. **Every move is a `SKILL_DIR/safe_move.py`
   call** (see *How files are moved*), never `mv`, `cp` or `rm`. Outside the vault you write only the summary file
   (Step 8). Never fix typos, rename files yourself, or rewrite other links: a cleanup goes to `.trash/` (Obsidian's
   own trash folder, which lw sync git-ignores), where the user can still recover it.
3. **Never ingest a note the preflight marks `ASK` or `FIX`.** Fix it or get the user's decision first. A bad ingest
   cannot be repaired by re-ingesting.
4. **Run `lw commit` only in Step 6, only on the changeset you opened in this run, and never with `--force`.** Never
   run `lw sync`, `lw revert`, `lw doctor --discard-changeset`, or `lw config set`. This overrides the `lw-ingest`
   guide's "do not commit": the user chose to have notes ingest commit by itself.
5. **At most 10 notes per ingest** (lw's limit). More than that: batches, as in Step 2.
6. **Clean up only what Step 7 verified.** A note leaves `notes/` only after its own committed raw copy has been
   checked. A failed or refused commit, a skipped note, a note something else links to, or any doubt: it stays.

## How files are moved

Every move in this guide is one call of the helper next to this file:

```bash
python3 -I SKILL_DIR/safe_move.py '<from>' '<to file or folder/>'
```

| At the destination | What it does | Prints |
|---|---|---|
| nothing | moves the file | `moved` |
| a file with the **same bytes** | moves over it: nothing is lost, and no duplicate is left behind | `replaced … (an identical file was there)` |
| a file with **different bytes** (same name only) | keeps that file, moves this one to `<stem>-2<ext>` (`-3`, … the first free name, or one already holding the same bytes) | `renamed … (<name> there holds different content)` |

The source is always gone afterwards, and a different file is never overwritten. Its last line is
`FINAL<TAB><path>`: the file's real new path. Use that path from then on, not the one you asked for. A non-zero exit
means nothing moved: report it and leave that file. The preflight plans the same names (it calls the same code), so
a `MOVE` line's destination normally already carries the suffix; if `FINAL` differs from it (a file appeared in
between), rerun the preflight before going on.

## Step 0: find lw, the vault, and this guide's folder (once)

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
command). `SKILL_DIR` is the folder holding this GUIDE.md: `<lw-ingest-router>/lw-notes-ingest` (resolve the skill's symlink
with `readlink -f`), where `notes_preflight.py`, `safe_move.py` and `summary-template.html` live.

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
- A note **not directly in `notes/`**: at the vault root, in another vault folder (`Clippings/`), or **outside the
  vault** (a chat export or download, e.g. `~/Downloads/short selling.md`). Keep its current path for now; Step 2b
  moves it into `notes/`.
- A **chat export** (a `**Link:**` line or `source:` with a chat URL, `## User:` / `## Assistant:` turns) whose chat
  URL appears in a note of the list: it is not a note. Take it out of the list and keep it as that note's companion
  (Step 2d); it never goes to the preflight, to `notes/` or through the title fix. One no note links to was routed as
  a source by the router (row 6b), not here.
- An **`.html` / `.htm` file** the user named: it is not a note. Take it out of the list and store it in Step 2c; it
  never goes to the preflight or `lw ingest`.

More than 10 notes: split them into batches of at most 10 and run Steps 3 to 7 once per batch: each batch is its own
changeset, committed and cleaned up before the next one starts (Step 1 stops on an open changeset). Write one summary
for the whole run (Step 8) after the last batch.

## Step 2b: move every note into notes/ (never copy)

Every note is ingested from `V/notes/`, so the vault keeps exactly one copy of it and Step 7 can clean it up. A note
the user named that is somewhere else is **moved** there, together with the images it embeds from its own folder.
Naming the file in the request is the user's go-ahead to move it, even from outside the vault; move nothing else from
outside the vault.

Run the preflight on the notes from Step 2 (same command as Step 3). For each note with the verdict `MOVE`, run its
`MOVE  python3 -I …/safe_move.py …` lines exactly as printed, note first, then images:

```bash
python3 -I SKILL_DIR/safe_move.py '<from>' '<to>'     # each MOVE line, as printed; read its FINAL line
```

Then replace the note's path in your list with its `FINAL` path. A name already taken in `notes/` does not stop the
move; the preflight shows how it is resolved:

- **The note gets a suffix** (`notes/foo.md` is a different note, so this one becomes `notes/foo-2.md`): when its
  title would come from the file name, the preflight also prints `FIX  title: foo`. Add it after the move (as in
  Step 3), so the raw file and the wiki keep the name the user knows. Wikilinks `[[foo]]` elsewhere keep pointing at
  the note that was already there.
- **An image gets a suffix** (`notes/p.png` is a different image): the preflight prints
  `FIX  embed: p.png -> p-2.png`. After the move, change that one embed in the moved note (`![[p.png]]` →
  `![[p-2.png]]`, `![…](p.png)` → `![…](p-2.png)`), and nothing else.
- **Same bytes** at the destination: replaced, nothing to change.

Rerun the preflight on the new paths; it must say `READY` (or only the `FIX  title:` you still have to add).

The preflight refuses a move (verdict `ASK`, nothing to run) when it would do damage:

| ASK line | Why it is not moved |
|---|---|
| `<file> links to it by path` | a markdown link (`](<name>.md)`) to it would break; wikilinks resolve by name and do not block |
| `is a path embed` | `![](sub/x.png)` would no longer resolve from `notes/` |
| `is also embedded by <file>` | another note beside it needs the same image |

Report every move (`from -> to`) in Step 9 under **Moved before ingest**.

## Step 2c: store HTML files as they are (move, never copy)

An HTML file (a saved interactive tutorial, an exported page) is kept **as it is**, not ingested: lw's extractor
keeps only its visible text, so a page that draws its content with JavaScript becomes a near-empty raw file that can
never be fixed or re-ingested. `lw storage add` keeps the exact bytes in `V/storage/`, catalogued in
`.llmwiki/storage.json`, and lw sync carries it to every device. It opens no changeset and needs no commit.

`lw storage add` copies, so finish the move yourself: store it, check the stored copy is byte-identical, then move
the original into `.trash/` so only one copy is left. For each HTML file `<from>`:

```bash
LW storage add "<from>" -m "<one line: what it is, and the note or raw source it goes with>" --vault V
LW storage show "<name>" --vault V          # <name> = the file name, unless lw printed another
shasum -a 256 "<from>"                       # must match the sha256 storage show prints
python3 -I SKILL_DIR/safe_move.py "<from>" "V/.trash/lw-notes-ingest/<YYYY-MM-DD>/storage/"
```

- `storage add` refuses because an entry with that name exists with a different sha256: **stop and ask**. Never pass
  `--force` (it overwrites the stored file on every device) and never pick another `--name` on your own.
- The sha256 does not match, or `storage show` does not find the entry: leave the original where it is and report it.
- `safe_move.py` exits non-zero: the original stays; report it.
- The HTML belongs with a note in this run (its companion, same subject): mention the stored file in the wiki page's
  text in Step 5, by its storage name. Do not cite it: citations point only to `raw/`.

Report each one in Step 9 under **Stored**.

## Step 2d: companion chats (never into notes/)

A note often cites the chat it was learned from by URL (`https://claude.ai/chat/<id>` under a heading), and the user
hands over that chat's export with it. The export is a reference, not the user's writing: it belongs in
`raw/articles/` beside the other chat exports, and the note should point to it. For each chat export from Step 2:

1. Find its chat URL: the `**Link:**` line near the top (Claude Exporter) or the frontmatter `source:`.
2. Find the notes of this run whose text contains that URL (`grep -nF "<url>" "<note>"`). None: it is not a
   companion; tell the router it is a source (row 6b). In a batched run, the companion joins its note's batch.
3. Leave the file where it is (e.g. `~/Downloads`). lw stores a file from outside `notes/` under `raw/articles/`, and
   names it after its first `# ` heading, which for a chat export is the chat's real title, so add no `title:`.

A URL in the note with no export given (a chat the user did not hand over): leave it as it is and list it in the
report under **Companions** as not saved.

## Step 3: preflight (read-only)

```bash
python3 -I SKILL_DIR/notes_preflight.py V "V/notes/<note 1>.md" "V/notes/<note 2>.md" ...
```

(No note arguments = every note in `notes/` and at the root.) Each note gets a verdict:

| Verdict / line | Meaning | Do |
|---|---|---|
| `READY` | every local embed resolves from the note's folder to a supported image, the title is right | ingest it |
| `MOVE  python3 -I …/safe_move.py …` | the note is not directly in `notes/` | Step 2b: run those lines, rerun the preflight on the new path |
| `FIX  python3 -I …/safe_move.py …` | an image is elsewhere in the vault, unique by name, and only notes in this folder embed it | run that exact line, then rerun the preflight |
| `FIX  embed: <old> -> <new>` | a moved image got a `-N` suffix because a different image had its name | after the move, change that one embed in the note (Step 2b) |
| `FIX  title: <file name>` | lw would name the raw file and its assets after a section heading | add that `title:` line yourself (below), without asking, then rerun the preflight |
| `ASK  title will be … never closed` | the note opens a `---` frontmatter it never closes, so lw ignores any `title:` in it | report it; skip the note |
| `ASK  stub: N words` | a near-empty note (`Testing`, `dlsite`) | skip it unless the user named it; a stub makes no wiki page worth reviewing |
| `ASK  … not on this PC` | the image is still on the device that pasted it | skip the note until it syncs (`lw sync`, or the `lw-notes-sync` skill on that device) |
| `ASK  … .heic` (etc.) | lw does not copy that format | ask: the user converts it to PNG/JPG in place, or accepts losing that image |
| `ASK  … several folders` / `copies` / path embed / non-image | moving would break another note, or lw cannot resolve it | report it; skip the note |

**Adding the title (no need to ask).** The file name is the title in Obsidian, so this restores what the user
already sees. If the note starts with a `---` frontmatter block, insert
`title: <file name without .md>` as the line right after the first `---`. If it has no frontmatter, prepend:

```
---
title: <file name without .md>
---
```

Change nothing else. Then rerun the preflight: the line must read `title='<file name>' (frontmatter)`. List every
title you added in the report (Step 5): it is an edit to the user's note, and if the phone edits the same note before
the next `lw sync`, that note can come back as a sync conflict.

Do not continue until every note you are about to ingest is `READY`.

## Step 4: prepare

**4a. Companions first** (skip when there are none). The note's link needs their raw paths, which only the brief
tells you (`--dry-run` does not print them):

```bash
LW ingest --prepare --agent <you>:<model> --vault V "<companion 1>.md" "<companion 2>.md" ...
```

Read each companion's `raw: raw/articles/<slug>.md` line in the brief (lw may add `-2`; use what it prints). A
`skipped …: already in the vault at raw/<path>` line means it was ingested before: link to that path instead.

**4b. Link each companion from its note.** On every line of the note that holds the companion's chat URL, append
right after the URL (after the closing `)` when the URL is inside a markdown link):

```
 ([[raw/articles/<slug>|saved chat]])
```

A vault-path wikilink, without `.md`: it resolves in Obsidian both from `notes/` and from the raw copy in
`raw/notes/`, and cannot be mistaken for a wiki page of the same name. Change nothing else. Rerun the preflight
on the note; it must still say `READY`.

**4c. The notes**, which join the changeset 4a opened (lw prints `joined open changeset …`):

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
`local embeds` from the preflight (an image embedded twice appears twice). With companions, also check that each
`[[raw/articles/<slug>|saved chat]]` from 4b is in the note's raw copy, and that the brief lists each companion under
`raw/articles/`, not `raw/notes/`. A raw slug that is not the note's file name
slugged means the title step was skipped. On any mismatch: **stop**, close nothing, commit nothing, clean up
nothing, and tell the user the changeset id and what is wrong; they discard it with `lw doctor --discard-changeset`.

## Step 5: curate and close

Follow the **`lw-ingest` guide from its Step 2 to its Step 4** (read every chunk, search the wiki, stage pages,
`stage.close`). It is beside this folder: `SKILL_DIR/../lw-ingest/GUIDE.md`; read it now if it is not
already loaded. Skip its Step 5 (report, do not commit): this guide commits in Step 6 and reports in Step 9. Two
extras for notes:

- Keep the relevant `![alt](../../raw/assets/<name>)` image links in the pages you stage. A screenshot of a command's
  output is often the only record of it: describe what it shows in the text too, so the page is useful without it.
- A hand-written note is one person's learning log, not a reference. Set `confidence` to match (`low` or `medium`
  unless the note cites a source), and do not present guesses in the note as settled fact.

## Step 6: commit (accepted by default)

The user wrote these notes and knows what is in them, so the changeset is **accepted by default**: commit it now,
without asking for a review. Leave it open for review only when the user asked for that in this request ("let me
review it", "don't commit").

```bash
LW status --vault V
LW commit -m "ingest notes: <note 1>, <note 2>, …" --vault V
```

`status` must name the changeset id from Step 4's brief, and nothing else. A different open changeset: stop, commit
nothing, and ask (as in Step 1). Then read the commit's output:

- **Committed:** note the commit id for the report and go on.
- **Refused for a lint regression:** never add `--force`. The changeset stays open. Report the findings lw printed and
  stop here: no cleanup (Step 7). The user fixes it, or reviews and commits it themselves (`lw tui` → Review, or
  `lw commit --force`); afterwards they can say "clean up my ingested notes" to run Steps 7 to 9 alone.
- **Refused as stale** (a page changed after you staged it) or any other error: same as above, stop and report.

If the vault has lw's auto-sync on, the commit may also sync. That is fine: it carries the new raw and wiki files.

## Step 7: clean up notes/

Do this only for notes whose commit succeeded in Step 6 (or, when the user asked for cleanup alone, notes they name
or that `LW note list --vault V` shows as ingested). For each note, **every check must pass**; one failure and the
note stays where it is, with the reason in the report:

1. **No open changeset:** `LW status --vault V` names none.
2. **Its raw copy is committed:**

   ```bash
   LW tool --vault V raw.list '{"query":"<raw slug>"}'
   ```

   The row for `raw/notes/<slug>.md` must end with an ingested date and sha256 prefix. A staging changeset id instead
   means it is not committed: stop the cleanup.
3. **Its images are in raw/assets/:** the Step 4 count still holds (`grep -c 'raw/assets/'` on `raw.get` is at least
   the note's `local embeds`), and each `V/raw/assets/<name>` it links to exists (`ls -l`).
4. **lw no longer counts it as pending:** its id is absent from `LW note list --pending --vault V`.
5. **Nothing else links to it.** Search the notes that stay (every `.md` under `V/notes`, `V/ledger` and the root,
   except the notes being cleaned in this run) for `[[<name>]]`, `[[<name>|`, `[[<name>#`, `](<name>.md)` and the
   `%20` form. A hit: leave the note and report which file links to it (ASK), since removing it breaks that link in
   Obsidian.

**Which images go with it:** each local embed the preflight listed for the note, as a file in the note's folder. An
image that a note which stays also embeds (same search as check 5, by image name) stays too, and is reported.

**Move, never delete.** Mirror the path under `notes/` inside one dated folder:

```bash
T="V/.trash/lw-notes-ingest/<YYYY-MM-DD>"
python3 -I SKILL_DIR/safe_move.py "V/notes/<note>.md" "$T/<same relative path>"
python3 -I SKILL_DIR/safe_move.py "V/notes/<image>"   "$T/<same relative path>"
```

(It creates the folders.) **Companions:** once `raw.list` shows each one's `raw/articles/<slug>.md` committed (an
ingested date, not a changeset id), move its original from where the user kept it (outside the vault only; one
inside the vault stays, as in the `lw-ingest` guide's Step 4b):

```bash
python3 -I SKILL_DIR/safe_move.py "<companion>" "$T/companions/"
```

A second run on the same day can meet a name already in `$T`: an identical file is
replaced, a different one gets a `-N` suffix; report the `FINAL` path. A non-zero exit: the file stays in `notes/`;
report it. `.trash/` is git-ignored by lw sync, so the
next `lw sync` removes these files from `notes/` on every device (phone included) while the copies in `.trash/` stay
on this PC only. lw sync holds back a mass deletion (more than 20 tracked files and over 25% of the vault) until the
user confirms it with a manual `lw sync`; mention that if this run moved more than 20 files.

## Step 8: write the revision summary

Fill the template `SKILL_DIR/summary-template.html` into **one** file per run, outside the vault (a new file inside
it would be synced, and `lw-notes-sync` flags unknown files):

```bash
mkdir -p "$HOME/lw-revision"
cp "SKILL_DIR/summary-template.html" "$HOME/lw-revision/<YYYY-MM-DD>-<changeset id>.html"
```

(Several batches: one file, named after the last changeset, every changeset id in `{{CHANGESET_ID}}`, comma
separated.) Then edit that copy.

**Content comes only from what was committed:** the raw notes (`raw.get`) and the wiki pages you staged (`wiki.get`).
Write it in your own words and add nothing the notes do not say. Commands, error messages and config lines are the
exception: copy those exactly.

**Topics.** Group the notes by subject: notes about the same thing (same wiki concept or entity, shared tags, or
linking to each other) form one topic; unrelated notes are separate topics, each in its own section. One topic is a
valid result. Order topics so a later one can build on an earlier one. `TOPIC_ID` is the topic title slugged
(`smb-enumeration`).

**Per topic:**

| Placeholder / block | What goes in it |
|---|---|
| `TOPIC_TITLE`, `TOPIC_TLDR` | a short title; one sentence on what the topic is and why it matters |
| `point` (3 to 7) | one line each, the facts worth remembering, most important first |
| `terms` (optional) | each term the notes define or rely on, with a one-line meaning |
| `commands` (optional) | each command or snippet verbatim, HTML-escaped, with one line on what it does |
| `figures` (optional) | the images that carry information; `FIGURE_SRC` is `file://` + the absolute path `V/raw/assets/<name>`; a caption saying what it shows |
| `qa` (2 to 4) | questions the notes answer, to test recall; the answer in one or two sentences |
| `caveat` (optional) | when a page went in as `confidence: low`, or the note guesses: say what is unverified |
| `page`, `source` | the wiki page paths and `raw/notes/` paths behind the topic |

**Filling rules:** replace every `{{…}}`, copy each `repeat:` block once per item, delete an `optional:` block whole
when it would be empty, HTML-escape all note text (`&` `<` `>` `"`), and delete every marker comment and the
instruction comment at the top. Then check:

```bash
F="$HOME/lw-revision/<file>.html"
grep -c '{{' "$F"; grep -c '<!--' "$F"   # both must print 0
```

## Step 9: report

Report in this shape, short:

- **Committed:** the commit id, the changeset id, and the wiki pages created or updated.
- **Moved before ingest:** each `MOVE` and `FIX` move (`from -> FINAL path`, and whether it was `moved`, `replaced`
  or `renamed`), including notes and images moved in from outside the vault, and each `title:` and `embed:` edit you
  made.
- **Companions:** each chat export saved with a note (`raw/articles/<slug>.md`, the note it is linked from, and
  where its original went), and each chat URL in a note that had no export.
- **Stored:** each HTML file kept with `lw storage add` (its storage name), and where its original went
  (`from -> .trash/lw-notes-ingest/<date>/storage/`), or why it stayed.
- **Skipped:** each note not ingested and why (stub, image not on this PC, unsupported format, open question).
- **Images:** how many images went to `raw/assets/` per note.
- **Cleaned:** each file moved from `notes/` to `.trash/lw-notes-ingest/<date>/`. Tell the user the next `lw sync`
  removes them from `notes/` on every device, and that moving a file back before that sync undoes the cleanup.
- **Left in notes/:** each ingested note or image that stayed, and why (linked from another note, image shared with a
  note that stays, a failed check, a refused commit).
- **Revision summary:** the path of the HTML file, and its topic titles.
