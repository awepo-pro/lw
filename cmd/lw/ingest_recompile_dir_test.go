package main

// ingest_recompile_dir_test.go pins 055 S1d, the fix wave from the fresh
// review of S1/S1b/S1c:
//
//	H1 a DIRECTORY argument reached the vault's raws through the walk and
//	   staged raws-of-raws (`lw ingest --recompile raw/` extracted every raw
//	   with its frontmatter, so no body hash ever matched);
//	M1 path spelling defeated detection (`--vault .`, `--vault ../v`, a
//	   symlinked vault root or argument);
//	M3 deleting `deps.Recompile = recompile` from the production wiring left
//	   every test green;
//	L1 `chunks:` was only ever tested with a one-chunk raw.
//
// Every test runs through the real run() dispatch with the newIngestAgent seam
// swapped for the rcSpy fake of ingest_recompile_test.go — except the M3
// end-to-end, which builds the real agent over an httptest LLM.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
)

const (
	rcRawKV  = "raw/articles/kv-cache-explained.md"
	rcRawLev = "raw/papers/leviathan-2023.md"
	rcPDF    = "raw/articles/a.pdf"

	rcNothing = "nothing to ingest: every source is already in the vault\n"
)

// rcStrayLine is the line a walked file inside the vault's raw/ that is not a
// committed raw source prints (055 S1d, H1) — with or without --recompile.
func rcStrayLine(walked string) string {
	return "skipped " + walked + ": inside the vault's raw/ but not a committed raw source\n"
}

// rcHintLine is D4's uncited skip line, restated here so the production
// builder is not its own oracle.
func rcHintLine(arg, raw string) string {
	return "skipped " + arg + ": already in the vault at " + raw + ", cited by no page — run lw ingest --recompile " + arg + " to write pages from it\n"
}

// rcDirVault is rcVault plus a PDF original beside raw/articles/a.md — a file
// the walker selects (it handles .pdf) that is no committed raw source. The
// fixture's own three raws are the walk's other files: a.md (cited by no page),
// kv-cache-explained.md and leviathan-2023.md (each cited by fixture pages).
func rcDirVault(t *testing.T) string {
	t.Helper()
	root, _ := rcVault(t)
	dirFile(t, root, rcPDF, "%PDF-1.4 not really a pdf\n")
	return root
}

