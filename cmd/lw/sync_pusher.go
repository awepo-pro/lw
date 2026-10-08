package main

// sync_pusher.go is the TUI's asynchronous pusher (042 D5). A CLI verb pushes
// synchronously, before it exits; the TUI cannot — a commit made in the review
// pane must not freeze the screen for the length of a network round trip, and
// the screen must not print what a background push found. So the engine's
// terminal hook only Triggers this, and the pusher does the work on its own
// goroutine:
//
//   - single flight: at most one push runs, and triggers that arrive during it
//     queue exactly one more, however many there are — the queued push sends
//     everything they stand for;
//   - debounce: a push starts only after the triggers have been quiet for the
//     debounce, 1 s in production. A commit's session record can land just
//     after the hook fires (A-042-2), and a burst of rejections is one push;
//   - results go to lw.log, never the screen (the run function logs them);
//   - Flush, at exit, waits up to its budget for the push in flight, sends
//     what is left and reports, so the caller can print the CLI's line after
//     the terminal is restored.
//
// Everything is guarded by one mutex and no lock is held across a push, so
// Trigger — called from the TUI's own goroutine — never blocks on the network.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// syncPushResult is what one push did: the remote that answered, how many
// commits it sent, and why it failed, if it did.
type syncPushResult struct {
	Remote string
	Pushed int
	Err    error
}

// pusherState is where the pusher is between triggers.
type pusherState int

const (
	pusherIdle    pusherState = iota // nothing pending
	pusherWaiting                    // a trigger is waiting out the debounce
	pusherRunning                    // a push is in flight
)

// syncCancelGrace is the most time Flush keeps in reserve, out of its budget,
// for a cancelled push to unwind: vaultsync stops a git it started with
// SIGTERM and, 1.5 s later, SIGKILL. Flush cancels that much before the budget
// ends, so the whole flush stays inside it.
const syncCancelGrace = 3 * time.Second

// syncPusher serialises and coalesces the pushes a session triggers.
type syncPusher struct {
	run      func(ctx context.Context) syncPushResult
	debounce time.Duration

	ctx    context.Context // cancelled by Flush when the budget runs out
	cancel context.CancelFunc

	mu     sync.Mutex
	state  pusherState
	queued bool          // a trigger arrived during a push
	closed bool          // Flush has begun: triggers are ignored
	timer  *time.Timer   // the debounce, while state is pusherWaiting
	idle   chan struct{} // closed while state is pusherIdle

	// The session's tally, for Flush's report.
	total   int
	remote  string
	lastErr error
}

// newSyncPusher returns an idle pusher that calls run for each push and waits
// debounce after a trigger before it does.
func newSyncPusher(run func(ctx context.Context) syncPushResult, debounce time.Duration) *syncPusher {
	ctx, cancel := context.WithCancel(context.Background())
	idle := make(chan struct{})
	close(idle)
	return &syncPusher{run: run, debounce: debounce, ctx: ctx, cancel: cancel, idle: idle}
}

// Trigger says the vault changed and should be pushed. It never blocks and is
// safe to call from any goroutine, including from inside the engine's
// terminal hook.
func (p *syncPusher) Trigger() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	switch p.state {
	case pusherIdle:
		p.state = pusherWaiting
		p.idle = make(chan struct{})
		p.timer = time.AfterFunc(p.debounce, p.fire)
	case pusherWaiting:
		p.timer.Reset(p.debounce)
	case pusherRunning:
		p.queued = true
	}
}

// fire is the debounce timer's function: the triggers have been quiet, so push.
func (p *syncPusher) fire() {
	p.mu.Lock()
	if p.state != pusherWaiting {
		// A stale timer: a Reset raced the firing, and the push it asked for is
		// already running or has been taken by Flush.
		p.mu.Unlock()
		return
	}
	if p.closed {
		// Flush stopped taking triggers while this timer was already firing; it
		// owns what was pending, so only the bookkeeping is left.
		p.state = pusherIdle
		close(p.idle)
		p.mu.Unlock()
		return
	}
	p.state = pusherRunning
	p.mu.Unlock()

	res := p.runOnce(p.ctx)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.record(res)
	if p.queued && !p.closed {
		p.queued = false
		p.state = pusherWaiting
		p.timer = time.AfterFunc(p.debounce, p.fire)
		return
	}
	p.state = pusherIdle
	close(p.idle)
}

// runOnce calls run under the auto-sync deadline.
func (p *syncPusher) runOnce(parent context.Context) syncPushResult {
	ctx, cancel := context.WithTimeout(parent, syncAutoTimeout)
	defer cancel()
	return p.run(ctx)
}

// record folds one push into the session tally (the caller holds mu): commits
// add up, and the latest attempt decides whether the session ends in failure.
func (p *syncPusher) record(res syncPushResult) {
	p.total += res.Pushed
	if res.Remote != "" {
		p.remote = res.Remote
	}
	p.lastErr = res.Err
}

// Flush ends the pusher's session, for the TUI's exit. It stops taking
// triggers, waits for the push in flight (cancelling it, and saying so, when
// the budget is nearly spent), then sends a final push when a trigger was
// still pending or needs() says the work tree or HEAD is ahead, and reports
// the session: every commit sent, the remote that answered, and the last
// attempt's error. The whole call stays inside budget — the cancellations are
// made early enough for the git they stop to be killed in time. A session in
// which nothing happened reports the zero result, and no push is made.
func (p *syncPusher) Flush(budget time.Duration, needs func() bool) syncPushResult {
	deadline := time.Now().Add(budget)
	grace := min(budget/6, syncCancelGrace)

	p.mu.Lock()
	p.closed = true
	pending := p.queued
	if p.state == pusherWaiting {
		pending = true
		if p.timer.Stop() {
			p.state = pusherIdle
			close(p.idle)
		}
		// else the timer is firing: fire sees closed, goes idle and closes idle.
	}
	idle := p.idle
	p.mu.Unlock()

	select {
	case <-idle:
	case <-time.After(time.Until(deadline) - grace):
		// The push in flight outlived the budget. Cancel it — vaultsync kills
		// what it started — give it the reserve to unwind, and report the
		// timeout.
		p.cancel()
		select {
		case <-idle:
		case <-time.After(grace):
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		return syncPushResult{Remote: p.remote, Pushed: p.total, Err: fmt.Errorf("timed out after %s waiting for the push", budget)}
	}

	p.mu.Lock()
	pending = pending || p.queued
	p.queued = false
	p.mu.Unlock()

	if pending || (needs != nil && needs()) {
		remaining := time.Until(deadline) - grace
		if remaining <= 0 {
			p.mu.Lock()
			defer p.mu.Unlock()
			return syncPushResult{Remote: p.remote, Pushed: p.total, Err: errors.New("timed out before the final push could start")}
		}
		ctx, cancel := context.WithTimeout(p.ctx, remaining)
		res := p.runOnce(ctx)
		cancel()
		p.mu.Lock()
		p.record(res)
		p.mu.Unlock()
	}

	// The session is reported once: a second Flush has nothing left to say.
	p.mu.Lock()
	defer p.mu.Unlock()
	out := syncPushResult{Remote: p.remote, Pushed: p.total, Err: p.lastErr}
	p.total, p.lastErr = 0, nil
	return out
}
