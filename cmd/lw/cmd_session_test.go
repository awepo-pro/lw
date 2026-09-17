package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/ui"
)

// twCell pads one table cell the way the two-space tabwriter does, so the
// exact-text assertions below can spell out their expected rows.
func twCell(w int, s string) string {
	return s + strings.Repeat(" ", w-len(s))
}

// listFixture writes the four-session vault the ordering tests share: one
// session per state with distinct fixed start times, plus one open session
// with no changeset.json and no records, whose start is unknown and which
// must therefore sort last.
func listFixture(t *testing.T) string {
	t.Helper()
	root := newSessionVault(t)
	writeSession(t, root, "committed", "cs-bbbb222222222222",
		changesetJSON("cs-bbbb222222222222", "2026-09-16T13:00:00Z"),
		srec("2026-09-16T13:00:01Z", "user", "one question"),
	)
	writeSession(t, root, "open", "cs-aaaa111111111111",
		changesetJSON("cs-aaaa111111111111", "2026-09-16T12:00:00Z"),
		srec("2026-09-16T12:00:01Z", "user", "first"),
		srec("2026-09-16T12:00:05Z", "assistant", "first answer"),
	)
	writeSession(t, root, "rejected", "cs-cccc333333333333",
		changesetJSON("cs-cccc333333333333", "2026-09-15T09:00:00Z"),
	)
	writeSessionNDJSON(t, root, "open", "cs-dddd444444444444", "")
	return root
}

