package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// envPrefix and keyringPrefix mark the two reference forms an api_key value
// may take; anything else is a literal (/PLAN.md §11.2 calls this
// "discouraged" but legal).
const (
	envPrefix     = "env:"
	keyringPrefix = "keyring:"
)

// configFileName is the file Load reads and Save writes, inside ConfigDir.
const configFileName = "config.toml"

// LLM is the language-model endpoint lw talks to.
type LLM struct {
	BaseURL     string  `toml:"base_url"`
	Model       string  `toml:"model"`
	APIKey      string  `toml:"api_key"` // "env:NAME" | "keyring:NAME" | literal
	Temperature float64 `toml:"temperature"`
	MaxTokens   int     `toml:"max_tokens"`
}

// Limits bounds the agent loop's resource usage.
type Limits struct {
	MaxToolRounds int `toml:"max_tool_rounds"`
	ContextTokens int `toml:"context_tokens"`
}

// Config is lw's top-level configuration, loaded from and saved to
// <ConfigDir()>/config.toml.
//
// Contract (backbone §11, C-96): this Go shape is flat and does not change —
// Config.Limits is a top-level field, because §9's NewLoop, S5-T3 and S6-T3
// all compile against it. The toml:"llm.limits" tag below is NOT what puts
// Limits on disk: BurntSushi reads a dotted struct tag as a literal top-level
// key named "llm.limits", not as a path into the nested [llm.limits] table
// /PLAN.md §11.2 documents. Load and Save therefore marshal through the
// private shadowConfig below instead of decoding/encoding Config directly;
// see toShadow/fromShadow.
type Config struct {
	LLM    LLM    `toml:"llm"`
	Limits Limits `toml:"llm.limits"`
	Theme  string `toml:"theme"`
}

// shadowLLM is LLM with Limits nested inside it as "limits", so encoding
// shadowConfig produces [llm] followed by a nested [llm.limits] table —
// byte-for-byte the wire format /PLAN.md §11.2 publishes.
type shadowLLM struct {
	BaseURL     string  `toml:"base_url"`
	Model       string  `toml:"model"`
	APIKey      string  `toml:"api_key"`
	Temperature float64 `toml:"temperature"`
	MaxTokens   int     `toml:"max_tokens"`
	Limits      Limits  `toml:"limits"`
}

// shadowConfig is the on-disk shape of Config: the same fields, with Limits
// relocated under LLM. It exists solely so Load/Save can hand BurntSushi a
// struct whose tags actually produce the documented nested table; Config
// itself is never decoded/encoded directly.
type shadowConfig struct {
	LLM   shadowLLM `toml:"llm"`
	Theme string    `toml:"theme"`
}

// toShadow converts a Config to its on-disk shape, for Save.
func toShadow(c *Config) shadowConfig {
	return shadowConfig{
		LLM: shadowLLM{
			BaseURL:     c.LLM.BaseURL,
			Model:       c.LLM.Model,
			APIKey:      c.LLM.APIKey,
			Temperature: c.LLM.Temperature,
			MaxTokens:   c.LLM.MaxTokens,
			Limits:      c.Limits,
		},
		Theme: c.Theme,
	}
}

// fromShadow converts a decoded on-disk shape back to the public Config, for
// Load.
func fromShadow(s shadowConfig) *Config {
	return &Config{
		LLM: LLM{
			BaseURL:     s.LLM.BaseURL,
			Model:       s.LLM.Model,
			APIKey:      s.LLM.APIKey,
			Temperature: s.LLM.Temperature,
			MaxTokens:   s.LLM.MaxTokens,
		},
		Limits: s.LLM.Limits,
		Theme:  s.Theme,
	}
}

// Load reads <ConfigDir()>/config.toml. A missing config file is not an
// error: it returns Default(), so a fresh install works before `lw init`
// has ever run.
//
// A file that IS present is merged over Default() rather than read alone.
// This is the fix for a measured defect: a hand-written file naming one key
// came back with every other field zeroed, which sent MaxToolRounds 0 and
// MaxTokens 0 into the agent loop (the hard stop /PLAN.md §11.2 calls the
// defence against a runaway agent) and made `lw config set` save an
// `api_key = ""` over the user's credential reference.
//
// The merge is per key, and "the file wins" includes an explicit zero: the
// distinction between a key the file OMITS (keep lw's default) and one it
// SETS to a zero value (keep the zero) is readable from BurntSushi's
// MetaData.IsDefined, so both are honoured. No field's zero value is a
// working setting — max_tokens 0 and 0 tool rounds produce dead requests,
// an empty api_key is no credentials — so a file can only ever ask lw for
// something that cannot work, and it stays the user's word to have done so.
// fromShadow is still what decodes the bytes; it is kept because the shadow
// shape, not Config, is what the file format is.
func Load() (*Config, error) {
	path := filepath.Join(ConfigDir(), configFileName)
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), nil
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var s shadowConfig
	md, err := toml.Decode(string(b), &s)
	if err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return mergeOverDefault(Default(), fromShadow(s), md), nil
}

