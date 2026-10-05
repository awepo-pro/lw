// Package eval is the dev-only accuracy harness (037): it re-runs a fixed
// set of questions and ingest tasks against a frozen copy of a real vault
// and the live provider, and keeps everything the runs produced as plain
// files a scorer can read later. lw itself has no accuracy metric — a
// prompt or provider change can only be judged by repeating the same work
// and comparing it with the run-to-run noise — and this package is the
// repeating half. It is never part of the lw binary: cmd/lw and every
// shipped package stay free of it, and `make install` builds ./cmd/lw only.
//
// A set is a directory (set.go):
//
//	<set>/cases.toml               the cases, the snapshot's name and sha256
//	<set>/vault-<yyyymmdd>.tar.gz  the frozen vault (snapshot.go)
//	<set>/inputs/                  ingest inputs
//	<set>/runs/<run-id>/           one directory per run (run.go)
//
// The harness adds no agent tool and changes no lw behaviour: it drives the
// real lw binary as a subprocess, one fresh extraction of the snapshot per
// run, so the agent still has no filesystem verb and the vault under test is
// never the user's own.
package eval
