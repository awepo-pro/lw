import { checkPath } from "./scope";
import { sha256hex } from "./hash";
import {
  HubClient,
  HubError,
  NetworkError,
} from "./hubclient";
import {
  CODE_BAD_REQUEST,
  CODE_CASE_COLLISION,
  CODE_DEVICE_PENDING,
  CODE_OUT_OF_SCOPE,
  CODE_TOO_LARGE,
  MAX_BODY_BYTES,
  PROTOCOL_VERSION,
  type Changes,
  type PutResult,
} from "./protocol";
import {
  decideNotice,
  formatStatusText,
  type StatusSummary,
} from "./status";

export interface VaultIO {
  list(): Promise<string[]>; // in-scope paths only
  read(p: string): Promise<Uint8Array | null>;
  write(p: string, bytes: Uint8Array): Promise<void>;
  remove(p: string): Promise<void>;
  exists(p: string): Promise<boolean>;
  writeIfUnchanged(p: string, expectedSha: string | null, bytes: Uint8Array): Promise<boolean>;
  removeIfUnchanged(p: string, expectedSha: string): Promise<boolean>;
}

export type Op =
  | { type: "put"; path: string; base?: string }
  | { type: "del"; path: string; base: string };

export interface State {
  head: string;
  files: Record<string, string>; // path -> recorded sha
  queue: Op[];
  deviceId: string;
}

export interface SyncEngineOptions {
  vault: VaultIO;
  client: HubClient;
  state: State;
  saveState?: (state: State) => Promise<void>;
  onNotice?: (msg: string) => void;
  onStatusChange?: (status: string) => void;
  pluginVersion?: string;
  manifest?: { version: string };
  isMobile?: boolean;
}

export function toDeviceSlug(name: string): string {
  const slug = name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 24);
  return slug || "device";
}

interface CoalescedOp {
  type: "put" | "del";
  path: string;
  base?: string;
  entries: Op[];
}

export class SyncEngine {
  hasSyncedOnce = false;
  lastSyncTime: string | null = null;
  lastError: string = "";

  constructor(public readonly options: SyncEngineOptions) {}

  getPluginVersion(): string {
    return this.options.manifest?.version || this.options.pluginVersion || "0.0.0";
  }

  getStatusSummary(): StatusSummary {
    return {
      pluginVersion: this.getPluginVersion(),
      lastSyncTime: this.lastSyncTime,
      queueLength: this.options.state.queue.length,
      lastError: this.lastError || "none",
    };
  }

  getStatusText(): string {
    return formatStatusText(this.getStatusSummary());
  }

  private notify(notice: string | null): void {
    if (notice && this.options.onNotice) {
      this.options.onNotice(notice);
    }
  }

  private async saveState(): Promise<void> {
    if (this.options.saveState) {
      await this.options.saveState(this.options.state);
    }
  }

  private setStatus(status: string): void {
    if (this.options.onStatusChange) {
      this.options.onStatusChange(status);
    }
  }

  queuePut(path: string, _base?: string): void {
    if (!checkPath(path).ok) {
      return;
    }
    this.options.state.queue.push({
      type: "put",
      path,
    });
    void this.saveState();
  }

  queueDel(path: string, base: string): void {
    if (!checkPath(path).ok) {
      return;
    }
    this.options.state.queue.push({
      type: "del",
      path,
      base,
    });
    void this.saveState();
  }

  queueRename(oldPath: string, newPath: string): void {
    if (checkPath(newPath).ok) {
      this.options.state.queue.push({
        type: "put",
        path: newPath,
        base: "new",
      });
    }
    if (checkPath(oldPath).ok) {
      const recorded = this.options.state.files[oldPath];
      if (recorded) {
        this.options.state.queue.push({
          type: "del",
          path: oldPath,
          base: recorded,
        });
      }
    }
    void this.saveState();
  }

  queueFolderDelete(prefix: string): void {
    const pfx = prefix.endsWith("/") ? prefix : `${prefix}/`;
    const paths = Object.keys(this.options.state.files).sort();
    for (const p of paths) {
      if (p.startsWith(pfx)) {
        const recorded = this.options.state.files[p];
        this.queueDel(p, recorded);
      }
    }
  }

