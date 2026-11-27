---
name: lw-search
description: >-
  Find information anywhere in the user's lw (llmwiki) vault: compiled wiki pages, raw sources, hand-written
  Obsidian notes in notes/, day notes, the bookkeeping ledger and budget plan, stored files, and what changed when.
  Use when the user (or your own task) needs to look something up in the vault and you do not know where it lives:
  "find X in my vault", "do I have notes on X", "where did I write about X", "what do I know about X", "did I
  already ingest X", "what did I spend on X", "which pages changed this week", "search everything for X". It is a
  read-only map: which folder holds what, which command searches it, and how much to trust each hit. For a
  synthesized, cited answer from the wiki hand off to lw-ask; for a spending review hand off to lw-finance-review.
  Works in any agent with a shell (Claude Code, Codex, Antigravity).
---

# lw-search: find anything in an lw vault

An lw vault is several stores under one folder, and **no single command searches all of them**. `lw search` only
sees committed wiki pages: it will not find a topic the user has only written about in `notes/`, or a purchase in
the ledger. This skill tells you where each kind of information lives, which read-only command reaches it, and how
much each hit can be trusted.

## Rules you must not break

1. **Read only.** Never create, edit, move or delete anything under the vault: no editor, `sed -i`, `>`, `mv`, `rm`.
2. **Never run a command that writes**, even when it would "help": `lw ingest`, `lw commit`, `lw sync`,
   `lw note -m`, `lw ledger collect`, `lw ledger import`, `lw storage add|rm`, `lw config set`, `lw lint --fix`,
   `lw doctor --unlock|--rebuild-index|--discard-changeset`, `lw revert`, or `lw tool` **without** `--ask`.
   If the user wants one of those, name the skill that does it (table at the end) and stop.
3. **Do not read `.llmwiki/` internals** (journal, objects, changesets, index). They are engine state, not content;
   `lw log`, `lw digest`, `lw status` and `lw stats` give you the same facts safely. Skip `.git/` and `.obsidian/`.
4. **Say where every hit came from** (vault-relative path) and **which trust level** it has (next section). Never
   present an unreviewed note as if it were reviewed wiki knowledge.
5. **Never add money up yourself.** Totals come from `lw ledger sum` / `lw ledger report`. If it prints a warning
   such as `ledger: N issue(s) found; totals may be wrong`, repeat that warning to the user.
6. **Relay `note:` lines.** If an `lw tool --ask` call prints a line starting with `note:` on stderr (unreviewed
   staged content), end your answer with that line verbatim.

## Trust levels: say which one a hit has

| Level | Where | What it means |
|---|---|---|
| **Reviewed** | `wiki/**` (committed) | Synthesized by the curator, approved hunk by hunk by the user, every claim cites `^[raw/...]`. |
| **Source** | `raw/**` | Immutable copy of what was ingested (article, paper, note). True to the source, not curated. |
| **Staged** | anything marked `(staged in cs-..., unreviewed)` | Proposed by an agent, not yet reviewed. Say so. |
| **Personal** | `notes/`, day notes, `bookkeeping.md` | Written by the user by hand. May be unfinished, wrong or a stub. Not reviewed. |

## Step 0: find lw and the vault (once)

Tool shells often lack lw on PATH. Find it, then write its **absolute path** in every command (`LW` below):

```bash
for c in "$(command -v lw)" "$HOME/go/bin/lw" "$HOME/.local/bin/lw" /usr/local/bin/lw; do
  [ -x "$c" ] && echo "$(readlink -f "$c"): $("$c" version)"; done; true
```

Use the newest. `lw search` / `lw show` need v2.66.0+; on an older lw use the ripgrep recipes below instead.

Vault path: the user names it, or read it from config (`VAULT` below):

```bash
LW config 2>&1 | grep '^vault.path'
```

Every `lw` command uses that vault by default; pass `--vault <path>` to search a different one.

## Step 1: route the question

