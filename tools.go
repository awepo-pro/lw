//go:build tools
// +build tools

// Package tools exists solely to pin the module's staged dependencies before
// any real package imports them, so `go mod tidy` cannot strip them out from
// under a later subtask. It is never compiled into the binary and may be
// deleted once every dependency has a real import site (S6).
package tools

import (
	_ "charm.land/bubbles/v2"
	_ "charm.land/bubbletea/v2"
	_ "charm.land/glamour/v2"
	_ "charm.land/lipgloss/v2"
	_ "github.com/BurntSushi/toml"
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
	_ "github.com/yuin/goldmark"
	_ "go.abhg.dev/goldmark/wikilink"
	_ "golang.org/x/net/html"
	_ "gopkg.in/yaml.v3"
)
