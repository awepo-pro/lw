// options.go adds the deterministic-test seam C24/D-3O introduces
// (contract §6 note 3): OpenEngine grows variadic Options so a caller
// outside the package can pin the engine clock and the changeset-id
// entropy source — the two inputs the changeset id and every journal
// timestamp are derived from. Production callers pass no options and get
// the defaults the Engine struct is built with in OpenEngine.
package stage

import (
	"io"
	"time"
)

// Option configures an Engine at OpenEngine time. Options exist for deterministic tests; production passes none.
type Option func(*Engine)

// WithClock replaces the engine clock (default time.Now). nil leaves the default.
func WithClock(now func() time.Time) Option {
	return func(e *Engine) {
		if now != nil {
			e.now = now
		}
	}
}

// WithEntropy replaces the changeset-id entropy source (default crypto/rand.Reader). nil leaves the default.
func WithEntropy(r io.Reader) Option {
	return func(e *Engine) {
		if r != nil {
			e.rand = r
		}
	}
}
