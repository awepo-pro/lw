# Mobile Notes and Bookkeeping Setup

This guide explains how to set up an Android phone for capturing quick notes and recording daily expenses directly into your `lw` vault using Obsidian and Syncthing-Fork.

## Vault Layout

Your vault uses dedicated root directories for mobile capture outside the compiled wiki:

- `notes/`: Quick text captures and inbox notes (from `lw note` or Obsidian).
- `ledger/YYYY/MM/YYYY-MM-DD.md`: Daily plain-text expense ledgers.
- `ledger/receipts/YYYY/MM/`: Local receipt images associated with transactions.

Both `notes/` and `ledger/` are kept outside `wiki/` and `raw/`, meaning they are not indexed or compiled into wiki pages. When a note from `notes/` is ingested into the wiki (via `lw ingest --note <id>` or `lw ingest notes/<file>.md`), it lands in `raw/notes/` (074 S3).

## Grammar v1 (Expense Tracking)

Ledger entries live in daily files such as `ledger/2026/10/2026-10-31.md`.
Each transaction is recorded on a single line:

`- (id:: 20261031-1230-a) (amt:: -45.00) (cur:: HKD) (cat:: food) (acct:: cash) (receipt:: [[r-20261031-1230.jpg]]) lunch`

### Field Rules

- Grammar v1 is expense tracking only (not double-entry bookkeeping).
- Key order is fixed: `(id:: ...) (amt:: ...) (cur:: ...) (cat:: ...) (acct:: ...) [(receipt:: ...)] <description>`
- `(id:: <val>)`: Unique transaction identifier composed of timestamp plus device letter (e.g., `20261031-1230-a`).
- `(amt:: <val>)`: Amount formatted as a fixed two-decimal string; negative numbers indicate spending/expenses (e.g. `-45.00`).
- `(cur:: <val>)`: Currency code, required on every line (e.g. `HKD`, `USD`).
- `(cat:: <val>)`: Category slug (e.g. `food`, `transit`, `supplies`).
- `(acct:: <val>)`: Account identifier (e.g. `cash`, `visa`, `bank`).
- `(receipt:: <val>)`: Optional receipt attachment link, e.g. `(receipt:: [[r-20261031-1230.jpg]])`.
- Free text: Transaction description follows the metadata tags.

## How the pieces fit

The phone runs **Syncthing and Obsidian only: no git, no `lw`, no agent.** It never talks to the remote vault.

```
phone (Obsidian)  --Syncthing-->  PC vault  --lw sync (git over ssh)-->  remote vault  -->  other PCs
  notes/, ledger/                   one PC runs lw-notes-sync / lw sync
```

Syncthing moves `notes/` and `ledger/` between the phone and **one** PC. That PC is the bridge: `lw sync` commits
what arrived and pushes it to the remote, and pulls notes written on your other PCs, which Syncthing then carries
back to the phone. The bridge PC must be on for the phone's notes to reach the remote.

## Syncthing-Fork Setup

Share the vault folder between the bridge PC and the phone (on the phone, an empty folder; Obsidian opens it as a
vault). Restrict synchronization to `notes/` and `ledger/` with a vault-root `.stignore` on **both** sides:

```
!/notes
!/ledger
*
```

Syncthing reads the patterns top-down and the first match wins: `notes/` and `ledger/` are included, everything else
is ignored. List what to *keep*, not what to skip: a skip list (`wiki`, `raw`, ...) still syncs `index.md`, `log.md`,
`.obsidian/` and any stray file at the vault root, and two Obsidians writing one `.obsidian/` can produce conflict copies.

> `.stignore` is not synced by Syncthing, so create it on both sides. Do it **before** the first sync, or Syncthing
> starts by exchanging the whole vault.

Syncthing also creates a `.stfolder/` marker folder at the vault root. Keep it out of git on the bridge PC, once
(`lw sync` rewrites `.gitignore`, so the PC's own exclude file is the place):

```bash
echo '/.stfolder/' >> "$VAULT/.git/info/exclude"
```

Without that line, `lw sync` (`git add -A`) commits the marker and pushes it to every PC.

## Obsidian Configuration

When opening the synced vault in Obsidian on Android:

1. **Attachment Location**: In Settings -> Files & Links, set "Default location for new attachments" to "same folder as current file". Required, on the phone and on every PC's Obsidian.
   - *Why*: `lw ingest` resolves image links like `![[img]]` relative to the note beside it, and with the `.stignore` above an image pasted at the vault root never leaves the phone (the note arrives on the PC with a broken embed).
2. **QuickAdd Capture**: Set up a QuickAdd capture macro to prompt for amount, currency, category, and description, appending a formatted line to today's ledger file at `ledger/YYYY/MM/YYYY-MM-DD.md`.

## Receipts

The managed `.gitignore` contains `/ledger/receipts/` to prevent large binary receipt images from cluttering the Git repository. Receipts delivered to `ledger/receipts/` via Syncthing are copied into `storage/` using `lw ledger import`:

```bash
lw ledger import
```

This stores receipts under `receipts/<path>` in content-addressed storage, where `lw sync` carries them across PCs. The original files in `ledger/receipts/` remain untouched.

## Ledger Verification and Totals

Use `lw ledger check` to validate ledger files for grammar syntax, directory layout, date matching, and duplicate IDs:

```bash
lw ledger check
```

Use `lw ledger sum` to aggregate amounts by currency and category, optionally filtered by month (`YYYY-MM`):

```bash
lw ledger sum 2026-10
```

## Daily loop

| Where | What |
|---|---|
| Phone | Write in `notes/` (finished or not), paste images into the note, add ledger lines and receipt photos. |
| Bridge PC, in an agent | "sync my notes": the `lw-notes-sync` skill moves stray pasted images beside their note, runs `lw ledger check` + `lw ledger import`, then `lw sync`. It never ingests. |
| Bridge PC, by hand | The same without an agent: `lw ledger check && lw ledger import && lw sync`. |
| Any PC, when a note is done | "ingest note <id>" (`lw-ingest` skill) or `lw ingest --note <id>`, then review and commit. |

Install the skills on each PC from the lw repo: `git pull && sh skills/install.sh`.

## What lw does not do

- The phone has no `lw` and no git: it cannot ingest, review, ask, or push to the remote by itself.

- `ledger/` is never compiled into the wiki and is invisible to `lw ask`/ingest tools by default.
- Double-entry accounting is out of scope for v1.
