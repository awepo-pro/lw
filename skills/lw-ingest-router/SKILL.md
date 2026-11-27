---
name: lw-ingest-router
description: >-
  The one way to put anything into the user's lw (llmwiki) vault: decides per input which of three routes it takes,
  then follows that route's guide in this folder. The user's own notes (Obsidian notes with pasted images, notes in
  the vault) go through lw-notes-ingest/GUIDE.md (preflight, ingest, commit, tidy originals into .trash/, revision
  page); a chat export the note links to rides along as its companion into raw/articles/, with a link injected next
  to its URL in the note. Outside sources (PDF, article, paper, URL, HTML text, a finished markdown document, a
  folder of them, recompile raw/…) go through lw-ingest/GUIDE.md (you stage pages; the user reviews and commits). An
  HTML page kept as it is goes to `lw storage add`. Handles a mixed list in one request, one changeset at a time.
  Use when the user says "ingest X", "ingest my notes", "ingest note X", "add this paper/article to my wiki/vault",
  "lw ingest X", "compile X into wiki pages", "recompile raw/…", "my notes have images, ingest them", "store/save
  this html in my vault", "route these into my vault", "which skill/command should ingest this", or "use
  lw-ingest-router". Works in any agent with a shell (Claude Code, Codex, Antigravity).
---

# lw-ingest-router: pick the right ingest for each input

This one skill holds both ingest procedures, as guides in its own folder. There are three ways into an lw vault, and
each input needs exactly one of them:

| Route | What it does | Who commits |
|---|---|---|
| **notes** (`lw-notes-ingest/GUIDE.md`) | moves the user's own note into `notes/`, fixes images and title so nothing is lost, ingests, **commits by itself** (accepted by default: the user wrote it), moves the original to `.trash/`, writes a revision page | you, inside the guide, without asking |
| **source** (`lw-ingest/GUIDE.md`) | ingests an outside source as-is; you stage the pages, then move the original file to `.trash/` | **the user** reviews and commits (`lw tui` → Review) |
| **store** (`lw storage add`, notes guide Step 2c) | keeps a file byte for byte in `storage/`, not parsed, not in the wiki; the original moves to `.trash/` | nothing to commit |

**Every route moves, never copies.** When a route finishes, a local file the user named outside the vault is no
longer where it was: a note is in `notes/` (and after its commit in `.trash/`), a stored page's original and an
ingested source's original are in `V/.trash/`. Nothing is deleted; `.trash/` keeps it on this PC.

A wrong route cannot be undone by re-running: a committed raw file is immutable and the same text is refused as a
duplicate. A raw source that only landed in the wrong folder (a chat export in `raw/notes/`) is moved, with its
citations, by `LW raw mv raw/notes/<x>.md raw/articles/<name>.md --vault V` (lw v2.67.0+), then `LW commit`; never
move a raw file by hand. So decide first, show the plan, and only then follow the chosen guides. Write nothing except what
those guides' own procedures write.

## Step 0: find lw and the vault

As in the source guide's Step 0: `LW` is the absolute path of the newest lw (v2.61.0 or later), `V` the vault from
`LW config | grep -E '^vault\.path'` (or the vault the user named, with `--vault V`). `ROUTER` is this skill's
folder (`readlink -f` of this SKILL.md, one level up). The guides are `ROUTER/lw-notes-ingest/GUIDE.md` (its
`SKILL_DIR` is `ROUTER/lw-notes-ingest`) and `ROUTER/lw-ingest/GUIDE.md`. Read a guide in full before you follow it.

## Step 1: list the inputs

Split the request into inputs as the notes guide's Step 2 does (commas and new lines, not spaces; expand `~`;
`ls -l` each local path; ask for any path that does not exist, and for any `…`). Keep the user's extra wording
("only chapter 3", "tag it cuda") with the input it is about.

Not an ingest at all, so hand it to its own skill and leave it out of the plan: a question about the vault
(`lw-ask`), finding something (`lw-search`), receipts or day notes about money (`lw-bookkeeping`), syncing
unfinished notes without ingesting them (`lw-notes-sync`).

## Step 2: route each input

Look at each input (read-only: `head -40`, `grep`, the preflight, `--dry-run`) and take the **first** row that
matches:

