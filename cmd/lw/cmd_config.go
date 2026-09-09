// cmd_config.go implements `lw config` (backbone §13, stage S6-T3): show the
// resolved configuration, set one key at a time, and probe the provider.
//
// The rule this verb exists to enforce: a secret is referenced, never stored
// or printed (backbone §11). The api_key line always shows the indirection —
// `env:NAME (set)` / `(missing)` — a literal already sitting in the file is
// reported by shape and never echoed, and `set` refuses a key-shaped literal
// outright, pointing at the env: indirection instead.
//
// This file never parses config.toml itself: internal/config owns the file
// format (backbone §11, C-96), and this verb reads only what config.Load and
// config.Save hand it.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/config"
)

// configProbeTimeout bounds the --probe request, exactly as lw doctor's
// provider check does: a hung endpoint must not hang a config command.
const configProbeTimeout = 20 * time.Second

// configFilePath is the file `lw config` reads and writes: config.toml inside
// config.ConfigDir(). internal/config owns that name too but does not export
// it (backbone §11 exports only ConfigDir and DataDir), so the join lives here.
func configFilePath() string {
	return filepath.Join(config.ConfigDir(), "config.toml")
}

// configField is one key `lw config set` accepts. get renders the stored
// value — unmasked, because it is compared against the default and never
// printed raw; display turns a stored value into the safe printed form.
type configField struct {
	key     string
	get     func(c *config.Config) string
	set     func(c *config.Config, v string) error
	display func(raw string) string
}

// displayPlain is the passthrough renderer shared by every non-secret key.
func displayPlain(raw string) string { return raw }

// configFields is every settable key, in the order `lw config` prints them.
var configFields = []configField{
	{key: "llm.base_url", get: func(c *config.Config) string { return c.LLM.BaseURL },
		set:     func(c *config.Config, v string) error { c.LLM.BaseURL = v; return nil },
		display: displayPlain},
	{key: "llm.model", get: func(c *config.Config) string { return c.LLM.Model },
		set:     func(c *config.Config, v string) error { c.LLM.Model = v; return nil },
		display: displayPlain},
	{key: "llm.api_key", get: func(c *config.Config) string { return c.LLM.APIKey },
		set: setAPIKey, display: displayAPIKey},
	{key: "llm.temperature", get: func(c *config.Config) string { return strconv.FormatFloat(c.LLM.Temperature, 'g', -1, 64) },
		set: func(c *config.Config, v string) error {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return fmt.Errorf("llm.temperature: want a number, got %q", v)
			}
			c.LLM.Temperature = f
			return nil
		},
		display: displayPlain},
	{key: "llm.max_tokens", get: func(c *config.Config) string { return strconv.Itoa(c.LLM.MaxTokens) },
		set:     setInt(func(c *config.Config) *int { return &c.LLM.MaxTokens }, "llm.max_tokens"),
		display: displayPlain},
	{key: "llm.limits.max_tool_rounds", get: func(c *config.Config) string { return strconv.Itoa(c.Limits.MaxToolRounds) },
		set:     setInt(func(c *config.Config) *int { return &c.Limits.MaxToolRounds }, "llm.limits.max_tool_rounds"),
		display: displayPlain},
	{key: "llm.limits.context_tokens", get: func(c *config.Config) string { return strconv.Itoa(c.Limits.ContextTokens) },
		set:     setInt(func(c *config.Config) *int { return &c.Limits.ContextTokens }, "llm.limits.context_tokens"),
		display: displayPlain},
	{key: "theme", get: func(c *config.Config) string { return c.Theme },
		set:     func(c *config.Config, v string) error { c.Theme = v; return nil },
		display: displayNotSet},
}

// configFieldByKey returns the field for a dotted key like "llm.model".
func configFieldByKey(key string) (configField, bool) {
	for _, f := range configFields {
		if f.key == key {
			return f, true
		}
	}
	return configField{}, false
}

// setInt returns a setter for an integer field, so the three integer keys
// share one coercion rule and one error message: a value that is not an
// integer is refused before anything is written.
func setInt(dst func(c *config.Config) *int, key string) func(*config.Config, string) error {
	return func(c *config.Config, v string) error {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s: want an integer, got %q", key, v)
		}
		*dst(c) = n
		return nil
	}
}

// displayNotSet renders an empty string as something a reader can tell apart
// from a value that happens to be blank.
func displayNotSet(raw string) string {
	if raw == "" {
		return "(not set)"
	}
	return raw
}

