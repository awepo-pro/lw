// display.go is 027 T2's display transform: what the pane shows of an
// assistant entry's raw text. The record — m.entries, the recorded answer,
// the fileability gate — always holds the raw bytes; this is a pure
// text→text step applied only on the render path (transcript.go's
// assistantLines), so ctrl+p re-renders the same entry without touching any
// state a turn or ctrl+s reads.
package ask

import "strings"

// vaultLabel is the first line the agent's system prompt tells it to write
// above an answer drawn from outside the vault (the "Not from your vault:"
// rule, internal/agent/prompt.go).
const vaultLabel = "Not from your vault:"

// displayAnswer renders an assistant entry's text for the pane. With
// showProv the raw text passes through byte for byte — the pre-027
// rendering. Without it the provenance markers and the vault label are
// hidden (027 T2). live marks the streaming turn's still-growing entry,
// whose half-arrived marker and half-arrived label must not flash.
func displayAnswer(text string, showProv, live bool) string {
	if showProv || text == "" {
		return text
	}
	return stripVaultLabel(stripMarkers(text, live), live)
}

// SetShowProvenance forces the ctrl+p toggle on or off — the harness seam
// (A-027-2): the conformance goldens pin the approved sources-shown
// rendering, so they construct their ask pane through this, and
// render_test.go's provenance_is_muted pins the marker's styling the same
// way.
func (m *Model) SetShowProvenance(on bool) { m.showProvenance = on }

// stripMarkers removes every ^[…] provenance marker outside code, together
// with one immediately preceding space or tab. Markers inside a fenced code
// block or an inline code span are the answer's own text, not a citation,
// and stay. With live, a trailing unterminated `^[` on the last line — a
// marker still arriving — is hidden from the `^[` on, spaces and all.
func stripMarkers(text string, live bool) string {
	lines := strings.Split(text, "\n")
	inFence := false
	var fenceCh byte
	var fenceLen int
	for i, line := range lines {
		if inFence {
			if closesFence(line, fenceCh, fenceLen) {
				inFence = false
			}
			continue // fence content is code, never a citation
		}
		if ch, n, ok := opensFence(line); ok {
			inFence, fenceCh, fenceLen = true, ch, n
			continue // the fence line itself carries no citation either
		}
		lines[i] = stripLineMarkers(line, live && i == len(lines)-1)
	}
	return strings.Join(lines, "\n")
}

// stripLineMarkers rewrites one line outside any fence: every `^[…]` whose
// closing `]` sits on this line goes, with one immediately preceding space
// or tab; backtick runs toggle the inline code span the marker rule must
// not reach. With last (the live turn's final line), a `^[` with no `]`
// after it is a marker still arriving: it and everything after it are
// hidden.
func stripLineMarkers(line string, last bool) string {
	out := make([]byte, 0, len(line))
	inCode := false
	for i := 0; i < len(line); {
		if line[i] == '`' {
			j := i
			for j < len(line) && line[j] == '`' {
				j++
			}
			inCode = !inCode
			out = append(out, line[i:j]...)
			i = j
			continue
		}
		if !inCode && line[i] == '^' && i+1 < len(line) && line[i+1] == '[' {
			end := strings.IndexByte(line[i+1:], ']')
			if end < 0 {
				if last {
					// still streaming: hide the arriving marker whole —
					// its one preceding space or tab with it
					if n := len(out); n > 0 && (out[n-1] == ' ' || out[n-1] == '\t') {
						out = out[:n-1]
					}
					return string(out)
				}
				out = append(out, line[i])
				i++
				continue
			}
			if n := len(out); n > 0 && (out[n-1] == ' ' || out[n-1] == '\t') {
				out = out[:n-1]
			}
			i += end + 2 // ^[ … ]
			continue
		}
		out = append(out, line[i])
		i++
	}
	return string(out)
}

// opensFence reports whether line opens a fenced code block: after at most
// three leading spaces, a run of at least three backticks or tildes — the
// rest of the line is the info string.
func opensFence(line string) (ch byte, n int, ok bool) {
	i := 0
	for i < 3 && i < len(line) && line[i] == ' ' {
		i++
	}
	if len(line)-i < 3 {
		return 0, 0, false
	}
	ch = line[i]
	if ch != '`' && ch != '~' {
		return 0, 0, false
	}
	for i+n < len(line) && line[i+n] == ch {
		n++
	}
	return ch, n, n >= 3
}

// closesFence reports whether line closes a fence opened with n of ch:
// that run alone, after at most three leading spaces.
func closesFence(line string, ch byte, n int) bool {
	i := 0
	for i < 3 && i < len(line) && line[i] == ' ' {
		i++
	}
	cnt := 0
	for i < len(line) && line[i] == ch {
		i++
		cnt++
	}
	return cnt >= n && i == len(line)
}

// stripVaultLabel removes the `Not from your vault:` line — and, when that
// removal leaves the text starting with blank lines, those too. With live,
// a first line that is a non-empty prefix of the label is still arriving
// and is hidden as well, so the label never flashes half-written.
func stripVaultLabel(text string, live bool) string {
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	removed := false
	for _, l := range lines {
		if strings.TrimSpace(l) == vaultLabel {
			removed = true
			continue
		}
		kept = append(kept, l)
	}
	if removed {
		for len(kept) > 0 && strings.TrimSpace(kept[0]) == "" {
			kept = kept[1:]
		}
	}
	out := strings.Join(kept, "\n")
	if live {
		first := out
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			first = out[:i]
		}
		if first != "" && strings.HasPrefix(vaultLabel, first) {
			if i := strings.IndexByte(out, '\n'); i >= 0 {
				out = out[i+1:]
			} else {
				out = ""
			}
		}
	}
	return out
}
