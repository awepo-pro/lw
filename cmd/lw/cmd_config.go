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
		set:     setAPIKeyRef(func(c *config.Config) *string { return &c.LLM.APIKey }, "llm.api_key"),
		display: displayAPIKey},
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
	{key: "llm.thinking", get: func(c *config.Config) string { return c.LLM.Thinking },
		set: func(c *config.Config, v string) error {
			switch v {
			case "off", "on", "default":
				c.LLM.Thinking = v
				return nil
			}
			return fmt.Errorf("llm.thinking: want off|on|default, got %q — off sends thinking:{\"type\":\"disabled\"}; default omits the key", v)
		},
		display: displayPlain},
	{key: "llm.stall_timeout", get: func(c *config.Config) string { return c.LLM.StallTimeout },
		set:     setStallTimeout,
		display: displayStallTimeout},
	{key: "llm.limits.max_tool_rounds", get: func(c *config.Config) string { return strconv.Itoa(c.Limits.MaxToolRounds) },
		set:     setInt(func(c *config.Config) *int { return &c.Limits.MaxToolRounds }, "llm.limits.max_tool_rounds"),
		display: displayPlain},
	{key: "llm.limits.context_tokens", get: func(c *config.Config) string { return strconv.Itoa(c.Limits.ContextTokens) },
		set:     setInt(func(c *config.Config) *int { return &c.Limits.ContextTokens }, "llm.limits.context_tokens"),
		display: displayPlain},
	{key: "web.provider", get: func(c *config.Config) string { return c.Web.Provider },
		set: func(c *config.Config, v string) error {
			if v != "tavily" {
				return fmt.Errorf("web.provider: unknown provider %q — tavily is the only built-in", v)
			}
			c.Web.Provider = v
			return nil
		},
		display: displayPlain},
	{key: "web.api_key", get: func(c *config.Config) string { return c.Web.APIKey },
		set:     setAPIKeyRef(func(c *config.Config) *string { return &c.Web.APIKey }, "web.api_key"),
		display: displayAPIKey},
	{key: "web.max_results", get: func(c *config.Config) string { return strconv.Itoa(c.Web.MaxResults) },
		set:     setIntBounded(func(c *config.Config) *int { return &c.Web.MaxResults }, "web.max_results", 1, 10),
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

// setStallTimeout validates llm.stall_timeout before anything is written,
// applying the single source of truth, config.ValidateStallTimeout (026 T3
// F.K2 — the same rule Load enforces when the saved file is read back): a Go
// duration, or "0"/"0s" to switch the bound off. "" stores the key absent,
// which Load reads as config.DefaultStallTimeout. A negative bound would fail
// every turn and is refused, not clamped.
func setStallTimeout(c *config.Config, v string) error {
	if err := config.ValidateStallTimeout(v); err != nil {
		return err
	}
	c.LLM.StallTimeout = v
	return nil
}

// displayStallTimeout renders the stored string. An empty value means the
// key is absent and Load applies config.DefaultStallTimeout, so the table
// says that instead of printing a blank a reader could mistake for "no
// bound".
func displayStallTimeout(raw string) string {
	if raw == "" {
		return fmt.Sprintf("(default %s)", config.DefaultStallTimeout)
	}
	return raw
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

// setIntBounded returns setInt's setter with an accepted range: a value
// outside min..max is refused before anything is written. web.max_results
// carries the same 1..10 bound the web.search tool's schema puts on the
// per-call argument (010 contract §3/§4), so a stored default can never
// ask the provider for something the verb itself would refuse.
func setIntBounded(dst func(c *config.Config) *int, key string, min, max int) func(*config.Config, string) error {
	return func(c *config.Config, v string) error {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s: want an integer, got %q", key, v)
		}
		if n < min || n > max {
			return fmt.Errorf("%s: want %d..%d, got %d", key, min, max, n)
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
		return raw + " (not implemented yet)"
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

// setAPIKeyRef returns the setter for an api_key field — llm.api_key and,
// since 010, web.api_key share one reference discipline (A-10-5): the
// indirection forms are stored verbatim; a literal that looks like a key is
// refused, because it would land unencrypted in a file every lw process
// reads and no diagnostic prints. A literal that does not look like a key is
// stored (backbone §11 keeps it legal) and is never echoed back by show or
// by this verb. key names the field in both error messages, so the fix each
// one suggests is copy-pasteable.
func setAPIKeyRef(dst func(c *config.Config) *string, key string) func(*config.Config, string) error {
	return func(c *config.Config, v string) error {
		switch {
		case v == "":
			return fmt.Errorf("%s: an empty value leaves lw without credentials; point it at an environment variable instead: lw config set %s env:NAME", key, key)
		case looksLikeSecret(v):
			return fmt.Errorf("refusing to store an API key literal in %s — it would sit unencrypted on disk; export the key and reference the variable instead: lw config set %s env:NAME", configFilePath(), key)
		}
		*dst(c) = v
		return nil
	}
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
  llm.max_tokens              llm.thinking
  llm.stall_timeout           llm.limits.max_tool_rounds
  llm.limits.context_tokens   web.provider
  web.api_key                 web.max_results
  theme

llm.thinking is off|on|default: off sends thinking:{"type":"disabled"} so a
thinking-mode provider spends its budget answering, on sends
thinking:{"type":"enabled"}, and default omits the key so the provider
decides.
llm.stall_timeout is a Go duration ("90s", "2m"): the longest a turn waits
with no bytes from the provider before failing (default 120s). "0" disables
the bound; omitting the key keeps the default.
llm.api_key and web.api_key are stored as references, never values: export
the key and set the variable's name, e.g. lw config set llm.api_key env:LW_API_KEY.
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
// not. config.Load merges the defaults behind the file, so a value the file
// omits resolves to lw's built-in and is labelled accordingly — the marker
// says which value is in force, not which lines the file happens to carry.
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
		if isAPIKeyField(f.key) && raw != "" && !strings.HasPrefix(raw, "env:") && !strings.HasPrefix(raw, "keyring:") {
			row.note = fmt.Sprintf("%s is stored in the config file; prefer an environment reference: lw config set %s env:NAME", f.key, f.key)
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

// withDefaults was deleted when internal/config.Load learned to merge
// config.Default() behind the file (S6 wrap-up): the merge happens at load
// time now, on every key the file omits, so a loaded Config is already
// complete and saving it writes the defaults a hand-written file implied
// rather than the zeros it never mentioned. The trade-off that helper
// carried — an explicit zero in the file read as "unset" and silently took
// the default — is gone with it: Load can tell "omitted" from "explicitly
// zero" (BurntSushi's MetaData), so the file's word is kept either way.

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
	if err := cfg.Save(); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "%s set to %s\n", key, storedDescription(key, field.get(cfg)))
	fmt.Fprintf(os.Stdout, "saved %s\n", configFilePath())
	warnUnresolvableKey(os.Stderr, cfg)
	return nil
}

// isAPIKeyField reports whether key is one of the api_key fields — llm.api_key
// and web.api_key — which share the reference discipline end to end.
func isAPIKeyField(key string) bool {
	return key == "llm.api_key" || key == "web.api_key"
}

// storedDescription is the confirmation's right-hand side. For an api_key
// field it is the reference that was stored, or a description rather than the
// value — an api_key literal is never echoed, so the confirmation stays safe
// to paste into a bug report. Every other key is echoed as set.
func storedDescription(key, raw string) string {
	if isAPIKeyField(key) && raw != "" && !strings.HasPrefix(raw, "env:") && !strings.HasPrefix(raw, "keyring:") {
		return "a literal value (not echoed)"
	}
	return raw
}

// warnUnresolvableKey says so when the key just stored cannot resolve in this
// shell, rather than letting the failure surface as a broken ingest (or a
// silently absent web.search) later.
func warnUnresolvableKey(w io.Writer, cfg *config.Config) {
	for _, k := range []struct {
		key string
		ref string
	}{{"llm.api_key", cfg.LLM.APIKey}, {"web.api_key", cfg.Web.APIKey}} {
		switch {
		case strings.HasPrefix(k.ref, "env:"):
			name := strings.TrimPrefix(k.ref, "env:")
			if v, ok := os.LookupEnv(name); !ok || v == "" {
				fmt.Fprintf(w, "warning: %s is not set in this shell; lw cannot resolve %s until it is exported\n", name, k.key)
			}
		case strings.HasPrefix(k.ref, "keyring:"):
			fmt.Fprintf(w, "note: keyring: references are accepted but not implemented yet, so %s will not resolve; use env:NAME\n", k.key)
		}
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
