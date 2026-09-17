package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/awepo-pro/lw/internal/mcp"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/tools"
)

// cmdMCP will run the MCP server over stdio, exposing the tool registry.
func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw mcp: unexpected argument %q\n", fs.Arg(0))
		return &exitError{code: 2}
	}

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	e, err := stage.OpenEngine(root)
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}
	defer e.Close()

	reg := tools.NewRegistry(mcpDeps(e))
	return mcp.Serve(context.Background(), reg, os.Stdin, os.Stdout)
}

// mcpDeps assembles the tool registry's dependencies for the MCP server.
//
// Extract is agentExtractors() (U7) — the same chain cmd_ingest.go's
// agents are built over — so stage_ingest_source works over MCP the same
// way it does in-process: the two-consumers contract (backbone §6/§7)
// promises the same tool surface to both, and without an extractor the
// tool answers "no extractor configured" for every source.
func mcpDeps(e *stage.Engine) tools.Deps {
	return tools.Deps{
		Vault:   e.Vault(),
		Index:   e.Index(),
		Engine:  e,
		Extract: agentExtractors(),
		Author:  stage.Author{Kind: "agent", Model: "mcp"},
	}
}
