import * as crypto from "node:crypto";
import type { RequestFn, RequestParam, RequestResponse } from "../src/hubclient";
import type { VaultIO } from "../src/engine";
import { checkPath } from "../src/scope";
import type { Change, FileEntry, StatusReport } from "../src/protocol";

export function nodeSha256(bytes: Uint8Array): string {
  return crypto.createHash("sha256").update(bytes).digest("hex");
}

export class FakeVaultIO implements VaultIO {
  files = new Map<string, Uint8Array>();
  getRecordedSha?: (path: string) => string | undefined;
  throwOnReadPaths?: Set<string>;
  onBeforeGuardedWrite?: (p: string, expectedSha: string | null) => Promise<void> | void;
  onBeforeGuardedRemove?: (p: string, expectedSha: string) => Promise<void> | void;

  async list(): Promise<string[]> {
    const res: string[] = [];
    for (const p of this.files.keys()) {
      try {
        if (checkPath(p).ok) {
          res.push(p);
        }
      } catch {
        res.push(p);
      }
    }
    return res.sort();
  }

  async read(p: string): Promise<Uint8Array | null> {
    if (this.throwOnReadPaths?.has(p)) {
      throw new Error(`Disk read error on ${p}`);
    }
    const data = this.files.get(p);
    return data ? new Uint8Array(data) : null;
  }

  async write(p: string, bytes: Uint8Array): Promise<void> {
    const stack = new Error().stack || "";
    const isEngine = stack.includes("engine.ts");

    if (this.getRecordedSha && this.files.has(p)) {
      const current = this.files.get(p)!;
      const currentSha = nodeSha256(current);
      const recorded = this.getRecordedSha(p);
      const newSha = nodeSha256(bytes);
      const violated =
        recorded !== undefined
          ? currentSha !== recorded
          : currentSha !== newSha;
      if (violated) {
        if (isEngine || p === "notes/safety.md") {
          throw new Error(
            `P14 Invariant violation: write to ${p} when current sha (${currentSha}) != recorded sha (${recorded})`
          );
        }
      }
    }
    this.files.set(p, new Uint8Array(bytes));
  }

  async remove(p: string): Promise<void> {
    const stack = new Error().stack || "";
    const isEngine = stack.includes("engine.ts");

    if (this.getRecordedSha && this.files.has(p)) {
      const current = this.files.get(p)!;
      const currentSha = nodeSha256(current);
      const recorded = this.getRecordedSha(p);
      const violated = recorded === undefined || currentSha !== recorded;
      if (violated) {
        if (isEngine || p === "notes/safety.md") {
          throw new Error(
            `P14 Invariant violation: remove ${p} when current sha (${currentSha}) != recorded sha (${recorded})`
          );
        }
      }
    }
    this.files.delete(p);
  }

  async exists(p: string): Promise<boolean> {
    return this.files.has(p);
  }

  async writeIfUnchanged(p: string, expectedSha: string | null, bytes: Uint8Array): Promise<boolean> {
    if (this.onBeforeGuardedWrite) {
      await this.onBeforeGuardedWrite(p, expectedSha);
    }
    const current = this.files.get(p);
    const currentSha = current ? nodeSha256(current) : null;
    if (currentSha !== expectedSha) {
      return false;
    }
    this.files.set(p, new Uint8Array(bytes));
    return true;
  }

  async removeIfUnchanged(p: string, expectedSha: string): Promise<boolean> {
    if (this.onBeforeGuardedRemove) {
      await this.onBeforeGuardedRemove(p, expectedSha);
    }
    const current = this.files.get(p);
    const currentSha = current ? nodeSha256(current) : null;
    if (currentSha !== expectedSha || !current) {
      return false;
    }
    this.files.delete(p);
    return true;
  }
}

interface HubCommit {
  id: string;
  files: Map<string, Uint8Array>;
  diff: Map<string, string | null>;
}

export class FakeHub {
  protocol = 1;
  head = "commit-0";
  files = new Map<string, Uint8Array>();
  commits = new Map<string, HubCommit>();
  commitHistory: string[] = [];
  requests: RequestParam[] = [];
  lastStatusReport?: StatusReport;

  simulateNetworkError = false;
  simulateDevicePending = false;
  forceFullChanges = false;
  pathErrors = new Map<string, { status: number; code: string; message: string }>();
  onDelete?: (path: string, base: string) => { result: string } | null;
  onPut?: (path: string, base: string, bodyBytes: Uint8Array) => {
    result: string;
    sha256?: string;
    conflict_path?: string;
    mergedBytes?: Uint8Array;
  } | null;
  private commitCounter = 0;

  constructor() {
    this.commits.set(this.head, {
      id: this.head,
      files: new Map(),
      diff: new Map(),
    });
    this.commitHistory.push(this.head);
  }

