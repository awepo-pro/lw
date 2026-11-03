import * as fs from "node:fs";
import * as path from "node:path";
import { describe, it, expect, beforeEach } from "vitest";
import { checkPath } from "../src/scope";
import { sha256hex } from "../src/hash";
import { HubClient } from "../src/hubclient";
import { SyncEngine, type State, type SyncEngineOptions } from "../src/engine";
import { FakeHub, FakeVaultIO, nodeSha256 } from "./fakes";

describe("S3 sync suite", () => {
  let fakeVault: FakeVaultIO;
  let fakeHub: FakeHub;
  let hubClient: HubClient;
  let state: State;
  let notices: string[];
  let statuses: string[];

  const DEVICE_ID = "01234567-89ab-cdef-0123-456789abcdef";
  const DEVICE_NAME = "phone";

  beforeEach(() => {
    fakeVault = new FakeVaultIO();
    fakeHub = new FakeHub();
    notices = [];
    statuses = [];

    hubClient = new HubClient({
      baseUrl: "http://localhost",
      deviceId: DEVICE_ID,
      deviceName: DEVICE_NAME,
      request: fakeHub.requestHandler,
    });

    state = {
      head: fakeHub.head,
      files: {},
      queue: [],
      deviceId: DEVICE_ID,
    };

    fakeVault.getRecordedSha = (p: string) => state.files[p];
  });

  function createEngine(customOptions: Partial<SyncEngineOptions> = {}): SyncEngine {
    return new SyncEngine({
      vault: fakeVault,
      client: hubClient,
      state,
      onNotice: (n: string) => notices.push(n),
      onStatusChange: (s: string) => statuses.push(s),
      saveState: async (s: State) => {
        state = { ...s, files: { ...s.files }, queue: [...s.queue] };
      },
      pluginVersion: "0.0.0",
      ...customOptions,
    });
  }

  it("P1 scope parity", () => {
    const scopeCasesPath = path.resolve(__dirname, "../../internal/hub/testdata/scope-cases.json");
    const raw = fs.readFileSync(scopeCasesPath, "utf-8");
    const data = JSON.parse(raw) as {
      cases: Array<{ path: string; allow: boolean; code?: "out_of_scope" | "bad_request" }>;
    };

    expect(data.cases.length).toBeGreaterThan(0);

    for (const c of data.cases) {
      const res = checkPath(c.path);
      if (c.allow) {
        expect(res, `path: "${c.path}" expected ok:true`).toEqual({ ok: true });
      } else {
        expect(res, `path: "${c.path}" expected ok:false code:${c.code}`).toEqual({
          ok: false,
          code: c.code,
        });
      }
    }
  });

  it("P2 local create in scope", async () => {
    const filePath = "notes/created.md";
    const content = new TextEncoder().encode("# Hello New File");
    await fakeVault.write(filePath, content);

    const engine = createEngine();
    engine.queuePut(filePath);
    await engine.flush();

    const expectedSha = nodeSha256(content);
    expect(state.files[filePath]).toBe(expectedSha);
    expect(fakeHub.files.has(filePath)).toBe(true);
    expect(nodeSha256(fakeHub.files.get(filePath)!)).toBe(expectedSha);
    expect(state.queue.length).toBe(0);
  });

  it("P3 local modify", async () => {
    const filePath = "notes/mod.md";
    const initialContent = new TextEncoder().encode("v1 content");
    await fakeVault.write(filePath, initialContent);
    const initialSha = nodeSha256(initialContent);

    state.files[filePath] = initialSha;
    fakeHub.setFile(filePath, initialContent);
    state.head = fakeHub.head;

    const engine = createEngine();

    // Three modifies before flush
    await fakeVault.write(filePath, new TextEncoder().encode("v2 content"));
    engine.queuePut(filePath);

    await fakeVault.write(filePath, new TextEncoder().encode("v3 content"));
    engine.queuePut(filePath);

    const finalContent = new TextEncoder().encode("v4 final content");
    await fakeVault.write(filePath, finalContent);
    engine.queuePut(filePath);

    const initialRequestCount = fakeHub.requests.length;
    await engine.flush();

    // Expect exactly one PUT for that path
    const putRequests = fakeHub.requests
      .slice(initialRequestCount)
      .filter((r) => (r.method || "GET").toUpperCase() === "PUT" && r.url.includes("path=notes%2Fmod.md"));
    expect(putRequests.length).toBe(1);

    const finalSha = nodeSha256(finalContent);
    expect(state.files[filePath]).toBe(finalSha);
    expect(nodeSha256(fakeHub.files.get(filePath)!)).toBe(finalSha);
  });

  it("P4 conflict_copy", async () => {
    const filePath = "notes/conflict.md";
    const hubContent = new TextEncoder().encode("hub original");
    fakeHub.setFile(filePath, hubContent);
    state.head = fakeHub.head;

    // Local client created the same file locally without knowing hub had it
    const localContent = new TextEncoder().encode("phone conflicting");
    await fakeVault.write(filePath, localContent);

    const engine = createEngine();
    engine.queuePut(filePath);
    await engine.flush();

    // Notice should contain the conflict path
    expect(notices.some((n) => n.includes("conflict"))).toBe(true);

    // Vault should now have the hub's version at filePath AND the conflict file locally
    const hubDataInVault = await fakeVault.read(filePath);
    expect(hubDataInVault).not.toBeNull();
    expect(new TextDecoder().decode(hubDataInVault!)).toBe("hub original");

    const vaultFiles = await fakeVault.list();
    const conflictFile = vaultFiles.find((p) => p.includes("conflict-"));
    expect(conflictFile).toBeDefined();

    const conflictData = await fakeVault.read(conflictFile!);
    expect(new TextDecoder().decode(conflictData!)).toBe("phone conflicting");
  });

  it("P5 local delete with a recorded sha", async () => {
    const filePath = "notes/deleted.md";
    const originalContent = new TextEncoder().encode("initial");
    const originalSha = nodeSha256(originalContent);

    fakeHub.setFile(filePath, originalContent);
    state.files[filePath] = originalSha;
    state.head = fakeHub.head;

    // User deletes locally
    await fakeVault.remove(filePath);

    // Concurrently hub modified the file
    const hubUpdatedContent = new TextEncoder().encode("hub updated in background");
    fakeHub.setFile(filePath, hubUpdatedContent);
    const hubUpdatedSha = nodeSha256(hubUpdatedContent);

    const engine = createEngine();
    engine.queueDel(filePath, originalSha);
    await engine.flush();

    // Since hub refused delete (refused_changed), the hub's new version should be downloaded
    expect(await fakeVault.exists(filePath)).toBe(true);
    const restored = await fakeVault.read(filePath);
    expect(new TextDecoder().decode(restored!)).toBe("hub updated in background");
    expect(state.files[filePath]).toBe(hubUpdatedSha);
  });

  it("P6 pull, changed path", async () => {
    const filePath = "notes/doc.md";
    const initialContent = new TextEncoder().encode("initial");
    const initialSha = nodeSha256(initialContent);

    fakeHub.setFile(filePath, initialContent);
    await fakeVault.write(filePath, initialContent);
    state.files[filePath] = initialSha;
    state.head = fakeHub.head;

    const engine = createEngine();

    // Subcase 1: local unchanged -> overwritten
    const hubContentV2 = new TextEncoder().encode("hub v2");
    fakeHub.setFile(filePath, hubContentV2);
    const hubShaV2 = nodeSha256(hubContentV2);

    await engine.flush();

    expect(new TextDecoder().decode((await fakeVault.read(filePath))!)).toBe("hub v2");
    expect(state.files[filePath]).toBe(hubShaV2);

    // Subcase 2: local changed -> not overwritten, PUT flushed first then pull repeats
    const localContent = new TextEncoder().encode("local edit v3");
    await fakeVault.write(filePath, localContent);

    const hubContentV4 = new TextEncoder().encode("hub concurrent v4");
    fakeHub.setFile(filePath, hubContentV4);

    await engine.flush();

    // Because local changed != recorded, engine flushes PUT with base = hubShaV2 first
    // Hub produces conflict_copy, pull downloads hub file and conflict copy
    const afterData = await fakeVault.read(filePath);
    expect(new TextDecoder().decode(afterData!)).toBe("hub concurrent v4");

    const vaultFiles = await fakeVault.list();
    const conflictFile = vaultFiles.find((p) => p.includes("conflict-"));
    expect(conflictFile).toBeDefined();
    const conflictData = await fakeVault.read(conflictFile!);
    expect(new TextDecoder().decode(conflictData!)).toBe("local edit v3");
  });

  it("P7 pull, deleted path", async () => {
    // Subcase 1: local unchanged -> removed
    const path1 = "notes/del1.md";
    const content1 = new TextEncoder().encode("c1");
    fakeHub.setFile(path1, content1);
    await fakeVault.write(path1, content1);
    state.files[path1] = nodeSha256(content1);
    state.head = fakeHub.head;

    const engine = createEngine();

    fakeHub.deleteFileDirect(path1);
    await engine.flush();

    expect(await fakeVault.exists(path1)).toBe(false);
    expect(state.files[path1]).toBeUndefined();

    // Subcase 2: local changed -> kept and PUT (hub resurrects)
    const path2 = "notes/resurrect.md";
    const content2 = new TextEncoder().encode("c2");
    fakeHub.setFile(path2, content2);
    await fakeVault.write(path2, content2);
    state.files[path2] = nodeSha256(content2);
    state.head = fakeHub.head;

    fakeHub.deleteFileDirect(path2);

    // Local modification
    const modContent = new TextEncoder().encode("local modified resurrect");
    await fakeVault.write(path2, modContent);

    await engine.flush();

    // File must be kept locally and resurrected on hub
    expect(await fakeVault.exists(path2)).toBe(true);
    expect(fakeHub.files.has(path2)).toBe(true);
    expect(state.files[path2]).toBe(nodeSha256(modContent));
  });

  it("P8 out-of-scope event", async () => {
    const engine = createEngine();

    // Out of scope paths should not be queued or sent
    engine.queuePut("wiki/x.md");
    engine.queuePut(".obsidian/x.json");
    engine.queueDel(".git/config", "some-sha");

    expect(state.queue.length).toBe(0);

    const reqCountBefore = fakeHub.requests.length;
    await engine.flush();

    const putOrDelRequests = fakeHub.requests
      .slice(reqCountBefore)
      .filter((r) => ["PUT", "DELETE"].includes((r.method || "GET").toUpperCase()));
    expect(putOrDelRequests.length).toBe(0);
  });

  it("P9 rename in scope", async () => {
    const oldPath = "notes/old.md";
    const newPath = "notes/new.md";
    const content = new TextEncoder().encode("rename content");
    const sha = nodeSha256(content);

    fakeHub.setFile(oldPath, content);
    await fakeVault.write(newPath, content);
    state.files[oldPath] = sha;
    state.head = fakeHub.head;

    const engine = createEngine();
    engine.queueRename(oldPath, newPath);

    // Assert queue has PUT new base=new, then DELETE old base=<recorded>
    expect(state.queue).toEqual([
      { type: "put", path: newPath, base: "new" },
      { type: "del", path: oldPath, base: sha },
    ]);

    await engine.flush();

    expect(fakeHub.files.has(oldPath)).toBe(false);
    expect(fakeHub.files.has(newPath)).toBe(true);
    expect(state.files[newPath]).toBe(sha);
    expect(state.files[oldPath]).toBeUndefined();
  });

  it("P10 network error", async () => {
    const filePath = "notes/retry.md";
    const content = new TextEncoder().encode("retry content");
    await fakeVault.write(filePath, content);

    const engine = createEngine();
    engine.queuePut(filePath);

    // Simulate network failure
    fakeHub.simulateNetworkError = true;

    await expect(engine.flush()).rejects.toThrow();

    // Op stays queued in order, state is saved
    expect(state.queue.length).toBe(1);
    expect(state.queue[0].path).toBe(filePath);

    // Network recovers
    fakeHub.simulateNetworkError = false;
    await engine.flush();

    expect(state.queue.length).toBe(0);
    expect(fakeHub.files.has(filePath)).toBe(true);
  });

  it("P11 403 device_pending", async () => {
    fakeHub.simulateDevicePending = true;

    const engine = createEngine();
    engine.queuePut("notes/test.md");

    await engine.flush();

    expect(statuses.some((s) => s.includes("lw: waiting for approval (run: lw hub approve phone)"))).toBe(true);

    // No further requests after 403
    expect(fakeHub.requests.length).toBe(1);
  });

  it("P12 hello protocol != 1", async () => {
    fakeHub.protocol = 2;

    const engine = createEngine();
    await engine.flush();

    expect(statuses.some((s) => s.toLowerCase().includes("protocol"))).toBe(true);

    // Only hello requested, no further file operations or changes pulled
    expect(fakeHub.requests.length).toBe(1);
    expect(fakeHub.requests[0].url).toContain("/v1/hello");
  });

  it("P13 full:true", async () => {
    // 1. Hub has a manifest file absent locally -> downloaded
    const remotePath = "notes/remote.md";
    const remoteContent = new TextEncoder().encode("remote content");
    fakeHub.setFile(remotePath, remoteContent);

    // 2. Local file with recorded sha equal to local sha, absent from manifest -> removed
    const stalePath = "notes/stale.md";
    const staleContent = new TextEncoder().encode("stale content");
    await fakeVault.write(stalePath, staleContent);
    state.files[stalePath] = nodeSha256(staleContent);

    // 3. Local in-scope file with NO recorded sha -> PUT base=new
    const unsyncedPath = "notes/unsynced.md";
    const unsyncedContent = new TextEncoder().encode("unsynced content");
    await fakeVault.write(unsyncedPath, unsyncedContent);

    // Force full sync
    fakeHub.forceFullChanges = true;

    const engine = createEngine();
    await engine.flush();

    // Assert 1: remote file downloaded
    expect(await fakeVault.exists(remotePath)).toBe(true);
    expect(new TextDecoder().decode((await fakeVault.read(remotePath))!)).toBe("remote content");
    expect(state.files[remotePath]).toBe(nodeSha256(remoteContent));

    // Assert 2: stale file removed
    expect(await fakeVault.exists(stalePath)).toBe(false);
    expect(state.files[stalePath]).toBeUndefined();

    // Assert 3: unsynced file uploaded
    expect(fakeHub.files.has(unsyncedPath)).toBe(true);
    expect(state.files[unsyncedPath]).toBe(nodeSha256(unsyncedContent));
  });

  it("P14 invariant: never removes or overwrites local file whose sha != recorded", async () => {
    // Verified by fakeVault.getRecordedSha assertion across all P2-P13 tests
    // Here we explicitly test that the fakeVault throws if violated
    fakeVault.files.set("notes/safety.md", new TextEncoder().encode("actual bytes"));
    state.files["notes/safety.md"] = "different-sha";

    await expect(fakeVault.write("notes/safety.md", new TextEncoder().encode("new bytes"))).rejects.toThrow(
      /P14 Invariant violation/
    );
    await expect(fakeVault.remove("notes/safety.md")).rejects.toThrow(/P14 Invariant violation/);
  });

  it("P15 sha256hex", async () => {
    const input = new TextEncoder().encode("abc");
    const result = await sha256hex(input);
    expect(result).toBe("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
  });

  it("P16 every request carries X-LW-Device and X-LW-Device-Name", async () => {
    await hubClient.hello();
    await hubClient.manifest();
    await hubClient.changes("commit-0");
    await hubClient.postStatus({
      plugin_version: "0.0.0",
      last_error: "",
      queue_len: 0,
      at: new Date().toISOString(),
    });

    expect(fakeHub.requests.length).toBeGreaterThan(0);
    for (const req of fakeHub.requests) {
      expect(req.headers?.["X-LW-Device"]).toBe(DEVICE_ID);
      expect(req.headers?.["X-LW-Device-Name"]).toBe(DEVICE_NAME);
    }
  });

  it("F1 pull: local file with no recorded sha and bytes != hub -> NOT overwritten", async () => {
    const filePath = "notes/syncthing.md";
    const hubContent = new TextEncoder().encode("hub content");
    fakeHub.setFile(filePath, hubContent);

    // Phone vault already holds notes/syncthing.md with different content and no recorded sha
    const phoneContent = new TextEncoder().encode("phone content");
    await fakeVault.write(filePath, phoneContent);
    const phoneSha = nodeSha256(phoneContent);
    const hubSha = nodeSha256(hubContent);

    expect(state.files[filePath]).toBeUndefined();

    const engine = createEngine();
    await engine.flush();

    // The conflict copy must exist with phone content
    const vaultFiles = await fakeVault.list();
    const conflictFile = vaultFiles.find((p) => p.includes("conflict-"));
    expect(conflictFile).toBeDefined();
    const conflictData = await fakeVault.read(conflictFile!);
    expect(new TextDecoder().decode(conflictData!)).toBe("phone content");
    expect(state.files[conflictFile!]).toBe(phoneSha);

    // Hub content is now at filePath, recorded = hubSha
    const mainData = await fakeVault.read(filePath);
    expect(new TextDecoder().decode(mainData!)).toBe("hub content");
    expect(state.files[filePath]).toBe(hubSha);
  });

  it("F2 flush: a PUT answered merged with sha != local sha -> downloads merged bytes", async () => {
    const filePath = "ledger/2026-11-03.md";
    const phoneContent = new TextEncoder().encode("2026-11-03 phone expense");
    await fakeVault.write(filePath, phoneContent);

    const mergedContent = new TextEncoder().encode("2026-11-03 pc expense\n2026-11-03 phone expense");
    const mergedSha = nodeSha256(mergedContent);

    let putCount = 0;
    fakeHub.onPut = (path, base, bodyBytes) => {
      if (path === filePath) {
        putCount++;
        if (putCount === 1) {
          return {
            result: "merged",
            mergedBytes: mergedContent,
            sha256: mergedSha,
          };
        }
      }
      return null;
    };

    const engine = createEngine();
    engine.queuePut(filePath);
    await engine.flush();

    expect(notices.some((n) => n.includes("Merged ledger/2026-11-03.md"))).toBe(true);

    const putRequests = fakeHub.requests.filter(
      (r) => (r.method || "GET").toUpperCase() === "PUT" && r.url.includes("ledger%2F2026-11-03.md")
    );
    expect(putRequests.length).toBe(1);

    const localBytes = await fakeVault.read(filePath);
    expect(localBytes).not.toBeNull();
    expect(new TextDecoder().decode(localBytes!)).toBe(new TextDecoder().decode(mergedContent));
    expect(state.files[filePath]).toBe(mergedSha);
  });

  it("F3 reconcileFull: manifest file exists locally", async () => {
    // Subcase 1: local sha == manifest sha -> records it
    const samePath = "notes/f3-same.md";
    const sameContent = new TextEncoder().encode("identical content");
    fakeHub.setFile(samePath, sameContent);
    await fakeVault.write(samePath, sameContent);

    // Subcase 2: recorded defined and local sha == recorded -> download (hub changed)
    const hubChangedPath = "notes/f3-hubchanged.md";
    const oldContent = new TextEncoder().encode("old content");
    const newHubContent = new TextEncoder().encode("new hub content");
    await fakeVault.write(hubChangedPath, oldContent);
    state.files[hubChangedPath] = nodeSha256(oldContent);
    fakeHub.setFile(hubChangedPath, newHubContent);

    // Subcase 3: manifest file exists locally with no recorded sha and local != hub -> uploads PUT base=new, conflict_copy settled
    const diffPath = "notes/f3-diff.md";
    const localDiffContent = new TextEncoder().encode("phone diff text");
    const hubDiffContent = new TextEncoder().encode("hub diff text");
    await fakeVault.write(diffPath, localDiffContent);
    fakeHub.setFile(diffPath, hubDiffContent);

    fakeHub.forceFullChanges = true;

    const engine = createEngine();
    await engine.flush();

    // Subcase 1: recorded
    expect(state.files[samePath]).toBe(nodeSha256(sameContent));

    // Subcase 2: downloaded new content
    const downloadedBytes = await fakeVault.read(hubChangedPath);
    expect(new TextDecoder().decode(downloadedBytes!)).toBe("new hub content");
    expect(state.files[hubChangedPath]).toBe(nodeSha256(newHubContent));

    // Subcase 3: phone diff preserved in conflict file, hub content at diffPath
    const vaultFiles = await fakeVault.list();
    const conflictFile = vaultFiles.find((p) => p.includes("conflict-"));
    expect(conflictFile).toBeDefined();
    const conflictData = await fakeVault.read(conflictFile!);
    expect(new TextDecoder().decode(conflictData!)).toBe("phone diff text");
    expect(state.files[diffPath]).toBe(nodeSha256(hubDiffContent));
  });

  it("F4 queue: coalesce consecutive puts without del, preserve order across del", async () => {
    const filePath = "notes/f4.md";
    const content1 = new TextEncoder().encode("content v1");
    const sha1 = nodeSha256(content1);

    await fakeVault.write(filePath, content1);
    state.files[filePath] = sha1;
    fakeHub.setFile(filePath, content1);
    state.head = fakeHub.head;

    const engine = createEngine();

    // User edits, then deletes, then recreates with v3
    await fakeVault.write(filePath, new TextEncoder().encode("content v2"));
    engine.queuePut(filePath);

    await fakeVault.remove(filePath);
    engine.queueDel(filePath, sha1);

    const content3 = new TextEncoder().encode("content v3");
    await fakeVault.write(filePath, content3);
    engine.queuePut(filePath);

    expect(state.queue.length).toBe(3);

    const reqBefore = fakeHub.requests.length;
    await engine.flush();

    const fileRequests = fakeHub.requests
      .slice(reqBefore)
      .filter((r) => r.url.includes("notes%2Ff4.md"))
      .map((r) => (r.method || "GET").toUpperCase());

    // Expect exactly three requests: PUT, DELETE, PUT
    expect(fileRequests).toEqual(["PUT", "DELETE", "PUT"]);
    expect(state.queue.length).toBe(0);
  });

  it("F5 VaultIO.read error propagates, aborts sync, keeps queue, no local write", async () => {
    // 1. Flush scenario with queued PUT
    const filePath = "notes/f5-err.md";
    const content = new TextEncoder().encode("content before read failure");
    await fakeVault.write(filePath, content);
    state.files[filePath] = nodeSha256(content);

    // Hub also has a change
    fakeHub.setFile(filePath, new TextEncoder().encode("hub new content"));

    // Throw on reading filePath
    fakeVault.throwOnReadPaths = new Set([filePath]);

    const engine = createEngine();
    engine.queuePut(filePath);

    // Flush must reject
    await expect(engine.flush()).rejects.toThrow("Disk read error");

    // Queue is kept intact
    expect(state.queue.length).toBe(1);
    expect(state.queue[0].path).toBe(filePath);

    // No local file was overwritten
    fakeVault.throwOnReadPaths.clear();
    const currentBytes = await fakeVault.read(filePath);
    expect(new TextDecoder().decode(currentBytes!)).toBe("content before read failure");

    // 2. Pull scenario: hub has changed file, local read throws
    const pullPath = "notes/f5-pull-err.md";
    const localPullContent = new TextEncoder().encode("local pull content");
    await fakeVault.write(pullPath, localPullContent);
    state.files[pullPath] = nodeSha256(localPullContent);
    state.head = fakeHub.head;

    fakeHub.setFile(pullPath, new TextEncoder().encode("hub updated bytes"));
    fakeVault.throwOnReadPaths = new Set([pullPath]);

    await expect(engine.flush()).rejects.toThrow("Disk read error");

    fakeVault.throwOnReadPaths.clear();
    const pullBytes = await fakeVault.read(pullPath);
    expect(new TextDecoder().decode(pullBytes!)).toBe("local pull content");
  });

  it("F6 echo: skip queued PUT when local sha equals recorded sha and base != new", async () => {
    const filePath = "notes/echo.md";
    const content = new TextEncoder().encode("echo content");
    const sha = nodeSha256(content);

    await fakeVault.write(filePath, content);
    state.files[filePath] = sha;
    fakeHub.setFile(filePath, content);
    state.head = fakeHub.head;

    const engine = createEngine();

    // Event queued a PUT with base = sha (recorded), and file bytes unchanged
    engine.queuePut(filePath, sha);
    expect(state.queue.length).toBe(1);

    const reqBefore = fakeHub.requests.length;
    await engine.flush();

    // No PUT request sent to hub!
    const putRequests = fakeHub.requests
      .slice(reqBefore)
      .filter((r) => (r.method || "GET").toUpperCase() === "PUT" && r.url.includes("notes%2Fecho.md"));
    expect(putRequests.length).toBe(0);

    // Queue is cleared
    expect(state.queue.length).toBe(0);
  });

  it("F7 NetworkError during flush sets status to lw: offline (<n> queued)", async () => {
    const filePath = "notes/f7.md";
    const content = new TextEncoder().encode("content f7");
    await fakeVault.write(filePath, content);

    const engine = createEngine();
    engine.queuePut(filePath);
    expect(state.queue.length).toBe(1);

    fakeHub.simulateNetworkError = true;

    await expect(engine.flush()).rejects.toThrow();

    expect(statuses).toContain("lw: offline (1 queued)");
  });
});
