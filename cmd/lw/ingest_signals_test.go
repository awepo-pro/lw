package main

// ingest_signals_test.go pins 061 FIX3 F5: every signal that ends a session
// takes the same rollback. A dropped ssh connection sends SIGHUP, not SIGINT —
// `lw ingest` over ssh, the connection drops, and the process died mid-turn
// with a half-staged changeset left open (cmd/lweval/main.go already listens
// for all three). TestIngestRealSIGINTRollsBack (ingest_interrupt_test.go)
// covers SIGINT; this covers the other two, with real signals sent to this
// process and no seam swapped.

import (
	"github.com/awepo-pro/lw/internal/testutil"
	"os"
	"os/signal"
	"syscall"
	"testing"
)

func TestIngestRealSignalsRollBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
	}{
		{"SIGTERM", syscall.SIGTERM},
		{"SIGHUP", syscall.SIGHUP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Registered first, so the signal can never take the test binary
			// down even if the verb installed no handler for it.
			guard := make(chan os.Signal, 4)
			signal.Notify(guard, tc.sig)
			t.Cleanup(func() { signal.Stop(guard) })

			maxTokens8192Env(t)
			root := testutil.CopyFixture(t, "minimal")
			src := shortSource(t)
			ag := withInterruptAgent(t, partialOps(), func() {
				if err := syscall.Kill(os.Getpid(), tc.sig); err != nil {
					t.Errorf("kill %s: %v", tc.name, err)
				}
			}, nil)

			_, err := ingestOnce(t, root, src)
			if err == nil || err.Error() != "agent turn: agent: context canceled" {
				t.Fatalf("error = %v, want the canceled-turn text", err)
			}
			if !ag.sawCancel {
				t.Fatalf("a real %s did not cancel the turn's ctx", tc.name)
			}
			assertNoOpen(t, root)
			if got := countChangesets(t, root, "rejected"); got != 1 {
				t.Errorf("rejected changesets = %d, want 1", got)
			}
		})
	}
}