// TestRecompileDirArgument is H1's headline. A directory argument that reaches
// the vault's raw/ is not extracted: every walked file that is a committed raw
// source is a recompile target (the flag) and the rest of raw/ — a PDF
// original beside a raw — is skipped with one exact line. Not one
// ingest_source is staged, and raw is left byte for byte as it was. Before the
// fix `raw`, `raw/` and `./raw` walked and extracted the raws with their
// frontmatter (a second raw each) and `raw/articles` died as "no committed raw
// source at that path".
func TestRecompileDirArgument(t *testing.T) {
	tests := []struct {
		name    string
		arg     func(root string) string
		cwdRoot bool     // run from the vault root (relative spellings) or from elsewhere
		walked  string   // how the walk spells the PDF
		targets []string // committed raws the walk reaches, lexical order
	}{
		{"raw", func(string) string { return "raw" }, true, rcPDF, []string{rcRawA, rcRawKV, rcRawLev}},
		{"raw_slash", func(string) string { return "raw/" }, true, rcPDF, []string{rcRawA, rcRawKV, rcRawLev}},
		{"dot_raw", func(string) string { return "./raw" }, true, rcPDF, []string{rcRawA, rcRawKV, rcRawLev}},
		{"raw_articles", func(string) string { return "raw/articles" }, true, rcPDF, []string{rcRawA, rcRawKV}},
		{"abs_root_raw", func(root string) string { return filepath.Join(root, "raw") }, false, "", []string{rcRawA, rcRawKV, rcRawLev}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := rcDirVault(t)
			if tc.cwdRoot {
				chdir(t, root)
			} else {
				chdir(t, t.TempDir())
			}
			arg := tc.arg(root)
			walked := tc.walked
			if walked == "" {
				walked = filepath.Join(root, rcPDF)
			}
			before := snapshotVaultFiles(t, root)
			spy := withRcSpy(t, []stage.Op{rcPageOp(rcRawA)})

			stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", arg)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
			}
			if !strings.Contains(stdout, rcStrayLine(walked)) {
				t.Errorf("stdout = %q, want it to contain the exact line %q", stdout, rcStrayLine(walked))
			}
			if got, want := strings.Join(spy.recompile, ","), strings.Join(tc.targets, ","); got != want {
				t.Errorf("recompile = %q, want %q", got, want)
			}
			// The announce lines come in walk order, each naming the raw's own path.
			at := 0
			for _, raw := range tc.targets {
				line := "recompiling " + raw + "\n"
				i := strings.Index(stdout[at:], line)
				if i < 0 {
					t.Fatalf("stdout = %q, want %q (in order) after offset %d", stdout, line, at)
				}
				at += i + len(line)
			}
			if got := strings.Count(stdout, "recompiling "); got != len(tc.targets) {
				t.Errorf("stdout has %d recompiling line(s), want %d:\n%s", got, len(tc.targets), stdout)
			}
			msg := spy.message()
			if !strings.HasPrefix(msg, rcWantHead) || strings.Contains(msg, "New source material") {
				t.Errorf("message is not a recompile-only message:\n%s", msg)
			}
			if got := strings.Count(msg, "- raw: "); got != len(tc.targets) {
				t.Errorf("message carries %d item block(s), want %d:\n%s", got, len(tc.targets), msg)
			}

			cs := rcOpenChangeset(t, root)
			if n := rcCount(cs, stage.OpIngestSource); n != 0 {
				t.Errorf("changeset has %d ingest_source op(s), want 0", n)
			}
			after := snapshotVaultFiles(t, root)
			if len(after) != len(before) {
				t.Errorf("working tree has %d wiki/raw files, had %d", len(after), len(before))
			}
			for p, b := range before {
				if after[p] != b {
					t.Errorf("%s changed on disk though nothing committed", p)
				}
			}
		})
	}

	t.Run("with_a_new_file_is_mixed", func(t *testing.T) {
		root := rcDirVault(t)
		chdir(t, root)
		fresh := writtenSource(t, "fresh.md", "# Fresh Source\n\nFresh body.\n")
		spy := withRcSpy(t, []stage.Op{rcPageOp(rcRawA)})

		stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", "raw/articles", fresh)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if got, want := strings.Join(spy.recompile, ","), rcRawA+","+rcRawKV; got != want {
			t.Errorf("recompile = %q, want %q", got, want)
		}
		wantSourcesEqual(t, originalSources(t, spy.message()), []string{fresh})
		if !strings.Contains(spy.message(), rcWantTail) {
			t.Errorf("a mixed invocation's message lacks the recompile tail:\n%s", spy.message())
		}
		if n := rcCount(rcOpenChangeset(t, root), stage.OpIngestSource); n != 0 {
			t.Errorf("changeset has %d ingest_source op(s); the fake stages none", n)
		}
	})

	t.Run("dry_run_lists_targets_and_never_extracts", func(t *testing.T) {
		root := rcDirVault(t)
		chdir(t, root)
		ingestLimitsEnv(t, 0)
		noAgentEver(t)

		stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", "--dry-run", "raw")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		want := rcStrayLine(rcPDF) +
			"would recompile raw/articles/a.md\n" +
			"would recompile raw/articles/kv-cache-explained.md\n" +
			"would recompile raw/papers/leviathan-2023.md\n" +
			"within limits: 3 files, 2 KB (limit 10 files, 282 KB)\n"
		if stdout != want {
			t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
		}
		noChangesetsAnywhere(t, root)
	})
}

