// names.go holds the wire-safe spelling for the registry's canonical dotted
// tool names, and the total, bijective mapping back to canonical. The table
// moved down from internal/mcp (backbone §6/§7's amendment, D-CY/C-112):
// every OpenAI-compatible endpoint enforces `^[a-zA-Z0-9_-]+$` on
// tools[*].function.name, exactly like MCP's own portability requirement,
// so BOTH consumers of Registry.Definitions — the MCP transport and the
// agent loop's chat-completions client — need this mapping, not just MCP.
// internal/mcp's MCPName/CanonicalName are one-line wrappers over
// WireName/CanonicalName below, so backbone §7's exported surface never
// changes.
package tools

import "strings"

// WireName converts a canonical dotted tool name to the spelling sent over
// the wire to either consumer: an MCP client, or an OpenAI-compatible
// chat-completions request via Registry.Definitions. Both reject a dot in
// a tool name, so "wiki.search" -> "wiki_search".
func WireName(canonical string) string {
	switch canonical {
	case "raw.get":
		return "raw_get"
	case "raw.list":
		return "raw_list"
	case "vault.orient":
		return "vault_orient"
	case "wiki.backlinks":
		return "wiki_backlinks"
	case "wiki.get":
		return "wiki_get"
	case "wiki.lint":
		return "wiki_lint"
	case "wiki.neighbors":
		return "wiki_neighbors"
	case "wiki.search":
		return "wiki_search"
	case "web.search":
		return "web_search"
	case "stage.open":
		return "stage_open"
	case "stage.create_page":
		return "stage_create_page"
	case "stage.patch_page":
		return "stage_patch_page"
	case "stage.rename_page":
		return "stage_rename_page"
	case "stage.merge_pages":
		return "stage_merge_pages"
	case "stage.split_page":
		return "stage_split_page"
	case "stage.add_link":
		return "stage_add_link"
	case "stage.ingest_source":
		return "stage_ingest_source"
	case "stage.retract":
		return "stage_retract"
	case "stage.close":
		return "stage_close"
	default:
		return strings.ReplaceAll(canonical, ".", "_")
	}
}

// CanonicalName converts a wire-spelled tool name — from either consumer —
// back to the registry's canonical dotted spelling.
//
// This is a literal switch, not strings.ReplaceAll(wire, "_", "."): several
// canonical names (e.g. "stage.create_page") carry their own underscore
// inside the leaf component, so a naive reverse would turn
// "stage_create_page" into "stage.create.page" instead of
// "stage.create_page". The switch is load-bearing (D-CY) — do not simplify
// it.
func CanonicalName(wire string) string {
	switch wire {
	case "raw_get":
		return "raw.get"
	case "raw_list":
		return "raw.list"
	case "vault_orient":
		return "vault.orient"
	case "wiki_backlinks":
		return "wiki.backlinks"
	case "wiki_get":
		return "wiki.get"
	case "wiki_lint":
		return "wiki.lint"
	case "wiki_neighbors":
		return "wiki.neighbors"
	case "wiki_search":
		return "wiki.search"
	case "web_search":
		return "web.search"
	case "stage_open":
		return "stage.open"
	case "stage_create_page":
		return "stage.create_page"
	case "stage_patch_page":
		return "stage.patch_page"
	case "stage_rename_page":
		return "stage.rename_page"
	case "stage_merge_pages":
		return "stage.merge_pages"
	case "stage_split_page":
		return "stage.split_page"
	case "stage_add_link":
		return "stage.add_link"
	case "stage_ingest_source":
		return "stage.ingest_source"
	case "stage_retract":
		return "stage.retract"
	case "stage_close":
		return "stage.close"
	default:
		return strings.ReplaceAll(wire, "_", ".")
	}
}
