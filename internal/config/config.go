package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// envPrefix and keyringPrefix mark the two reference forms an api_key value
// may take; anything else is a literal (/docs/design.md §11.2 calls this
// "discouraged" but legal).
const (
	envPrefix     = "env:"
	keyringPrefix = "keyring:"
)

// configFileName is the file Load reads and Save writes, inside ConfigDir.
const configFileName = "config.toml"

// MinRecommendedMaxTokens is the llm.max_tokens floor lw doctor warns
// below: thinking-mode models spend most of a round's budget reasoning, and
// a live GLM ingest measured 5,247 reasoning tokens in one round (008 W0).
const MinRecommendedMaxTokens = 16000

// DefaultStallTimeout is the llm.stall_timeout bound a config that omits the
// key gets (026 T3): the longest llm waits with no bytes from the provider
// before failing the turn with llm.ErrStalled.
const DefaultStallTimeout = 120 * time.Second

// DefaultExtractTimeout is the [extract] timeout bound a config that omits
// the key gets (007 T3): the wall-clock ceiling on one PDF's extraction,
// sized for a cold first run — the backend may need to download its models
// (~30 s) on top of ~0.7 s CPU per page.
const DefaultExtractTimeout = 300 * time.Second

// DefaultTraceKeepMB is the trace.keep_mb bound a config that omits the key
// gets (038 T3): how many MiB of turn traces <vault>/.llmwiki/traces may
// hold before `lw trace` prunes the oldest. 0 means tracing is off; the
// ceiling Load enforces is 10240.
const DefaultTraceKeepMB = 256

// LLM is the language-model endpoint lw talks to.
type LLM struct {
	BaseURL     string  `toml:"base_url"`
	Model       string  `toml:"model"`
	APIKey      string  `toml:"api_key"` // "env:NAME" | "keyring:NAME" | literal
	Temperature float64 `toml:"temperature"`
	MaxTokens   int     `toml:"max_tokens"`
	// Thinking is the GLM thinking-mode switch (022): "off" — lw's default —
	// sends thinking:{"type":"disabled"} on every request, "on" sends
	// thinking:{"type":"enabled"}, and "default" sends no thinking key so
	// the provider's own default applies. The mapping lives in
	// llm.buildRequestBody; this key only carries the user's word.
	Thinking string `toml:"thinking"`
	// StallTimeout is the provider-silence bound (026 T3): a Go duration
	// string ("90s", "2m"); "0" or "0s" disables the bound. The mapping to
	// the duration llm.Config consumes lives in StallTimeoutDuration below;
	// this key only carries the user's word, like Thinking.
	StallTimeout string `toml:"stall_timeout"`
}

// StallTimeoutDuration maps the raw llm.stall_timeout string onto the
// time.Duration llm.Config consumes (026 T3). "" — the key absent, which is
// how Default ships — is DefaultStallTimeout; "0" and "0s" parse to 0, which
// llm reads as no bound. Load rejects an unparsable or negative value
// (validateStallTimeout), so by the time a loaded config reaches this method
// the string always parses.
func (l LLM) StallTimeoutDuration() time.Duration {
	if l.StallTimeout == "" {
		return DefaultStallTimeout
	}
	d, _ := time.ParseDuration(l.StallTimeout)
	return d
}

// Extract configures the PDF extraction backend behind `lw ingest` (007
// T3). Command is an argv prefix — split on whitespace, the PDF path is
// appended as the last argument — so a pinned install reads
// "uvx --from docling==2.130.0 docling". Timeout is a Go duration string
// bounding one extraction; unlike llm.stall_timeout there is no "0 = off":
// an extraction that never returns is a defect, so Load rejects zero too
// (validateExtractTimeout).
type Extract struct {
	Command string `toml:"command"` // argv prefix; "" → ["docling"]
	// Timeout is the per-document wall-clock bound ("90s", "2m"). The
	// mapping to the duration the backend consumes lives in
	// TimeoutDuration below; this key only carries the user's word, like
	// StallTimeout.
	Timeout string `toml:"timeout"`
}