| # | The input is | Route |
|---|---|---|
| 1 | a URL, a PDF, a folder of sources, plain text, or `recompile raw/…` | source |
| 2 | an `.html` / `.htm` file | **store**, unless rule 3 applies |
| 3 | an `.html` file the user explicitly wants **read into the wiki** ("ingest the article in this page") | source, only if its text survives: `LW ingest --dry-run "<file>" --vault V` must report a size close to the page's visible text, not ~1 KB for a large file. A page that draws its content with JavaScript fails this: tell the user, store it instead, and ask for the text as markdown |
| 4 | an `.md` id from `LW note list --pending`, or a note **inside the vault** (`notes/`, the root, `Clippings/`, any folder) | notes |
| 5 | an `.md` that embeds images Obsidian-style (`![[…]]`, `Pasted image …`) or relies on images beside it | notes |
| 6 | an `.md` **chat export** (frontmatter `source:` or a `**Link:**` line with a chat URL such as gemini.google.com / chatgpt.com / claude.ai/chat, or `## User:` / `## User —` / `## Assistant` turns) whose chat URL appears in a note of this request (row 4 or 5) | **companion** of that note: notes route, guide Step 2d. It goes to `raw/articles/` (never `notes/`, no `title:` edit) in the note's changeset, and the note gets a link to it beside the URL |
| 6b | a chat export no note in the request links to | source (it is a reference, not the user's writing; it lands in `raw/articles/`) |
| 7 | an `.md` outside the vault that is a **finished document**: its first line is `# <real title>` and the file name is a slug of it (`thinking-fast-and-slow-distilled.md`), it reads as a reference (sections, tables, citations), and it has no Obsidian embeds | source |
| 8 | anything else | ask the user: "your own note (committed for you, then tidied away) or a source (you review and commit)?" |

How to tell rows 6 and 7 apart when unsure: run the notes preflight on it (read-only)

```bash
python3 -I ROUTER/lw-notes-ingest/notes_preflight.py V "<file>"
```

If it proposes `FIX  title: <file name>` to **replace a real document title** in the first line, the file is a
document (row 7): the notes route would throw that title away. If its title comes from the frontmatter or the file
name, or it finds embeds, it is a note (rows 4 to 6).

The user's own words win over the table: "these are my notes" → notes; "I want to review it first" or
"don't commit" → source; "keep it as it is" / "just save it" → store.

## Step 3: show the plan, then run it

Print one line per input, then the order:

```
<input>  ->  <route>   (<the rule that decided it, in a few words>)
```

If the user only asked **which** route or command to use, stop here: give the plan, the exact request or command
for each route, and why. Otherwise run it, in this order, without waiting for a reply unless a row needed asking:

1. **Check no changeset is open:** `LW status --vault V`. One you did not open: stop and ask, as in the notes guide's
   Step 1. Both ingest routes join an open changeset, so two unrelated ingests would be reviewed as one.
2. **Store** every store input: follow the notes guide's **Step 2c** for each one (`lw storage add`, the sha256
   check, then the original to `.trash/` with `safe_move.py`). No changeset is involved, so this goes first.
3. **Notes** with all its inputs, companion chats included, in one run (it batches by 10 itself; a companion goes
   in the same batch as the note that links to it): read `ROUTER/lw-notes-ingest/GUIDE.md` and
   follow it from Step 1 (its Step 1 is the check above). It commits and leaves no changeset open. If it stops (a refused commit, a changeset left open),
   **stop the whole plan there** and report: the next route would join that changeset.
4. **Sources** last, with all its inputs in one `--prepare` (at most 10 per changeset; more: ask the user,
   since each further batch needs their commit before it can start). Read `ROUTER/lw-ingest/GUIDE.md` and follow it. It ends with an
   open changeset for the user to review, which is why it goes last.

Pass each input's extra wording on with `--instruction`, to the route that ingests it.

## Step 4: report

One short block per route that ran, then what is left:

- **Stored:** storage names, and where each original went.
- **Notes:** the notes guide's Step 9 report (commit id, pages, moves, cleaned files, revision page).
- **Sources:** each original moved to `.trash/lw-ingest/<date>/` (or left, and why), the open changeset id and the pages staged, and that the user reviews and commits it
  (`lw tui` → Review, or `lw commit -m "…"`).
- **Not run:** each input you asked about or skipped, and why.