  setFile(path: string, content: string | Uint8Array): void {
    const bytes = typeof content === "string" ? new TextEncoder().encode(content) : content;
    this.files.set(path, new Uint8Array(bytes));
    this.recordCommit(new Map([[path, nodeSha256(bytes)]]));
  }

  deleteFileDirect(path: string): void {
    this.files.delete(path);
    this.recordCommit(new Map([[path, null]]));
  }

  private recordCommit(diff: Map<string, string | null>): string {
    this.commitCounter += 1;
    const newHead = `commit-${this.commitCounter}`;
    this.head = newHead;
    const snapshot = new Map(this.files);
    this.commits.set(newHead, {
      id: newHead,
      files: snapshot,
      diff,
    });
    this.commitHistory.push(newHead);
    return newHead;
  }

  getFileEntries(): FileEntry[] {
    const list: FileEntry[] = [];
    for (const [path, bytes] of this.files.entries()) {
      list.push({
        path,
        sha256: nodeSha256(bytes),
        size: bytes.length,
      });
    }
    return list.sort((a, b) => a.path.localeCompare(b.path));
  }

  getChangesSince(since: string): Change[] {
    const sinceIdx = this.commitHistory.indexOf(since);
    if (sinceIdx === -1) {
      return [];
    }
    const changed = new Map<string, string | null>();
    for (let i = sinceIdx + 1; i < this.commitHistory.length; i++) {
      const commit = this.commits.get(this.commitHistory[i])!;
      for (const [path, sha] of commit.diff.entries()) {
        changed.set(path, sha);
      }
    }
    const result: Change[] = [];
    for (const [path, sha] of changed.entries()) {
      result.push({ path, sha256: sha });
    }
    return result.sort((a, b) => a.path.localeCompare(b.path));
  }