// TestRecompileDirOutsideVaultWalkedNormally: a directory is the vault's raw/
// only when it IS that directory. The working directory here is a second tree
// whose own raw/articles holds a new file; `--recompile raw/articles` walks
// THAT directory (the S1c rule, for directories) and ingests the file as the
// new source it is — it is neither an error ("no committed raw source at that
// path") nor skipped as if it were the target vault's raw.
func TestRecompileDirOutsideVaultWalkedNormally(t *testing.T) {
	root := rcDirVault(t)
	cwd := t.TempDir()
	dirFile(t, cwd, "raw/articles/elsewhere.md", "# Elsewhere\n\nA file in another tree that only looks like the vault's raw/.\n")
	chdir(t, cwd)
	spy := withRcSpy(t, []stage.Op{rcPageOp("raw/articles/kv-cache-explained.md")})

	stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", "raw/articles")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if strings.Contains(stdout, "inside the vault's raw/") || strings.Contains(stdout, "skipped") {
		t.Errorf("stdout = %q, want no skip line for a directory outside the vault", stdout)
	}
	wantSourcesEqual(t, originalSources(t, spy.message()), []string{"raw/articles/elsewhere.md"})
	if len(spy.recompile) != 0 {
		t.Errorf("recompile = %q, want none: nothing walked is the vault's raw", spy.recompile)
	}
}

// TestIngestDirUnderRawWithoutFlagSkips: without --recompile a walked committed
// raw is the A-807 skip (D4: the uncited one names the flag, the cited one is
// today's plain line) and is never extracted — extracting it staged a raw-of-a-
// raw — and a walked non-raw inside raw/ gets the stray line. A command whose
// every file is skipped opens nothing and builds no agent.
func TestIngestDirUnderRawWithoutFlagSkips(t *testing.T) {
	t.Run("raw_dir_from_the_vault_root", func(t *testing.T) {
		root := rcDirVault(t)
		chdir(t, root)
		spy := withRcSpy(t, nil)

		stdout, stderr, code := rcRun(t, "--vault", root, "raw")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		want := rcStrayLine(rcPDF) +
			rcHintLine("raw/articles/a.md", rcRawA) +
			"skipped raw/articles/kv-cache-explained.md: already in the vault at raw/articles/kv-cache-explained.md\n" +
			"skipped raw/papers/leviathan-2023.md: already in the vault at raw/papers/leviathan-2023.md\n" +
			rcNothing
		if stdout != want {
			t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
		}
		if spy.built != 0 {
			t.Errorf("newIngestAgent built %d time(s), want 0", spy.built)
		}
		noChangesetsAnywhere(t, root)
	})

	t.Run("absolute_dir_from_elsewhere", func(t *testing.T) {
		root := rcDirVault(t)
		chdir(t, t.TempDir())
		spy := withRcSpy(t, nil)

		dir := filepath.Join(root, "raw", "articles")
		rcSafeArg(t, dir)
		stdout, stderr, code := rcRun(t, "--vault", root, dir)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		a := dir + "/a.md"
		want := rcStrayLine(dir+"/a.pdf") +
			rcHintLine(a, rcRawA) +
			"skipped " + dir + "/kv-cache-explained.md: already in the vault at raw/articles/kv-cache-explained.md\n" +
			rcNothing
		if stdout != want {
			t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
		}
		if spy.built != 0 {
			t.Errorf("newIngestAgent built %d time(s), want 0", spy.built)
		}
	})

	t.Run("the_rest_still_ingests", func(t *testing.T) {
		root := rcDirVault(t)
		chdir(t, root)
		fresh := writtenSource(t, "fresh.md", "# Fresh Source\n\nFresh body.\n")
		spy := withRcSpy(t, []stage.Op{rcPageOp("raw/articles/kv-cache-explained.md")})

		stdout, stderr, code := rcRun(t, "--vault", root, "raw/articles", fresh)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if !strings.Contains(stdout, rcStrayLine(rcPDF)) {
			t.Errorf("stdout = %q, want the stray line", stdout)
		}
		wantSourcesEqual(t, originalSources(t, spy.message()), []string{fresh})
		if len(spy.recompile) != 0 {
			t.Errorf("recompile = %q, want none without the flag", spy.recompile)
		}
	})
}

