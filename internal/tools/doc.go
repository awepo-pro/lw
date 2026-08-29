// Package tools defines the fixed set of 17 agent tools — read-only vault
// queries and staged mutations — shared by the in-process agent loop and the
// MCP transport. There is no filesystem verb, and none is ever added. It
// will hold registry.go, schema.go, read_vault.go, read_wiki.go,
// read_raw.go, stage_page.go, stage_link.go, stage_source.go and
// stage_session.go.
package tools
