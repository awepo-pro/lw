// Package lintview is the lint report screen (backbone §12 ui.Pane): the
// 15 checks from internal/lint.All() (C-85/D-V; page-abstract added by
// 014) run over the engine's
// vault, and the findings shown as one flat, focused Findings panel —
// glyph, check name, path and message in aligned columns, severity as a
// coloured glyph. See lintview.go for the model, view.go for the panel and
// keys.go for keys and the shell's optional interfaces.
package lintview
