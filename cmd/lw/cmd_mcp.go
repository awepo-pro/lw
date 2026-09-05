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

	reg := tools.NewRegistry(tools.Deps{
		Vault:  e.Vault(),
		Index:  e.Index(),
		Engine: e,
		Author: stage.Author{Kind: "agent", Model: "mcp"},
	})
	return mcp.Serve(context.Background(), reg, os.Stdin, os.Stdout)
}