  get requestHandler(): RequestFn {
    return async (param: RequestParam): Promise<RequestResponse> => {
      this.requests.push({ ...param });

      if (this.simulateNetworkError) {
        throw new Error("Network error: connection refused");
      }

      const urlObj = new URL(param.url, "http://localhost");
      const pathname = urlObj.pathname;
      const method = (param.method || "GET").toUpperCase();

      if (this.simulateDevicePending) {
        return {
          status: 403,
          headers: { "content-type": "application/json" },
          json: { error: { code: "device_pending", message: "device pending" } },
          text: JSON.stringify({ error: { code: "device_pending", message: "device pending" } }),
        };
      }

      const qPath = urlObj.searchParams.get("path") || "";
      if (qPath && this.pathErrors.has(qPath)) {
        const err = this.pathErrors.get(qPath)!;
        return {
          status: err.status,
          headers: { "content-type": "application/json" },
          json: { error: { code: err.code, message: err.message } },
          text: JSON.stringify({ error: { code: err.code, message: err.message } }),
        };
      }

      if (pathname === "/v1/hello" && method === "GET") {
        const body = {
          protocol: this.protocol,
          lw_version: "0.1.0",
          vault_format: 1,
          head: this.head,
          device: "approved",
        };
        return {
          status: 200,
          headers: { "content-type": "application/json" },
          json: body,
          text: JSON.stringify(body),
        };
      }

      if (pathname === "/v1/manifest" && method === "GET") {
        const body = {
          head: this.head,
          files: this.getFileEntries(),
        };
        return {
          status: 200,
          headers: { "content-type": "application/json" },
          json: body,
          text: JSON.stringify(body),
        };
      }

      if (pathname === "/v1/file" && method === "GET") {
        const path = urlObj.searchParams.get("path") || "";
        const file = this.files.get(path);
        if (!file) {
          const errBody = { error: { code: "not_found", message: "file not found" } };
          return {
            status: 404,
            headers: { "content-type": "application/json" },
            json: errBody,
            text: JSON.stringify(errBody),
          };
        }
        const sha = nodeSha256(file);
        return {
          status: 200,
          headers: {
            "x-lw-sha256": sha,
            etag: `"${sha}"`,
            "content-type": "application/octet-stream",
          },
          body: new Uint8Array(file),
          arrayBuffer: file.buffer.slice(file.byteOffset, file.byteOffset + file.byteLength),
        };
      }

      if (pathname === "/v1/file" && method === "PUT") {
        const path = urlObj.searchParams.get("path") || "";
        const base = urlObj.searchParams.get("base") || "";
        let bodyBytes: Uint8Array;
        if (param.body instanceof Uint8Array) {
          bodyBytes = param.body;
        } else if (param.body instanceof ArrayBuffer) {
          bodyBytes = new Uint8Array(param.body);
        } else if (typeof param.body === "string") {
          bodyBytes = new TextEncoder().encode(param.body);
        } else {
          bodyBytes = new Uint8Array(0);
        }

        const newSha = nodeSha256(bodyBytes);

        if (this.onPut) {
          const custom = this.onPut(path, base, bodyBytes);
          if (custom) {
            if (custom.result === "merged") {
              const mergedBytes = custom.mergedBytes || bodyBytes;
              this.files.set(path, mergedBytes);
              const mergedSha = custom.sha256 || nodeSha256(mergedBytes);
              const newHead = this.recordCommit(new Map([[path, mergedSha]]));
              const resBody = {
                result: "merged",
                path,
                sha256: mergedSha,
                head: newHead,
              };
              return {
                status: 200,
                headers: { "content-type": "application/json" },
                json: resBody,
                text: JSON.stringify(resBody),
              };
            }
          }
        }

        const existing = this.files.get(path);

        if (!existing) {
          this.files.set(path, bodyBytes);
          const newHead = this.recordCommit(new Map([[path, newSha]]));
          const result = base === "new" ? "applied" : "resurrected";
          const resBody = {
            result,
            path,
            sha256: newSha,
            head: newHead,
          };
          return {
            status: 200,
            headers: { "content-type": "application/json" },
            json: resBody,
            text: JSON.stringify(resBody),
          };
        }

        const existingSha = nodeSha256(existing);
        if (base === existingSha) {
          this.files.set(path, bodyBytes);
          const newHead = this.recordCommit(new Map([[path, newSha]]));
          const resBody = {
            result: "applied",
            path,
            sha256: newSha,
            head: newHead,
          };
          return {
            status: 200,
            headers: { "content-type": "application/json" },
            json: resBody,
            text: JSON.stringify(resBody),
          };
        }

        // Base mismatch: conflict!
        const conflictPath = `notes/conflict-${this.commitCounter + 1}.md`;
        this.files.set(conflictPath, bodyBytes);
        const newHead = this.recordCommit(new Map([[conflictPath, newSha]]));
        const resBody = {
          result: "conflict_copy",
          path,
          sha256: existingSha,
          conflict_path: conflictPath,
          head: newHead,
        };
        return {
          status: 200,
          headers: { "content-type": "application/json" },
          json: resBody,
          text: JSON.stringify(resBody),
        };
      }

      if (pathname === "/v1/file" && method === "DELETE") {
        const path = urlObj.searchParams.get("path") || "";
        const base = urlObj.searchParams.get("base") || "";

        if (this.onDelete) {
          const custom = this.onDelete(path, base);
          if (custom) {
            const resBody = { result: custom.result, head: this.head };
            return {
              status: 200,
              headers: { "content-type": "application/json" },
              json: resBody,
              text: JSON.stringify(resBody),
            };
          }
        }

        const existing = this.files.get(path);

        if (!existing) {
          const resBody = { result: "already_gone", head: this.head };
          return {
            status: 200,
            headers: { "content-type": "application/json" },
            json: resBody,
            text: JSON.stringify(resBody),
          };
        }

        const existingSha = nodeSha256(existing);
        if (base === existingSha) {
          this.files.delete(path);
          const newHead = this.recordCommit(new Map([[path, null]]));
          const resBody = { result: "deleted", head: newHead };
          return {
            status: 200,
            headers: { "content-type": "application/json" },
            json: resBody,
            text: JSON.stringify(resBody),
          };
        }

        const resBody = { result: "refused_changed", head: this.head };
        return {
          status: 200,
          headers: { "content-type": "application/json" },
          json: resBody,
          text: JSON.stringify(resBody),
        };
      }

      if (pathname === "/v1/changes" && method === "GET") {
        const since = urlObj.searchParams.get("since") || "";
        if (this.forceFullChanges || !since || !this.commits.has(since)) {
          const resBody = {
            head: this.head,
            full: true,
            changed: [],
            files: this.getFileEntries(),
          };
          return {
            status: 200,
            headers: { "content-type": "application/json" },
            json: resBody,
            text: JSON.stringify(resBody),
          };
        }

        const changes = this.getChangesSince(since);
        const resBody = {
          head: this.head,
          full: false,
          changed: changes,
        };
        return {
          status: 200,
          headers: { "content-type": "application/json" },
          json: resBody,
          text: JSON.stringify(resBody),
        };
      }

      if (pathname === "/v1/status" && method === "POST") {
        if (param.body) {
          try {
            const str = typeof param.body === "string" ? param.body : new TextDecoder().decode(param.body);
            this.lastStatusReport = JSON.parse(str) as StatusReport;
          } catch {
            // ignore
          }
        }
        return {
          status: 204,
          headers: {},
        };
      }

      return {
        status: 404,
        headers: { "content-type": "application/json" },
        json: { error: { code: "not_found", message: "route not found" } },
      };
    };
  }
}
