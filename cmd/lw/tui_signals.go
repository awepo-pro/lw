package main

// tui_signals.go keeps the TUI's exit flush alive through a hangup (042 S3d H1).
// A terminal that closes, or an ssh session that drops, sends the process
// SIGHUP; unhandled, that kills it where it stands — before auto-sync's
// deferred exit flush has pushed the commit the user just made. cmd/lweval
// handles the same signals for the same reason (its config copy would
// otherwise stay on disk). bubbletea already turns SIGINT and SIGTERM into a
// quit while the program runs; this adds SIGHUP, and keeps all of them caught
// for the whole of cmdTUI, because the flush runs after the program has
// returned and a termination signal arriving then would otherwise end the
// process with the push half done.

import (
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// quitter is what a signal asks to quit: the tea.Program.
type quitter interface{ Quit() }

// signalQuit turns hangup and termination signals into a quit of the program
// once there is one.
type signalQuit struct {
	ch   chan os.Signal
	done chan struct{}
	once sync.Once

	mu   sync.Mutex
	q    quitter
	hung bool // a signal arrived while no program was attached
}

// quitOnSignals starts catching SIGHUP and SIGTERM. The program does not exist
// yet when cmdTUI wants this (the pull at its start can take seconds), so the
// quit is attached later; a signal that comes first is remembered and acted on
// at attach.
func quitOnSignals() *signalQuit {
	s := &signalQuit{ch: make(chan os.Signal, 4), done: make(chan struct{})}
	signal.Notify(s.ch, syscall.SIGHUP, syscall.SIGTERM)
	go s.loop()
	return s
}

func (s *signalQuit) loop() {
	for {
		select {
		case sig := <-s.ch:
			slog.Info("tui: signal", "signal", sig.String())
			s.mu.Lock()
			q := s.q
			if q == nil {
				s.hung = true
			}
			s.mu.Unlock()
			if q != nil {
				q.Quit()
			}
		case <-s.done:
			return
		}
	}
}

// attach hands the program to the catcher; if a signal already arrived it
// quits at once.
func (s *signalQuit) attach(q quitter) {
	s.mu.Lock()
	s.q = q
	hung := s.hung
	s.mu.Unlock()
	if hung {
		q.Quit()
	}
}

// detach forgets the program, which has returned. Signals are still caught —
// the exit flush is still running — but nothing is asked to quit.
func (s *signalQuit) detach() {
	s.mu.Lock()
	s.q, s.hung = nil, false
	s.mu.Unlock()
}

// stop gives the signals back to their default action.
func (s *signalQuit) stop() {
	s.once.Do(func() {
		signal.Stop(s.ch)
		close(s.done)
	})
}