// Argv splits Command into the argv prefix the extraction runner execs,
// with the document path appended after it. An empty Command still names a
// backend: it falls back to docling, the same default Default() ships, so
// a hand-written `[extract]` table naming only timeout cannot leave the
// runner without a command.
func (x Extract) Argv() []string {
	if f := strings.Fields(x.Command); len(f) > 0 {
		return f
	}
	return []string{"docling"}
}

// TimeoutDuration maps the raw extract.timeout string onto the
// time.Duration the extraction runner consumes (007 T3). "" — the key
// absent, which is how Default ships — is DefaultExtractTimeout. Load
// rejects an unparsable, zero or negative value (validateExtractTimeout),
// so by the time a loaded config reaches this method the string always
// parses.
func (x Extract) TimeoutDuration() time.Duration {
	if x.Timeout == "" {
		return DefaultExtractTimeout
	}
	d, _ := time.ParseDuration(x.Timeout)
	return d
}

// Open configures how lw's UI opens a PDF original at a page (034 T5). PDF
// is a viewer template — split on whitespace, {file} and {page} substituted
// inside each field, the file appended when no field names {file} — so one
// line covers every common viewer: "papers -i {page} {file}", "mupdf {file}
// {page}", or a bare "zathura". It configures the TUI's citation picker
// only; nothing the agent runs ever execs a viewer.
type Open struct {
	PDF string `toml:"pdf"` // viewer template; "" → no viewer wired
}

// Trace configures turn tracing (038 T3): whether the agent records the
// exact request bytes and stream per turn under <vault>/.llmwiki/traces/.
// KeepMB is a pointer so "the key is absent" and "the key is 0" stay
// distinct — absent means the DefaultTraceKeepMB default, 0 means tracing
// is off — which is why the whole table is omitted on Save while KeepMB is
// nil, the same rule stall_timeout's omitempty follows in shadowLLM.
type Trace struct {
	KeepMB *int `toml:"keep_mb,omitempty"`
}

// TraceKeepBytes maps the user's keep_mb onto the byte bound the trace
// pruner consumes (038 T3). A nil KeepMB — the key absent, which is how
// Default ships — is DefaultTraceKeepMB << 20; 0 maps to 0, which reads as
// tracing off. Load rejects anything outside 0..10240 (ValidateTraceKeepMB),
// so the value is always in range by the time it reaches this method.
func (c *Config) TraceKeepBytes() int64 {
	if c.Trace.KeepMB == nil {
		return DefaultTraceKeepMB << 20
	}
	return int64(*c.Trace.KeepMB) << 20
}

// ErrNoPDFViewer is what Open.Argv returns for a template that names no
// viewer: there is nothing to compile, and inventing a default would exec
// a program the user never chose. (034 T5.)
var ErrNoPDFViewer = errors.New("config: open.pdf: no PDF viewer configured")

// Argv compiles the template into the argv the viewer is exec'd with (034
// T5): fields split on whitespace, {file} and {page} replaced inside each
// field — a viewer that spells the page as an option value
// (--page-label=12) is exactly the one-line config this exists for — and
// the file appended when no field names {file}. A page below 1 becomes 1:
// unpaged markers reach the picker as 0, and every viewer needs a real
// page. An empty or whitespace-only template is ErrNoPDFViewer.
func (o Open) Argv(file string, page int) ([]string, error) {
	fields := strings.Fields(o.PDF)
	if len(fields) == 0 {
		return nil, ErrNoPDFViewer
	}
	if page < 1 {
		page = 1
	}
	argv := make([]string, 0, len(fields)+1)
	sawFile := false
	for _, f := range fields {
		if strings.Contains(f, "{file}") {
			sawFile = true
		}
		f = strings.ReplaceAll(f, "{file}", file)
		f = strings.ReplaceAll(f, "{page}", strconv.Itoa(page))
		argv = append(argv, f)
	}
	if !sawFile {
		argv = append(argv, file)
	}
	return argv, nil
}

// Limits bounds the agent loop's resource usage.
type Limits struct {
	MaxToolRounds int `toml:"max_tool_rounds"`
	ContextTokens int `toml:"context_tokens"`
}

