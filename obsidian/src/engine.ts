import { checkPath } from "./scope";
import { sha256hex } from "./hash";
import {
  HubClient,
  HubError,
  NetworkError,
} from "./hubclient";
import {
  CODE_DEVICE_PENDING,
  PROTOCOL_VERSION,
  type Changes,
  type PutResult,
} from "./protocol";

export interface VaultIO {
  list(): Promise<string[]>; // in-scope paths only
  read(p: string): Promise<Uint8Array | null>;
  write(p: string, bytes: Uint8Array): Promise<void>;
  remove(p: string): Promise<void>;
  exists(p: string): Promise<boolean>;
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
  base: string;
  entries: Op[];
}

export class SyncEngine {
  constructor(public readonly options: SyncEngineOptions) {}

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

  queuePut(path: string, base?: string): void {
    if (!checkPath(path).ok) {
      return;
    }
    let resolvedBase = base;
    if (resolvedBase === undefined) {
      const lastOpForPath = [...this.options.state.queue].reverse().find((q) => q.path === path);
      if (lastOpForPath && lastOpForPath.type === "del") {
        resolvedBase = "new";
      } else {
        resolvedBase = this.options.state.files[path] || "new";
      }
    }
    this.options.state.queue.push({
      type: "put",
      path,
      base: resolvedBase,
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
            base: op.base !== undefined ? op.base : (this.options.state.files[op.path] || "new"),
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
        await this.options.vault.write(res.conflict_path, bytes);
        this.options.state.files[res.conflict_path] = localSha;
      }
      this.options.state.files[path] = localSha;
      await this.saveState();

      const hubFile = await this.options.client.getFile(path);
      await this.options.vault.write(path, hubFile.data);
      this.options.state.files[path] = hubFile.sha256;
      await this.saveState();
    } else {
      if (res.result === "merged") {
        this.options.onNotice?.(`Merged ${path}`);
      }
      if (res.sha256 !== localSha) {
        const hubFile = await this.options.client.getFile(path);
        this.options.state.files[path] = localSha;
        await this.saveState();
        await this.options.vault.write(path, hubFile.data);
        this.options.state.files[path] = res.sha256;
        await this.saveState();
      } else {
        this.options.state.files[path] = res.sha256;
        await this.saveState();
      }
    }

    return res;
  }

  async flush(): Promise<void> {
    let lastError = "";
    let canReportStatus = false;

    try {
      this.setStatus("lw: syncing...");

      // 1. Check Hello
      let hello;
      try {
        hello = await this.options.client.hello();
      } catch (err) {
        if (err instanceof HubError && err.code === CODE_DEVICE_PENDING) {
          const slug = toDeviceSlug(this.options.client.options.deviceName);
          this.setStatus(`lw: waiting for approval (run: lw hub approve ${slug})`);
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
              const localSha = await sha256hex(bytes);
              const recorded = this.options.state.files[op.path];

              if (recorded !== undefined && localSha === recorded && op.base !== "new") {
                // F6 echo: skip request
              } else {
                const base = op.base || recorded || "new";
                await this.uploadAndSettle(op.path, base, bytes);
              }
            }
          } else if (op.type === "del") {
            const base = this.options.state.files[op.path] || op.base;
            const res = await this.options.client.deleteFile(op.path, base);
            if (res.result === "deleted" || res.result === "already_gone") {
              delete this.options.state.files[op.path];
            } else if (res.result === "refused_changed") {
              const hubFile = await this.options.client.getFile(op.path);
              await this.options.vault.write(op.path, hubFile.data);
              this.options.state.files[op.path] = hubFile.sha256;
            }
          }

          coalesced.shift();
          const entrySet = new Set(op.entries);
          this.options.state.queue = this.options.state.queue.filter((q) => !entrySet.has(q));
          await this.saveState();
        } catch (err) {
          if (err instanceof HubError && err.code === CODE_DEVICE_PENDING) {
            const slug = toDeviceSlug(this.options.client.options.deviceName);
            this.setStatus(`lw: waiting for approval (run: lw hub approve ${slug})`);
            return;
          }
          throw err;
        }
      }

