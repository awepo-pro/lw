export type StatusEvent =
  | { kind: "waiting_approval"; slug: string }
  | { kind: "offline"; queued: number }
  | { kind: "offline_episode"; host: string; queued: number }
  | { kind: "back_online"; filesSynced: number }
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
    case "offline_episode":
      return `lw: can't reach the hub (${event.host}). Is Tailscale on? ${event.queued} change(s) saved on this phone; they will sync when you're back.`;
    case "back_online":
      return `lw: back online - synced ${event.filesSynced} change(s)`;
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

export function shouldPollNow(
  visible: boolean,
  msSinceLastSyncEnd: number,
  syncing: boolean
): boolean {
  return visible && !syncing && msSinceLastSyncEnd >= 30000;
}

export function shouldSyncOnNoteOpen(syncing: boolean, msSinceLastSyncEnd: number): boolean {
  return !syncing && msSinceLastSyncEnd >= 10000;
}

export function getHubHost(url: string): string {
  if (!url) return "";
  try {
    const withProto = url.includes("://") ? url : `http://${url}`;
    const parsed = new URL(withProto);
    return parsed.host || parsed.hostname || url;
  } catch {
    return url;
  }
}
