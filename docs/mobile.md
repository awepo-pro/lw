# Mobile Notes and Bookkeeping Setup

This guide explains how to set up an Android phone for capturing quick notes and recording daily expenses directly into your `lw` vault using Obsidian. The primary setup uses `lw hub`, an always-on sync service running over your Tailscale tailnet. If you do not run an always-on host, Syncthing is supported as an alternative.

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

## Phone sync with lw hub

`lw hub` provides built-in phone synchronization over your private Tailscale network (tailnet).

### How it fits

The phone runs Obsidian with the "lw sync" plugin. It talks HTTPS directly to your hub host over Tailscale:

```
phone (Obsidian + lw sync)  --HTTPS over tailnet-->  tailscale serve (hub host)  -->  lw hub serve (127.0.0.1:8779)  -->  hub vault clone  --lw sync (git)-->  remote vault  -->  every PC
```

- **Always-on host**: The hub runs on an always-on machine on your tailnet that can reach your vault's remote. No PC needs to be on.
- **No tooling on the phone**: The phone runs no git, no `lw`, and no Syncthing.
- **Authentication**: Access requires Tailscale identity verification (the incoming `Tailscale-User-Login` header must match an entry in `[hub] users`) plus explicit per-device approval by the administrator on the hub host.
- **Sync scope**: The phone syncs `notes/`, `ledger/` day files, `ledger/receipts/`, and loose notes Obsidian creates at the vault root. It never receives or touches `wiki/`, `raw/`, `index.md`, `log.md`, `SCHEMA.md`, `curator-memory.md`, `.llmwiki/`, or `storage/`.

### Host setup

Run these steps on your always-on host. The host must be connected to your tailnet and able to reach your vault remote.

1. **Clone the vault on the hub host**:
   ```bash
   lw sync clone <remote> <dir>
   ```
   For example, if the remote lives locally on that host:
   ```bash
   lw sync clone ~/lw-vault.git ~/lw-hub-vault
   ```
   Ensure the host's `~/.config/lw/config.toml` has `[sync] remotes = ["<remote>"]` pointing to that same remote.

2. **Configure the hub**:
   Add the `[hub]` section to the host's `~/.config/lw/config.toml`:
   ```toml
   [hub]
     port = 8779
     users = ["you@github"]      # your Tailscale login name, as `tailscale status` shows it
   ```

3. **Install and run the service**:
   Generate the systemd user service unit and setup commands with `--print` (this prints the configuration and changes nothing):
   ```bash
   lw hub install --print --vault <dir>
   ```
   Example output:
   ```ini
   # ~/.config/systemd/user/lw-hub.service
   [Unit]
   Description=llmwiki phone sync hub
   After=network.target

   [Service]
   Type=simple
   ExecStart=/home/you/go/bin/lw hub serve --vault /home/you/lw-hub-vault
   Restart=on-failure

   [Install]
   WantedBy=default.target

   # Commands to enable and run:
   tailscale serve --bg --https=8779 http://127.0.0.1:8779
   systemctl --user daemon-reload
   systemctl --user enable --now lw-hub.service
   loginctl enable-linger you
   ```
   Save the unit file to `~/.config/systemd/user/lw-hub.service`, then run the four commands:
   ```bash
   tailscale serve --bg --https=8779 http://127.0.0.1:8779
   systemctl --user daemon-reload
   systemctl --user enable --now lw-hub.service
   loginctl enable-linger you
   ```

4. **Verify hub status**:
   ```bash
   lw hub status --vault <dir>
   ```
   Expected output:
   ```
   port: 8779
   users: awepo-pro@github
   devices: 2 approved, 0 pending
   remote: in sync
   ```
   If active conflict copies exist, `lw hub status` also outputs `conflicts: <n>` followed by indented `  <path>` lines. If the hub is diverged, it outputs a line containing `diverged`.

### Phone setup

Configure your Android device. The plugin is currently sideloaded; BRAT plugin support is planned for after repository release.

