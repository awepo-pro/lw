// deps.go implements contract §6's Deps: the ui.Deps a screen is
// constructed with in a test, built the same way cmd/lw builds them —
// default theme at the requested polarity, default keys — plus the agent
// the screen under test should drive (nil when the test has none).
package uitest

import (
	"fmt"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/ui"
)

// Deps returns ui.Deps for v with the default theme at the given polarity,
// default keys, and agent ag (may be nil).
//
// The theme and keys come from ui.LoadTheme("") and ui.LoadKeys, which read
// the user's config directory for overlays. A test that must be hermetic
// points XDG_CONFIG_HOME at an empty temp directory before calling — the
// same thing internal/ui's own tests do — so the defaults here are the
// compiled-in ones, not whatever the developer's machine carries.
func Deps(v *Vault, dark bool, ag agent.Agent) ui.Deps {
	theme, err := ui.LoadTheme("")
	if err != nil {
		// There is no *testing.T in the contract's signature to fail, and a
		// broken theme silently renders nothing; refuse to hand out Deps
		// that cannot draw.
		panic(fmt.Sprintf("uitest: Deps: LoadTheme: %v", err))
	}
	keys, err := ui.LoadKeys()
	if err != nil {
		panic(fmt.Sprintf("uitest: Deps: LoadKeys: %v", err))
	}

	return ui.Deps{
		Engine: v.Engine,
		Agent:  ag,
		Theme:  theme.WithDark(dark),
		Keys:   keys,
	}
}
