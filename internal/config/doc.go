// Package config loads and saves lw's TOML configuration — LLM endpoint,
// limits, theme and the optional [web] lookup — resolving secrets by
// reference rather than storing them, and locates the XDG config and data
// directories. It will hold config.go and paths.go.
package config
