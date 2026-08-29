// Package config loads and saves lw's TOML configuration — LLM endpoint,
// limits and theme — resolving secrets by reference rather than storing them,
// and locates the XDG config and data directories. It will hold config.go
// and paths.go.
package config
