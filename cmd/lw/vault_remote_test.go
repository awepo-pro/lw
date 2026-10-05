// vault_remote_test.go pins 047 S2 (042 gap #1): `--vault host:path` is not a
// remote vault — lw has none — and findVaultRoot must say so, with the one
// command that does work, instead of letting the verb fail later on a
// directory literally named "home:~". The line is fixed text; every word of
// it is asserted byte for byte.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

func TestVaultRemoteForm(t *testing.T) {
	chdir(t, t.TempDir()) // a relative host:path must not resolve to anything real

	cases := []struct {
		name     string
		explicit string
		want     string
	}{
		{
			"the frozen example", "home:~/ai-vault",
			`--vault "home:~/ai-vault" looks like host:path, but lw has no remote vaults; run lw on that host instead: ssh home -t lw tui --vault ~/ai-vault`,
		},
		{
			"dotted host and an absolute path", "box.example.com:/srv/vault",
			`--vault "box.example.com:/srv/vault" looks like host:path, but lw has no remote vaults; run lw on that host instead: ssh box.example.com -t lw tui --vault /srv/vault`,
		},
		{
			"the split is at the first colon", "home:/mnt/a:b",
			`--vault "home:/mnt/a:b" looks like host:path, but lw has no remote vaults; run lw on that host instead: ssh home -t lw tui --vault /mnt/a:b`,
		},
		{
			"digits, dashes and underscores are host characters", "vm-1_a:vault",
			`--vault "vm-1_a:vault" looks like host:path, but lw has no remote vaults; run lw on that host instead: ssh vm-1_a -t lw tui --vault vault`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := findVaultRoot(tc.explicit)
			if err == nil {
				t.Fatalf("findVaultRoot(%q) = %q, nil; want the remote-form error", tc.explicit, got)
			}
			if err.Error() != tc.want {
				t.Errorf("error =\n%s\nwant\n%s", err.Error(), tc.want)
			}
		})
	}

	// The message reaches the user through every verb that takes --vault,
	// behind dispatch's own "lw: <verb>: " prefix — tui included, which fails
	// here before it would ever draw a frame.
	for _, verb := range []string{"tui", "status", "lint", "note"} {
		t.Run("through_lw_"+verb, func(t *testing.T) {
			args := []string{verb, "--vault", "home:~/ai-vault"}
			if verb == "note" {
				args = append(args, "-m", "hello")
			}
			stdout, stderr, code := captureRun(t, func() int { return run(args) })
			if code != 1 {
				t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
			}
			want := "lw: " + verb + ": " + cases[0].want + "\n"
			if stdout != "" || stderr != want {
				t.Errorf("stdout = %q, stderr = %q, want empty and %q", stdout, stderr, want)
			}
		})
	}
}

// TestVaultRemoteFormExistingLocalPath: a colon in a path that exists is a
// colon in a path. The check fires only when os.Stat fails, so a real
// directory named host:vault — or a/b:c, or an absolute path — is used as given.
func TestVaultRemoteFormExistingLocalPath(t *testing.T) {
	// The fixture is copied before the chdir: testutil finds spec/fixtures by
	// walking up from the working directory.
	src := testutil.CopyFixture(t, "minimal")
	parent := t.TempDir()
	for _, name := range []string{"a:b", "host:vault"} {
		if err := os.Mkdir(filepath.Join(parent, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(src, filepath.Join(parent, "vault:copy")); err != nil {
		t.Fatalf("rename fixture: %v", err)
	}
	chdir(t, parent)

	for _, explicit := range []string{
		"a:b",                               // the spec's literal example (one-letter host: never matched at all)
		"host:vault",                        // two or more characters before the colon: matched, but it exists
		filepath.Join(parent, "host:vault"), // absolute: starts with "/", never matched
	} {
		got, err := findVaultRoot(explicit)
		if err != nil || got != explicit {
			t.Errorf("findVaultRoot(%q) = %q, %v; want it used as given", explicit, got, err)
		}
	}

	t.Run("and_it_works_as_a_vault_end_to_end", func(t *testing.T) {
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"status", "--vault", "vault:copy"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if !strings.Contains(stdout, "4 pages · 2 raw · 12 tags") {
			t.Errorf("stdout = %q, want the vault's status", stdout)
		}
	})
}

// TestVaultRemoteFormNotMatched pins what is NOT the remote form: a
// single-letter drive (C:\x, C:/x — one character before the colon), and
// anything whose first character cannot start a host name. Each is used as
// given and gets the ordinary not-found path, wherever the verb next fails.
func TestVaultRemoteFormNotMatched(t *testing.T) {
	chdir(t, t.TempDir())

	for _, explicit := range []string{
		`C:\x`,
		`C:/x`,
		`d:vault`,
		`/home/u:vault`,
		`~/vault:v2`,
		`./home:vault`,
		`.home:vault`,
		`-home:vault`,
		`home/x:vault`,
		`home`,
	} {
		got, err := findVaultRoot(explicit)
		if err != nil || got != explicit {
			t.Errorf("findVaultRoot(%q) = %q, %v; want it used as given, no remote-form error", explicit, got, err)
		}
	}

	t.Run("ordinary_not_found_through_lw", func(t *testing.T) {
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"status", "--vault", `C:\x`})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
		}
		if strings.Contains(stderr, "host:path") || strings.Contains(stderr, "no remote vaults") {
			t.Errorf("stderr = %q: a drive letter must not get the remote-vault message", stderr)
		}
	})
}