1. Install Tailscale on the phone, log in to your tailnet, and keep it connected.
2. In Obsidian on Android, open your vault folder (an empty directory is fine; the plugin downloads all files in scope on first sync).
3. Copy two files into `<vault>/.obsidian/plugins/lw-sync/`:
   - `main.js` (built with `npm run build` in the repo's `obsidian/` folder, output to `obsidian/dist/main.js`)
   - `manifest.json` (from the repository root)
   Transfer these files via USB cable, file manager, or local network download from a PC on the tailnet.
4. In Obsidian: open **Settings** > **Community plugins**, turn on community plugins, and enable **lw sync**.
5. Open the plugin settings:
   - **Hub URL**: `https://<hub-host>.<tailnet>.ts.net:8779` (e.g., `https://vostro.tailef1cc9.ts.net:8779`)
   - **Device Name**: A recognisable name (e.g., `phone`)
6. The phone displays: `"lw: waiting for approval (run: lw hub approve phone)"`. On the hub host, list devices:
   ```bash
   lw hub devices
   ```
   This displays columns: `NAME  STATE  LAST SEEN  ADDR  PLUGIN  LAST ERROR`. Verify the device, then approve it:
   ```bash
   lw hub approve phone
   ```
   Approve only devices you recognise. You can revoke access at any time with `lw hub revoke <name>`.
7. The first sync runs automatically and reports `"lw: synced N files"`.
8. Set Obsidian's attachment location as described in [Obsidian Configuration](#obsidian-configuration) (it applies to the hub path too).

*Note on BRAT (planned)*: Once the `lw` repository is published with a release, Obsidian's BRAT plugin will be able to install and update the plugin directly from `github.com/awepo-pro/lw`. Until then, install by sideloading the two files.

### Daily use

Sync runs automatically in the background without needing manual commands:
- when Obsidian starts
- when Obsidian comes to the foreground
- 3 seconds after any edit
- every 30 seconds while Obsidian is open on screen
- when you open any note

Android pauses apps in the background, so notes committed on a PC appear on the phone within approximately 30 seconds of opening Obsidian.

You can also trigger sync manually from Obsidian's command palette:
- `lw: Sync now`: forces an immediate synchronization cycle.
- `lw: Show status`: displays plugin version, timestamp of last sync, queue length, and last error message. (Obsidian mobile has no status bar, so notices are used.)

### Offline behaviour

When the phone cannot reach the hub (for example, if Tailscale is disconnected):
- Obsidian continues to work normally. Edits are saved locally and queued.
- A single persistent notice appears and stays until tapped:
  `lw: can't reach the hub (<host>). Is Tailscale on? <n> change(s) saved on this phone; they will sync when you're back.`
- No additional error notices pop up while offline.
- Once connectivity is restored, the plugin reconnects automatically and shows a single confirmation:
  `lw: back online - synced <n> change(s)`.

### Conflicts

If the same note is edited on the phone and elsewhere before syncing, no changes are lost:
- The note keeps the version that reached the hub first (usually the PC's version).
- The conflicting version is saved beside it as `<name>.conflict-<device>-<YYYYMMDD-HHMMSS>.md` (timestamp in UTC), for example:
  `notes/from-pc.conflict-phone-20261104-083911.md`
- Resolve the conflict by hand: open both files in Obsidian, merge the desired text into the main note, and delete the `.conflict-*.md` file. Deleting the conflict file syncs across all devices.
- Ledger day files (`ledger/YYYY/MM/YYYY-MM-DD.md`) never create conflict files: transactions from both sides are automatically merged line by line.
- A PC discovers conflict copies on its next `lw sync`, which prints:
  ```
  <n> conflict cop(y|ies) to resolve: merge each into its note, then delete it:
    <path>
  ```
  `lw hub status` on the hub host also lists these conflict copies.

### Diverged hub

Hub divergence is rare. The hub pulls from the remote before every write and automatically heals divergence caused solely by its own automated commits (`hub: ...`): the remote branch takes precedence, the hub's local changes are saved as conflict copies or merged, and the commits are preserved on a branch named `lw-diverged-<time>`.

If someone made manual commits directly inside the hub's vault clone, the hub cannot automatically resolve the divergence:
- The hub stops accepting incoming writes.
- The phone shows an error: `lw: hub error (hub_diverged)`.
- `lw hub status` on the hub host prints a line containing `diverged`.
- To fix it, log in to the hub host and run:
  ```bash
  lw sync --vault <dir>
  ```
  If `lw sync` refuses due to conflicting changes, run:
  ```bash
  lw sync --take-remote --vault <dir>
  ```
  This adopts the remote state as authoritative and archives the hub's local commits on an `lw-diverged-<time>` branch.

### Quirks and traps

- **Double `.md` extension**: Obsidian automatically adds `.md` to file titles. If you name a note `from-phone.md` in Obsidian, it saves as `from-phone.md.md`.
- **Daily notes root location**: Obsidian's Daily Notes core plugin places notes at the vault root by default. These files sync to PCs, but they are not processed by `lw ledger`. Ledger files must reside in `ledger/YYYY/MM/YYYY-MM-DD.md` using Grammar v1 syntax (`(amt:: ...)` lines).
- **Global remote trap**: `lw sync` always uses the remote configured in the global configuration file (`~/.config/lw/config.toml` under `[sync] remotes`), for every vault on that machine. If you test or scratch-test a vault with plain `lw sync`, it will sync against your production remote! Always set `XDG_CONFIG_HOME=<scratch-config-dir>` when experimenting with test vaults.

## Alternative: Syncthing

If you do not run an always-on host or Tailscale, you can sync phone notes using Syncthing and a bridge PC.

### How the pieces fit

The phone runs **Syncthing and Obsidian only: no git, no `lw`, no agent.** It never talks to the remote vault.

```
phone (Obsidian)  --Syncthing-->  PC vault  --lw sync (git over ssh)-->  remote vault  -->  other PCs
  notes/, ledger/                   one PC runs lw-notes-sync / lw sync
```

Syncthing moves `notes/` and `ledger/` between the phone and **one** PC. That PC is the bridge: `lw sync` commits
what arrived and pushes it to the remote, and pulls notes written on your other PCs, which Syncthing then carries
back to the phone. The bridge PC must be on for the phone's notes to reach the remote.

### Syncthing-Fork Setup

Share the vault folder between the bridge PC and the phone (on the phone, an empty folder; Obsidian opens it as a
vault). Create the same vault-root `.stignore` on **both** sides, before the first sync:

```
// lw vault <-> phone (Syncthing). Same file on both devices.
// lw's own root files stay on the PCs
/index.md
/log.md
/SCHEMA.md
/curator-memory.md
// what Obsidian on the phone writes: notes, bookkeeping, and notes/images at the vault root
!/notes
!/ledger
!/*.md
(?i)!/*.png
(?i)!/*.jpg
(?i)!/*.jpeg
(?i)!/*.gif
(?i)!/*.webp
(?i)!/*.svg
(?i)!/*.heic
// everything else (wiki, raw, .obsidian, .git, .llmwiki, storage) stays put
*
```

Syncthing reads the patterns top-down and the first match wins:

- lw's own root files (`index.md`, `log.md`, `SCHEMA.md`, `curator-memory.md`) never reach the phone.
- `notes/`, `ledger/`, and notes and images at the **vault root** are synced. Obsidian creates a note in whatever
  folder you are in and, with the attachment setting below, pastes its images beside it, so on the phone both often
  land at the root. The `lw-notes-sync` skill on the bridge PC then moves them into `notes/` (Syncthing carries the
  move back to the phone).
- Everything else is ignored: `wiki/`, `raw/`, `.obsidian/` (each device keeps its own), `.git/`, `.llmwiki/`.
  A note written in another folder on the phone (say `ideas/`) is **not** synced; keep to the root, `notes/` and
  `ledger/`.

> `.stignore` is not synced by Syncthing, so create it on both sides. Do it **before** the first sync, or Syncthing
> starts by exchanging the whole vault.

Syncthing also creates a `.stfolder/` marker folder at the vault root. Keep it out of git on the bridge PC
(`lw sync` rewrites `.gitignore`, so the PC's own exclude file is the place; the skill does this the first time):

```bash
echo '/.stfolder/' >> "$VAULT/.git/info/exclude"
```

Without that line, `lw sync` (`git add -A`) commits the marker and pushes it to every PC.

### Obsidian Configuration

When opening the synced vault in Obsidian on Android:

1. **Attachment Location**: In Settings -> Files & Links, set "Default location for new attachments" to "Same folder as current file", on the phone and on every PC's Obsidian.
   - *Why*: `lw ingest` resolves image links like `![[img]]` relative to the note. With this setting the image always sits beside its note, so when `lw-notes-sync` moves a root note into `notes/` it can move the image with it. A receipt photo pasted into a ledger day file lands beside it (`ledger/2026/11/`); the skill moves it into `ledger/receipts/2026/11/`, which `lw ledger import` carries.
2. **QuickAdd Capture**: Set up a QuickAdd capture macro to prompt for amount, currency, category, and description, appending a formatted line to today's ledger file at `ledger/YYYY/MM/YYYY-MM-DD.md`.

### Daily loop

| Where | What |
|---|---|
| Phone | Write at the vault root or in `notes/` (finished or not), paste images, add ledger lines and receipt photos. |
| Bridge PC, in an agent | "sync my notes": the `lw-notes-sync` skill moves root notes into `notes/` with their images, receipt photos into `ledger/receipts/`, runs `lw ledger check` + `lw ledger import`, then `lw sync`. It never ingests and never deletes. |
| Bridge PC, by hand | Move root notes and images into `notes/` and receipt photos into `ledger/receipts/YYYY/MM/` yourself, then `lw ledger check && lw ledger import && lw sync`. |
| Any PC, when a note is done | "ingest note <id>" (`lw-ingest` skill) or `lw ingest --note <id>`, then review and commit. |

Install the skills on each PC from the lw repo: `git pull && sh skills/install.sh`.

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

## What lw does not do

- The phone has no `lw` and no git: it cannot ingest, review, ask, or push to the remote by itself.
- `ledger/` is never compiled into the wiki and is invisible to `lw ask`/ingest tools by default.
- Double-entry accounting is out of scope for v1.