| You are looking for | It lives in | Search it with |
|---|---|---|
| What the user **knows** about a topic (curated) | `wiki/{concepts,entities,comparisons,queries,summaries}/` | `lw search`, `lw show` (Recipe A) |
| A **cited answer** to a real question | wiki + raw | hand off to the **lw-ask** skill |
| The **original text** of an article / paper / transcript | `raw/{articles,papers,transcripts,notes}/*.md` | `raw.list`, `raw.get`, rg (Recipe B) |
| Something the user **wrote by hand** in Obsidian | `notes/*.md` | `lw note list`, rg (Recipe C) |
| **Whether X was already ingested** | `raw/` + `index.md` | Recipe B, then Recipe A |
| A **day's** notes or expenses | `YYYY-MM-DD.md` at the vault root (not collected yet), `ledger/YYYY/MM/YYYY-MM-DD.md` (collected) | Recipe D |
| **Money**: spending, a purchase, a category, the budget | `bookkeeping.md`, `ledger/plan.json` | `lw ledger sum/report`, rg (Recipe D) |
| A **stored file** (PDF, receipt photo) | `storage/` | `lw storage list/show` (Recipe E) |
| **What changed / when** | the journal (through lw), `log.md` | `lw digest`, `lw log`, `lw status` (Recipe F) |
| The **shape** of the vault: tags, page list, gaps | `SCHEMA.md`, `index.md`, `curator-memory.md` | `lw stats`, read the files (Recipe F) |

If you do not know which row fits, run **Recipe G** (search everything) first and route from its hits.

## Recipe A: wiki pages (reviewed knowledge)

```bash
LW search -n 10 <words>                          # ranked pages + snippet; exit 1 = no match
LW search --type concept --tag tools -n 5 nmap   # filter: --type entity|concept|comparison|query|summary, --tag <SCHEMA tag>
LW search --json -n 5 <words>                    # path, title, score, snippet as JSON
LW show <page>                                   # one page by path, basename, title or alias
LW show --links <page>                           # + outbound links and backlinks (with the linking line)
```

- Exit 1 with `no pages match` means **the wiki** has nothing. It does **not** mean the vault has nothing: go on
  to Recipes B and C before you say "not found".
- Words are matched literally. Try a synonym, the acronym and the expanded form (`TLS` / `transport layer
  security`), and the singular.
- `lw search`, `lw show` and ripgrep see **committed** pages only. Pages an open changeset has staged but the user
  has not reviewed are visible only through the ask tools, marked `(staged in cs-..., unreviewed)`:
  `LW tool --ask wiki.search '{"q":"<words>","limit":5}'`. Run `LW status` to see whether a changeset is open.
- `index.md` is a one-line-per-page catalog; reading it is the fastest way to see every page title at once.
- To follow a topic outward: `lw tool --ask wiki.neighbors '{"page":"<page>","depth":1}'`.

## Recipe B: raw sources (immutable originals)

```bash
LW tool --ask raw.list '{"query":"footprint"}'            # matching raws: path, title, origin path, ingested date
LW tool --ask raw.get '{"source":"raw/articles/x.md","chunk":1}'   # read one chunk; ask for chunk 2, 3... as needed
rg -n -i --glob '*.md' '<pattern>' "$VAULT/raw"            # full-text, line-level
```

- The `.md` file is the searchable text. Skip `.orig` (pre-edit copies), `.pdf` / `.html` (originals) and
  `raw/assets/` (images) when grepping.
- `raw.list` prints where each raw came from (`notes/footprint FTP.md`, a URL, a local path), which answers "did I
  already ingest this file?".
- A raw with no wiki page citing it is listed by `lw stats` under `uncited raws`.

## Recipe C: the user's own notes (`notes/`)

`lw search` does **not** index `notes/`. Use:

```bash
LW note list -n 20            # newest first: name, age, first line
LW note list --pending        # notes not yet ingested into the wiki
rg -l -i '<pattern>' "$VAULT/notes" --glob '*.md'        # which notes mention it
rg -n -i -C2 '<pattern>' "$VAULT/notes" --glob '*.md'    # with context lines
```

- Filenames are human titles with spaces (`notes/IPsec.md`, `notes/firewall types.md`): quote them.
- `Pasted image *.png` files are images embedded by `![[...]]`; read an image only when the note's text is not
  enough and your agent can see images.
