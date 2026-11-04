---
name: lw-notes-sync
description: >-
  Tidy and sync notes and bookkeeping the user wrote directly in Obsidian inside their lw (llmwiki) vault, on the phone
  or on a PC: move notes Obsidian left at the vault root into notes/, put pasted images beside the note that embeds them
  and receipt photos into ledger/receipts/, refuse to push hand edits under wiki/ or raw/, then `lw sync` to the remote.
  This is NOT ingest: the notes may be unfinished. Use when the user says "sync my notes", "handle my notes", "I wrote
  notes in Obsidian / on my phone", "push my vault notes", or "I pasted images in Obsidian". Works in any agent with a
  shell (Claude Code, Codex, Antigravity).
---

# lw-notes-sync: put hand-written notes on the remote vault, without ingesting them

The user writes notes and bookkeeping directly in Obsidian (on the phone, or on a PC), not through `lw`. Obsidian puts
a new note wherever the user is, often the **vault root**, and with the recommended attachment setting
(*same folder as current file*) the pasted image lands beside it, so also at the root. lw wants notes in `notes/` and
receipts in `ledger/receipts/`.

With `lw hub`, the phone's notes reach the remote directly; no bridge PC has to carry them. They arrive on every PC
where Obsidian left them (often the vault root), so this skill is still how a PC tidies them into `notes/`, whichever
device wrote them, before `lw sync`. On the Syncthing phone path this skill also bridges what arrived from the phone.

Your job: move what Obsidian scattered to where lw expects it, make sure nothing that
needs review is about to be pushed, and sync. You do not ingest, edit or judge the notes: they may be unfinished.
Follow this procedure exactly.

## Rules you must not break

1. **Never ingest.** No `lw ingest`, no `lw tool`. If the user wants a note compiled, that is the `lw-ingest` skill,
   on their say-so, later.
2. **Never change a file's text or name.** No typo fixes, no frontmatter, no renames, no link rewrites. You only
   *move* files, keeping their names, and only as Step 2 allows. Every move uses `mv -n` (never overwrite).
3. **Never touch `wiki/`, `raw/`, `index.md`, `log.md`, `SCHEMA.md`, `curator-memory.md`, `.llmwiki/`, `storage/`, or
   `.git/`.** Hand edits there skip the review gate (`wiki/`) or break immutability (`raw/`). If they are dirty, stop
   and report (Step 3). The one exception is appending `/.stfolder/` to `.git/info/exclude` (Step 3).
4. **Never run `lw sync --take-remote`, `lw commit`, `lw revert`, `lw doctor --discard-changeset`, `git reset`,
   `git checkout`, `git stash`, `git push --force`, or `rm`.** Nothing is ever deleted. A sync conflict goes back to
   the user.
5. **Sync only when Step 3 found nothing in the STOP or ASK class.** `lw sync` stages the whole vault (`git add -A`),
   so whatever is in the work tree gets pushed.

## Step 0: find lw and the vault (once)

Your shell may not have lw on PATH. Find it once:

```bash
for c in "$(command -v lw)" "$HOME/go/bin/lw" "$HOME/.local/bin/lw" /usr/local/bin/lw; do
  [ -x "$c" ] && echo "$(readlink -f "$c"): $("$c" version)"; done; true
```

Use the newest one (`lw v2.57.0` or later knows `ledger/`; older ones are fine for notes only). Below, `LW` is its
**absolute path**. Find the vault:

```bash
LW config | grep -E '^(vault\.path|sync\.remotes)'
```

`V` below is the `vault.path` value, written out absolutely in each command. If the user named another vault, use that
and pass `--vault V` to every `LW` command. If `sync.remotes` is empty, the vault is not under lw sync: stop and tell
the user to run `lw sync init <remote>` themselves.

## Step 1: look at the vault, not only at git

lw's auto-sync (after `lw commit`, `lw note`, an ingest) also runs `git add -A`, so a note Obsidian dropped at the root
may already be **committed** there. Work from the files on disk:

```bash
find V -maxdepth 1 -type f ! -name '.*' | sort                     # root files
find V/ledger -type f ! -path '*/ledger/receipts/*' ! -name '*.md' 2>/dev/null | sort   # non-text files in the ledger
git -C V status --short --untracked-files=all
LW sync status          # ahead/behind; it fetches but changes nothing
```

`index.md`, `log.md`, `SCHEMA.md` and `curator-memory.md` are lw's own root files: leave them. Every other root file
is a candidate for Step 2.

## Step 2: move what Obsidian scattered

Do these in order. Below, `N` is a file name and `enc(N)` is `N` with spaces as `%20`.

