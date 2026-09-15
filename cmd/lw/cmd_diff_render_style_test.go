package main

import (
	"testing"

	"github.com/awepo-pro/lw/internal/ui"
)

// TestDiffRenderStyleTokens covers the token mapping --render builds its
// markdown.Style from (003 contract §3, as amended by W5 F3/D-3W): every
// token comes from the theme palette at the terminal's polarity, with the
// W5 Heading and Code tokens included.
func TestDiffRenderStyleTokens(t *testing.T) {
	t.Run("heading_and_code_from_palette", func(t *testing.T) {
		for _, dark := range []bool{true, false} {
			polarity := "dark"
			if !dark {
				polarity = "light"
			}

			// The TTY seam decides the polarity renderOptions resolves
			// the theme at (003 contract §8.4); both polarities map the
			// same way, so both are checked.
			orig := stdoutTerm
			stdoutTerm = termProbe{
				isTTY: func() bool { return true },
				width: func() int { return 40 },
				dark:  func() bool { return dark },
			}
			o, err := renderOptions()
			stdoutTerm = orig
			if err != nil {
				t.Fatalf("%s: renderOptions: %v", polarity, err)
			}

			theme, err := ui.LoadTheme("")
			if err != nil {
				t.Fatalf("%s: LoadTheme: %v", polarity, err)
			}
			theme = theme.WithDark(dark)

			if o.style.Dark != theme.IsDark {
				t.Errorf("%s: style.Dark = %v, want %v", polarity, o.style.Dark, theme.IsDark)
			}
			for _, tok := range []struct{ name, got, want string }{
				// W5 F3: the two new tokens.
				{"Heading", o.style.Heading, theme.Palette.Heading},
				{"Code", o.style.Code, theme.Palette.Code},
				// The previous tokens still ride along.
				{"Fg", o.style.Fg, theme.Palette.Fg},
				{"Muted", o.style.Muted, theme.Palette.Muted},
				{"Faint", o.style.Faint, theme.Palette.Faint},
				{"Border", o.style.Border, theme.Palette.Border},
				{"Accent", o.style.Accent, theme.Palette.Accent},
				{"Good", o.style.Good, theme.Palette.Good},
				{"Warn", o.style.Warn, theme.Palette.Warn},
				{"Bad", o.style.Bad, theme.Palette.Bad},
			} {
				if tok.got != tok.want {
					t.Errorf("%s: style.%s = %q, want the palette's %q",
						polarity, tok.name, tok.got, tok.want)
				}
			}
		}
	})
}
