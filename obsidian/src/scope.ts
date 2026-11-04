export type ScopeResult =
  | { ok: true }
  | { ok: false; code: "out_of_scope" | "bad_request" };

const ALLOWED_ROOT_EXTS = new Set([
  ".md",
  ".png",
  ".jpg",
  ".jpeg",
  ".gif",
  ".webp",
  ".svg",
  ".heic",
]);

const DISALLOWED_ROOT_NAMES = new Set([
  "index.md",
  "log.md",
  "schema.md",
  "curator-memory.md",
]);

export function checkPath(p: string): ScopeResult {
  if (typeof p !== "string" || p.length === 0) {
    return { ok: false, code: "bad_request" };
  }

  // Check valid UTF-8
  const checkWellFormed = (p as unknown as { isWellFormed?: () => boolean }).isWellFormed;
  if (typeof checkWellFormed === "function" && !checkWellFormed.call(p)) {
    return { ok: false, code: "bad_request" };
  }

  // No NUL or backslash
  if (p.includes("\0") || p.includes("\\")) {
    return { ok: false, code: "bad_request" };
  }

  // Max 512 bytes UTF-8
  const bytes = new TextEncoder().encode(p);
  if (bytes.length > 512) {
    return { ok: false, code: "bad_request" };
  }

  // Relative slash path: cannot start or end with slash
  if (p.startsWith("/") || p.endsWith("/")) {
    return { ok: false, code: "bad_request" };
  }

  const segments = p.split("/");

  // No empty segment, no "." or ".." segment
  for (const seg of segments) {
    if (seg === "" || seg === "." || seg === "..") {
      return { ok: false, code: "bad_request" };
    }
  }

  // No segment starting with "."
  for (const seg of segments) {
    if (seg.startsWith(".")) {
      return { ok: false, code: "out_of_scope" };
    }
  }

  // Allowed: under notes/ or ledger/ (must have at least one child segment)
  if (segments.length >= 2 && (segments[0] === "notes" || segments[0] === "ledger")) {
    return { ok: true };
  }

  // Allowed: single root segment ending in allowed extension, except disallowed names
  if (segments.length === 1) {
    const filename = segments[0];
    const lower = filename.toLowerCase();

    if (DISALLOWED_ROOT_NAMES.has(lower)) {
      return { ok: false, code: "out_of_scope" };
    }

    const dotIdx = lower.lastIndexOf(".");
    if (dotIdx !== -1) {
      const ext = lower.slice(dotIdx);
      if (ALLOWED_ROOT_EXTS.has(ext)) {
        return { ok: true };
      }
    }
  }

  return { ok: false, code: "out_of_scope" };
}