  queueFolderRename(oldPrefix: string, newPrefix: string): void {
    const oldPfx = oldPrefix.endsWith("/") ? oldPrefix : `${oldPrefix}/`;
    const newPfx = newPrefix.endsWith("/") ? newPrefix : `${newPrefix}/`;
    const paths = Object.keys(this.options.state.files).sort();
    for (const p of paths) {
      if (p.startsWith(oldPfx)) {
        const suffix = p.slice(oldPfx.length);
        const newPath = newPfx + suffix;
        this.queuePut(newPath);
        const recorded = this.options.state.files[p];
        this.queueDel(p, recorded);
      }
    }
  }

  private coalesceQueue(): CoalescedOp[] {
    const queue = this.options.state.queue;
    const result: CoalescedOp[] = [];
    const activePutByPath = new Map<string, CoalescedOp>();

    for (const op of queue) {
      if (op.type === "put") {
        const existing = activePutByPath.get(op.path);
        if (existing) {
          existing.entries.push(op);
        } else {
          const coalesced: CoalescedOp = {
            type: "put",
            path: op.path,
            entries: [op],
          };
          activePutByPath.set(op.path, coalesced);
          result.push(coalesced);
        }
      } else if (op.type === "del") {
        activePutByPath.delete(op.path);
        const coalesced: CoalescedOp = {
          type: "del",
          path: op.path,
          base: op.base,
          entries: [op],
        };
        result.push(coalesced);
      }
    }
    return result;
  }

  private async uploadAndSettle(
    path: string,
    base: string,
    bytes: Uint8Array
  ): Promise<PutResult> {
    const localSha = await sha256hex(bytes);
    const res: PutResult = await this.options.client.putFile(path, base, bytes);

    if (res.result === "conflict_copy") {
      if (res.conflict_path) {
        this.options.onNotice?.(`Conflict on ${path}: saved to ${res.conflict_path}`);
        const okConflict = await this.options.vault.writeIfUnchanged(res.conflict_path, null, bytes);
        if (okConflict) {
          this.options.state.files[res.conflict_path] = localSha;
        } else {
          this.queuePut(res.conflict_path);
        }
      }
      const hubFile = await this.options.client.getFile(path);
      const ok = await this.options.vault.writeIfUnchanged(path, localSha, hubFile.data);
      if (ok) {
        this.options.state.files[path] = hubFile.sha256;
      } else {
        this.queuePut(path);
      }
      await this.saveState();
    } else if (res.result === "merged") {
      this.options.onNotice?.(`Merged ${path}`);
      const hubFile = await this.options.client.getFile(path);
      const ok = await this.options.vault.writeIfUnchanged(path, localSha, hubFile.data);
      if (ok) {
        this.options.state.files[path] = res.sha256;
      } else {
        this.queuePut(path);
      }
      await this.saveState();
    } else {
      this.options.state.files[path] = res.sha256;
      await this.saveState();
    }

    return res;
  }

