// Package hub is lw's phone sync service (079). An Obsidian plugin on the
// phone talks HTTPS over the user's tailnet to `lw hub serve`, which runs on
// an always-on host beside its own clone of the vault. Every change the phone
// sends is applied to that clone under the vault lock, committed, and carried
// to the other PCs by the existing lw sync remote; the phone never sees git.
//
// This file is the frozen seam of workflow 079 (MASTER "Contract" §1). The
// engine (S1), the HTTP server (internal/hubserver, S2), the CLI (cmd/lw, S4)
// and the TypeScript plugin (obsidian/, S3) are all written against it. Do not
// edit it to fit an implementation; a needed change is a logged amendment.
//
// The phone reaches only notes/, ledger/ and notes or images at the vault
// root (CheckPath, testdata/scope-cases.json). wiki/, raw/, .llmwiki/ and lw's
// own root files are not reachable at all, so the review invariant is
// untouched. Nothing the hub does destroys bytes it was not told to replace:
// a write against a stale base keeps both versions, and a delete happens only
// when the phone names the exact version it is deleting.
package hub

import (
	"context"
	"time"
)

// ProtocolVersion is the wire version. A client refuses a hub whose Hello
// carries a version it does not know.
const ProtocolVersion = 1

// MaxBodyBytes caps one uploaded file (a receipt photo is the largest case).
const MaxBodyBytes = 20 << 20

// MaxStatusBytes caps a POST /v1/status body.
const MaxStatusBytes = 64 << 10

// BaseNew is the base of a PUT for a file the phone has never synced.
const BaseNew = "new"

// Result values of a write.
const (
	ResultApplied        = "applied"
	ResultConflictCopy   = "conflict_copy"
	ResultResurrected    = "resurrected"
	ResultMerged         = "merged"
	ResultDeleted        = "deleted"
	ResultRefusedChanged = "refused_changed"
	ResultAlreadyGone    = "already_gone"
)

// Error codes: the "code" of an error body. The HTTP status of each is fixed
// in MASTER "Contract" §2 and implemented by internal/hubserver.
const (
	CodeBadRequest    = "bad_request"    // 400
	CodeUnauthorized  = "unauthorized"   // 401
	CodeOutOfScope    = "out_of_scope"   // 403
	CodeDevicePending = "device_pending" // 403
	CodeDeviceRevoked = "device_revoked" // 403
	CodeNotFound      = "not_found"      // 404
	CodeCaseCollision = "case_collision" // 409
	CodeTooLarge      = "too_large"      // 413
	CodeInternal      = "internal"       // 500
	CodeDiverged      = "hub_diverged"   // 503
)

// Device states in the devices store (internal/hubserver).
const (
	DevicePending  = "pending"
	DeviceApproved = "approved"
	DeviceRevoked  = "revoked"
)

// FileEntry is one in-scope file: its vault-relative slash path, the
// lowercase hex SHA-256 of its bytes, and its size in bytes.
type FileEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Hello answers GET /v1/hello. Head is the hub vault's git HEAD ("" before
// the first commit). Device is the caller's state; the server only answers a
// Hello for an approved device, so it is always "approved" on the wire.
type Hello struct {
	Protocol    int    `json:"protocol"`
	LWVersion   string `json:"lw_version"`
	VaultFormat int    `json:"vault_format"`
	Head        string `json:"head"`
	Device      string `json:"device"`
}

// Manifest answers GET /v1/manifest: every in-scope regular file, sorted by path.
type Manifest struct {
	Head  string      `json:"head"`
	Files []FileEntry `json:"files"`
}

// PutResult answers PUT /v1/file. Path is the path the request named; SHA256
// is the hash of the bytes now at Path; ConflictPath is set only for
// ResultConflictCopy and names where the request's bytes were written.
type PutResult struct {
	Result       string `json:"result"`
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
	ConflictPath string `json:"conflict_path,omitempty"`
	Head         string `json:"head"`
}

// DeleteResult answers DELETE /v1/file.
type DeleteResult struct {
	Result string `json:"result"`
	Head   string `json:"head"`
}

// Change is one path that differs between two commits. SHA256 is nil when the
// path no longer exists at the newer commit.
type Change struct {
	Path   string  `json:"path"`
	SHA256 *string `json:"sha256"`
}

// Changes answers GET /v1/changes. When Full is true the caller's since was
// empty or unknown to the hub: Changed is empty and Files is the whole
// manifest, which the caller reconciles against what it last synced.
type Changes struct {
	Head    string      `json:"head"`
	Full    bool        `json:"full"`
	Changed []Change    `json:"changed"`
	Files   []FileEntry `json:"files,omitempty"`
}

// StatusReport is what the plugin POSTs to /v1/status so the hub host can
// see the phone's state without adb (`lw hub devices`).
type StatusReport struct {
	PluginVersion string `json:"plugin_version"`
	LastError     string `json:"last_error"`
	QueueLen      int    `json:"queue_len"`
	At            string `json:"at"`
}

// APIError is the body of every error response, inside ErrorBody.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrorBody is the JSON of every non-2xx response: {"error":{"code","message"}}.
type ErrorBody struct {
	Error APIError `json:"error"`
}

// Device is the caller of a write, as the server hands it to the Service:
// ID is the plugin's persisted UUID, Name the approved device's slug (it goes
// into commit messages and conflict-copy names).
type Device struct {
	ID   string
	Name string
}

// Error is a refusal with a protocol code. A Service returns it for every
// request it refuses on purpose; any other error is an internal failure.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Service is the hub's vault-facing half. *Engine (engine.go, S1) implements
// it; internal/hubserver turns HTTP into these calls.
type Service interface {
	Hello(ctx context.Context) (Hello, error)
	Manifest(ctx context.Context) (Manifest, error)
	// Read returns the bytes at path and their hex SHA-256.
	Read(ctx context.Context, path string) (data []byte, sha256 string, err error)
	Put(ctx context.Context, dev Device, path, base string, body []byte) (PutResult, error)
	Delete(ctx context.Context, dev Device, path, base string) (DeleteResult, error)
	Changes(ctx context.Context, since string) (Changes, error)
}

// Options wires an Engine to the vault it serves. Every func is optional
// (nil = no-op), which is how the engine's own tests run without lw sync.
type Options struct {
	// Dir is the vault root: a git work tree.
	Dir string
	// Lock takes the vault lock for one read-compare-write. The engine holds
	// it only across that, never across Commit, ImportReceipts or Refresh.
	Lock func() (release func(), err error)
	// Commit commits the work tree with message. A *Error it returns (e.g.
	// CodeDiverged) is passed through to the caller after the write.
	Commit func(message string) error
	// Refresh brings the vault up to date with the remote. It is called
	// before Manifest and Changes; a *Error it returns is passed through, any
	// other error is ignored (the hub serves its local state).
	Refresh func(ctx context.Context) error
	// ImportReceipts copies receipts the phone uploaded under
	// ledger/receipts/ into lw storage (the `lw ledger import` path).
	ImportReceipts func(paths []string) error
	// Now is the clock for conflict-copy names; nil = time.Now.
	Now func() time.Time
	// LWVersion and VaultFormat are reported by Hello.
	LWVersion   string
	VaultFormat int
}
