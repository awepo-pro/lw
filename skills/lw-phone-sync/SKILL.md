---
name: lw-phone-sync
description: >-
  Teach and maintain phone sync for the user's lw vault using lw hub and Obsidian over Tailscale. Use when the user says
  "set up phone sync", "sync my phone", "my phone isn't syncing", "lw hub", "check my hub", "any conflicts?",
  "approve my phone", or "phone sync status". Works in any agent with a shell (Claude Code, Codex, Antigravity).
---

# lw-phone-sync — teach and maintain phone sync over Tailscale

`lw hub` provides built-in phone synchronization for `lw` vaults over your private Tailscale network (tailnet).
Obsidian on your phone uses the "lw sync" plugin to talk HTTPS to an always-on host running `lw hub serve`.
The hub commits to its own clone of the vault and pushes to your remote via git, reaching every PC.
The phone runs no git, no `lw`, and no Syncthing; only notes, ledger day files, receipts, and root notes sync.
No PC needs to be running; only the always-on hub host and Tailscale must be active.

```
phone (Obsidian + lw sync)  --HTTPS over tailnet-->  tailscale serve (hub host)  -->  lw hub serve (127.0.0.1:8779)  -->  hub vault clone  --lw sync-->  remote vault  -->  every PC
```

## Rules you must not break

1. **Never approve a device the user has not confirmed is theirs.** Show its `NAME`, `ADDR`, and `LAST SEEN` from `lw hub devices` and ask the user to confirm before running `lw hub approve <name>`.
2. **Never delete a conflict copy or edit a note without the user's explicit yes, and never merge by guessing.** Show both versions (or a diff) side by side and let the user choose what to keep.
3. **Never run `tailscale serve`, `systemctl`, `loginctl`, or write the systemd unit without the user's yes.** These commands change system configuration outside the vault; print the exact commands and file paths and ask first.
4. **Never edit `hub-devices.json` or anything in `.git/`.** Device approvals and registrations must go through `lw hub approve` and `lw hub revoke`. Internal git state belongs to git and `lw sync`.
5. **Never run `lw sync --take-remote` without the user's yes.** It makes the vault match the remote; the vault's own commits are not deleted but moved to a branch `lw-diverged-<time>`, so they disappear from the files the user sees. Explain the divergence and confirm before running it.
6. **Never put a real vault under a test config or a test vault under the real config.** `lw sync` reads `[sync] remotes` from global configuration (`~/.config/lw/config.toml`). When testing with a scratch vault, always set `XDG_CONFIG_HOME=<scratch-dir>`.

## Procedure A: Check and remind

Use this default procedure whenever the user asks about sync status or how their hub is running. Run on the hub host (or via `ssh <hub-host> lw hub status` and `ssh <hub-host> lw hub devices` from a PC; ask which host if unknown). A non-interactive ssh shell often lacks `lw` on `PATH`; if `lw: command not found`, use the full path (`ssh <hub-host> ~/go/bin/lw hub status`, or whatever `command -v lw` prints in an interactive shell there):

```bash
lw hub status [--vault <dir>]
lw hub devices
```

Inspect output and report, in this exact order, only items that require user attention:

