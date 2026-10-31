package main

import (
	"strconv"

	"github.com/awepo-pro/lw/internal/stage"
)

// formatCur is the format version this lw writes and formatNew the first one it
// refuses. The format-gate tests spell their expectations with these, so a bump
// (069 made it 3, 074 made it 4) moves them with stage.FormatVersion instead of
// leaving a literal 3 or 4 that quietly changes meaning.
var (
	formatCur = strconv.Itoa(stage.FormatVersion)
	formatNew = strconv.Itoa(stage.FormatVersion + 1)
)
