// Package stage is the trust boundary: the content-addressed object store,
// the lock, changeset and op types, validation, apply/commit, diffing, the
// append-only journal, and snapshot/revert. It is the only package that
// mutates the vault working tree. It will hold engine.go, cas.go, lock.go,
// id.go, changeset.go, op.go, validate.go, apply.go, diff.go, journal.go,
// snapshot.go and revert.go.
package stage
