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
// classification. The message carries a protocol prefix: "net/http: …" on
// HTTP/1.1, "http2: …" when the TLS connection negotiated h2 — which is
// what every https provider does — so the match is on the shared suffix,
// never the prefix. Dial and TLS failures are timeouts too, but their text
// is not this one, and a connect failure is not the provider going silent.
func (c *Client) asStalled(err error) error {
	if c.cfg.StallTimeout <= 0 || err == nil {
		return err
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() &&
		strings.Contains(err.Error(), "timeout awaiting response headers") {
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
	// window is the silence bound the next arm uses. It equals timeout
	// except while drain shortens it; timeout stays what stallError reports.
	window time.Duration
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
	b := &stallBody{rc: rc, timeout: timeout, window: timeout, cancel: cancel}
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
	b.timer = time.AfterFunc(b.window, func() {
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
// sees closed and does nothing. It also cancels the stream request's
// context, releasing the child context WithCancel created: without this a
// normally-finished stream leaks that child, registered on the caller's
// context until the caller's own context dies. Order matters here: the
// wrapped body is closed FIRST, so net/http can finish the response and
// return the connection to the idle pool, and only then does the cancel
// fire — by which time the round trip is done and pooling is unaffected
// (verified against Go 1.27). Cancelling first closes the connection out
// from under that drain and every turn re-dials.
//
// Close is not what makes the pooling DETERMINISTIC. A body closed before
// its EOF has been read is pooled by net/http asynchronously — its read
// loop drains the tail in its own goroutine after Close has already
// returned — so a request issued right after Close can dial a second
// connection while the first is still on its way back to the pool. A
// stream that ended cleanly therefore calls drain first (consumeStreamTimed,
// at [DONE]); Close's order then only matters for an exit that never saw
// the end of the body.
func (b *stallBody) Close() error {
	b.mu.Lock()
	b.closed = true
	t := b.timer
	b.timer = nil
	b.mu.Unlock()
	if t != nil {
		t.Stop()
	}
	err := b.rc.Close()
	b.cancel()
	return err
}

// tailWindow and tailMax bound drain: they are net/http's own limits for
// draining an early-closed body (maxPostCloseReadTime, maxPostCloseReadBytes),
// so reading the tail here costs a turn no more than the transport's own
// asynchronous drain would, and a provider that never ends the response
// after [DONE] still releases the turn after at most tailWindow.
const (
	tailWindow = 50 * time.Millisecond
	tailMax    = 256 << 10
)

// drain reads the response behind a cleanly ended stream to its EOF. SSE
// ends at [DONE] while the HTTP response ends a few bytes later (a chunked
// body's terminating chunk, an h1 body's close), and net/http only knows
// the connection is reusable once a Read has returned EOF: that Read blocks
// until the connection is back in the idle pool, which is what makes the
// pooling complete before the stream's channel closes instead of racing the
// next request. The wait is a fresh silence window of min(timeout,
// tailWindow), armed through the same expiry machinery as any other read,
// so a provider that holds the response open after [DONE] is cut by the
// request cancel — the connection is then not reused, exactly as an
// un-drained Close would have left it. Whatever drain reads or fails on is
// irrelevant: the stream's answer was already delivered.
func (b *stallBody) drain() {
	b.mu.Lock()
	if b.closed || b.stalled {
		b.mu.Unlock()
		return
	}
	if b.timeout > tailWindow {
		b.window = tailWindow
	}
	if b.timer != nil {
		b.timer.Stop()
	}
	b.armLocked()
	b.mu.Unlock()
	_, _ = io.Copy(io.Discard, io.LimitReader(b, tailMax))
}

// drainTail is the stream-end entry to drain: only a stallBody has the
// expiry machinery that bounds the wait, so a body left unwrapped (stall
// bound off, or a test's fake) is untouched — it is read no further than
// before.
func drainTail(body io.ReadCloser) {
	if sb, ok := body.(*stallBody); ok {
		sb.drain()
	}
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