// displayAPIKey renders the api_key reference, never the secret it stands
// for: the env: indirection with whether the variable resolves, the keyring
// form with its status, or — for a literal, which backbone §11 keeps legal —
// only its shape.
func displayAPIKey(raw string) string {
	switch {
	case raw == "":
		return "(not set)"
	case strings.HasPrefix(raw, "env:"):
		if v, ok := os.LookupEnv(strings.TrimPrefix(raw, "env:")); ok && v != "" {
			return raw + " (set)"
		}
		return raw + " (missing)"
	case strings.HasPrefix(raw, "keyring:"):
		return raw + " (not implemented in v0.1)"
	default:
		return "literal (set)"
	}
}

// secretPrefixes are the token prefixes the major API vendors hand out. A
// value starting with one of them is a credential, not a config value.
var secretPrefixes = []string{
	"sk-", "sk_", "sk.", // OpenAI / DeepSeek / Anthropic style
	"pk-", "rk-", // paired publish/rollback styles
	"hf_",         // Hugging Face
	"ghp_",        // GitHub classic PAT
	"github_pat_", // GitHub fine-grained PAT
	"glpat-",      // GitLab PAT
	"xox",         // Slack
	"aiza",        // Google
}

// looksLikeSecret reports whether v is a credential rather than a value a
// human chose. Two rules, both cheap to predict: it starts with a known key
// prefix, or it is a long unbroken token — no separator at all in 24+
// characters is what a generated key looks like, and no base_url, model or
// theme a person types is ever in that shape.
func looksLikeSecret(v string) bool {
	if v == "" || strings.HasPrefix(v, "env:") || strings.HasPrefix(v, "keyring:") {
		return false
	}
	lower := strings.ToLower(v)
	for _, p := range secretPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return len(v) >= 24 && !strings.ContainsAny(v, " \t:./-")
}

// setAPIKey stores an api_key reference. The indirection forms are stored
// verbatim; a literal that looks like a key is refused, because it would land
// unencrypted in a file every lw process reads and no diagnostic prints. A
// literal that does not look like a key is stored (backbone §11 keeps it
// legal) and is never echoed back by show or by this verb.
func setAPIKey(c *config.Config, v string) error {
	switch {
	case v == "":
		return fmt.Errorf("llm.api_key: an empty value leaves lw without credentials; point it at an environment variable instead: lw config set llm.api_key env:NAME")
	case looksLikeSecret(v):
		return fmt.Errorf("refusing to store an API key literal in %s — it would sit unencrypted on disk; export the key and reference the variable instead: lw config set llm.api_key env:NAME", configFilePath())
	}
	c.LLM.APIKey = v
	return nil
}

// cmdConfig shows the resolved configuration, sets one key, prints the config
// file path, or probes the provider. A bad subcommand or argument prints the
// usage to stderr and exits 2 (backbone §13); show and probe print their own
// result to stdout and signal a failure with *exitError (MASTER §9 D-AC).
func cmdConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	probe := fs.Bool("probe", false, "check that the configured provider is reachable and returns tool calls (exit 0 ok, 1 failed)")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	rest := fs.Args()

	if len(rest) == 0 {
		if *probe {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			return runConfigProbe(context.Background(), cfg)
		}
		return runConfigShow(os.Stdout)
	}

	switch rest[0] {
	case "path":
		if *probe || len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "lw config path takes no other flag or argument")
			configUsage(os.Stderr)
			return &exitError{code: 2}
		}
		fmt.Println(configFilePath())
		return nil
	case "set":
		if *probe {
			fmt.Fprintln(os.Stderr, "lw config set cannot be combined with --probe")
			configUsage(os.Stderr)
			return &exitError{code: 2}
		}
		if len(rest) != 3 {
			fmt.Fprintf(os.Stderr, "lw config set takes exactly a key and a value (got %d argument(s))\n", len(rest)-1)
			configUsage(os.Stderr)
			return &exitError{code: 2}
		}
		return runConfigSet(rest[1], rest[2])
	default:
		fmt.Fprintf(os.Stderr, "lw config: unknown subcommand %q\n", rest[0])
		configUsage(os.Stderr)
		return &exitError{code: 2}
	}
}

// configUsage prints the verb's own usage — on stderr, with exit 2, so a
// scripted reader of stdout sees only results.
func configUsage(w io.Writer) {
	fmt.Fprint(w, `usage: lw config                    show the resolved configuration
       lw config --probe            check the configured provider (exit 0 ok, 1 failed)
       lw config path               print the config file path
       lw config set <key> <value>  set one key and save

keys:
  llm.base_url                llm.model
  llm.api_key                 llm.temperature
  llm.max_tokens              llm.limits.max_tool_rounds
  llm.limits.context_tokens   theme

llm.api_key is stored as a reference, never a value: export the key and set
the variable's name, e.g. lw config set llm.api_key env:DEEPSEEK_API_KEY.
A literal that looks like a key is refused.
`)
}

