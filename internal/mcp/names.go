package mcp

import "github.com/awepo-pro/lw/internal/tools"

// MCPName converts a canonical dotted tool name to the spelling used on the
// MCP wire. Canonical tool names use dots for namespace separators; MCP
// names use underscores so they remain portable across clients.
//
// The 19-entry mapping — 18 vault tools plus the conditional web.search —
// itself lives in internal/tools (WireName), since
// the agent loop's OpenAI-compatible chat-completions client needs the
// identical translation for the identical reason — a dotted name is
// rejected by both consumers (backbone §6/§7's amendment, D-CY/C-112).
// This is a one-line wrapper so §7's exported surface is unchanged.
func MCPName(canonical string) string {
	return tools.WireName(canonical)
}

// CanonicalName converts an MCP tool name back to the registry's canonical
// dotted spelling. See internal/tools.CanonicalName for why this is a
// table, not strings.ReplaceAll(mcpName, "_", ".").
func CanonicalName(mcpName string) string {
	return tools.CanonicalName(mcpName)
}
