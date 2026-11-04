import {
  Notice,
  Platform,
  Plugin,
  PluginSettingTab,
  Setting,
  TFile,
  type DataAdapter,
  requestUrl,
} from "obsidian";
import { checkPath } from "./scope";
import { sha256hex } from "./hash";
import {
  HubClient,
  adaptObsidianResponse,
  type RequestFn,
  type RequestParam,
  type RequestResponse,
} from "./hubclient";
import {
  SyncEngine,
  handleVaultDeleteEvent,
  handleVaultRenameEvent,
  listRecursively,
  type State,
  type VaultIO,
} from "./engine";
import { DEFAULT_SETTINGS, type PluginSettings } from "./status";

interface PersistedData {
  settings?: PluginSettings;
  state?: State;
}

export default class LWSyncPlugin extends Plugin {
  settings: PluginSettings = DEFAULT_SETTINGS;
  engine!: SyncEngine;
  private statusBarEl!: HTMLElement;
  private debounceTimer: ReturnType<typeof setTimeout> | null = null;

  async onload(): Promise<void> {
    await this.loadPluginData();

    if (!Platform.isMobile) {
      this.statusBarEl = this.addStatusBarItem();
      this.updateStatus("lw: ready");
    }

    const vaultIO = this.createVaultIO(this.app.vault.adapter);

    const requestFn: RequestFn = async (param: RequestParam): Promise<RequestResponse> => {
      let bodyBuffer: ArrayBuffer | string | undefined;
      if (param.body instanceof Uint8Array) {
        bodyBuffer = param.body.buffer.slice(
          param.body.byteOffset,
          param.body.byteOffset + param.body.byteLength
        );
      } else {
        bodyBuffer = param.body;
      }

      const res = await requestUrl({
        url: param.url,
        method: param.method || "GET",
        headers: param.headers,
        body: bodyBuffer,
        throw: false,
      });

      return adaptObsidianResponse(res);
    };

    const client = new HubClient({
      baseUrl: this.settings.hubUrl,
      deviceId: this.engineState.deviceId,
      deviceName: this.settings.deviceName,
      request: requestFn,
    });

    this.engine = new SyncEngine({
      vault: vaultIO,
      client,
      state: this.engineState,
      saveState: async (s: State) => {
        this.engineState = s;
        await this.savePluginData();
      },
      onNotice: (msg: string) => {
        new Notice(msg);
      },
      onStatusChange: (status: string) => {
        this.updateStatus(status);
      },
      manifest: this.manifest,
      pluginVersion: this.manifest?.version || "0.0.0",
      isMobile: Platform.isMobile,
    });

    this.registerVaultEvents();

    this.addCommand({
      id: "sync-now",
      name: "lw: Sync now",
      callback: () => {
        void this.triggerSync();
      },
    });

    this.addCommand({
      id: "show-status",
      name: "lw: Show status",
      callback: () => {
        new Notice(this.engine.getStatusText());
      },
    });

    this.addSettingTab(new LWSyncSettingTab(this.app, this));

    // Visibility change trigger
    const onVisibilityChange = (): void => {
      if (document.visibilityState === "visible") {
        void this.triggerSync();
      }
    };
    document.addEventListener("visibilitychange", onVisibilityChange);
    this.register(() => {
      document.removeEventListener("visibilitychange", onVisibilityChange);
    });

    // 5 min interval trigger
    this.registerInterval(
      window.setInterval(() => {
        void this.triggerSync();
      }, 5 * 60 * 1000)
    );

    // Initial sync on plugin load
    void this.triggerSync();
  }

  onunload(): void {
    if (this.debounceTimer) {
      clearTimeout(this.debounceTimer);
    }
  }

  private engineState!: State;

  private async loadPluginData(): Promise<void> {
    const loaded = (await this.loadData()) as PersistedData | null;
    this.settings = Object.assign({}, DEFAULT_SETTINGS, loaded?.settings);

    let deviceId = loaded?.state?.deviceId;
    if (!deviceId) {
      deviceId =
        typeof globalThis.crypto?.randomUUID === "function"
          ? globalThis.crypto.randomUUID()
          : "device-" + Math.random().toString(36).substring(2, 15);
    }

    this.engineState = {
      head: loaded?.state?.head || "",
      files: loaded?.state?.files || {},
      queue: loaded?.state?.queue || [],
      deviceId,
    };
  }

  private async savePluginData(): Promise<void> {
    const data: PersistedData = {
      settings: this.settings,
      state: this.engineState,
    };
    await this.saveData(data);
  }

  private updateStatus(text: string): void {
    if (this.statusBarEl) {
      this.statusBarEl.setText(text);
    }
  }

  private registerVaultEvents(): void {
    this.registerEvent(
      this.app.vault.on("create", (file) => {
        if (file instanceof TFile && checkPath(file.path).ok) {
          this.engine.queuePut(file.path);
          this.scheduleSync();
        }
      })
    );

    this.registerEvent(
      this.app.vault.on("modify", (file) => {
        if (file instanceof TFile && checkPath(file.path).ok) {
          this.engine.queuePut(file.path);
          this.scheduleSync();
        }
      })
    );

    this.registerEvent(
      this.app.vault.on("delete", (file) => {
        handleVaultDeleteEvent(this.engine, file, file instanceof TFile);
        this.scheduleSync();
      })
    );

    this.registerEvent(
      this.app.vault.on("rename", (file, oldPath) => {
        handleVaultRenameEvent(this.engine, file, oldPath, file instanceof TFile);
        this.scheduleSync();
      })
    );
  }

