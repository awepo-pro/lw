# Mobile Notes and Bookkeeping Setup

This guide explains how to set up an Android phone for capturing quick notes and recording daily expenses directly into your `lw` vault using Obsidian. The setup uses `lw hub`, an always-on sync service running over your Tailscale tailnet.

## Vault Layout

Your vault uses dedicated root directories for mobile capture outside the compiled wiki:

- `notes/`: Quick text captures and inbox notes (from `lw note` or Obsidian).
- `ledger/YYYY/MM/YYYY-MM-DD.md`: Daily plain-text expense ledgers.
- `ledger/receipts/YYYY/MM/`: Local receipt images associated with transactions.

Both `notes/` and `ledger/` are kept outside `wiki/` and `raw/`, meaning they are not indexed or compiled into wiki pages. When a note from `notes/` is ingested into the wiki (via `lw ingest --note <id>` or `lw ingest notes/<file>.md`), it lands in `raw/notes/` (074 S3).

## Bookkeeping

Daily notes and money tracking are explained in [docs/bookkeeping.md](bookkeeping.md), including day note line formats, the central `bookkeeping.md` ledger, reporting commands, and legacy Grammar v1 syntax.

## Phone sync with lw hub

`lw hub` provides built-in phone synchronization over your private Tailscale network (tailnet).

### How it fits

The phone runs Obsidian with the "lw sync" plugin. It talks HTTPS directly to your hub host over Tailscale:

```
phone (Obsidian + lw sync)  --HTTPS over tailnet-->  tailscale serve (hub host)  -->  lw hub serve (127.0.0.1:8779)  -->  hub vault clone  --lw sync (git)-->  remote vault  -->  every PC
```

- **Always-on host**: The hub runs on an always-on machine on your tailnet that can reach your vault's remote. No PC needs to be on.
- **No tooling on the phone**: The phone runs no git and no `lw`.
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

- **Trap**: When the hub host has another lw vault or its own `[sync]` config, run the hub with its own config directory (`XDG_CONFIG_HOME=~/.config/lw-hub`, also as `Environment=XDG_CONFIG_HOME=/home/you/.config/lw-hub` in the systemd unit) so the two never share `[sync] remotes`.

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
8. Set Obsidian's attachment location: in **Settings** > **Files & Links**, set "Default location for new attachments" to "Same folder as current file" (so pasted images sit beside the note that embeds them).
9. Enable Obsidian's core **Daily notes** plugin (**Settings** > **Core plugins** > **Daily notes**). Set the date format to `YYYY-MM-DD` and new file location to the vault root (default). The phone writes day notes here.

*Note on BRAT (planned)*: Once the `lw` repository is published with a release, Obsidian's BRAT plugin will be able to install and update the plugin directly from `github.com/awepo-pro/lw`. Until then, install by sideloading the two files.

### Daily use

The phone writes day notes directly in Obsidian using the core Daily notes plugin (date format `YYYY-MM-DD` at the vault root). Type expenses or paste receipt photos; `lw ledger collect` on your PC gathers and archives them into the central ledger.

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
- **Daily notes root location**: Obsidian's Daily Notes core plugin places notes at the vault root by default (date format `YYYY-MM-DD`). This is the expected location: `lw ledger collect` reads `YYYY-MM-DD.md` files at the vault root and moves them into `ledger/YYYY/MM/` automatically.
- **Global remote trap**: `lw sync` always uses the remote configured in the global configuration file (`~/.config/lw/config.toml` under `[sync] remotes`), for every vault on that machine. If you test or scratch-test a vault with plain `lw sync`, it will sync against your production remote! Always set `XDG_CONFIG_HOME=<scratch-config-dir>` when experimenting with test vaults.
- **Pointing at a different hub**: Before pointing the plugin at a different hub, delete the old phone vault or the plugin's `data.json`.

## Receipts

The managed `.gitignore` contains `/ledger/receipts/` to prevent large binary receipt images from cluttering the Git repository. Receipts delivered to `ledger/receipts/` via the hub are copied into `storage/` using `lw ledger import`:

```bash
lw ledger import
```

This stores receipts under `receipts/<path>` in content-addressed storage, where `lw sync` carries them across PCs. The original files in `ledger/receipts/` remain untouched.

## Ledger Verification and Totals

Verification and reporting commands (`lw ledger check`, `lw ledger sum`, `lw ledger report`, `lw ledger collect`) are documented in [docs/bookkeeping.md](bookkeeping.md).

## What lw does not do

- The phone has no `lw` and no git: it cannot ingest, review, ask, or push to the remote by itself.
- `ledger/` is never compiled into the wiki and is invisible to `lw ask`/ingest tools by default.
- Double-entry accounting is out of scope for v1.
