// Command lw is the llmwiki CLI. This file owns dispatch and usage — every
// verb's behaviour lives in its own cmd_<verb>.go, implementing the
// func cmd<Verb>(args []string) error shape.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// processStart is the process's own clock, set at package init — as close
// to exec as Go lets us get without touching main(). 025 T3's
// "tui first frame" line measures from here (F.W8: "since process start"),
// so the span includes config loads, vault root discovery and OpenEngine,
// all of which run before ui.NewApp exists to time anything.
var processStart = time.Now()

// verb pairs one CLI verb with its handler, in the order shown by usage.
type verb struct {
	name string
	fn   func(args []string) error
}

var verbs = []verb{
	{"init", cmdInit},
	{"config", cmdConfig},
	{"ingest", cmdIngest},
	{"note", cmdNote},
	{"status", cmdStatus},
	{"diff", cmdDiff},
	{"commit", cmdCommit},
	{"log", cmdLog},
	{"session", cmdSession},
	{"trace", cmdTrace},
	{"revert", cmdRevert},
	{"query", cmdQuery},
	{"lint", cmdLint},
	{"mcp", cmdMCP},
	{"doctor", cmdDoctor},
	{"sync", cmdSync},
	{"tui", cmdTUI},
	{"stage", cmdStage},
	{"version", cmdVersion},
}

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches args and returns the process exit code: 0 on success, 1 on
// a verb error, 2 on unusable usage.
func run(args []string) int {
	if len(args) == 0 {
		initLogging("tui") // attach-only fallback; cmdTUI re-installs at its resolved root
		return dispatch("tui", cmdTUI, nil)
	}

	switch args[0] {
	case "--version":
		fmt.Println("lw " + version)
		return 0
	case "--help", "-h":
		usage(os.Stdout)
		return 0
	}

	for _, v := range verbs {
		if v.name == args[0] {
			// Attach-only fallback (A-10-2): flags are not parsed yet, so
			// this resolves cwd-ancestry only and never creates a log dir.
			// The verb re-installs at the root its own
			// findVaultRoot(*vaultPath) resolves — explicit --vault wins
			// over the working directory (C-1009).
			initLogging(v.name)
			return dispatch(v.name, v.fn, args[1:])
		}
	}

	usage(os.Stderr)
	return 2
}

// dispatch runs one verb and applies the error/exit convention: stderr
// "lw: <verb>: <err>" and exit 1 on failure, no stack traces.
//
// Contract (backbone §13, MASTER §9 D-AC): a verb whose own stdout is the
// result signals its exit code with *exitError instead of a bare error, so
// its output is not followed by a spurious "lw: <verb>: ..." line. A bare
// error keeps the original behaviour exactly.
func dispatch(name string, fn func(args []string) error, args []string) int {
	code := dispatchVerb(name, fn, args)
	// 042: whatever the verb asked to be done once it has said its piece — the
	// auto-sync push — runs after its result, or its error, is on the screen.
	runVerbEnd()
	return code
}

// dispatchVerb runs one verb and prints its error, if any.
func dispatchVerb(name string, fn func(args []string) error, args []string) int {
	err := fn(args)
	if err == nil {
		return 0
	}

	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}

	fmt.Fprintf(os.Stderr, "lw: %s: %v\n", name, err)
	return 1
}

// verbEnd holds what verbs have asked to run after dispatch has printed their
// result (042 M2). The one user is auto-sync: a commit's hook runs inside
// Engine.Commit, long before `lw commit` prints "committed <id>", and a push
// over a slow tunnel must not come between the user and that line.
var verbEnd struct {
	sync.Mutex
	fns []func()
}

// atVerbEnd registers fn to run when the current verb has finished and
// dispatch has printed what it had to say.
func atVerbEnd(fn func()) {
	verbEnd.Lock()
	defer verbEnd.Unlock()
	verbEnd.fns = append(verbEnd.fns, fn)
}

// runVerbEnd runs and clears the registered functions, in order.
func runVerbEnd() {
	verbEnd.Lock()
	fns := verbEnd.fns
	verbEnd.fns = nil
	verbEnd.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// discardVerbEnd drops what is registered without running it: tests that call
// a step directly, with no verb around it, must not leave a push for the next
// test's dispatch to run.
func discardVerbEnd() {
	verbEnd.Lock()
	verbEnd.fns = nil
	verbEnd.Unlock()
}

// exitError lets a verb choose the process exit code and suppress the
// "lw: <verb>: " prefix, for a verb whose own stdout is the result.
type exitError struct{ code int }

// Error implements the error interface. dispatch never prints this string —
// it only reads code — but exitError must still satisfy error.
func (e *exitError) Error() string {
	return fmt.Sprintf("exit %d", e.code)
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: lw <command> [arguments]

commands:
  init [--schema <domain>]     initialize a new vault
  config                       view or edit configuration
  ingest <url|path|dir>...     ingest one or more sources
  note [-m "..."]              capture a quick raw note outside the wiki (no LLM, no review)
  note list [-n N]             list captured notes, newest first
  status                       show the open changeset, if any
  diff [--op <id>]             show the projected diff of the open changeset
  commit -m "..." [--force]    commit the open changeset
  log [--rejected] [--agent]   show changeset history
  session list [--json]        list recorded agent sessions
  session show [<id>] [--plain] [--json] [--thinking]
                               print one session's transcript
  trace [-n N]                 list agent turn traces; trace show <ref> inspects one
  revert <commit-id>           open a reverse changeset for review
  query "..."                  ask the curator agent a question
  lint [--fix]                 run the lint checks
  mcp                          run the MCP server over stdio
  doctor [--unlock] [--rebuild-index] [--discard-changeset] [--json]
                               check vault and lock health
  sync [--take-remote]         pull then push the vault through its [sync] remotes
  sync status                  show ahead/behind against the remote, without changing anything
  sync init <remote>           put this vault under lw sync and push it to an empty remote
  sync clone <remote> <dir>    copy a synced vault from its remote to this PC
                               (lw sync owns the vault's .gitignore and rewrites it)
  tui                          launch the terminal UI (default with no command)
  version                      print the build version and exit

flags:
  --version    print the version and exit
  --help, -h   print this message and exit
`)
}
