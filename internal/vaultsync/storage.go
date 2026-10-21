package vaultsync

import "context"

// SyncStorage was 062's two-way mirror of storage/ by file name. It never read
// the catalog, so a removal came back (B1), a same-size replacement was lost
// (B2) and an auto-push shipped the catalog without the bytes (B6). 066
// replaces it with the content-addressed object store in objects.go, driven by
// the catalog from cmd/lw.
//
// Deprecated: 066 S3 deletes this and its call sites. Until then it moves no
// bytes, so that cmd/lw keeps building between subtasks.
func SyncStorage(_ context.Context, _ Options) error {
	return nil
}
