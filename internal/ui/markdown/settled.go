package markdown

import "strings"

// SettledPrefix splits a partial markdown buffer into the portion whose
// top-level blocks are complete and the tail that is still being written.
//
// The rule, which follows from how blocks are flushed (a block closes on a
// blank line or on a closing fence): EVERY BLOCK EXCEPT THE LAST IS
// SETTLED. If a fence is still open, the whole open fence is the last block
// and is therefore in tail.
//
// settled is safe to hand to RenderFragment: it can never contain a
// half-open fence or a half-written table. tail must be drawn as plain
// text until another block starts after it.
//
// For a buffer with one block or none, settled is empty and tail is buf.
func SettledPrefix(buf string) (settled, tail string) {
	var (
		fence    bool // inside an open ``` fence
		open     bool // a block is currently being extended
		start    int  // byte offset of the most recent block's first line
		sawBlock bool // any block seen at all
	)

	// A line scan, not a parse (workflow 005 contract §2 note 2): the only
	// state tracked is the same fence toggle splitBlocks tracks — a line
	// whose left-trimmed form starts with "```" — plus the byte offset
	// where the most recent block begins. Because the result is one slice
	// of buf at a block boundary, settled + tail == buf holds by
	// construction: the blank lines before the last block stay in settled,
	// every byte from the last block's first line on stays in tail, and a
	// "\r" before a "\n" is just another byte on the line it belongs to.
	for i := 0; i < len(buf); {
		line := buf[i:]
		end := len(buf)
		if j := strings.IndexByte(buf[i:], '\n'); j >= 0 {
			line, end = buf[i:i+j], i+j+1
		}

		trimmed := strings.TrimLeft(line, " \t")
		switch {
		case strings.HasPrefix(trimmed, "```"):
			// An opening fence line begins a new block (the previous one,
			// if any, ended); a closing line belongs to the fence block,
			// which then ends — exactly splitBlocks' flush behaviour.
			if !fence {
				sawBlock, open, start = true, true, i
			}
			fence = !fence
			if !fence {
				open = false
			}
		case fence:
			// Inside an open fence every line — blank included — is part
			// of the fence block.
		case strings.TrimSpace(line) == "":
			open = false // a blank line outside a fence ends the block
		default:
			if !open {
				sawBlock, open, start = true, true, i
			}
		}
		i = end
	}

	if !sawBlock {
		return "", buf
	}
	return buf[:start], buf[start:]
}