// Web configures the web lookup behind the web.search tool. With an empty
// APIKey the tool is never denied — it is simply not offered: the tool
// registry gains web.search only when a provider is wired (010 contract §4).
// APIKey follows the same reference rules as LLM.APIKey — "env:NAME",
// "keyring:NAME", or a literal — and is resolved, never logged, at wiring
// time. Provider names the search provider; "tavily" is the only built-in.
type Web struct {
	Provider   string `toml:"provider"`    // default "tavily"; only built-in
	APIKey     string `toml:"api_key"`     // "" → web.search not offered
	MaxResults int    `toml:"max_results"` // default 5
}

// Config is lw's top-level configuration, loaded from and saved to
// <ConfigDir()>/config.toml.
//
// Contract (backbone §11, C-96): this Go shape is flat and does not change —
// Config.Limits is a top-level field, because §9's NewLoop, S5-T3 and S6-T3
// all compile against it. The toml:"llm.limits" tag below is NOT what puts
// Limits on disk: BurntSushi reads a dotted struct tag as a literal top-level
// key named "llm.limits", not as a path into the nested [llm.limits] table
// /docs/design.md §11.2 documents. Load and Save therefore marshal through the
// private shadowConfig below instead of decoding/encoding Config directly;
// see toShadow/fromShadow. The [web] table is the one post-v1 addition,
// amended by workflow 010 (C-1001).
type Config struct {
	LLM     LLM     `toml:"llm"`
	Limits  Limits  `toml:"llm.limits"`
	Web     Web     `toml:"web"`
	Extract Extract `toml:"extract"`
	Open    Open    `toml:"open"`
	Theme   string  `toml:"theme"`
	Trace   Trace   `toml:"trace"`
}

// shadowLLM is LLM with Limits nested inside it as "limits", so encoding
// shadowConfig produces [llm] followed by a nested [llm.limits] table —
// byte-for-byte the wire format /docs/design.md §11.2 publishes.
type shadowLLM struct {
	BaseURL     string  `toml:"base_url"`
	Model       string  `toml:"model"`
	APIKey      string  `toml:"api_key"`
	Temperature float64 `toml:"temperature"`
	MaxTokens   int     `toml:"max_tokens"`
	Thinking    string  `toml:"thinking"`
	// omitempty keeps a keyless config keyless on Save (026 T3, F.K3): the
	// 120s default lives in DefaultStallTimeout, not on disk, so a file
	// that never named the key round-trips byte-identically.
	StallTimeout string `toml:"stall_timeout,omitempty"`
	Limits       Limits `toml:"limits"`
}

// shadowExtract is Extract with the on-disk tags (007 T3, F.X4): omitempty
// on timeout keeps a keyless config keyless on Save — the 300s default
// lives in DefaultExtractTimeout, not on disk, exactly as stall_timeout is
// treated in shadowLLM.
type shadowExtract struct {
	Command string `toml:"command"`
	Timeout string `toml:"timeout,omitempty"`
}

// shadowConfig is the on-disk shape of Config: the same fields, with Limits
// relocated under LLM. It exists solely so Load/Save can hand BurntSushi a
// struct whose tags actually produce the documented nested table; Config
// itself is never decoded/encoded directly. Web needs no relocation — its
// tags already match the flat [web] table the file carries (C-1001).
type shadowConfig struct {
	LLM     shadowLLM     `toml:"llm"`
	Web     Web           `toml:"web"`
	Extract shadowExtract `toml:"extract"`
	// omitempty keeps a viewerless config viewerless on Save (034 T5): the
	// whole [open] table is skipped while Open is the zero value —
	// BurntSushi's isEmpty treats a comparable struct like a scalar — so a
	// file that never named the key round-trips byte-identically, the same
	// rule stall_timeout's omitempty follows one level down.
	Open  Open   `toml:"open,omitempty"`
	Theme string `toml:"theme"`
	// omitempty keeps a traceless config traceless on Save (038 T3): with
	// KeepMB nil the whole [trace] table is skipped — BurntSushi's isEmpty
	// reads a nil pointer as empty — so a file that never named the key
	// round-trips byte-identically.
	Trace Trace `toml:"trace,omitempty"`
}

