// Command lw is the llmwiki CLI. This file owns dispatch, usage and the
// version flag only — every verb's behaviour lives in its own cmd_<verb>.go,
// implementing the func cmd<Verb>(args []string) error shape.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is the build version. Override at link time with
// -ldflags "-X main.version=...".
var version = "0.1.0-dev"

// verb pairs one CLI verb with its handler, in the order shown by usage.
type verb struct {
	name string
	fn   func(args []string) error
}

var verbs = []verb{
	{"init", cmdInit},
	{"config", cmdConfig},
	{"ingest", cmdIngest},
	{"status", cmdStatus},
	{"diff", cmdDiff},
	{"commit", cmdCommit},
	{"log", cmdLog},
	{"revert", cmdRevert},
	{"query", cmdQuery},
	{"lint", cmdLint},
	{"mcp", cmdMCP},
	{"doctor", cmdDoctor},
	{"tui", cmdTUI},
}

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches args and returns the process exit code: 0 on success, 1 on
// a verb error, 2 on unusable usage.
func run(args []string) int {
	if len(args) == 0 {
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
			return dispatch(v.name, v.fn, args[1:])
		}
	}

	usage(os.Stderr)
	return 2
}

// dispatch runs one verb and applies the error/exit convention: stderr
// "lw: <verb>: <err>" and exit 1 on failure, no stack traces.
func dispatch(name string, fn func(args []string) error, args []string) int {
	if err := fn(args); err != nil {
		fmt.Fprintf(os.Stderr, "lw: %s: %v\n", name, err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: lw <command> [arguments]

commands:
  init [--schema <domain>]     initialize a new vault
  config                       view or edit configuration
  ingest <url|path>...         ingest one or more sources
  status                       show the open changeset, if any
  diff [--op <id>]             show the projected diff of the open changeset
  commit -m "..." [--force]    commit the open changeset
  log [--rejected] [--agent]   show changeset history
  revert <commit-id>           open a reverse changeset for review
  query "..."                  ask the curator agent a question
  lint [--fix]                 run the lint checks
  mcp                          run the MCP server over stdio
  doctor [--unlock]            check vault and lock health
  tui                          launch the terminal UI (default with no command)

flags:
  --version    print the version and exit
  --help, -h   print this message and exit
`)
}
