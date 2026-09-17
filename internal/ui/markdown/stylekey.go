package markdown

import (
	"strconv"
	"strings"
)

// styleID is the identity of the style a line's visible text opens with:
// its foreground colour and the two attributes glamour combines into one
// run. The same style serializes with its parameters in either order
// ("38;2;…;3" vs "3;38;2;…"), so runs are compared as parsed values, not
// bytes.
type styleID struct {
	fg     string
	bold   bool
	italic bool
}

// leadingStyleKey reduces the styling runs that precede a rendered line's
// first visible character to one styleID. styled is false when the line
// opens with no runs at all — then no style signal exists to join by.
func leadingStyleKey(l string) (key styleID, styled bool) {
	for i := 0; i < len(l); {
		seq, n, isReset, ok := decodeSGR(l[i:])
		if !ok {
			return key, i > 0
		}
		styled = true
		i += n
		if isReset {
			key = styleID{}
			continue
		}
		key = applySGR(key, seq)
	}
	return key, styled
}

// applySGR folds one SGR sequence's parameters into key: colours (38;2;r;g;b
// or 38;5;n), bold and italic, with their reset parameters clearing them.
func applySGR(key styleID, seq string) styleID {
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m"), ";")
	for j := 0; j < len(parts); j++ {
		switch parts[j] {
		case "1":
			key.bold = true
		case "3":
			key.italic = true
		case "22":
			key.bold = false
		case "23":
			key.italic = false
		case "39":
			key.fg = ""
		case "38":
			switch {
			case j+1 < len(parts) && parts[j+1] == "2" && j+4 < len(parts):
				key.fg = parts[j+2] + ";" + parts[j+3] + ";" + parts[j+4]
				j += 4
			case j+1 < len(parts) && parts[j+1] == "5" && j+2 < len(parts):
				key.fg = parts[j+2]
				j += 2
			}
		}
	}
	return key
}

// quoteTextStyleKey is the leading style of a quote paragraph's text: the
// BlockQuote primitive glamour cascades onto it — Muted, italic
// (style.go). The colour is in the terms glamour actually emits, the 24-bit
// SGR arguments a "#RRGGBB" token serializes to, which is what
// leadingStyleKey parses back out of a rendered line.
func quoteTextStyleKey(s Style) styleID {
	return styleID{fg: srgbArgs(s.Muted), italic: true}
}

// srgbArgs returns the "r;g;b" SGR arguments of a "#RRGGBB" token, "" when
// the token is not one.
func srgbArgs(hex string) string {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return ""
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return ""
	}
	return strconv.Itoa(int(v>>16&0xff)) + ";" + strconv.Itoa(int(v>>8&0xff)) + ";" + strconv.Itoa(int(v&0xff))
}
