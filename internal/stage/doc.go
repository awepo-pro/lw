// Package stage is the trust boundary: the content-addressed object store,
// the lock, changeset and op types, validation, apply/commit, diffing, the
// append-only journal, and snapshot/revert. It is the only package that
// mutates the vault working tree. It holds apply.go, cas.go, changeset.go,
// diff.go, engine.go, engine_changeset.go, id.go, journal.go, lock.go,
// op.go, revert.go, snapshot.go and validate.go.
package stage
