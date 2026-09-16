package markdown

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
)

// chromaRegMu serialises registration in chroma's style registry — and
// nothing else. chroma's styles.Registry is a plain unlocked map, and every
// fenced-code render reads it unprotected (quick.Highlight → styles.Get),
// so the mutex does NOT make a render safe against a concurrent
// registration; it only keeps two Render calls with different Styles from
// racing on styles.Register/styles.Registry with each other. Safety against
// a registry read inside a render comes from lw's usage, not from this
// mutex: rendering happens on one goroutine (the Bubble Tea UI goroutine;
// `lw diff --render` is single-threaded), and registration happens once per
// distinct Style, at the first render of that palette. No rendered output
// or renderer state is memoized under the mutex.
var chromaRegMu sync.Mutex

// chromaStyleName returns the registry name for s: "lw-" plus the first 16
// hex digits of sha256 over the whole Style value (contract §2 note 3).
// Distinct styles therefore get distinct names, and one name always means
// one exact set of token colours.
func chromaStyleName(s Style) string {
	sum := sha256.Sum256([]byte(fmt.Sprint(s)))
	return "lw-" + hex.EncodeToString(sum[:])[:16]
}

// chromaTheme returns the chroma style name for s, registering a style
// built solely from s the first time that distinct value is seen. glamour
// selects the registered style through CodeBlock.Theme. StyleCodeBlock.Chroma
// must never be used instead: glamour registers that config under the one
// global name "charm", only if absent (ansi/codeblock.go:82-86), so the
// first palette rendered would win for the whole process.
func chromaTheme(s Style) string {
	name := chromaStyleName(s)

	chromaRegMu.Lock()
	defer chromaRegMu.Unlock()
	if _, ok := styles.Registry[name]; !ok {
		styles.Register(chroma.MustNewStyle(name, chromaStyleEntries(s)))
	}
	return name
}

// chromaStyleEntries maps Style's tokens onto chroma token types (contract
// §2 note 3): keywords Heading; types and builtins Code; strings Good;
// numbers Warn; comments Faint + italic; function names Accent; everything
// else Fg through chroma's own inheritance from Text. No background entry:
// chroma's terminal16m formatter clears any background anyway, and an unset
// entry emits no SGR at all.
func chromaStyleEntries(s Style) chroma.StyleEntries {
	return chroma.StyleEntries{
		chroma.Text:          s.Fg,
		chroma.Keyword:       s.Heading,
		chroma.KeywordType:   s.Code,
		chroma.NameBuiltin:   s.Code,
		chroma.NameFunction:  s.Accent,
		chroma.LiteralString: s.Good,
		chroma.LiteralNumber: s.Warn,
		chroma.Comment:       s.Faint + " italic",
	}
}
