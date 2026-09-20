package main

// logging.go installs lw's file-only debug log (010 contract §0, D-10B, as
// amended by MASTER §5 A-10-2). For every verb that operates on a vault,
// the verb itself points slog.Default at <vault>/.llmwiki/logs/lw.log
// immediately after its findVaultRoot succeeds, so an explicit --vault is
// honoured whatever the working directory is (C-1009). main.go installs a
// cwd-ancestry fallback before flag parse for the paths that fail before a
// root is resolved; a verb's own install supersedes it. The install is
// never fatal: a command with no vault, or one whose log dir cannot be
// created, simply keeps the default logger.
//
// Who creates the log dir is exactly who may write to the vault: verbs
// that stage, commit, repair or run the agent create <root>/.llmwiki/logs
// (initLoggingAt); read-only verbs — session, status, diff, log, the lint
// report, doctor — only join a trail that is already there
// (attachLoggingAt). A read-only run never materialises .llmwiki state
// (005 contract §4's byte-exact session pin; doctor's fresh-vault check),
// and a state-less vault has produced no trail to join anyway.

import (
	"os"

	"github.com/awepo-pro/lw/internal/logging"
)

// hasVault reports whether the named verb gets the pre-dispatch fallback
// install (initLogging). It gates only that fallback: every vault-carrying
// verb resolves its root itself and installs against it, which is the
// install that counts. init creates a vault (there is none to log into
// yet) and config edits the user-level config file; neither even gets the
// fallback.
func hasVault(verb string) bool {
	switch verb {
	case "init", "config":
		return false
	default:
		return true
	}
}

// initLogging is the pre-dispatch fallback install. At this point flags
// are not parsed, so an explicit --vault is unknown and the root resolves
// the cwd-ancestry way. It attaches only — a fallback must never create
// state the dispatched verb is not entitled to — and every vault verb
// supersedes it anyway once its own resolution succeeds (re-Init is safe:
// the file reopens O_APPEND, the last install wins, nothing logs in
// between). It never fails the command: no vault found, or no trail to
// join, leaves the default logger in place (010 contract §0).
func initLogging(verb string) {
	if !hasVault(verb) {
		return
	}
	root, err := findVaultRoot("")
	if err != nil {
		return
	}
	attachLoggingAt(root)
}

// initLoggingAt installs the file logger under an explicit vault root,
// creating the log dir. The verbs with a write grant — ingest, commit,
// revert, stage, query, tui, mcp, lint --fix — call it right after
// findVaultRoot(*vaultPath) succeeds, so `lw <verb> --vault X` logs under
// X from any working directory (C-1009) and a cwd that happens to sit in
// another vault misfiles nothing. It is also the seam the cmd-level test
// drives directly. Calling it after the fallback — or again with the same
// root — is fine: Init reopens the file O_APPEND and the newer install
// wins. Init failure is swallowed on purpose: the contract makes the file
// log best-effort, never fatal.
func initLoggingAt(root string) {
	_ = logging.Init(logging.Dir(root), logging.LevelFromEnv())
}

// attachLoggingAt installs the file logger under root only when the vault
// already carries one. The read-only verbs (session, status, diff, log,
// the lint report, doctor) and the pre-dispatch fallback use it: they must
// never create .llmwiki state — lw session is pinned byte-exact by 005
// contract §4, and a plain `lw doctor` on a state-less vault must stay all
// clear — and they emit no records of their own, so silence costs nothing.
func attachLoggingAt(root string) {
	if _, err := os.Stat(logging.Dir(root)); err != nil {
		return
	}
	initLoggingAt(root)
}