// TestIngestDirUnderRawExplicitFileWins: the stray line is for WALKED files. A
// file named outright keeps today's rules (S1b/S1c) — here, a file inside raw/
// that no committed raw is, named explicitly beside the directory that also
// walks it, is ingested as the file it is, and no stray line is printed for it.
func TestIngestDirUnderRawExplicitFileWins(t *testing.T) {
	root := rcDirVault(t)
	dirFile(t, root, "raw/articles/stray.md", "# Stray\n\nNo frontmatter: not a committed raw.\n")
	chdir(t, root)
	spy := withRcSpy(t, []stage.Op{rcPageOp("raw/articles/kv-cache-explained.md")})

	stdout, stderr, code := rcRun(t, "--vault", root, "raw/articles", "raw/articles/stray.md")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	if strings.Contains(stdout, "raw/articles/stray.md: inside the vault's raw/") {
		t.Errorf("stdout = %q, want no stray line for an explicitly named file", stdout)
	}
	// The walked committed raws are skipped, never extracted; the explicitly
	// named file (also reached by the walk) is the one new source.
	wantSourcesEqual(t, originalSources(t, spy.message()), []string{"raw/articles/stray.md"})
}

// rcSibling makes a working directory next to the vault's directory, so
// "../<vault dir>/…" spells the vault from outside it.
func rcSibling(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(filepath.Dir(root), "elsewhere")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// rcSpellings runs one command shape over several spellings of the SAME
// committed raw and checks that they are told apart from files by nothing but
// what they name: with --recompile each is a target (no ingest_source, the
// agent built with the raw's vault path, the message the recompile-only one);
// without it each is the A-807 skip (D4's hint line, as typed) and nothing is
// extracted or opened.
func rcSpellings(t *testing.T, vaultArg func(root string) string, cwd func(t *testing.T, root string) string, args []func(root string) string) {
	t.Helper()
	for i, arg := range args {
		t.Run(fmt.Sprintf("flag_spelling_%d", i), func(t *testing.T) {
			root, _ := rcVault(t)
			chdir(t, cwd(t, root))
			spy := withRcSpy(t, []stage.Op{rcPageOp(rcRawA)})
			a := arg(root)

			stdout, stderr, code := rcRun(t, "--vault", vaultArg(root), "--recompile", a)
			if code != 0 {
				t.Fatalf("%q: exit code = %d, want 0; stderr=%q stdout=%q", a, code, stderr, stdout)
			}
			if !strings.Contains(stdout, "recompiling raw/articles/a.md\n") {
				t.Errorf("%q: stdout = %q, want the line `recompiling raw/articles/a.md`", a, stdout)
			}
			if got := strings.Join(spy.recompile, ","); got != rcRawA {
				t.Errorf("%q: recompile = %q, want [%s]", a, spy.recompile, rcRawA)
			}
			if got, want := spy.message(), rcWantHead+rcItem(rcRawA, 1, "Alpha Source", "no page"); got != want {
				t.Errorf("%q: message =\n%q\nwant\n%q", a, got, want)
			}
			if n := rcCount(rcOpenChangeset(t, root), stage.OpIngestSource); n != 0 {
				t.Errorf("%q: changeset has %d ingest_source op(s), want 0", a, n)
			}
		})
		t.Run(fmt.Sprintf("noflag_spelling_%d", i), func(t *testing.T) {
			root, _ := rcVault(t)
			chdir(t, cwd(t, root))
			spy := withRcSpy(t, nil)
			a := arg(root)
			rcSafeArg(t, a)

			stdout, stderr, code := rcRun(t, "--vault", vaultArg(root), a)
			if code != 0 {
				t.Fatalf("%q: exit code = %d, want 0; stderr=%q stdout=%q", a, code, stderr, stdout)
			}
			if want := rcHintLine(a, rcRawA) + rcNothing; stdout != want {
				t.Errorf("%q: stdout =\n%q\nwant\n%q", a, stdout, want)
			}
			if spy.built != 0 {
				t.Errorf("%q: newIngestAgent built %d time(s), want 0", a, spy.built)
			}
			noChangesetsAnywhere(t, root)
		})
	}
}

// TestRecompileRelativeVault is M1 for a relative --vault: findVaultRoot hands
// the flag back verbatim, so Rel(".", "/abs/…") failed and an absolute or
// ../-spelled argument was read as a file — extracted with its frontmatter, a
// second raw. `--vault .` from inside the vault and `--vault ../<dir>` from a
// sibling must detect raw/articles/a.md, <abs>/raw/articles/a.md and
// ../<dir>/raw/articles/a.md alike.
func TestRecompileRelativeVault(t *testing.T) {
	t.Run("vault_dot_from_inside", func(t *testing.T) {
		rcSpellings(t,
			func(string) string { return "." },
			func(_ *testing.T, root string) string { return root },
			[]func(string) string{
				func(string) string { return "raw/articles/a.md" },
				func(root string) string { return filepath.Join(root, "raw", "articles", "a.md") },
				func(root string) string { return "../" + filepath.Base(root) + "/raw/articles/a.md" },
				func(string) string { return "./raw/articles/../articles/a.md" },
			})
	})

	t.Run("vault_dotdot_from_a_sibling", func(t *testing.T) {
		rcSpellings(t,
			func(root string) string { return "../" + filepath.Base(root) },
			rcSibling,
			[]func(string) string{
				func(string) string { return "raw/articles/a.md" },
				func(root string) string { return filepath.Join(root, "raw", "articles", "a.md") },
				func(root string) string { return "../" + filepath.Base(root) + "/raw/articles/a.md" },
			})
	})

	t.Run("directory_argument_through_a_relative_vault", func(t *testing.T) {
		root := rcDirVault(t)
		chdir(t, rcSibling(t, root))
		spy := withRcSpy(t, []stage.Op{rcPageOp(rcRawA)})

		stdout, stderr, code := rcRun(t, "--vault", "../"+filepath.Base(root), "--recompile", "../"+filepath.Base(root)+"/raw/articles")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if got, want := strings.Join(spy.recompile, ","), rcRawA+","+rcRawKV; got != want {
			t.Errorf("recompile = %q, want %q", got, want)
		}
		if want := rcStrayLine("../" + filepath.Base(root) + "/" + rcPDF); !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	})
}

// TestRecompileSymlinkedVault is M1 for symlinks: a vault root reached through
// a link, an argument spelled through the link or the real directory, and a
// symlink to the raw file itself all name the same committed raw — Abs +
// EvalSymlinks on both sides before Rel — and none is read as a file.
func TestRecompileSymlinkedVault(t *testing.T) {
	// link makes <tmp>/vault-link -> root and <tmp>/alias.md -> root/raw/articles/a.md.
	link := func(t *testing.T, root string) (vaultLink, fileLink string) {
		t.Helper()
		tmp := t.TempDir()
		vaultLink = filepath.Join(tmp, "vault-link")
		fileLink = filepath.Join(tmp, "alias.md")
		if err := os.Symlink(root, vaultLink); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "raw", "articles", "a.md"), fileLink); err != nil {
			t.Fatal(err)
		}
		return vaultLink, fileLink
	}
	elsewhere := func(t *testing.T, _ string) string { return t.TempDir() }

	// The link paths are per-test temp dirs, so each spelling is a function of
	// the paths its own subtest made.
	spellings := func(t *testing.T, name string, vaultSpelling func(root, vaultLink string) string, argSpellings ...func(root, vaultLink, fileLink string) string) {
		t.Run(name, func(t *testing.T) {
			for i, spell := range argSpellings {
				t.Run(fmt.Sprintf("flag_%d", i), func(t *testing.T) {
					root, _ := rcVault(t)
					vl, fl := link(t, root)
					chdir(t, elsewhere(t, root))
					spy := withRcSpy(t, []stage.Op{rcPageOp(rcRawA)})
					a := spell(root, vl, fl)

					stdout, stderr, code := rcRun(t, "--vault", vaultSpelling(root, vl), "--recompile", a)
					if code != 0 {
						t.Fatalf("%q: exit code = %d, want 0; stderr=%q stdout=%q", a, code, stderr, stdout)
					}
					if !strings.Contains(stdout, "recompiling raw/articles/a.md\n") {
						t.Errorf("%q: stdout = %q, want the line `recompiling raw/articles/a.md`", a, stdout)
					}
					if got := strings.Join(spy.recompile, ","); got != rcRawA {
						t.Errorf("%q: recompile = %q, want [%s]", a, spy.recompile, rcRawA)
					}
					if n := rcCount(rcOpenChangeset(t, root), stage.OpIngestSource); n != 0 {
						t.Errorf("%q: changeset has %d ingest_source op(s), want 0", a, n)
					}
				})
				t.Run(fmt.Sprintf("noflag_%d", i), func(t *testing.T) {
					root, _ := rcVault(t)
					vl, fl := link(t, root)
					chdir(t, elsewhere(t, root))
					spy := withRcSpy(t, nil)
					a := spell(root, vl, fl)
					rcSafeArg(t, a)

					stdout, stderr, code := rcRun(t, "--vault", vaultSpelling(root, vl), a)
					if code != 0 {
						t.Fatalf("%q: exit code = %d, want 0; stderr=%q stdout=%q", a, code, stderr, stdout)
					}
					if want := rcHintLine(a, rcRawA) + rcNothing; stdout != want {
						t.Errorf("%q: stdout =\n%q\nwant\n%q", a, stdout, want)
					}
					if spy.built != 0 {
						t.Errorf("%q: newIngestAgent built %d time(s), want 0", a, spy.built)
					}
					noChangesetsAnywhere(t, root)
				})
			}
		})
	}

	spellings(t, "vault_given_as_the_link",
		func(_, vl string) string { return vl },
		func(root, vl, fl string) string { return filepath.Join(root, "raw", "articles", "a.md") },
		func(root, vl, fl string) string { return filepath.Join(vl, "raw", "articles", "a.md") },
		func(root, vl, fl string) string { return fl },
		func(root, vl, fl string) string { return "raw/articles/a.md" },
	)
	spellings(t, "vault_given_as_the_real_path",
		func(root, _ string) string { return root },
		func(root, vl, fl string) string { return filepath.Join(vl, "raw", "articles", "a.md") },
		func(root, vl, fl string) string { return fl },
	)

	t.Run("directory_through_the_link", func(t *testing.T) {
		root := rcDirVault(t)
		vl, _ := link(t, root)
		chdir(t, t.TempDir())
		spy := withRcSpy(t, []stage.Op{rcPageOp(rcRawA)})

		stdout, stderr, code := rcRun(t, "--vault", vl, "--recompile", filepath.Join(vl, "raw", "articles"))
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
		}
		if got, want := strings.Join(spy.recompile, ","), rcRawA+","+rcRawKV; got != want {
			t.Errorf("recompile = %q, want %q", got, want)
		}
		if want := rcStrayLine(filepath.Join(vl, rcPDF)); !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	})
}

