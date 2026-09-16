package main

// cmd_doctor_git.go is doctor's one publish-leak check (005 contract §7): a
// vault whose git repository tracks anything under .llmwiki/ commits every
// session.ndjson — whole file dumps and raw provider output — on the next
// `git add -A`, and a vault may be public.
//
// The check is read-only, and only ever runs `git rev-parse
// --is-inside-work-tree` and `git ls-files`. lw never untracks the
// directory, never touches the index, never rewrites history: it reports
// the exact command and lets the user decide.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// doctorGitTimeout bounds each read-only git call: a git that blocks — a
// hung network filesystem, a stale worktree on a dead mount — must not hang
// a health check.
const doctorGitTimeout = 10 * time.Second

// checkTracked reports whether the vault's git repository tracks anything
// under .llmwiki/. A vault that is not a repository, or a machine without
// git, is skipped with the reason — most vaults are not repos and must not
// be nagged. A repository with nothing tracked passes. A repository that
// tracks .llmwiki/ warns: OK stays true, because lw deliberately refuses to
// fix this and a scripted caller must not see a pre-existing repo state as
// a doctor failure.
func checkTracked(root string) doctorCheck {
	const name = "git"

	if _, err := exec.LookPath("git"); err != nil {
		return skippedTracked("git is not installed")
	}

	git := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), doctorGitTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		out, err := cmd.Output()
		return string(out), err
	}

	if _, err := git("rev-parse", "--is-inside-work-tree"); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return skippedTracked("not a git repository")
		}
		return skippedTracked("git rev-parse failed: " + err.Error())
	}

	out, err := git("ls-files", "--", stateDirName)
	if err != nil {
		return skippedTracked("git ls-files failed: " + err.Error())
	}
	tracked := nonEmptyLines(out)
	if len(tracked) == 0 {
		return doctorCheck{Name: name, OK: true, Detail: "nothing under " + stateRel + "/ is tracked"}
	}
	return doctorCheck{
		Name: name,
		OK:   true,
		Warn: true,
		Detail: fmt.Sprintf(
			"git tracks %d file(s) under %s/ — session transcripts would be committed",
			len(tracked), stateDirName),
		Remedy: fmt.Sprintf(
			"untrack them yourself: git rm -r --cached %s/ && git commit — adding %s to %s only stops future commits; it does not remove what git history already holds",
			stateDirName, gitignoreEntry, gitignoreName),
	}
}

// skippedTracked is the skip every non-diagnosable vault gets, with its
// reason: a skipped check is not a failure and never nags.
func skippedTracked(reason string) doctorCheck {
	return doctorCheck{Name: "git", OK: true, Skipped: true, Detail: "skipped: " + reason}
}

// nonEmptyLines splits git's ls-files output into the paths it listed.
func nonEmptyLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
