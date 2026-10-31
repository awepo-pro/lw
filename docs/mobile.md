# Mobile Notes and Bookkeeping Setup

This guide explains how to set up an Android phone for capturing quick notes and recording daily expenses directly into your `lw` vault using Obsidian and Syncthing-Fork.

## Vault Layout

Your vault uses dedicated root directories for mobile capture outside the compiled wiki:

- `notes/`: Quick text captures and inbox notes (from `lw note` or Obsidian).
- `ledger/YYYY/MM/YYYY-MM-DD.md`: Daily plain-text expense ledgers.
- `ledger/receipts/YYYY/MM/`: Local receipt images associated with transactions.

Both `notes/` and `ledger/` are kept outside `wiki/` and `raw/`, meaning they are not indexed or compiled into wiki pages.

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

## Syncthing-Fork Setup

To sync your mobile phone with your PC without syncing the entire wiki or Git history, use Syncthing-Fork (or standard Syncthing) configured with selective folder ignores.

Share the vault folder between PC and Android, but restrict synchronization to `notes/` and `ledger/`.

Create a vault-root `.stignore` file on **both** sides containing:

```
.git
.llmwiki
wiki
raw
storage
```

> Note: `.stignore` itself is not synced between devices by default, so ensure it is created on both the PC and the phone.

With this configuration, only `notes/` and `ledger/` (including `ledger/receipts/`) are exchanged over Syncthing.

## Obsidian Configuration

When opening the synced vault in Obsidian on Android:

1. **Attachment Location**: In Settings -> Files & Links, set "Default location for new attachments" to "same folder as current file".
   - *Why*: `lw ingest` resolves image links like `![[img]]` relative to the note beside it.
2. **QuickAdd Capture**: Set up a QuickAdd capture macro to prompt for amount, currency, category, and description, appending a formatted line to today's ledger file at `ledger/YYYY/MM/YYYY-MM-DD.md`.

## Receipts

Receipts stored under `ledger/receipts/` are not synced by `lw sync` until S2. Syncthing is their only transport for now.

The managed `.gitignore` contains `/ledger/receipts/` to prevent large binary receipt images from cluttering the Git repository. When workflow 074 stage S2 lands, `lw storage` will carry content-addressed receipts across PCs alongside `lw sync`.

## What lw does not do

- `ledger/` is never compiled into the wiki and is invisible to `lw ask`/ingest tools by default.
- No totals yet (S4 introduces `lw ledger check` and `lw ledger sum` for validation and aggregation).
- Double-entry accounting is out of scope for v1.