  async flush(): Promise<void> {
    let lastError = "";
    let canReportStatus = false;
    let filesSynced = 0;

    try {
      this.setStatus("lw: syncing...");

      // 0. Check empty hub URL
      if (!this.options.client.options.baseUrl || !this.options.client.options.baseUrl.trim()) {
        this.setStatus("lw: set the hub URL in settings");
        const notice = decideNotice(
          { kind: "empty_hub_url" },
          { isMobile: !!this.options.isMobile, hasSyncedOnce: this.hasSyncedOnce }
        );
        this.notify(notice);
        return;
      }

      // 1. Check Hello
      let hello;
      try {
        hello = await this.options.client.hello();
      } catch (err) {
        if (err instanceof HubError && err.code === CODE_DEVICE_PENDING) {
          const slug = toDeviceSlug(this.options.client.options.deviceName);
          this.setStatus(`lw: waiting for approval (run: lw hub approve ${slug})`);
          const notice = decideNotice(
            { kind: "waiting_approval", slug },
            { isMobile: !!this.options.isMobile, hasSyncedOnce: this.hasSyncedOnce }
          );
          this.notify(notice);
          return;
        }
        throw err;
      }

      if (hello.protocol !== PROTOCOL_VERSION) {
        this.setStatus(`lw: unsupported protocol version ${hello.protocol}, expected ${PROTOCOL_VERSION}`);
        return;
      }

      // Approved and protocol matches
      canReportStatus = true;

      // 2. Process Queued Ops
      const coalesced = this.coalesceQueue();

      while (coalesced.length > 0) {
        const op = coalesced[0];

        try {
          if (op.type === "put") {
            const bytes = await this.options.vault.read(op.path);
            if (bytes !== null) {
              if (bytes.length > MAX_BODY_BYTES) {
                const reason = CODE_TOO_LARGE;
                lastError = `${op.path}: ${reason}`;
                this.options.onNotice?.(`${op.path}: ${reason}`);
                coalesced.shift();
                const entrySet = new Set(op.entries);
                this.options.state.queue = this.options.state.queue.filter((q) => !entrySet.has(q));
                await this.saveState();
                continue;
              }

              const localSha = await sha256hex(bytes);
              const recorded = this.options.state.files[op.path];

              if (recorded !== undefined && localSha === recorded) {
                // skip already in sync
              } else {
                const base = recorded !== undefined ? recorded : "new";
                await this.uploadAndSettle(op.path, base, bytes);
                filesSynced++;
              }
            }
          } else if (op.type === "del") {
            const base = this.options.state.files[op.path] || op.base || "";
            const res = await this.options.client.deleteFile(op.path, base);
            if (res.result === "deleted" || res.result === "already_gone") {
              delete this.options.state.files[op.path];
              filesSynced++;
            } else if (res.result === "refused_changed") {
              const hubFile = await this.options.client.getFile(op.path);
              const ok = await this.options.vault.writeIfUnchanged(op.path, null, hubFile.data);
              if (ok) {
                this.options.state.files[op.path] = hubFile.sha256;
              } else {
                this.queuePut(op.path);
              }
              filesSynced++;
            }
          }

          coalesced.shift();
          const entrySet = new Set(op.entries);
          this.options.state.queue = this.options.state.queue.filter((q) => !entrySet.has(q));
          await this.saveState();
        } catch (err) {
          if (
            err instanceof HubError &&
            (err.code === CODE_TOO_LARGE ||
              err.code === CODE_OUT_OF_SCOPE ||
              err.code === CODE_BAD_REQUEST ||
              err.code === CODE_CASE_COLLISION)
          ) {
            const reason = err.code;
            lastError = `${op.path}: ${reason}`;
            this.options.onNotice?.(`${op.path}: ${reason}`);
            coalesced.shift();
            const entrySet = new Set(op.entries);
            this.options.state.queue = this.options.state.queue.filter((q) => !entrySet.has(q));
            await this.saveState();
            continue;
          }
          if (err instanceof HubError && err.code === CODE_DEVICE_PENDING) {
            const slug = toDeviceSlug(this.options.client.options.deviceName);
            this.setStatus(`lw: waiting for approval (run: lw hub approve ${slug})`);
            const notice = decideNotice(
              { kind: "waiting_approval", slug },
              { isMobile: !!this.options.isMobile, hasSyncedOnce: this.hasSyncedOnce }
            );
            this.notify(notice);
            return;
          }
          throw err;
        }
      }

      // 3. Pull Changes
      const pulledCount = await this.pull(this.options.state.head);
      filesSynced += pulledCount;

      if (this.options.state.queue.length === 0) {
        this.setStatus("lw: synced");
      } else {
        this.setStatus(`lw: ${this.options.state.queue.length} queued`);
      }

      const notice = decideNotice(
        { kind: "synced", filesSynced },
        { isMobile: !!this.options.isMobile, hasSyncedOnce: this.hasSyncedOnce }
      );
      this.notify(notice);
      this.hasSyncedOnce = true;
      this.lastSyncTime = new Date().toISOString();
      this.lastError = "";
    } catch (err: unknown) {
      if (err instanceof HubError && err.code === CODE_DEVICE_PENDING) {
        const slug = toDeviceSlug(this.options.client.options.deviceName);
        this.setStatus(`lw: waiting for approval (run: lw hub approve ${slug})`);
        const notice = decideNotice(
          { kind: "waiting_approval", slug },
          { isMobile: !!this.options.isMobile, hasSyncedOnce: this.hasSyncedOnce }
        );
        this.notify(notice);
        return;
      }
      lastError = err instanceof Error ? err.message : String(err);
      this.lastError = lastError;
      if (err instanceof NetworkError) {
        const n = this.options.state.queue.length;
        this.setStatus(`lw: offline (${n} queued)`);
        const notice = decideNotice(
          { kind: "offline", queued: n },
          { isMobile: !!this.options.isMobile, hasSyncedOnce: this.hasSyncedOnce }
        );
        this.notify(notice);
      } else {
        this.setStatus(`lw: error: ${lastError}`);
        if (err instanceof HubError) {
          const notice = decideNotice(
            { kind: "hub_error", code: err.code },
            { isMobile: !!this.options.isMobile, hasSyncedOnce: this.hasSyncedOnce }
          );
          this.notify(notice);
        }
      }
      throw err;
    } finally {
      if (canReportStatus) {
        try {
          await this.options.client.postStatus({
            plugin_version: this.getPluginVersion(),
            last_error: lastError,
            queue_len: this.options.state.queue.length,
            at: new Date().toISOString(),
          });
        } catch {
          // Ignore status reporting errors
        }
      }
    }
  }