// configRow is one line of `lw config`'s table.
type configRow struct {
	key    string
	value  string // already masked: never a literal secret
	source string // "file" when the value is not lw's built-in, else "default"
	note   string // extra indented line under the row, "" when none
}

// configRows renders cfg in configFields order. A row is marked "(file)"
// when its value differs from config.Default()'s and "(default)" when it does
// not — the one provenance test internal/config's flat API supports, since
// Load returns the file's values and nothing else (a file that omits a field
// does not get the default back; see the report on this subtask).
func configRows(cfg, def *config.Config) []configRow {
	rows := make([]configRow, 0, len(configFields))
	for _, f := range configFields {
		raw := f.get(cfg)
		row := configRow{key: f.key, value: f.display(raw)}
		if raw != f.get(def) {
			row.source = "file"
		} else {
			row.source = "default"
		}
		if f.key == "llm.api_key" && raw != "" && !strings.HasPrefix(raw, "env:") && !strings.HasPrefix(raw, "keyring:") {
			row.note = "llm.api_key is stored in the config file; prefer an environment reference: lw config set llm.api_key env:NAME"
		}
		rows = append(rows, row)
	}
	return rows
}

// writeConfigRows prints the table, keys padded to one shared width so the
// rows stay greppable and the columns stable.
func writeConfigRows(w io.Writer, rows []configRow) {
	width := 0
	for _, r := range rows {
		if len(r.key) > width {
			width = len(r.key)
		}
	}
	for _, r := range rows {
		fmt.Fprintf(w, "%-*s = %s   (%s)\n", width, r.key, r.value, r.source)
		if r.note != "" {
			fmt.Fprintf(w, "  note: %s\n", r.note)
		}
	}
}

// runConfigShow prints the resolved configuration: the file it came from, and
// every field of config.Config (backbone §11 — the flat Go shape, whatever
// nesting the file itself uses), each marked as read from that file or still
// at lw's built-in default.
func runConfigShow(w io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	path := configFilePath()

	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(w, "config file: %s (loaded)\n", path)
	} else {
		fmt.Fprintf(w, "config file: %s (not present — showing lw's defaults)\n", path)
	}
	fmt.Fprintln(w)
	writeConfigRows(w, configRows(cfg, config.Default()))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "(file) = not lw's built-in value · (default) = lw's built-in value; change one with lw config set <key> <value>")
	return nil
}

// withDefaults overlays cfg onto config.Default(): a field left at its zero
// value takes the built-in default, and the result is what `lw config set`
// writes. config.Load returns the file's values and nothing else — a file
// that omits llm.api_key comes back with an empty one, and saving that
// verbatim would write the empty key and zero limits to disk, stripping the
// user's credentials the first time they changed an unrelated setting.
// Materializing a complete file is what a first `set` on a fresh install
// already does, so the semantic is not new; this only extends it to a file a
// hand wrote.
//
// The cost is that a field cannot be set to its zero value on purpose: an
// explicit temperature of 0, or an empty base_url, read as "unset" and take
// the default. No zero value is a working setting for any of these fields —
// max_tokens 0 and 0 tool rounds produce dead requests, and an empty key is
// no credentials — so nothing workable is lost.
func withDefaults(cfg *config.Config) *config.Config {
	def := config.Default()
	merged := *cfg
	if merged.LLM.BaseURL == "" {
		merged.LLM.BaseURL = def.LLM.BaseURL
	}
	if merged.LLM.Model == "" {
		merged.LLM.Model = def.LLM.Model
	}
	if merged.LLM.APIKey == "" {
		merged.LLM.APIKey = def.LLM.APIKey
	}
	if merged.LLM.Temperature == 0 {
		merged.LLM.Temperature = def.LLM.Temperature
	}
	if merged.LLM.MaxTokens == 0 {
		merged.LLM.MaxTokens = def.LLM.MaxTokens
	}
	if merged.Limits.MaxToolRounds == 0 {
		merged.Limits.MaxToolRounds = def.Limits.MaxToolRounds
	}
	if merged.Limits.ContextTokens == 0 {
		merged.Limits.ContextTokens = def.Limits.ContextTokens
	}
	return &merged
}