func TestSessionList(t *testing.T) {
	t.Run("empty_vault_says_no_sessions", func(t *testing.T) {
		root := newSessionVault(t)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "list", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if stdout != "no sessions\n" {
			t.Errorf("stdout = %q, want %q", stdout, "no sessions\n")
		}

		stdout, _, code = captureRun(t, func() int {
			return run([]string{"session", "list", "--vault", root, "--json"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (--json)", code)
		}
		if stdout != "[]\n" {
			t.Errorf("stdout = %q, want %q", stdout, "[]\n")
		}
	})

	t.Run("newest_first_with_state_and_size", func(t *testing.T) {
		root := listFixture(t)
		sizeA := sessionSize(t, root, "open", "cs-aaaa111111111111")
		sizeB := sessionSize(t, root, "committed", "cs-bbbb222222222222")
		sizeC := sessionSize(t, root, "rejected", "cs-cccc333333333333")
		sizeD := sessionSize(t, root, "open", "cs-dddd444444444444")

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "list", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		// ShortID keeps 9 runes per id, so the ID column is 9+2 wide;
		// STATE is 11 ("committed"+2), STARTED 20+2, RECORDS 7+2, BYTES
		// trailing and unpadded.
		want := strings.Join([]string{
			twCell(11, "ID") + twCell(11, "STATE") + twCell(22, "STARTED") + twCell(9, "RECORDS") + "BYTES",
			twCell(11, "cs-bbbb22") + twCell(11, "committed") + twCell(22, "2026-09-16T13:00:00Z") + twCell(9, "1") + fmt.Sprint(sizeB),
			twCell(11, "cs-aaaa11") + twCell(11, "open") + twCell(22, "2026-09-16T12:00:00Z") + twCell(9, "2") + fmt.Sprint(sizeA),
			twCell(11, "cs-cccc33") + twCell(11, "rejected") + twCell(22, "2026-09-15T09:00:00Z") + twCell(9, "0") + fmt.Sprint(sizeC),
			twCell(11, "cs-dddd44") + twCell(11, "open") + twCell(22, "-") + twCell(9, "0") + fmt.Sprint(sizeD),
		}, "\n") + "\n"
		if stdout != want {
			t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
		}
	})

	t.Run("json_carries_every_field", func(t *testing.T) {
		root := listFixture(t)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "list", "--vault", root, "--json"})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}

		var rows []struct {
			ID      string `json:"id"`
			State   string `json:"state"`
			Started string `json:"started"`
			Records *int   `json:"records"`
			Bytes   int64  `json:"bytes"`
		}
		if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
			t.Fatalf("decode rows: %v\n%s", err, stdout)
		}
		if len(rows) != 4 {
			t.Fatalf("got %d rows, want 4", len(rows))
		}
		sizeB := sessionSize(t, root, "committed", "cs-bbbb222222222222")
		sizeD := sessionSize(t, root, "open", "cs-dddd444444444444")
		want := []struct {
			id, state, started string
			records            int
			bytes              int64
		}{
			{"cs-bbbb222222222222", "committed", "2026-09-16T13:00:00Z", 1, sizeB},
			{"cs-aaaa111111111111", "open", "2026-09-16T12:00:00Z", 2, sessionSize(t, root, "open", "cs-aaaa111111111111")},
			{"cs-cccc333333333333", "rejected", "2026-09-15T09:00:00Z", 0, sessionSize(t, root, "rejected", "cs-cccc333333333333")},
			{"cs-dddd444444444444", "open", "", 0, sizeD},
		}
		for i, w := range want {
			if rows[i].ID != w.id {
				t.Errorf("row %d id = %q, want the full %q", i, rows[i].ID, w.id)
			}
			if rows[i].State != w.state {
				t.Errorf("row %d state = %q, want %q", i, rows[i].State, w.state)
			}
			if rows[i].Started != w.started {
				t.Errorf("row %d started = %q, want %q", i, rows[i].Started, w.started)
			}
			if rows[i].Records == nil || *rows[i].Records != w.records {
				t.Errorf("row %d records = %v, want %d", i, rows[i].Records, w.records)
			}
			if rows[i].Bytes != w.bytes {
				t.Errorf("row %d bytes = %d, want %d", i, rows[i].Bytes, w.bytes)
			}
		}
	})

	t.Run("short_id_is_the_tui_short_id", func(t *testing.T) {
		root := listFixture(t)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "list", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		full := "cs-aaaa111111111111"
		short := ui.ShortID(full)
		if !strings.Contains(stdout, short+" ") {
			t.Errorf("output lacks the TUI short id cell %q:\n%s", short, stdout)
		}
		if strings.Contains(stdout, full) {
			t.Errorf("text output leaks the full id %q; only ShortID may appear:\n%s", full, stdout)
		}
	})

	t.Run("undecodable_session_still_listed", func(t *testing.T) {
		root := newSessionVault(t)
		writeSession(t, root, "open", "cs-aaaa111111111111",
			changesetJSON("cs-aaaa111111111111", "2026-09-16T12:00:00Z"),
			srec("2026-09-16T12:00:01Z", "user", "fine"),
		)
		writeSession(t, root, "committed", "cs-bbbb222222222222",
			changesetJSON("cs-bbbb222222222222", "2026-09-16T11:00:00Z"))
		writeSessionNDJSON(t, root, "committed", "cs-bbbb222222222222",
			`{"ts":"2026-09-16T11:00:00Z","role":"user","content":"ok"}`+"\n"+
				`{"ts":"2026-09-16T11:00:0`)

		stdout, _, code := captureRun(t, func() int {
			return run([]string{"session", "list", "--vault", root})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0: one torn file must not kill the list", code)
		}
		lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
		if len(lines) != 3 {
			t.Fatalf("got %d lines, want header + 2 rows:\n%s", len(lines), stdout)
		}
		// The torn session (11:00) sorts after the healthy one (12:00),
		// newest first, and shows ?.
		wantTorn := twCell(11, "cs-bbbb22") + twCell(11, "committed") + twCell(22, "2026-09-16T11:00:00Z") + twCell(9, "?") + fmt.Sprint(sessionSize(t, root, "committed", "cs-bbbb222222222222"))
		if lines[2] != wantTorn {
			t.Errorf("torn row = %q, want %q", lines[2], wantTorn)
		}
		if !strings.Contains(lines[1], twCell(9, "1")) {
			t.Errorf("healthy row lost its count: %q", lines[1])
		}

		stdout, _, _ = captureRun(t, func() int {
			return run([]string{"session", "list", "--vault", root, "--json"})
		})
		var rows []struct {
			ID      string `json:"id"`
			Records *int   `json:"records"`
		}
		if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
			t.Fatalf("decode rows: %v\n%s", err, stdout)
		}
		if rows[1].ID != "cs-bbbb222222222222" || rows[1].Records != nil {
			t.Errorf("torn row = %+v, want id cs-bbbb222222222222 with records null", rows[1])
		}
		if rows[0].Records == nil || *rows[0].Records != 1 {
			t.Errorf("healthy row records = %v, want 1", rows[0].Records)
		}
	})
}