// toShadow converts a Config to its on-disk shape, for Save.
func toShadow(c *Config) shadowConfig {
	return shadowConfig{
		LLM: shadowLLM{
			BaseURL:      c.LLM.BaseURL,
			Model:        c.LLM.Model,
			APIKey:       c.LLM.APIKey,
			Temperature:  c.LLM.Temperature,
			MaxTokens:    c.LLM.MaxTokens,
			Thinking:     c.LLM.Thinking,
			StallTimeout: c.LLM.StallTimeout,
			Limits:       c.Limits,
		},
		Web:     c.Web,
		Extract: shadowExtract{Command: c.Extract.Command, Timeout: c.Extract.Timeout},
		Open:    c.Open,
		Theme:   c.Theme,
		Trace:   c.Trace,
	}
}

// fromShadow converts a decoded on-disk shape back to the public Config, for
// Load.
func fromShadow(s shadowConfig) *Config {
	return &Config{
		LLM: LLM{
			BaseURL:      s.LLM.BaseURL,
			Model:        s.LLM.Model,
			APIKey:       s.LLM.APIKey,
			Temperature:  s.LLM.Temperature,
			MaxTokens:    s.LLM.MaxTokens,
			Thinking:     s.LLM.Thinking,
			StallTimeout: s.LLM.StallTimeout,
		},
		Limits:  s.LLM.Limits,
		Web:     s.Web,
		Extract: Extract{Command: s.Extract.Command, Timeout: s.Extract.Timeout},
		Open:    s.Open,
		Theme:   s.Theme,
		Trace:   s.Trace,
	}
}

// Load reads <ConfigDir()>/config.toml. A missing config file is not an
// error: it returns Default(), so a fresh install works before `lw init`
// has ever run.
//
// A file that IS present is merged over Default() rather than read alone.
// This is the fix for a measured defect: a hand-written file naming one key
// came back with every other field zeroed, which sent MaxToolRounds 0 and
// MaxTokens 0 into the agent loop (the hard stop /docs/design.md §11.2 calls the
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
	cfg := mergeOverDefault(Default(), fromShadow(s), md)
	if err := ValidateStallTimeout(cfg.LLM.StallTimeout); err != nil {
		return nil, err
	}
	if err := ValidateExtractTimeout(cfg.Extract.Timeout); err != nil {
		return nil, err
	}
	if err := ValidateTraceKeepMB(cfg.Trace.KeepMB); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ValidateTraceKeepMB rejects a trace.keep_mb value Load cannot honour (038
// T3, L3): negative, or past 10240 MiB — a cap the pruner could never
// outpace is not a cap. nil is the key absent and always valid;
// TraceKeepBytes owns the default. Exported so writers of the key (lw
// config set) apply the same rule before saving.
func ValidateTraceKeepMB(mb *int) error {
	if mb == nil {
		return nil
	}
	if *mb < 0 || *mb > 10240 {
		return fmt.Errorf("config: trace.keep_mb must be between 0 and 10240, got %d", *mb)
	}
	return nil
}

// ValidateStallTimeout rejects an llm.stall_timeout value Load cannot honour
// (026 T3, F.K2): not a Go duration string, or negative — a negative bound
// would fail every turn, which is not what "0 = off" means. "" is the key
// absent and always valid; StallTimeoutDuration owns the default. Exported so
// writers of the key (lw config set) apply the same rule before saving.
func ValidateStallTimeout(v string) error {
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fmt.Errorf("config: llm.stall_timeout: %q is not a duration (e.g. \"90s\", \"2m\"; \"0\" disables the bound)", v)
	}
	if d < 0 {
		return fmt.Errorf("config: llm.stall_timeout: %q is negative; use \"0\" to disable the bound", v)
	}
	return nil
}