// runConfigSet validates the key, coerces and applies the value, then saves
// the whole config — load, mutate, save, the only path internal/config
// offers, so every other value the file carries survives. Any bad key or
// value is a usage error: the reason on stderr and exit 2, with nothing
// written.
func runConfigSet(key, value string) error {
	field, ok := configFieldByKey(key)
	if !ok {
		fmt.Fprintf(os.Stderr, "lw config: unknown key %q\n\nknown keys:\n%s\n", key, configKeyList())
		configUsage(os.Stderr)
		return &exitError{code: 2}
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := field.set(cfg, value); err != nil {
		fmt.Fprintf(os.Stderr, "lw config: %v\n", err)
		return &exitError{code: 2}
	}
	if err := withDefaults(cfg).Save(); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "%s set to %s\n", key, storedDescription(key, field.get(cfg)))
	fmt.Fprintf(os.Stdout, "saved %s\n", configFilePath())
	warnUnresolvableKey(os.Stderr, cfg)
	return nil
}

// storedDescription is the confirmation's right-hand side. For llm.api_key it
// is the reference that was stored, or a description rather than the value —
// an api_key literal is never echoed, so the confirmation stays safe to paste
// into a bug report. Every other key is echoed as set.
func storedDescription(key, raw string) string {
	if key == "llm.api_key" && raw != "" && !strings.HasPrefix(raw, "env:") && !strings.HasPrefix(raw, "keyring:") {
		return "a literal value (not echoed)"
	}
	return raw
}

// warnUnresolvableKey says so when the key just stored cannot resolve in this
// shell, rather than letting the failure surface as a broken ingest later.
func warnUnresolvableKey(w io.Writer, cfg *config.Config) {
	ref := cfg.LLM.APIKey
	switch {
	case strings.HasPrefix(ref, "env:"):
		name := strings.TrimPrefix(ref, "env:")
		if v, ok := os.LookupEnv(name); !ok || v == "" {
			fmt.Fprintf(w, "warning: %s is not set in this shell; lw cannot resolve the key until it is exported\n", name)
		}
	case strings.HasPrefix(ref, "keyring:"):
		fmt.Fprintln(w, "note: keyring: references are accepted but not implemented in v0.1, so this key will not resolve; use env:NAME")
	}
}

// configKeyList is the known keys, one per line, for the unknown-key error.
func configKeyList() string {
	keys := make([]string, 0, len(configFields))
	for _, f := range configFields {
		keys = append(keys, f.key)
	}
	return "  " + strings.Join(keys, "\n  ")
}

// runConfigProbe runs the provider check and prints the result — its own
// stdout is the result, so every outcome is printed and a failure is
// signalled with *exitError{1} rather than a bare error (backbone §13, D-AC).
// "Ok" means reachable *and* returning tool calls, the same bar lw doctor
// applies: ingest, query and the ask pane are dead without tool calling.
//
// It runs through the same probeProvider seam lw doctor uses (cmd_doctor.go),
// so tests substitute a fake and never touch the network. Probe caps its own
// request at 256 tokens (backbone §8, D-CP).
func runConfigProbe(ctx context.Context, cfg *config.Config) error {
	// Resolved here only to name the problem precisely; the value is dropped,
	// never printed.
	if _, err := cfg.ResolveAPIKey(); err != nil {
		fmt.Fprintf(os.Stdout, "probe failed: %v\n", err)
		fmt.Fprintln(os.Stdout, "  fix: export the variable llm.api_key names, or re-point it: lw config set llm.api_key env:NAME")
		return &exitError{code: 1}
	}

	pctx, cancel := context.WithTimeout(ctx, configProbeTimeout)
	defer cancel()
	res := probeProvider(pctx, cfg)

	switch {
	case res.Err != nil:
		fmt.Fprintf(os.Stdout, "probe failed: %s at %s is unreachable: %v\n", cfg.LLM.Model, cfg.LLM.BaseURL, res.Err)
		fmt.Fprintln(os.Stdout, "  fix: check llm.base_url and the key (lw config), and that the endpoint is reachable from this machine")
		return &exitError{code: 1}
	case !res.Reachable:
		fmt.Fprintf(os.Stdout, "probe failed: %s at %s returned no response\n", cfg.LLM.Model, cfg.LLM.BaseURL)
		fmt.Fprintln(os.Stdout, "  fix: check llm.base_url and the key (lw config), and that the endpoint is reachable from this machine")
		return &exitError{code: 1}
	case !res.ToolCalling:
		fmt.Fprintf(os.Stdout, "probe failed: %s answered but returned no tool call\n", res.Model)
		fmt.Fprintln(os.Stdout, "  fix: tool calling is required for ingest, query and the ask pane — configure a model that returns tool_calls: lw config set llm.model <name>")
		return &exitError{code: 1}
	}

	fmt.Fprintf(os.Stdout, "probe ok: %s reachable at %s, tool calling ok (%dms)\n", res.Model, cfg.LLM.BaseURL, res.Latency.Milliseconds())
	return nil
}