**2a. Root notes go to `notes/`.** For each root `X.md` (not one of lw's four):

- If `V/notes/X.md` exists, do not move; report the clash (ASK).
- If any file links to it with a markdown path link (`](X.md)`, `](./X.md)`, or the `enc` form), moving would break
  that link: do not move; report (ASK). Wikilinks (`[[X]]`) resolve by name anywhere, so they do not block a move.

  ```bash
  grep -rlF -e "](X.md)" -e "](./X.md)" -e "]($(printf '%s' 'X.md' | sed 's/ /%20/g'))" V/notes V/ledger V/*.md 2>/dev/null
  ```
- Otherwise: `mv -n "V/X.md" "V/notes/X.md"`.

**2b. Root images go beside whoever embeds them.** An image is `.png .jpg .jpeg .gif .webp .svg .heic .avif .bmp`
(any case). For each root image `N`, after 2a, find the embedders:

```bash
grep -rlF -e "N" -e "$(printf '%s' 'N' | sed 's/ /%20/g')" V/notes V/ledger V/wiki 2>/dev/null
```

- **Only notes, all in one folder `D` under `notes/`, every embed path-free** (`![[N]]`, `![[N|caption]]`, `![](N)` or
  `![](enc(N))`, no `/` before the name): `mv -n "V/N" "V/D/N"`.
- **Only ledger day files of one month `ledger/YYYY/MM/`, path-free:** `mkdir -p V/ledger/receipts/YYYY/MM` then
  `mv -n "V/N" "V/ledger/receipts/YYYY/MM/N"`.
- **Embedded with a path, from `wiki/`, from several folders, or by nothing:** leave it at the root and report it
  (not a blocker: it is not committed anywhere new, see Step 3). An image nothing embeds is often one whose note has
  not synced yet; the next run will place it.

**2c. Images inside `ledger/` go to `ledger/receipts/`.** Obsidian pastes a receipt photo beside the day file
(`ledger/2026/11/r.jpg`); there it would be committed to git as a binary. For each non-`.md` file under
`ledger/YYYY/MM/` (outside `receipts/`): `mkdir -p V/ledger/receipts/YYYY/MM` then
`mv -n` it to `V/ledger/receipts/YYYY/MM/N`. Path-free embeds (`[[N]]`, `![[N]]`) still resolve in Obsidian.
`ledger/receipts/` is git-ignored; Step 4's `lw ledger import` carries it instead.

**2d. Dangling embeds.** For each note under `notes/` that changed or moved, list embedded images that are not on this
PC:

```bash
grep -oE '!\[\[[^]|]+\.(png|jpe?g|gif|webp|svg|heic|avif|bmp)' "V/<note>" | sed 's/^!\[\[//' | sort -u |
  while IFS= read -r img; do [ -n "$(find V -name "$(basename "$img")" -not -path '*/.git/*' -print -quit)" ] || echo "missing: $img"; done
```

A `missing:` image is still on the device that wrote the note (not synced yet, or pasted into a folder that device does
not sync). Report it; it does not block the sync. `.heic` / `.avif` / `.bmp` images sync but `lw ingest` cannot embed
them; mention that if you moved one.

## Step 3: classify what git will push

```bash
git -C V status --short --untracked-files=all
```

Put each path in exactly one class:

| Class | Paths | Action |
|---|---|---|
| OK | `notes/**`, `ledger/**`, moves made in Step 2 (they show as a delete at the root plus an add) | sync |
| OK | `.obsidian/**`, `.gitignore`, `.stignore` | sync (lw sync manages `.gitignore` itself) |
| OK | a root image Step 2 left in place | sync; it was reported |
| EXCLUDE | `.stfolder/**` (Syncthing's per-device folder marker) | never commit it; see below |
| STOP | anything under `wiki/`, `raw/`, `.llmwiki/`, or `index.md`, `log.md`, `SCHEMA.md`, `curator-memory.md` | do not sync; report |
| ASK | anything else (a new folder, a PDF, a root note Step 2 could not move) | do not sync; report and ask |

EXCLUDE: `lw sync` rewrites `.gitignore`, so the marker goes in this PC's own git exclude file instead. Append it once:

```bash
grep -qxF '/.stfolder/' V/.git/info/exclude 2>/dev/null || echo '/.stfolder/' >> V/.git/info/exclude
```

Never delete `.stfolder/`: Syncthing stops syncing the folder without it.

A Syncthing conflict copy (`*.sync-conflict-*`) is OK to sync, but report it: merging the two versions is the user's
call. Then check Obsidian's attachment setting once:

```bash
cat V/.obsidian/app.json 2>/dev/null
```

If `attachmentFolderPath` is missing or is not `"./"`, tell the user (do not edit it yourself): *Obsidian, Settings,
Files & links, Default location for new attachments: Same folder as current file.* The phone has its own
`.obsidian/`; the same setting is needed there.

## Step 4: sync

Only if no path is STOP or ASK:

```bash
LW ledger check         # only if a ledger/ file changed; a grammar error is ASK: report it, do not sync
LW ledger import        # copies ledger/receipts/ into lw storage, which lw sync carries (receipts are not in git)
LW sync
```

lw sync commits everything (`lw sync` as the message), pulls (rebasing this PC's commits on the remote's when nothing
conflicts) and pushes. Read its output:

- **Success:** go to Step 5.
- **Conflict refused** ("nothing changes"): the same file was edited on two PCs. Report the paths it names and the two
  ways out (merge by hand, or `lw sync --take-remote`, which keeps the remote and saves this PC's commits on a
  `lw-diverged-<time>` branch). Do not choose for the user.
- **Exit 1 after the push, naming storage:** the notes are synced; the `lw storage` upload failed and retries next time.
  Report it.
- **ssh / network error:** nothing was pushed. Report the error as printed; do not retry in a loop.

## Step 5: verify and report

```bash
git -C V status --short --untracked-files=all   # expect empty
LW sync status                                   # expect ahead 0, behind 0
LW note list --pending -n 20                     # notes not ingested yet; informational only
```

Report in this shape, short:

- **Synced:** the notes and ledger files that went up (names, not contents).
- **Moved:** `from -> to` for each note and image.
- **Left for you:** every STOP / ASK path and why, images left at the root, `missing:` images, conflict copies, and
  the Obsidian setting if it is wrong.
- **Not ingested (by design):** the pending notes. Say "say `ingest note <id>` when one is finished".

Never claim "synced" unless Step 5 showed `ahead 0`.
