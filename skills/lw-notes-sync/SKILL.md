---
name: lw-notes-sync
description: >-
  Tidy and sync notes the user wrote by hand in their lw (llmwiki) vault (in Obsidian, an editor, or on the phone):
  move pasted images next to the note that embeds them, check that nothing outside notes/ and ledger/ was hand-edited,
  then `lw sync` to the remote vault. This is NOT ingest: the notes may be unfinished, so they stay in notes/ and nothing
  is compiled. Use when the user says "sync my notes", "I wrote notes in the vault, push them", "handle my vault notes",
  "lw sync my notes", or "I pasted images in Obsidian". Works in any agent with a shell (Claude Code, Codex, Antigravity).
---

# lw-notes-sync: put hand-written notes on the remote vault, without ingesting them

`notes/` (and `ledger/`) are vault-root folders lw never compiles into the wiki. A note written there by hand is a
draft: it may be half done. Your job is to make the vault safe to sync and then sync it. You do not ingest, edit or
judge the notes. Follow this procedure exactly.

## Rules you must not break

1. **Never ingest.** No `lw ingest`, no `lw tool`. If the user wants a note compiled, that is the `lw-ingest` skill,
   on their say-so, later.
2. **Never change a note's text or name.** No typo fixes, no frontmatter, no renames. You may only *move image files*
   as Step 3 allows.
3. **Never touch `wiki/`, `raw/`, `index.md`, `log.md`, `.llmwiki/`, `storage/`, or `.git/`.** Hand edits there skip the
   review gate (`wiki/`) or break immutability (`raw/`). If they are dirty, stop and report (Step 2).
4. **Never run `lw sync --take-remote`, `lw commit`, `lw revert`, `lw doctor --discard-changeset`, `git reset`,
   `git checkout`, `git stash`, `git push --force`, or `rm` on anything you did not just move.** A sync conflict goes
   back to the user.
5. **Sync only when Step 2 found nothing in the STOP class.** `lw sync` stages the whole vault (`git add -A`), so
   whatever is dirty gets pushed.

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

## Step 1: see what changed

```bash
git -C V status --short --untracked-files=all
LW sync status          # ahead/behind; it fetches but changes nothing
```

If `git status` is empty and `ahead 0`, there is nothing to push. Still run `LW sync` to pull if `behind` > 0, then
report.

## Step 2: classify every path

Put each path from `git status` in exactly one class:

| Class | Paths | Action |
|---|---|---|
| OK | `notes/**`, `ledger/**` | sync as is |
| OK | `.obsidian/**`, `.gitignore`, `.stignore` | sync as is (lw sync manages `.gitignore` itself) |
| IMAGE | an image (`.png .jpg .jpeg .gif .webp .svg .bmp .avif`) anywhere outside `notes/`, `ledger/`, `raw/`, `wiki/` (Obsidian's default paste location is the vault root) | Step 3 |
| STOP | anything under `wiki/`, `raw/`, `.llmwiki/`, or `index.md` / `log.md` | do not sync; report |
| ASK | anything else (a stray `.md` at the root, a PDF, a new folder) | do not sync; report and ask |

A `.md` note written at the vault root instead of `notes/` is ASK, not OK: ask whether it belongs in `notes/`. Moving it
is the user's call, because other notes may link to it by path.

## Step 3: put each pasted image where its embed is

ingest resolves an image embed relative to the note's own folder, so an image at the vault root is lost when the note
is ingested later. For each IMAGE path `P` with file name `N`:

1. Find who embeds it. Search for the file name and its URL-encoded form (spaces become `%20`):

   ```bash
   grep -rlF -e "N" -e "$(printf '%s' "N" | sed 's/ /%20/g')" V/notes V/ledger V/wiki 2>/dev/null
   ```

2. Decide:
   - **Embedded only by notes that all sit in one folder `D` under `notes/`, and every embed is path-free**
     (`![[N]]`, `![[N|caption]]`, or `![](N)` with no `/` before the name): move it there with `mv -n "V/P" "V/D/N"`
     (`-n` never overwrites). Path-free embeds still resolve in Obsidian, and now ingest finds them too. If `V/D/N`
     already exists, do not move; report the clash.
   - **Embedded only by ledger day files `ledger/YYYY/MM/*.md`, all in one month:** move it to
     `ledger/receipts/YYYY/MM/N` (create the folder with `mkdir -p`), same `-n` and path-free conditions.
     `ledger/receipts/` is git-ignored, so the move takes it out of `git status`; Step 4 carries it with
     `LW ledger import` instead.
   - **Embedded with a path** (`![](../x.png)`, `![[attachments/x.png]]`): moving it would break the link and fixing
     the link means editing the note. Leave it, report it as ASK.
   - **Embedded from `wiki/`, or from notes in different folders, or by nothing at all:** leave it, report as ASK.
     An image nothing embeds may be one the user deleted the embed for; that is their decision.

3. After moving, run `git -C V status --short --untracked-files=all` again and re-classify.

Then check Obsidian's attachment setting once:

```bash
cat V/.obsidian/app.json 2>/dev/null
```

If `attachmentFolderPath` is missing or is not `"./"`, tell the user (do not edit it yourself): *Obsidian, Settings,
Files & links, Default location for new attachments: Same folder as current file.* Then the next pasted image lands
beside its note and this step has nothing to do.

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
- **Images moved:** `from -> to` for each.
- **Left for you:** every STOP / ASK path and why, plus the Obsidian setting if it is wrong.
- **Not ingested (by design):** the pending notes. Say "say `ingest note <id>` when one is finished".

Never claim "synced" unless Step 5 showed `ahead 0`.