- A note that was ingested also exists as `raw/notes/<slug>.md`; prefer the wiki page built from it (Recipe A) and
  mention the note as the user's original.
- Notes are **Personal** trust: they may be a stub, a link dump, or half a thought. Say so.

## Recipe D: money and day notes

```bash
LW ledger sum 2026-11         # totals by currency and tag for a month (omit the month for all time)
LW ledger report 2026-11      # monthly spending against the budget in ledger/plan.json
LW ledger check               # syntax / date / duplicate problems that make totals wrong
rg -n -i '<item>' "$VAULT/bookkeeping.md"                    # find a purchase: date, amount, (label), #tag
rg -n -i '<item>' "$VAULT/ledger" --glob '*.md'              # collected day notes
ls "$VAULT"/[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9].md     # day notes not collected yet
```

- `bookkeeping.md` lines look like `2026-11-07, 198 (buffets) #social + 1500 (rog falcata) #gadget`: amount,
  optional `(label)`, optional `#tag`; an untagged item counts as the default tag in `ledger/plan.json`
  (`default_tag`). Month lines (`Nov - 家用 4000`, `Nov + salary 16000`) are fixed outgoings and income.
- `ledger/plan.json` holds the budget buckets, each tag's meaning and its budget.
- Quote amounts from the line you found; for any **total**, run `lw ledger sum` (rule 5).
- A day note at the vault root that is not yet in `bookkeeping.md` is not counted by `lw ledger sum` until the user
  runs `lw ledger collect` (the **lw-bookkeeping** skill). Say so if the question covers that day.
- Receipt photos: `ledger/receipts/` (inbox) and `storage/receipts/` (imported); see Recipe E.
- A weekly or monthly review page belongs to the **lw-finance-review** skill.

## Recipe E: stored files (`storage/`)

```bash
LW storage list               # name, size, date added, note (e.g. "ledger receipt")
LW storage show <name>        # metadata of one file
```

Stored files are not wiki content and are not searchable as text. To read a stored PDF, find its text in `raw/`
first (it may have been ingested), and only then open the file itself if your agent can read PDFs.

## Recipe F: history and vault shape

```bash
LW digest --since 7d          # pages added/updated and sources ingested in a window (--since 2026-11-01 works too)
LW log | tail -40             # changeset history: opened, proposed ops, committed, rejected
LW status                     # an open (unreviewed) changeset, if any
LW stats                      # page counts by type, tags, confidence, uncited raws, freshness
```

- `SCHEMA.md`: the domain and the tag taxonomy; use its tags with `lw search --tag`.
- `curator-memory.md`: the user's standing editorial preferences.
- `log.md`: human-readable append-only log.

## Recipe G: search everything at once

When you do not know where it is, one ripgrep pass over the content stores, grouped by store:

```bash
rg -l -i --glob '*.md' --glob '!.git/**' --glob '!.llmwiki/**' --glob '!.obsidian/**' \
   --glob '!**/*.orig' '<pattern>' "$VAULT" | sort
```

Then read the hits store by store in trust order: `wiki/` first, then `raw/`, then `notes/` and day notes, and
report the best answer with its path and trust level. `wikilinks.md` and `index.md` are generated catalogs; a hit
there only points you to the real page.

## Step 2: report

- Lead with the answer, then the evidence: one line per hit with its **path** and **trust level**.
- If the wiki has nothing but a note does, say exactly that ("not in your wiki; your note `notes/IPsec.md` covers
  it, unreviewed") and offer to ingest it (the **lw-ingest-router** skill; it routes notes to its notes-ingest guide).
- If nothing anywhere matches, say which stores you searched and which words you tried.
- If a staged (`cs-...`) result or a `note:` line appeared, say the content is unreviewed (rule 6).

## Hand-offs: when the task is not just finding

| The user wants to | Skill |
|---|---|
| A synthesized answer with citations | `lw-ask` |
| Add a source, ingest hand notes with images, or store an HTML page | `lw-ingest-router` |
| Tidy and push Obsidian notes | `lw-notes-sync` |
| Collect day notes, read receipts | `lw-bookkeeping` |
| A weekly / monthly spending review page | `lw-finance-review` |
| Phone sync, hub, conflicts | `lw-phone-sync` |