      // 3. Pull Changes
      await this.pull(this.options.state.head);

      this.setStatus("lw: synced");
    } catch (err: unknown) {
      if (err instanceof HubError && err.code === CODE_DEVICE_PENDING) {
        const slug = toDeviceSlug(this.options.client.options.deviceName);
        this.setStatus(`lw: waiting for approval (run: lw hub approve ${slug})`);
        return;
      }
      lastError = err instanceof Error ? err.message : String(err);
      if (err instanceof NetworkError) {
        const n = this.options.state.queue.length;
        this.setStatus(`lw: offline (${n} queued)`);
      } else {
        this.setStatus(`lw: error: ${lastError}`);
      }
      throw err;
    } finally {
      if (canReportStatus) {
        try {
          await this.options.client.postStatus({
            plugin_version: this.options.pluginVersion || "0.0.0",
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

  private async pull(since: string): Promise<void> {
    const changes: Changes = await this.options.client.changes(since);

    if (changes.full) {
      await this.reconcileFull(changes);
      this.options.state.head = changes.head;
      await this.saveState();
      return;
    }

    for (const c of changes.changed) {
      const localBytes = await this.options.vault.read(c.path);

      if (c.sha256 === null) {
        if (!localBytes) {
          delete this.options.state.files[c.path];
          continue;
        }
        const localSha = await sha256hex(localBytes);
        const recorded = this.options.state.files[c.path];

        if (localSha === recorded) {
          await this.options.vault.remove(c.path);
          delete this.options.state.files[c.path];
        } else {
          await this.uploadAndSettle(c.path, recorded || "new", localBytes);
        }
      } else {
        if (!localBytes) {
          const hubFile = await this.options.client.getFile(c.path);
          await this.options.vault.write(c.path, hubFile.data);
          this.options.state.files[c.path] = hubFile.sha256;
          continue;
        }

        const localSha = await sha256hex(localBytes);
        const recorded = this.options.state.files[c.path];

        if (localSha === c.sha256) {
          this.options.state.files[c.path] = c.sha256;
        } else if (recorded !== undefined && localSha === recorded) {
          const hubFile = await this.options.client.getFile(c.path);
          await this.options.vault.write(c.path, hubFile.data);
          this.options.state.files[c.path] = hubFile.sha256;
        } else {
          await this.uploadAndSettle(c.path, recorded || "new", localBytes);
        }
      }
    }

    this.options.state.head = changes.head;
    await this.saveState();
  }

  private async reconcileFull(changes: Changes): Promise<void> {
    const files = changes.files || [];
    const manifestMap = new Map(files.map((f) => [f.path, f.sha256]));

    for (const f of files) {
      const exists = await this.options.vault.exists(f.path);
      if (!exists) {
        const dl = await this.options.client.getFile(f.path);
        await this.options.vault.write(f.path, dl.data);
        this.options.state.files[f.path] = dl.sha256;
        continue;
      }

      const localBytes = await this.options.vault.read(f.path);
      if (!localBytes) {
        const dl = await this.options.client.getFile(f.path);
        await this.options.vault.write(f.path, dl.data);
        this.options.state.files[f.path] = dl.sha256;
        continue;
      }

      const localSha = await sha256hex(localBytes);
      const recorded = this.options.state.files[f.path];

      if (localSha === f.sha256) {
        this.options.state.files[f.path] = f.sha256;
      } else if (recorded !== undefined && localSha === recorded) {
        const dl = await this.options.client.getFile(f.path);
        await this.options.vault.write(f.path, dl.data);
        this.options.state.files[f.path] = dl.sha256;
      } else {
        const putRes = await this.uploadAndSettle(f.path, recorded || "new", localBytes);
        if (putRes.conflict_path) {
          manifestMap.set(putRes.conflict_path, localSha);
        }
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
        await this.options.vault.remove(p);
        delete this.options.state.files[p];
      } else {
        const putRes = await this.uploadAndSettle(p, recorded || "new", bytes);
        if (putRes.conflict_path) {
          manifestMap.set(putRes.conflict_path, localSha);
        }
      }
    }
  }
}