  private async pull(since: string): Promise<number> {
    const changes: Changes = await this.options.client.changes(since);
    let filesSynced = 0;

    if (changes.full) {
      filesSynced = await this.reconcileFull(changes);
      this.options.state.head = changes.head;
      await this.saveState();
      return filesSynced;
    }

    for (const c of changes.changed) {
      if (this.options.state.queue.some((q) => q.path === c.path)) {
        continue;
      }
      const localBytes = await this.options.vault.read(c.path);

      if (c.sha256 === null) {
        if (!localBytes) {
          delete this.options.state.files[c.path];
          continue;
        }
        const localSha = await sha256hex(localBytes);
        const recorded = this.options.state.files[c.path];

        if (localSha === recorded) {
          delete this.options.state.files[c.path];
          const ok = await this.options.vault.removeIfUnchanged(c.path, localSha);
          if (!ok) {
            this.options.state.files[c.path] = localSha;
            this.queuePut(c.path);
          }
          filesSynced++;
        } else {
          await this.uploadAndSettle(c.path, recorded || "new", localBytes);
          filesSynced++;
        }
      } else {
        if (!localBytes) {
          const hubFile = await this.options.client.getFile(c.path);
          const ok = await this.options.vault.writeIfUnchanged(c.path, null, hubFile.data);
          if (ok) {
            this.options.state.files[c.path] = hubFile.sha256;
          } else {
            this.queuePut(c.path);
          }
          filesSynced++;
          continue;
        }

        const localSha = await sha256hex(localBytes);
        const recorded = this.options.state.files[c.path];

        if (localSha === c.sha256) {
          this.options.state.files[c.path] = c.sha256;
        } else if (recorded !== undefined && localSha === recorded) {
          const hubFile = await this.options.client.getFile(c.path);
          const ok = await this.options.vault.writeIfUnchanged(c.path, localSha, hubFile.data);
          if (ok) {
            this.options.state.files[c.path] = hubFile.sha256;
          } else {
            this.queuePut(c.path);
          }
          filesSynced++;
        } else {
          await this.uploadAndSettle(c.path, recorded || "new", localBytes);
          filesSynced++;
        }
      }
    }

    this.options.state.head = changes.head;
    await this.saveState();
    return filesSynced;
  }

