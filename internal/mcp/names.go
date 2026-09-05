package mcp

import "strings"

// MCPName converts a canonical dotted tool name to the spelling used on the
// MCP wire. Canonical tool names use dots for namespace separators; MCP names
// use underscores so they remain portable across clients.
func MCPName(canonical string) string {
	switch canonical {
	case "raw.get":
		return "raw_get"
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

// CanonicalName converts an MCP tool name back to the registry's canonical
// dotted spelling.
func CanonicalName(mcpName string) string {
	switch mcpName {
	case "raw_get":
		return "raw.get"
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
		return strings.ReplaceAll(mcpName, "_", ".")
	}
}