  private scheduleSync(): void {
    if (this.debounceTimer) {
      clearTimeout(this.debounceTimer);
    }
    this.debounceTimer = setTimeout(() => {
      void this.triggerSync();
    }, 3000);
  }

  async triggerSync(): Promise<void> {
    if (!this.settings.hubUrl || !this.settings.hubUrl.trim()) {
      this.updateStatus("lw: set the hub URL in settings");
      if (Platform.isMobile) {
        new Notice("lw: set the hub URL in settings");
      }
      return;
    }
    await this.engine.triggerSync();
  }

  private createVaultIO(adapter: DataAdapter): VaultIO {
    const ensureDir = async (dirPath: string): Promise<void> => {
      if (!dirPath || dirPath === "." || dirPath === "/") return;
      const exists = await adapter.exists(dirPath);
      if (!exists) {
        const slashIdx = dirPath.lastIndexOf("/");
        if (slashIdx !== -1) {
          await ensureDir(dirPath.slice(0, slashIdx));
        }
        try {
          await adapter.mkdir(dirPath);
        } catch {
          // ignore directory creation errors
        }
      }
    };

    return {
      list: async (): Promise<string[]> => {
        const all = await listRecursively(adapter, "");
        return all.sort();
      },
      read: async (p: string): Promise<Uint8Array | null> => {
        const exists = await adapter.exists(p);
        if (!exists) return null;
        const buf = await adapter.readBinary(p);
        return new Uint8Array(buf);
      },
      write: async (p: string, bytes: Uint8Array): Promise<void> => {
        const slashIdx = p.lastIndexOf("/");
        if (slashIdx !== -1) {
          await ensureDir(p.slice(0, slashIdx));
        }
        const buf = bytes.buffer.slice(
          bytes.byteOffset,
          bytes.byteOffset + bytes.byteLength
        );
        await adapter.writeBinary(p, buf);
      },
      remove: async (p: string): Promise<void> => {
        const exists = await adapter.exists(p);
        if (exists) {
          await adapter.remove(p);
        }
      },
      exists: async (p: string): Promise<boolean> => {
        return await adapter.exists(p);
      },
      writeIfUnchanged: async (
        p: string,
        expectedSha: string | null,
        bytes: Uint8Array
      ): Promise<boolean> => {
        const exists = await adapter.exists(p);
        let currentSha: string | null = null;
        if (exists) {
          const curBuf = await adapter.readBinary(p);
          currentSha = await sha256hex(new Uint8Array(curBuf));
        }
        if (currentSha !== expectedSha) {
          return false;
        }
        const slashIdx = p.lastIndexOf("/");
        if (slashIdx !== -1) {
          await ensureDir(p.slice(0, slashIdx));
        }
        const buf = bytes.buffer.slice(
          bytes.byteOffset,
          bytes.byteOffset + bytes.byteLength
        );
        await adapter.writeBinary(p, buf);
        return true;
      },
      removeIfUnchanged: async (
        p: string,
        expectedSha: string
      ): Promise<boolean> => {
        const exists = await adapter.exists(p);
        if (!exists) {
          return false;
        }
        const curBuf = await adapter.readBinary(p);
        const currentSha = await sha256hex(new Uint8Array(curBuf));
        if (currentSha !== expectedSha) {
          return false;
        }
        await adapter.remove(p);
        return true;
      },
    };
  }
}

class LWSyncSettingTab extends PluginSettingTab {
  plugin: LWSyncPlugin;

  constructor(app: Plugin["app"], plugin: LWSyncPlugin) {
    super(app, plugin);
    this.plugin = plugin;
  }

  display(): void {
    const { containerEl } = this;
    containerEl.empty();

    containerEl.createEl("h2", { text: "lw Sync Settings" });

    new Setting(containerEl)
      .setName("Hub URL")
      .setDesc("HTTPS URL of your lw hub serve instance (over tailnet)")
      .addText((text) =>
        text
          .setPlaceholder("https://hub.example.com")
          .setValue(this.plugin.settings.hubUrl)
          .onChange(async (value) => {
            this.plugin.settings.hubUrl = value.trim();
            await this.plugin.saveData({
              settings: this.plugin.settings,
              state: this.plugin.engine.options.state,
            });
            // Update client baseURL
            (this.plugin.engine.options.client.options as { baseUrl: string }).baseUrl =
              this.plugin.settings.hubUrl;
          })
      );

    new Setting(containerEl)
      .setName("Device Name")
      .setDesc("Device name for the hub devices list (default: phone)")
      .addText((text) =>
        text
          .setPlaceholder("phone")
          .setValue(this.plugin.settings.deviceName)
          .onChange(async (value) => {
            this.plugin.settings.deviceName = value.trim() || "phone";
            await this.plugin.saveData({
              settings: this.plugin.settings,
              state: this.plugin.engine.options.state,
            });
            (this.plugin.engine.options.client.options as { deviceName: string }).deviceName =
              this.plugin.settings.deviceName;
          })
      );

    new Setting(containerEl)
      .setName("Device ID")
      .setDesc("Unique UUID generated for this phone")
      .addText((text) =>
        text
          .setValue(this.plugin.engine.options.state.deviceId)
          .setDisabled(true)
      );
  }
}
