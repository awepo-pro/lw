export type StatusEvent =
  | { kind: "waiting_approval"; slug: string }
  | { kind: "offline"; queued: number }
  | { kind: "hub_error"; code: string }
  | { kind: "synced"; filesSynced: number }
  | { kind: "empty_hub_url" };

export interface StatusDecisionContext {
  isMobile: boolean;
  hasSyncedOnce: boolean;
}

export function decideNotice(
  event: StatusEvent,
  context: StatusDecisionContext
): string | null {
  if (!context.isMobile) {
    return null;
  }
  switch (event.kind) {
    case "waiting_approval":
      return `lw: waiting for approval (run: lw hub approve ${event.slug})`;
    case "offline":
      return `lw: offline (${event.queued} queued)`;
    case "hub_error":
      return `lw: hub error (${event.code})`;
    case "synced":
      if (!context.hasSyncedOnce) {
        return `lw: synced ${event.filesSynced} files`;
      }
      return null;
    case "empty_hub_url":
      return "lw: set the hub URL in settings";
  }
}

export interface PluginSettings {
  hubUrl: string;
  deviceName: string;
}

export const DEFAULT_SETTINGS: PluginSettings = {
  hubUrl: "",
  deviceName: "phone",
};

export interface StatusSummary {
  pluginVersion: string;
  lastSyncTime: string | null;
  queueLength: number;
  lastError: string | null;
}

export function formatStatusText(summary: StatusSummary): string {
  const version = summary.pluginVersion || "0.0.0";
  const lastSync = summary.lastSyncTime || "never";
  const queue = summary.queueLength;
  const lastError = summary.lastError || "none";
  return `lw: version ${version}\nLast sync: ${lastSync}\nQueue length: ${queue}\nLast error: ${lastError}`;
}
