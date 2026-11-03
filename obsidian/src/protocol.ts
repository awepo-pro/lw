// Wire types and constants mirrored from internal/hub/protocol.go.

export const PROTOCOL_VERSION = 1;
export const ProtocolVersion = PROTOCOL_VERSION;

export const MAX_BODY_BYTES = 20 << 20; // 20 MiB
export const MaxBodyBytes = MAX_BODY_BYTES;

export const MAX_STATUS_BYTES = 64 << 10; // 64 KiB
export const MaxStatusBytes = MAX_STATUS_BYTES;

export const BASE_NEW = "new";
export const BaseNew = BASE_NEW;

// Result values of a write
export const RESULT_APPLIED = "applied";
export const ResultApplied = RESULT_APPLIED;

export const RESULT_CONFLICT_COPY = "conflict_copy";
export const ResultConflictCopy = RESULT_CONFLICT_COPY;

export const RESULT_RESURRECTED = "resurrected";
export const ResultResurrected = RESULT_RESURRECTED;

export const RESULT_MERGED = "merged";
export const ResultMerged = RESULT_MERGED;

export const RESULT_DELETED = "deleted";
export const ResultDeleted = RESULT_DELETED;

export const RESULT_REFUSED_CHANGED = "refused_changed";
export const ResultRefusedChanged = RESULT_REFUSED_CHANGED;

export const RESULT_ALREADY_GONE = "already_gone";
export const ResultAlreadyGone = RESULT_ALREADY_GONE;

// Error codes
export const CODE_BAD_REQUEST = "bad_request";
export const CodeBadRequest = CODE_BAD_REQUEST;

export const CODE_UNAUTHORIZED = "unauthorized";
export const CodeUnauthorized = CODE_UNAUTHORIZED;

export const CODE_OUT_OF_SCOPE = "out_of_scope";
export const CodeOutOfScope = CODE_OUT_OF_SCOPE;

export const CODE_DEVICE_PENDING = "device_pending";
export const CodeDevicePending = CODE_DEVICE_PENDING;

export const CODE_DEVICE_REVOKED = "device_revoked";
export const CodeDeviceRevoked = CODE_DEVICE_REVOKED;

export const CODE_NOT_FOUND = "not_found";
export const CodeNotFound = CODE_NOT_FOUND;

export const CODE_CASE_COLLISION = "case_collision";
export const CodeCaseCollision = CODE_CASE_COLLISION;

export const CODE_TOO_LARGE = "too_large";
export const CodeTooLarge = CODE_TOO_LARGE;

export const CODE_INTERNAL = "internal";
export const CodeInternal = CODE_INTERNAL;

export const CODE_DIVERGED = "hub_diverged";
export const CodeDiverged = CODE_DIVERGED;

// Device states
export const DEVICE_PENDING = "pending";
export const DevicePending = DEVICE_PENDING;

export const DEVICE_APPROVED = "approved";
export const DeviceApproved = DEVICE_APPROVED;

export const DEVICE_REVOKED = "revoked";
export const DeviceRevoked = DEVICE_REVOKED;

export interface FileEntry {
  path: string;
  sha256: string;
  size: number;
}

export interface Hello {
  protocol: number;
  lw_version: string;
  vault_format: number;
  head: string;
  device: string;
}

export interface Manifest {
  head: string;
  files: FileEntry[];
}

export interface PutResult {
  result: string;
  path: string;
  sha256: string;
  conflict_path?: string;
  head: string;
}

export interface DeleteResult {
  result: string;
  head: string;
}

export interface Change {
  path: string;
  sha256: string | null;
}

export interface Changes {
  head: string;
  full: boolean;
  changed: Change[];
  files?: FileEntry[];
}

export interface StatusReport {
  plugin_version: string;
  last_error: string;
  queue_len: number;
  at: string;
}

export interface APIError {
  code: string;
  message: string;
}

export interface ErrorBody {
  error: APIError;
}

export interface Device {
  id: string;
  name: string;
}