// mergeOverDefault copies onto def every field md reports as present in the
// decoded file, and returns def. Keys the file names win; keys it omits
// keep whatever def already holds. The key paths are the shadow shape's —
// Limits lives at ("llm","limits", …), because that is where the file's
// [llm.limits] table lands (C-96).
func mergeOverDefault(def, file *Config, md toml.MetaData) *Config {
	if md.IsDefined("llm", "base_url") {
		def.LLM.BaseURL = file.LLM.BaseURL
	}
	if md.IsDefined("llm", "model") {
		def.LLM.Model = file.LLM.Model
	}
	if md.IsDefined("llm", "api_key") {
		def.LLM.APIKey = file.LLM.APIKey
	}
	if md.IsDefined("llm", "temperature") {
		def.LLM.Temperature = file.LLM.Temperature
	}
	if md.IsDefined("llm", "max_tokens") {
		def.LLM.MaxTokens = file.LLM.MaxTokens
	}
	if md.IsDefined("llm", "limits", "max_tool_rounds") {
		def.Limits.MaxToolRounds = file.Limits.MaxToolRounds
	}
	if md.IsDefined("llm", "limits", "context_tokens") {
		def.Limits.ContextTokens = file.Limits.ContextTokens
	}
	if md.IsDefined("theme") {
		def.Theme = file.Theme
	}
	return def
}

// Save writes c to <ConfigDir()>/config.toml with 0600 permissions,
// creating ConfigDir() if it does not exist. Secrets are referenced, never
// stored (§11 contract): Save serializes c.LLM.APIKey exactly as held, so a
// value such as "env:DEEPSEEK_API_KEY" round-trips as that reference and
// the literal secret it resolves to is never written to disk.
func (c *Config) Save() error {
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: create %s: %w", dir, err)
	}

	var buf strings.Builder
	if err := toml.NewEncoder(&buf).Encode(toShadow(c)); err != nil {
		return fmt.Errorf("config: encode config: %w", err)
	}

	path := filepath.Join(dir, configFileName)
	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	// os.WriteFile only applies the mode bits when it creates the file; make
	// sure a pre-existing config.toml ends up at 0600 too.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("config: chmod %s: %w", path, err)
	}
	return nil
}

// ResolveAPIKey returns the literal API key for c.LLM.APIKey. "env:NAME"
// reads that environment variable and errors clearly if it is unset or
// empty; "keyring:NAME" errors because v0.1 does not support a keyring;
// anything else is a literal, returned as-is.
func (c *Config) ResolveAPIKey() (string, error) {
	ref := c.LLM.APIKey
	switch {
	case strings.HasPrefix(ref, envPrefix):
		name := strings.TrimPrefix(ref, envPrefix)
		v, ok := os.LookupEnv(name)
		if !ok || v == "" {
			return "", fmt.Errorf("config: environment variable %s is not set", name)
		}
		return v, nil
	case strings.HasPrefix(ref, keyringPrefix):
		return "", fmt.Errorf("config: keyring references are not supported in v0.1")
	default:
		return ref, nil
	}
}

// Default returns lw's out-of-the-box configuration: the DeepSeek endpoint
// from /PLAN.md §11.2 (MASTER §9 D-CG), with its API key referenced from the
// environment rather than stored.
func Default() *Config {
	return &Config{
		LLM: LLM{
			BaseURL:     "https://api.deepseek.com/v1",
			Model:       "deepseek-v4-flash",
			APIKey:      "env:DEEPSEEK_API_KEY",
			Temperature: 0.2,
			MaxTokens:   8192,
		},
		Limits: Limits{
			MaxToolRounds: 24,
			ContextTokens: 96000,
		},
	}
}
