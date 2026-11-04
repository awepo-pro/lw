package hub_test

import (
	"context"
	"errors"
	"testing"

	"github.com/awepo-pro/lw/internal/hub"
)

// TestA6Fix1RefreshBeforeLock: Engine.Put and Engine.Delete call Options.Refresh
// BEFORE taking the lock (once per Put/Delete). A *hub.Error is returned; any
// other error is ignored.
func TestA6Fix1RefreshBeforeLock(t *testing.T) {
	ctx := context.Background()

	var events []string
	var lastRefreshContext context.Context
	var refreshErr error

	eng, _, _ := setupEngine(t, func(o *hub.Options) {
		o.Refresh = func(rctx context.Context) error {
			events = append(events, "refresh")
			lastRefreshContext = rctx
			return refreshErr
		}
		o.Lock = func() (func(), error) {
			events = append(events, "lock")
			return func() {
				events = append(events, "unlock")
			}, nil
		}
	})

	// 1. Put calls Refresh once before Lock, with write context
	events = nil
	refreshErr = nil
	putRes, err := eng.Put(ctx, testDevice, "notes/a.md", hub.BaseNew, []byte("content\n"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if putRes.Result != hub.ResultApplied {
		t.Fatalf("Put result want applied, got %s", putRes.Result)
	}
	if len(events) < 2 || events[0] != "refresh" || events[1] != "lock" {
		t.Fatalf("Put events want [refresh lock ...], got %v", events)
	}
	if !hub.IsWriteContext(lastRefreshContext) {
		t.Fatalf("expected write context in Refresh during Put")
	}

	// 2. Delete calls Refresh once before Lock, with write context
	events = nil
	readBytes, readSHA, err := eng.Read(ctx, "notes/a.md")
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	_ = readBytes
	delRes, err := eng.Delete(ctx, testDevice, "notes/a.md", readSHA)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if delRes.Result != hub.ResultDeleted {
		t.Fatalf("Delete result want deleted, got %s", delRes.Result)
	}
	if len(events) < 2 || events[0] != "refresh" || events[1] != "lock" {
		t.Fatalf("Delete events want [refresh lock ...], got %v", events)
	}
	if !hub.IsWriteContext(lastRefreshContext) {
		t.Fatalf("expected write context in Refresh during Delete")
	}

	// 3. Refresh returns *hub.Error -> passed through, Lock never called
	events = nil
	refreshErr = &hub.Error{Code: hub.CodeDiverged, Message: "diverged"}
	_, err = eng.Put(ctx, testDevice, "notes/b.md", hub.BaseNew, []byte("content\n"))
	if err == nil {
		t.Fatalf("Put expected error, got nil")
	}
	var hubErr *hub.Error
	if !errors.As(err, &hubErr) || hubErr.Code != hub.CodeDiverged {
		t.Fatalf("Put error want CodeDiverged, got %v", err)
	}
	for _, ev := range events {
		if ev == "lock" {
			t.Fatalf("Lock should not be called when Refresh returns *hub.Error")
		}
	}

	events = nil
	_, err = eng.Delete(ctx, testDevice, "notes/b.md", readSHA)
	if err == nil {
		t.Fatalf("Delete expected error, got nil")
	}
	if !errors.As(err, &hubErr) || hubErr.Code != hub.CodeDiverged {
		t.Fatalf("Delete error want CodeDiverged, got %v", err)
	}
	for _, ev := range events {
		if ev == "lock" {
			t.Fatalf("Lock should not be called when Refresh returns *hub.Error")
		}
	}

	// 4. Refresh returns generic error -> ignored, Put/Delete proceeds
	events = nil
	refreshErr = errors.New("transient network error")
	putRes, err = eng.Put(ctx, testDevice, "notes/c.md", hub.BaseNew, []byte("content\n"))
	if err != nil {
		t.Fatalf("Put with generic refresh error want nil, got %v", err)
	}
	if putRes.Result != hub.ResultApplied {
		t.Fatalf("Put result want applied, got %s", putRes.Result)
	}
	foundLock := false
	for _, ev := range events {
		if ev == "lock" {
			foundLock = true
		}
	}
	if !foundLock {
		t.Fatalf("expected Lock to be called despite generic refresh error")
	}
}