1. **Diverged hub**: If `lw hub status` reports `diverged`, explain that manual commits in the hub's vault clone stopped writes. Offer the fix:
   ```bash
   lw sync --vault <dir>
   ```
   (and, only if refused, `lw sync --take-remote --vault <dir>` with the user's consent).
2. **Pending devices**: If devices show state `pending`, show their `NAME`, `ADDR`, and `LAST SEEN`. Ask the user to confirm if the device is theirs, then approve:
   ```bash
   lw hub approve <name>
   ```
3. **Conflicts**: If `conflicts: <n>` appears, list the paths. Offer to show each conflicting pair side by side and guide manual resolution (Procedure C).
4. **Device errors**: If a device's `LAST ERROR` is not `-`, explain the error and suggest the fix from the Troubleshooting table.
5. **Stale devices**: If an approved device has not been seen for more than 7 days, remind the user (Tailscale disconnected? Obsidian mobile not opened?).

If nothing needs attention, report in one line:
`Hub is healthy: remote in sync, devices approved, no conflicts.`
End your response with at most three actionable reminders.

## Procedure B: Set up (host then phone)

Follow these steps one at a time, confirming each step before proceeding to the next.

### Host setup (on the always-on machine)

The machine must be on your tailnet and able to access the vault remote.

1. **Clone the vault**:
   ```bash
   lw sync clone <remote> <dir>
   ```
   Confirm that `~/.config/lw/config.toml` on the host contains `[sync] remotes = ["<remote>"]`.
2. **Add hub configuration**:
   In `~/.config/lw/config.toml` on the hub host, add:
   ```toml
   [hub]
     port = 8779
     users = ["you@github"]      # your Tailscale login name as shown by tailscale status
   ```
3. **Print and install the service**:
   Run:
   ```bash
   lw hub install --print --vault <dir>
   ```
   Review the printed unit and commands. With user consent, save to `~/.config/systemd/user/lw-hub.service` and run:
   ```bash
   tailscale serve --bg --https=8779 http://127.0.0.1:8779
   systemctl --user daemon-reload
   systemctl --user enable --now lw-hub.service
   loginctl enable-linger <user>
   ```
4. **Verify host status**:
   ```bash
   lw hub status --vault <dir>
   ```
   Confirm `port`, `users`, and `remote: in sync`.

### Phone setup (Android)

1. **Install Tailscale**: Install Tailscale on the phone, log in to the same tailnet, and keep it active.
2. **Open vault folder**: In Obsidian on Android, open your vault folder (an empty folder is fine; files download on first sync).
3. **Sideload the plugin**: Copy `main.js` (built in `obsidian/dist/main.js`) and `manifest.json` (repository root) into `<vault>/.obsidian/plugins/lw-sync/`. (Installation via the Obsidian BRAT plugin is planned for after repository release.)
4. **Enable plugin**: In Obsidian > Settings > Community plugins, enable community plugins and toggle on **lw sync**.
5. **Configure settings**:
   - **Hub URL**: `https://<hub-host>.<tailnet>.ts.net:8779`
   - **Device Name**: A recognisable name (e.g. `phone`)
6. **Approve device**: The phone displays `"lw: waiting for approval (run: lw hub approve phone)"`. On the hub host, run:
   ```bash
   lw hub devices
   ```
   Verify `NAME`, `ADDR`, and `LAST SEEN`. Ask user confirmation, then run:
   ```bash
   lw hub approve phone
   ```
7. **First sync**: Obsidian displays `"lw: synced N files"`. Notes and ledger files are now synced.

## Procedure C: Resolve a conflict copy with the user

When concurrent edits occur on the phone and another device, the version reaching the hub first is preserved in the main note, while the other is saved as `<name>.conflict-<device>-<YYYYMMDD-HHMMSS>.md`.

1. **Inspect diff**: Display the difference between the primary note and the conflict file:
   ```bash
   diff -u "<note>" "<conflict-file>"
   ```
2. **Ask what to keep**: Present the changes clearly to the user and ask which lines or sections to retain.
3. **Apply merge**: Edit the primary note with the merged content only after receiving the user's explicit approval.
4. **Delete conflict file**: Remove the conflict copy only after receiving the user's explicit approval:
   ```bash
   rm "<conflict-file>"
   ```
5. **Propagate**: Run `lw sync` on the PC:
   ```bash
   lw sync
   ```
   This commits the resolution and deletes the conflict copy across the remote, hub, and phone.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Phone shows `"lw: waiting for approval"` | Device has registered but is not yet approved on the hub host | Run `lw hub devices` on the hub host, verify the device identity, then run `lw hub approve <name>`. |
| Phone shows `"lw: can't reach the hub (<host>)"` | Tailscale is off, or hub services are not running | Check that Tailscale is connected on phone. On hub host, check `systemctl --user status lw-hub.service` and `tailscale serve status`. |
| Phone shows `"lw: hub error (hub_diverged)"` or `lw hub status` shows `diverged` | Non-hub manual commits in the hub's vault clone diverged from remote | On hub host run `lw sync --vault <dir>`. If refused, run `lw sync --take-remote --vault <dir>` with user consent. |
| Phone shows `"lw: hub error (unauthorized)"` | Tailscale login name is not listed in `[hub] users` | Run `tailscale status` on hub host to check your login name. Add it to `users = [...]` under `[hub]` in `~/.config/lw/config.toml` and restart `lw-hub.service`. |
| A PC does not show a phone note yet | The PC has not run `lw sync`, or phone changes are pending | Run `lw sync` on the PC. On phone, ensure Obsidian is open to trigger sync (within 30 s) or use command palette `lw: Sync now`. |
| Note title created as `name.md.md` | Obsidian appends `.md` extension automatically to titles | Rename the note in Obsidian and leave `.md` out of the title; Obsidian adds it. |
| Daily note appears at vault root | Obsidian Daily Notes plugin defaults to root folder | It syncs, but it is a note, not a ledger entry: do not move it into `ledger/`. Record an expense as a Grammar v1 line (`(amt:: ...)`) in `ledger/YYYY/MM/YYYY-MM-DD.md`. To stop daily notes landing at the root, set Obsidian > Daily notes > New file location (e.g. `notes`); the `lw-notes-sync` skill moves root notes into `notes/` on a PC. |
