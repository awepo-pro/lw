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
// and stay. A line left as nothing but a list bullet — an item whose only
// content was a citation — goes with the marker, so no empty bullet is left
// behind; a line that was bare before the strip stays as it was. With live,
// a trailing unterminated `^[` on the last line — or the lone `^` one byte
// before its `[` — is a marker still arriving, hidden from the `^` on,
// spaces and all.
func stripMarkers(text string, live bool) string {
	lines := strings.Split(text, "\n")
	inFence := false
	var fenceCh byte
	var fenceLen int
	kept := make([]string, 0, len(lines))
	for i, line := range lines {
		if inFence {
			if closesFence(line, fenceCh, fenceLen) {
				inFence = false
			}
			kept = append(kept, line) // fence content is code, never a citation
			continue
		}
		if ch, n, ok := opensFence(line); ok {
			inFence, fenceCh, fenceLen = true, ch, n
			kept = append(kept, line) // the fence line itself carries no citation either
			continue
		}
		stripped := stripLineMarkers(line, live && i == len(lines)-1)
		if stripped != line && bareListItem(stripped) {
			continue // the line was nothing but a citation: no empty item behind
		}
		kept = append(kept, stripped)
	}
	return strings.Join(kept, "\n")
}

// bareListItem reports whether s is nothing but a list bullet — `-`, `*`,
// `+`, or digits and a `.`/`)`: the leftover of a list item whose only
// content was a citation. A line the strip never touched is never tested
// against this (stripMarkers guards on the line having changed), so an
// item the answer itself left bare renders as it always did.
func bareListItem(s string) bool {
	t := strings.TrimSpace(s)
	if t == "-" || t == "*" || t == "+" {
		return true
	}
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	return i > 0 && i < len(t) && (t[i] == '.' || t[i] == ')') && i == len(t)-1
}

// stripLineMarkers rewrites one line outside any fence: every `^[…]` whose
// closing `]` sits on this line goes, with one immediately preceding space
// or tab; backtick runs toggle the inline code span the marker rule must
// not reach. With last (the live turn's final line), a `^[` with no `]`
// after it — or a bare `^` with nothing after it at all — is a marker
// still arriving: it and everything after it are hidden.
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
		if !inCode && line[i] == '^' {
			if i+1 >= len(line) {
				if last {
					// still streaming: the marker is one byte short of its
					// `[` — hide what is there, its one preceding space or
					// tab with it
					if n := len(out); n > 0 && (out[n-1] == ' ' || out[n-1] == '\t') {
						out = out[:n-1]
					}
					return string(out)
				}
				out = append(out, line[i])
				i++
				continue
			}
			if line[i+1] == '[' {
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
		}
		out = append(out, line[i])
		i++
	}
	return string(out)
}

// opensFence reports whether line opens a fenced code block: after at most
// three leading spaces, a run of at least three backticks or tildes — the
// rest of the line is the info string. Three spaces is CommonMark's
// top-level limit; a fence indented deeper (inside a nested list) is not
// recognized and loses its markers in the hidden view. Accepted (027
// Tier-2): the alternative — fence detection at any indent — misclassifies
// ordinary indented prose as code and leaves machinery showing, against
// the user's whole point; the record and ctrl+p always keep the bytes.
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
// removal leaves the text starting with blank lines, those too. 027 T3
// (F.N2) adds the inline form: a label that OPENS the FIRST line — the
// answer's text following it on the same line, as a real provider turn
// wrote it — loses the label and the spaces after it, the rest of the line
// staying. Only there: the prompt mandates the label as the answer's first
// line, so an inline label anywhere else is the answer's own words and
// stays, and the same words inside a fenced code block are the answer's own
// code and stay (the first line cannot be fence content — a fence opening
// before it would occupy the first line itself). The rule is a rule on
// PROSE, so the fence state walks the lines exactly as stripMarkers walked
// them (the markers pass never rewrites a fence line, so both passes see
// the same fences). With live, a line that is a non-empty prefix of the
// label is still arriving and is hidden — the FIRST line, and also the LAST
// while no fence is open, because the label can sit mid-answer after a
// vault-backed part; so the label never flashes half-written on any line it
// may land on, and an inline label already arrived strips live exactly as
// it will when finished.
func stripVaultLabel(text string, live bool) string {
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	inFence := false
	var fenceCh byte
	var fenceLen int
	removed := false
	for i, l := range lines {
		if inFence {
			kept = append(kept, l) // fence content is code, never the label
			if closesFence(l, fenceCh, fenceLen) {
				inFence = false
			}
			continue
		}
		if ch, n, ok := opensFence(l); ok {
			inFence, fenceCh, fenceLen = true, ch, n
			kept = append(kept, l)
			continue
		}
		if strings.TrimSpace(l) == vaultLabel {
			removed = true
			continue
		}
		if i == 0 && strings.HasPrefix(l, vaultLabel) {
			l = strings.TrimLeft(l[len(vaultLabel):], " \t")
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
		if !inFence && out != "" {
			last := out
			cut := -1
			if i := strings.LastIndexByte(out, '\n'); i >= 0 {
				last, cut = out[i+1:], i
			}
			if last != "" && strings.HasPrefix(vaultLabel, last) {
				if cut >= 0 {
					out = out[:cut]
				} else {
					out = ""
				}
			}
		}
	}
	return out
}
