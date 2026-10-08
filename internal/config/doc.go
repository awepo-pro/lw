// Package config loads and saves lw's TOML configuration — LLM endpoint,
// limits, theme, the optional [web] lookup and, since 042, the [vault] and
// [sync] tables that name the default vault and the remotes lw sync moves it
// through — resolving secrets by reference rather than storing them, and
// locates the XDG config and data directories. It will hold config.go and
// paths.go.
package config
