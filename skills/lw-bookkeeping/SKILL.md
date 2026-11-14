---
name: lw-bookkeeping
description: >-
  Collect daily bookkeeping notes, process receipts, and summarise expenses into the central ledger of an lw (llmwiki) vault.
  Use when the user says "collect my bookkeeping", "summarise my day notes", "read my receipts", "process my receipts",
  or "update my ledger". Reads receipt images, extracts items, records day lines via `lw ledger collect`, imports receipts
  into storage, and runs reports. Works in any agent with a shell and multimodal capabilities.
---

# lw-bookkeeping: summarise daily expense notes and receipts into the central ledger

The user writes daily expense notes (`YYYY-MM-DD.md`) at the vault root or in `ledger/`, containing daily amounts, notes, and pasted receipt images. lw handles parsing, archiving, and reporting deterministically, while you assist by reading receipt photos that have no typed amount.

Your job: inspect candidate notes, collect readable ones with `lw ledger collect`, inspect receipt images for waiting notes, record their lines via `lw ledger collect --day D --line "..."`, import receipts into storage, and report totals.

Follow this procedure exactly.

## Rules you must not break

1. **Never edit `bookkeeping.md` or a day note with a file tool.** Never use `write_file`, `edit_file`, `sed`, or shell redirection on them. `lw ledger collect` is the only command that appends to `bookkeeping.md` and moves day notes to the archive.
2. **Never do sums.** Do not calculate totals or balances yourself in your head or with code. `lw ledger sum` and `lw ledger report` compute and verify exact arithmetic.
3. **Never guess an amount that is not legible.** If a receipt image is blurry, cropped, ambiguous, or unreadable, leave the note `waiting` and explain why.
4. **Must be able to view images.** Reading receipts requires multimodal image inspection. If you cannot view images or your agent environment lacks image capabilities, stop immediately and say so.
5. **Always tell the user what was recorded from a photo.** Every line synthesized from a receipt photo must be explicitly stated to the user in the final message so they can check it.
6. **Never run `lw sync` unless the user explicitly asked.** Bookkeeping collection is a local operation; sync to remotes only occurs when specifically requested.
7. **Never run destructive git or vault commands.** No `lw sync --take-remote`, `git reset`, `git checkout`, `rm`, or force pushes.

## Step 0: find lw and the vault (once)

Your shell may not have lw on PATH. Find it once:

```bash
for c in "$(command -v lw)" "$HOME/go/bin/lw" "$HOME/.local/bin/lw" /usr/local/bin/lw; do
  [ -x "$c" ] && echo "$(readlink -f "$c"): $("$c" version)"; done; true
```

Use the newest one (`lw v2.67.0` or later: `lw ledger collect` does not exist before it). Below, `LW` is its **absolute path**. Find the vault:

```bash
LW config | grep -E '^(vault\.path|sync\.remotes)'
```

`V` below is the `vault.path` value, written out absolutely in each command. If the user named another vault, use that and pass `--vault V` to every `LW` command.

## Step 1: dry run collection

Check what candidate day notes exist and which ones are readable or waiting:

```bash
LW ledger collect --vault V --dry-run
```
(If the user asked to include today's note as well, pass `--include-today`.)

Read the output:
- Notes that lw can parse automatically are marked `would collect`.
- Notes that require attention are marked `waiting <note>: <reason>`.
- Notes with future dates or today's date (without `--include-today`) are marked `skipped`.

## Step 2: run batch collect

Collect all notes that lw can read without assistance:

```bash
LW ledger collect --vault V
```
(Include `--include-today` if requested.)

This appends parsed day lines to `bookkeeping.md`, moves notes to `ledger/YYYY/MM/YYYY-MM-DD.md`, and moves embedded receipts to `ledger/receipts/YYYY/MM/`.

## Step 3: resolve waiting notes and receipts

If Step 2 left notes `waiting`, resolve each one:

For each waiting note `D.md` (date `D`):
1. **Read the note**:
   Read the markdown content of `V/D.md` (or `V/ledger/D.md`) to see its text and embedded image links (`![[image.png]]` or `![](image.png)`).
2. **Inspect embedded images**:
   Look at each receipt photo embedded in the note.
   > **CRITICAL**: You must visually inspect the image. If you cannot view images in your agent environment, stop and notify the user: *"Cannot read receipt image: multimodal image viewing is unavailable."*
3. **Parse line items**:
   - Transcribe each legible item and its amount.
   - Format items: amount first `44 (lunch)` or note first `lunch 44`.
   - Chain multiple items with `+`: `25 (lunch) #food + 12 (bus) #transit`.
   - If an amount is illegible or missing, do NOT guess. Leave the note waiting and report why.
4. **Show the proposed line to the user**:
   Show the user the exact line you worked out before recording it.
5. **Record the line via `lw ledger collect`**:
   ```bash
   LW ledger collect --vault V --day D --line "<items>"
   ```
   For example:
   ```bash
   LW ledger collect --vault V --day 2026-11-05 --line "88 (supermarket) #groceries"
   ```
   lw parses the line, appends it to `bookkeeping.md`, archives the day note into `ledger/YYYY/MM/`, and moves the receipt photos into `ledger/receipts/YYYY/MM/`.

## Step 4: import receipts into storage

When receipts were moved to `ledger/receipts/`:

```bash
LW ledger import --vault V
```

This hashes and copies receipts into `storage/` as `receipts/<path>`, where `lw sync` can carry them across PCs.

## Step 5: check and report

Validate ledger consistency and display the monthly report:

```bash
LW ledger check --vault V
LW ledger report --vault V
```

`lw ledger check` verifies syntax and duplicate IDs (expect `ok: N transaction(s) in M file(s)`). `lw ledger report` displays monthly spending, fixed costs, income, net out, and budget status.

## Step 6: sync (only if requested)

Only if the user explicitly asked to sync:

```bash
LW sync --vault V
```

## Step 7: report to the user

Provide a short summary:
- **Collected**: list the day notes collected and archived.
- **Lines from photos**: state every line recorded from a receipt photo so the user can check it.
- **Left waiting**: any notes that could not be parsed (e.g. illegible receipt, unreadable handwriting) and why.
- **Summary**: present the output from `lw ledger report`.

If the user wants a review of the week or month (budget against actual, where the money went, an interactive
page), that is the `lw-finance-review` skill, not this one.