  private async reconcileFull(changes: Changes): Promise<number> {
    const files = changes.files || [];
    const manifestMap = new Map(files.map((f) => [f.path, f.sha256]));
    let filesSynced = 0;

    for (const f of files) {
      if (this.options.state.queue.some((q) => q.path === f.path)) {
        continue;
      }
      const localBytes = await this.options.vault.read(f.path);
      if (!localBytes) {
        const dl = await this.options.client.getFile(f.path);
        const ok = await this.options.vault.writeIfUnchanged(f.path, null, dl.data);
        if (ok) {
          this.options.state.files[f.path] = dl.sha256;
        } else {
          this.queuePut(f.path);
        }
        filesSynced++;
        continue;
      }

      const localSha = await sha256hex(localBytes);
      const recorded = this.options.state.files[f.path];

      if (localSha === f.sha256) {
        this.options.state.files[f.path] = f.sha256;
      } else if (recorded !== undefined && localSha === recorded) {
        const dl = await this.options.client.getFile(f.path);
        const ok = await this.options.vault.writeIfUnchanged(f.path, localSha, dl.data);
        if (ok) {
          this.options.state.files[f.path] = dl.sha256;
        } else {
          this.queuePut(f.path);
        }
        filesSynced++;
      } else {
        const putRes = await this.uploadAndSettle(f.path, recorded || "new", localBytes);
        if (putRes.conflict_path) {
          manifestMap.set(putRes.conflict_path, localSha);
        }
        filesSynced++;
      }
    }

    const localPaths = await this.options.vault.list();
    for (const p of localPaths) {
      if (manifestMap.has(p)) {
        continue;
      }
      const recorded = this.options.state.files[p];
      const bytes = await this.options.vault.read(p);
      if (!bytes) {
        continue;
      }
      const localSha = await sha256hex(bytes);
      if (recorded !== undefined && localSha === recorded) {
        delete this.options.state.files[p];
        const ok = await this.options.vault.removeIfUnchanged(p, localSha);
        if (!ok) {
          this.options.state.files[p] = localSha;
          this.queuePut(p);
        }
        filesSynced++;
      } else {
        const putRes = await this.uploadAndSettle(p, recorded || "new", bytes);
        if (putRes.conflict_path) {
          manifestMap.set(putRes.conflict_path, localSha);
        }
        filesSynced++;
      }
    }
    return filesSynced;
  }

  private syncInProgress = false;
  private syncPending = false;

  async triggerSync(): Promise<void> {
    if (this.syncInProgress) {
      this.syncPending = true;
      return;
    }
    this.syncInProgress = true;
    try {
      do {
        this.syncPending = false;
        try {
          await this.flush();
        } catch {
          // errors are surfaced through engine status
        }
      } while (this.syncPending);
    } finally {
      this.syncInProgress = false;
    }
  }
}

export function handleVaultDeleteEvent(
  engine: SyncEngine,
  file: { path: string },
  isFile: boolean
): void {
  if (isFile) {
    if (checkPath(file.path).ok) {
      const recorded = engine.options.state.files[file.path];
      if (recorded) {
        engine.queueDel(file.path, recorded);
      }
    }
  } else {
    engine.queueFolderDelete(file.path);
  }
}

export function handleVaultRenameEvent(
  engine: SyncEngine,
  file: { path: string },
  oldPath: string,
  isFile: boolean
): void {
  if (isFile) {
    engine.queueRename(oldPath, file.path);
  } else {
    engine.queueFolderRename(oldPath, file.path);
  }
}

export interface DirectoryListing {
  files: string[];
  folders: string[];
}

export interface DirectoryAdapter {
  list(folder: string): Promise<DirectoryListing>;
}

export async function listRecursively(adapter: DirectoryAdapter, folder: string): Promise<string[]> {
  const result: string[] = [];
  try {
    const listing = await adapter.list(folder);
    for (const f of listing.files) {
      const normalized = f.replace(/\\/g, "/");
      if (checkPath(normalized).ok) {
        result.push(normalized);
      }
    }
    for (const sub of listing.folders) {
      const normalizedSub = sub.replace(/\\/g, "/");
      const parts = normalizedSub.split("/").filter((p) => p.length > 0);
      if (parts.some((p) => p.startsWith("."))) {
        continue;
      }
      if (folder === "" || folder === ".") {
        const rootFolder = parts[0];
        if (rootFolder !== "notes" && rootFolder !== "ledger") {
          continue;
        }
      }
      const children = await listRecursively(adapter, sub);
      result.push(...children);
    }
  } catch {
    // ignore
  }
  return result;
}
