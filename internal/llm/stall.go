package llm

// stall.go is 026 T2's silence bound: the longest the client waits with no
// bytes from the provider, before the response headers (the transport's
// ResponseHeaderTimeout, set in New and re-labelled by asStalled) or
// between body reads (the stallBody wrapper below). It is deliberately not
// Config.Timeout — that bounds the whole streaming read and would cut a
// long answer; the stall bound fires only when nothing arrives at all, so
// a stream that keeps sending bytes is never cut, however long it runs.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// ErrStalled is the sentinel every stall failure wraps (026 T2): the
// provider went silent — no response headers, or no body bytes — for longer
// than Config.StallTimeout. Consumers match it with errors.Is; a stall's
// log line (010 D-10B) carries the duration and never a body or a key.
var ErrStalled = errors.New("llm: provider stalled")

// stallError builds the one message shape both stall exits use: the
// sentinel, then "no response bytes for <StallTimeout>" in Go's
// Duration.String form, then the transport's cause if there is one.
func stallError(d time.Duration, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: no response bytes for %s", ErrStalled, d)
	}
	return fmt.Errorf("%w: no response bytes for %s: %w", ErrStalled, d, cause)
}

// asStalled re-labels the transport's own header-deadline error as a stall
// (026 T2 F.S2). ResponseHeaderTimeout surfaces as a generic net timeout
// with no sentinel of its own, so the match is on its documented message —
// stable in net/http for over a decade — together with the Timeout
// classification. Dial and TLS failures are timeouts too, but their text is
// not this one, and a connect failure is not the provider going silent.
func (c *Client) asStalled(err error) error {
	if c.cfg.StallTimeout <= 0 || err == nil {
		return err
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() &&
		strings.Contains(err.Error(), "net/http: timeout awaiting response headers") {
		return stallError(c.cfg.StallTimeout, err)
	}
	return err
}

// stallBody wraps the SSE response body so every successful Read re-arms a
// fresh StallTimeout expiry (026 T2 F.S3). When an expiry wins — no bytes
// for the whole window — the request is cancelled and the wrapper reports
// the stall to the reader in place of whatever the aborted connection
// returned.
type stallBody struct {
	rc      io.ReadCloser
	timeout time.Duration
	// cancel aborts the stream request's context. It is the child cancel
	// Stream created: firing it closes the connection, which is what
	// actually unblocks a Read parked in the provider's silence.
	cancel context.CancelFunc

	mu      sync.Mutex
	gen     int         // bumped each time a fresh expiry window is armed
	timer   *time.Timer // the armed window; replaced on every re-arm
	closed  bool        // the stream ended by any path; expiries are obsolete
	stalled bool        // an expiry won
}

// newStallBody arms the first expiry window immediately, so the gap between
// the headers arriving and the first body read is bounded too.
func newStallBody(rc io.ReadCloser, timeout time.Duration, cancel context.CancelFunc) *stallBody {
	b := &stallBody{rc: rc, timeout: timeout, cancel: cancel}
	b.arm()
	return b
}

// arm starts a fresh expiry window. Each window's callback checks its own
// generation under b.mu, so a window made obsolete by a later Read — or by
// Close — is a no-op even if its callback is already in flight; that check
// is what makes replace-on-read re-arming race-free without Timer.Reset.
func (b *stallBody) arm() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.armLocked()
}

func (b *stallBody) armLocked() {
	b.gen++
	g := b.gen
	b.timer = time.AfterFunc(b.timeout, func() {
		b.mu.Lock()
		if b.closed || g != b.gen {
			// A newer Read re-armed, or the stream already ended: this
			// window is history.
			b.mu.Unlock()
			return
		}
		b.stalled = true
		b.mu.Unlock()
		// Cancel the request — closing the connection is the only thing
		// that unblocks a Read parked in the provider's silence. rc.Close()
		// from here cannot do that: net/http's response body serialises
		// Close behind the same mutex an in-flight Read holds, so it would
		// wait out the very stall it is meant to break. The deferred
		// body.Close() in consumeStreamTimed does the real close at exit.
		b.cancel()
	})
}

// Read delegates to the wrapped body and re-arms the window when bytes
// arrived — the silence clock restarts at every successful read, which is
// why a stream that keeps sending is never cut, whatever its cadence. A
// read that comes back after an expiry reports the stall, discarding
// whatever error the aborted connection handed back: the deadline, not that
// error, is what ended the stream.
func (b *stallBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	stalled := b.stalled
	b.mu.Unlock()
	if stalled {
		return 0, stallError(b.timeout, nil)
	}

	n, err := b.rc.Read(p)

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stalled {
		// The expiry fired while this Read sat in the silence. Bytes the
		// aborted connection squeezed out with the error are still real
		// bytes — hand them over with the stall attached.
		return n, stallError(b.timeout, nil)
	}
	if err == nil {
		b.timer.Stop() // the window being replaced; its callback no-ops on gen anyway
		b.armLocked()
	}
	return n, err
}

// Close stops the expiry machinery and closes the wrapped body. It is
// consumeStreamTimed's deferred close, so it runs on every stream exit
// path — no timer outlives the stream, and any callback already in flight
// sees closed and does nothing.
func (b *stallBody) Close() error {
	b.mu.Lock()
	b.closed = true
	t := b.timer
	b.timer = nil
	b.mu.Unlock()
	if t != nil {
		t.Stop()
	}
	return b.rc.Close()
}

// wrapStallBody is the single decision point for wrapping a stream body
// (026 T2): a positive stall timeout gets the stallBody wrapper, 0 returns
// the body unchanged — today's unbounded behaviour.
func wrapStallBody(body io.ReadCloser, stall time.Duration, cancel context.CancelFunc) io.ReadCloser {
	if stall <= 0 {
		return body
	}
	return newStallBody(body, stall, cancel)
}