// ValidateExtractTimeout rejects an extract.timeout value Load cannot honour
// (007 T3, F.X3): not a Go duration string, zero, or negative — zero here is
// a defect, not a mode: llm.stall_timeout reads "0" as "no bound", but an
// extraction that never runs would leave every PDF unprocessed, so the
// validator rejects it the way it rejects a negative bound. "" is the key
// absent and always valid; TimeoutDuration owns the default. Exported so
// writers of the key (lw config set) apply the same rule before saving.
func ValidateExtractTimeout(v string) error {
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fmt.Errorf("config: extract.timeout: %q is not a duration (e.g. \"90s\", \"2m\")", v)
	}
	if d <= 0 {
		return fmt.Errorf("config: extract.timeout: %q is not positive; extraction needs a real wall-clock bound", v)
	}
	return nil
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
	if md.IsDefined("llm", "thinking") {
		def.LLM.Thinking = file.LLM.Thinking
	}
	if md.IsDefined("llm", "stall_timeout") {
		def.LLM.StallTimeout = file.LLM.StallTimeout
	}
	if md.IsDefined("llm", "limits", "max_tool_rounds") {
		def.Limits.MaxToolRounds = file.Limits.MaxToolRounds
	}
	if md.IsDefined("llm", "limits", "context_tokens") {
		def.Limits.ContextTokens = file.Limits.ContextTokens
	}
	if md.IsDefined("web", "provider") {
		def.Web.Provider = file.Web.Provider
	}
	if md.IsDefined("web", "api_key") {
		def.Web.APIKey = file.Web.APIKey
	}
	if md.IsDefined("web", "max_results") {
		def.Web.MaxResults = file.Web.MaxResults
	}
	if md.IsDefined("extract", "command") {
		def.Extract.Command = file.Extract.Command
	}
	if md.IsDefined("extract", "timeout") {
		def.Extract.Timeout = file.Extract.Timeout
	}
	if md.IsDefined("open", "pdf") {
		def.Open = file.Open
	}
	if md.IsDefined("theme") {
		def.Theme = file.Theme
	}
	if md.IsDefined("trace", "keep_mb") {
		def.Trace.KeepMB = file.Trace.KeepMB
	}
	return def
}

// Save writes c to <ConfigDir()>/config.toml with 0600 permissions,
// creating ConfigDir() if it does not exist. Secrets are referenced, never
// stored (§11 contract): Save serializes c.LLM.APIKey exactly as held, so a
// value such as "env:DEEPSEEK_API_KEY" round-trips as that reference and
// the literal secret it resolves to is never written to disk. Web.APIKey
// follows the same rule (C-1001).
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
// empty; "keyring:NAME" errors because lw does not support a keyring yet;
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
		return "", fmt.Errorf("config: keyring references are not supported yet")
	default:
		return ref, nil
	}
}

// Default returns lw's out-of-the-box configuration: the DeepSeek endpoint
// from /docs/design.md §11.2 (MASTER §9 D-CG), with its API key referenced from the
// environment rather than stored. Thinking ships "off" (022): the explicit
// thinking:disabled keeps a thinking-mode provider from spending the round's
// budget reasoning instead of answering — "on" and "default" are the user's
// word. Web ships with the tavily provider named but no key: web.search
// stays unoffered until the user configures one.
func Default() *Config {
	return &Config{
		LLM: LLM{
			BaseURL:     "https://api.deepseek.com/v1",
			Model:       "deepseek-v4-flash",
			APIKey:      "env:DEEPSEEK_API_KEY",
			Temperature: 0.2,
			MaxTokens:   32768,
			Thinking:    "off",
		},
		Limits: Limits{
			MaxToolRounds: 24,
			ContextTokens: 96000,
		},
		Web: Web{
			Provider:   "tavily",
			APIKey:     "",
			MaxResults: 5,
		},
		// Extract ships the default backend and a keyless timeout (007 T3):
		// the 300s lives in TimeoutDuration, not on disk.
		Extract: Extract{
			Command: "docling",
			Timeout: "",
		},
		// Open ships unset (034 T5): no viewer is chosen for the user, and
		// the citation picker reports the unset hint until one is.
		Open: Open{},
	}
}