// TestRecompileItemCountsChunks is L1: the item block's `chunks:` is what
// raw.get reports for the committed body, so a raw of several chunks shows
// several. Every other recompile test commits a one-chunk raw, which left a
// hard-coded `chunks: 1` green.
func TestRecompileItemCountsChunks(t *testing.T) {
	root, _ := rcVault(t)
	const rawLong = "raw/articles/long.md"
	body := "# Long Source\n\n" + strings.Repeat("abcdefghi\n", 4000) // 40014 runes: 3 chunks of 16000
	rcCommitRaw(t, root, rawLong, body)
	if n := tools.RawChunkCount(body); n != 3 {
		t.Fatalf("fixture body has %d chunk(s), want 3: the test would not pin the count", n)
	}
	spy := withRcSpy(t, []stage.Op{rcPageOp(rawLong)})

	stdout, stderr, code := rcRun(t, "--vault", root, "--recompile", rawLong, rcRawA)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	want := rcWantHead + rcItem(rawLong, 3, "Long Source", "no page") + rcItem(rcRawA, 1, "Alpha Source", "no page")
	if got := spy.message(); got != want {
		t.Errorf("message =\n%q\nwant\n%q", got, want)
	}
}

// TestNewIngestAgentDepsCarryRecompile is M3. The recompile list must reach the
// registry's tools.Deps in production, or stage.close never holds a recompile
// target to 040's read-every-chunk rule — and that wiring sat in newIngestAgent
// with no test: deleting it left every test green. The first subtest pins the
// Deps construction newIngestAgent calls; the second builds the REAL agent over
// an httptest LLM, has the model close at once, and checks the refusal for the
// unread target comes back to the model.
func TestNewIngestAgentDepsCarryRecompile(t *testing.T) {
	t.Run("deps_construction", func(t *testing.T) {
		root, _ := rcVault(t)
		e := openEngine(t, root)
		cfg := config.Default()

		deps := ingestToolDeps(e, cfg, agentExtractors(root, cfg), []string{rcRawA, rcRawKV})
		if got := strings.Join(deps.Recompile, ","); got != rcRawA+","+rcRawKV {
			t.Errorf("Deps.Recompile = %q, want [%s %s]", deps.Recompile, rcRawA, rcRawKV)
		}
		if deps.Vault == nil || deps.Engine == nil || deps.Index == nil || deps.Extract == nil {
			t.Errorf("Deps lost the agentToolDeps wiring: %+v", deps)
		}
		if got := ingestToolDeps(e, cfg, agentExtractors(root, cfg), nil).Recompile; len(got) != 0 {
			t.Errorf("Deps.Recompile = %q for no targets, want none", got)
		}
	})

	t.Run("close_guard_through_newIngestAgent", func(t *testing.T) {
		root, _ := rcVault(t)
		e := openEngine(t, root)

		var mu sync.Mutex
		var bodies []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(b))
			n := len(bodies)
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			if n == 1 {
				// Round one: the model closes without reading a chunk.
				call := `{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"stage.close","arguments":"{}"}}]}}]}`
				fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", call,
					`{"choices":[{"index":0,"finish_reason":"tool_calls","delta":{}}]}`)
				return
			}
			fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n",
				`{"choices":[{"index":0,"delta":{"role":"assistant","content":"done"}}]}`,
				`{"choices":[{"index":0,"finish_reason":"stop","delta":{}}]}`)
		}))
		defer srv.Close()

		cfg := config.Default()
		cfg.LLM.BaseURL = srv.URL
		cfg.LLM.APIKey = "test-key"
		sessions := agent.NewFileSessions(root)
		cs, _, err := e.OpenOrJoin("recompile "+rcRawA, stage.Author{Kind: "agent", Model: cfg.LLM.Model})
		if err != nil {
			t.Fatalf("OpenOrJoin: %v", err)
		}
		sess, err := sessions.Create(cs.ID)
		if err != nil {
			t.Fatalf("sessions.Create: %v", err)
		}
		ag, err := newIngestAgent(e, cfg, sessions, agentExtractors(root, cfg), []string{rcRawA})
		if err != nil {
			t.Fatalf("newIngestAgent: %v", err)
		}
		if err := runAgentTurn(context.Background(), ag, sess.ID, "recompile "+rcRawA, io.Discard); err != nil {
			t.Fatalf("turn: %v", err)
		}

		mu.Lock()
		defer mu.Unlock()
		if len(bodies) < 2 {
			t.Fatalf("the LLM saw %d request(s), want at least 2 (the close and the round after it)", len(bodies))
		}
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal([]byte(bodies[1]), &req); err != nil {
			t.Fatalf("decode the second request: %v", err)
		}
		want := "stage.close: not every chunk of the sources ingested in this changeset was read — " + rcRawA + " chunks 1 of 1 unread"
		for _, m := range req.Messages {
			if m.Role == "tool" && strings.HasPrefix(m.Content, want) {
				return
			}
		}
		t.Errorf("no tool message in the second request starts with %q; messages: %+v", want, req.Messages)
	})
}

// TestRcFixtureSanity keeps the fixtures this file leans on honest: the
// minimal vault's two raws are cited and the walked PDF is not a source.
func TestRcFixtureSanity(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	e := openEngine(t, root)
	for _, raw := range []string{rcRawKV, rcRawLev} {
		if _, ok := e.Vault().RawSource(raw); !ok {
			t.Errorf("fixture has no committed raw %s", raw)
		}
		if len(rawCitations(e.Vault())[raw]) == 0 {
			t.Errorf("fixture raw %s is cited by no page; the D4 plain line would be a hint", raw)
		}
	}
}
