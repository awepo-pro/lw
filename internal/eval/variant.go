package eval

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"github.com/awepo-pro/lw/internal/config"
)

// DefaultConfigPath is the config.toml a plain `lw` would read:
// $XDG_CONFIG_HOME/lw/config.toml, else ~/.config/lw/config.toml — the same
// resolution lw itself uses, so a variant starts from what the user runs
// with. (037 T3.)
func DefaultConfigPath() string {
	return filepath.Join(config.ConfigDir(), "config.toml")
}

// secretKeyRe matches the names of settings that may hold a credential. It
// is a rule about NAMES, not values — a value cannot be recognised as a
// secret, a name can be guessed at — so it errs wide: "llm.max_tokens"
// matches "token" and is treated as secret too. (A-037-7.)
var secretKeyRe = regexp.MustCompile(`(?i)key|token|secret|password|auth`)

// SecretKey reports whether a dotted config key looks like it holds a secret:
// its value must then never be echoed in a note or an error. (037 T3,
// A-037-7.)
func SecretKey(key string) bool {
	return secretKeyRe.MatchString(key)
}

// VariantNote is the run note a variant gets when the caller gave none:
// "set-config:" and the key=value pairs, space-separated, so a run directory
// says what was changed without anyone remembering — with a key that looks
// secret (SecretKey) listed by NAME only. The note lands in run.json and in
// the scorecard, both of which get pasted into reports; an API key in the
// variant must reach lw through the private config copy and nowhere else.
// (037 T3, A-037-7.)
func VariantNote(sets []string) string {
	if len(sets) == 0 {
		return ""
	}
	parts := make([]string, len(sets))
	for i, s := range sets {
		key, _, _ := strings.Cut(s, "=")
		if key = strings.TrimSpace(key); SecretKey(key) {
			parts[i] = key
		} else {
			parts[i] = s
		}
	}
	return "set-config: " + strings.Join(parts, " ")
}

// staleVariantAge is how long an lweval-config-* directory must have gone
// untouched before a new run sweeps it: well past the heartbeat, so a live
// run's directory is never taken for a leftover.
const staleVariantAge = time.Hour

// variantHeartbeat is how often a running variant touches its directory so
// the sweep's age test sees it as live — a full N=3 eval can outlast the
// sweep's hour. A variable so a test can shorten it.
var variantHeartbeat = 5 * time.Minute

// variantDirRe is the name MkdirTemp("", "lweval-config-") gives: the prefix
// and a run of digits. Nothing else in $TMPDIR is ours to delete.
var variantDirRe = regexp.MustCompile(`^lweval-config-[0-9]+$`)

// SweepVariantDirs removes the lweval-config-* directories in $TMPDIR that a
// killed run left behind: really directories (never a symlink), owned by the
// current user, named as WithVariant names them, and untouched for over an
// hour. It returns the paths it removed, sorted. (037 T3, A-037-7.)
//
// A SIGKILL cannot be handled, so without this a copy of the user's config —
// API key included — would sit in /tmp for good after every kill -9. The sweep
// runs when a new run starts; WithVariant's heartbeat keeps a live run's
// directory young.
func SweepVariantDirs(now time.Time) ([]string, error) {
	return sweepVariantDirs(os.TempDir(), now, staleVariantAge, os.Getuid())
}

// sweepVariantDirs is SweepVariantDirs over an explicit directory, age and
// owner, so a test can aim it without touching the real $TMPDIR.
func sweepVariantDirs(tmp string, now time.Time, maxAge time.Duration, uid int) ([]string, error) {
	entries, err := os.ReadDir(tmp)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("sweep %s: %w", tmp, err)
	}
	var removed []string
	var errs []error
	for _, e := range entries { // sorted by name
		if !variantDirRe.MatchString(e.Name()) {
			continue
		}
		full := filepath.Join(tmp, e.Name())
		info, err := os.Lstat(full)
		if err != nil || !info.IsDir() {
			continue // gone already, a file, or a symlink: not ours to remove
		}
		if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
			continue
		}
		if now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		if err := removeAll(full); err != nil {
			errs = append(errs, fmt.Errorf("sweep %s: %w", full, err))
			continue
		}
		removed = append(removed, full)
	}
	return removed, errors.Join(errs...)
}

// WithVariant runs fn under a private copy of the config at configPath with
// the key=value pairs in sets rewritten, and removes that copy on every way
// out (037 T3).
//
// An eval variant ("same set, thinking on") must not touch the user's own
// config — that is the file their daily lw reads, and an interrupted run
// would leave it changed. So the copy lives in its own 0700 temp directory as
// <dir>/lw/config.toml (0600: it can hold an API key), and fn receives the
// environment entry that points lw at it, XDG_CONFIG_HOME=<dir>. Rewriting
// happens before the directory exists, so a refused key leaves nothing
// behind.
//
// The directory is removed when fn returns — by value, by error or by panic
// — and also the moment ctx is cancelled, without waiting for fn to notice:
// Ctrl-C must not leave a copy of the API key in /tmp because a child
// process is slow to die. Both paths share one sync.Once, so WithVariant
// never returns before the removal has finished.
func WithVariant(ctx context.Context, configPath string, sets []string, fn func(env []string) error) error {
	if len(sets) == 0 {
		return errors.New("--set-config: no key=value given")
	}
	src, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("--set-config: read config: %w", err)
	}
	out, err := RewriteConfig(src, sets)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	dir, err := os.MkdirTemp("", "lweval-config-")
	if err != nil {
		return fmt.Errorf("--set-config: %w", err)
	}
	var once sync.Once
	cleanup := func() { once.Do(func() { removeAll(dir) }) }
	defer cleanup()
	defer context.AfterFunc(ctx, cleanup)()

	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("--set-config: %w", err)
	}
	sub := filepath.Join(dir, "lw")
	if err := os.Mkdir(sub, 0o700); err != nil {
		return fmt.Errorf("--set-config: %w", err)
	}
	file := filepath.Join(sub, "config.toml")
	if err := os.WriteFile(file, out, 0o600); err != nil {
		return fmt.Errorf("--set-config: %w", err)
	}
	if err := os.Chmod(file, 0o600); err != nil {
		return fmt.Errorf("--set-config: %w", err)
	}

	// The heartbeat keeps the directory's mtime young while fn runs, so a
	// sweep by another lweval never mistakes a long run's config copy for a
	// leftover. It stops before the directory is removed.
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(variantHeartbeat)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				now := time.Now()
				_ = os.Chtimes(dir, now, now)
			case <-stop:
				return
			}
		}
	}()
	defer func() {
		close(stop)
		<-done
	}()
	return fn([]string{"XDG_CONFIG_HOME=" + dir})
}

// RewriteConfig returns src with the value of every key=value pair in sets
// replaced, and nothing else changed (037 T3).
//
// A key is dotted — the table, then the key (llm.thinking, llm.limits.
// max_tool_rounds, or a bare root key) — and must already be in the file:
// a variant changes a setting the user has, it does not invent one, and a
// typo ("llm.thinkng") must be an error instead of a silent no-op run that
// costs a full eval. The rewrite is line-based and touches only the bytes of
// the value: indentation, the spacing around "=", a trailing comment, line
// endings and every other line stay as written. The old value's TOML type is
// kept — a string stays quoted (in the same quote style when it can),
// integers, floats and booleans stay bare, and a value that does not fit
// the old type is refused rather than written as a different type lw would
// then reject at start-up. Arrays, dates and multi-line strings are refused.
//
// Lines inside a multi-line string or array are never read as keys or table
// headers. The result is decoded again before it is returned, so a rewrite
// that would produce invalid TOML (or a string that does not read back as
// asked) is an error here, not an lw failure in the middle of a run.
func RewriteConfig(src []byte, sets []string) ([]byte, error) {
	type pair struct{ key, value string }
	var pairs []pair
	seen := map[string]bool{}
	for _, s := range sets {
		key, value, ok := strings.Cut(s, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			// Never echo the pair: without its "key=" it may be nothing but
			// the secret. (A-037-7.)
			return nil, errors.New("--set-config: want key=value")
		}
		if seen[key] {
			return nil, fmt.Errorf("--set-config %s: given twice", key)
		}
		seen[key] = true
		pairs = append(pairs, pair{key, value})
	}

	var tree map[string]any
	if _, err := toml.Decode(string(src), &tree); err != nil {
		return nil, fmt.Errorf("config.toml: %w", err)
	}
	lines := strings.SplitAfter(string(src), "\n")
	index := scanKeys(lines)

	want := map[string]string{} // key -> string value the rewrite must read back as
	for _, p := range pairs {
		old, ok := lookupKey(tree, p.key)
		if !ok {
			return nil, fmt.Errorf("--set-config %s: no such key in config.toml", p.key)
		}
		loc, ok := index[p.key]
		if !ok {
			return nil, fmt.Errorf("--set-config %s: not a plain `key = value` line in its table (dotted and inline-table keys cannot be rewritten)", p.key)
		}
		var text string
		switch old.(type) {
		case string:
			if loc.multiString {
				return nil, fmt.Errorf("--set-config %s: a multi-line string cannot be rewritten", p.key)
			}
			text = tomlString(p.value, lines[loc.line][loc.start] == '\'')
			want[p.key] = p.value
		case int64:
			if !intRe.MatchString(p.value) {
				return nil, fmt.Errorf("--set-config %s: want an integer%s", p.key, got(p.key, p.value))
			}
			text = p.value
		case float64:
			f, err := strconv.ParseFloat(p.value, 64)
			if !floatRe.MatchString(p.value) || err != nil || math.IsInf(f, 0) {
				return nil, fmt.Errorf("--set-config %s: want a number%s", p.key, got(p.key, p.value))
			}
			text = p.value
			if !strings.ContainsAny(text, ".eE") {
				text += ".0" // keep it a float: an integer here would change the key's type
			}
		case bool:
			if p.value != "true" && p.value != "false" {
				return nil, fmt.Errorf("--set-config %s: want true or false%s", p.key, got(p.key, p.value))
			}
			text = p.value
		default:
			return nil, fmt.Errorf("--set-config %s: the value is %s; only strings, integers, floats and booleans can be rewritten", p.key, kindOf(old))
		}
		line := lines[loc.line]
		lines[loc.line] = line[:loc.start] + text + line[loc.end:]
	}

	out := strings.Join(lines, "")
	var after map[string]any
	if _, err := toml.Decode(out, &after); err != nil {
		return nil, fmt.Errorf("--set-config: the rewritten config is not valid TOML: %w", err)
	}
	for key, v := range want {
		if read, _ := lookupKey(after, key); read != v {
			return nil, fmt.Errorf("--set-config %s: the value does not read back as written", key)
		}
	}
	return []byte(out), nil
}

// got renders the offending value for a refusal — ", got "lots"" — except
// for a key that looks secret, whose value is never repeated. (A-037-7.)
func got(key, value string) string {
	if SecretKey(key) {
		return ""
	}
	return fmt.Sprintf(", got %q", value)
}

var (
	intRe   = regexp.MustCompile(`^[+-]?[0-9]+$`)
	floatRe = regexp.MustCompile(`^[+-]?[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

	// A header may carry a comment; the line keeps its own newline, which
	// "." does not match, so the trailing \s* is what lets $ reach the end.
	tableRe      = regexp.MustCompile(`^\s*\[([^\[\]]+)\]\s*(#[^\n]*)?\s*$`)
	arrayTableRe = regexp.MustCompile(`^\s*\[\[[^\[\]]+\]\]\s*(#[^\n]*)?\s*$`)
	keyRe        = regexp.MustCompile(`^\s*(?:([A-Za-z0-9_-]+)|"((?:[^"\\]|\\.)*)"|'([^']*)')\s*=\s*`)
)

// lookupKey finds a dotted key in a decoded TOML tree. A table is not a
// value: only a key that holds one can be set.
func lookupKey(tree map[string]any, key string) (any, bool) {
	node := tree
	parts := strings.Split(key, ".")
	for i, part := range parts {
		v, ok := node[part]
		if !ok {
			return nil, false
		}
		if i == len(parts)-1 {
			if _, isTable := v.(map[string]any); isTable {
				return nil, false
			}
			return v, true
		}
		next, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		node = next
	}
	return nil, false
}

// kindOf names a decoded value's TOML kind with its article, for the refusal
// that says what the key holds.
func kindOf(v any) string {
	switch v.(type) {
	case []any, []map[string]any:
		return "an array"
	case map[string]any:
		return "a table"
	case time.Time:
		return "a datetime"
	}
	return fmt.Sprintf("a %T", v)
}

// keyLoc is where a plain `key = value` line keeps its value: the line, the
// byte span of the value text within it, and whether the value is a
// multi-line string (which the rewrite refuses).
type keyLoc struct {
	line, start, end int
	multiString      bool
}

// scanKeys indexes every plain `key = value` line of a TOML file by its
// dotted path — table name, then key. Lines inside a multi-line string or
// array are skipped, as are keys of [[array tables]], which a dotted path
// cannot name. The first occurrence of a path wins (TOML forbids a second).
func scanKeys(lines []string) map[string]keyLoc {
	idx := map[string]keyLoc{}
	table, addressable := "", true
	var cont *continuation
	for i, line := range lines {
		if cont != nil {
			if cont.feed(line) {
				cont = nil
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed[0] == '#' {
			continue
		}
		if arrayTableRe.MatchString(line) {
			table, addressable = "", false
			continue
		}
		if m := tableRe.FindStringSubmatch(line); m != nil {
			table, addressable = normalizeTable(m[1]), true
			continue
		}
		m := keyRe.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		var key string
		switch {
		case m[2] >= 0:
			key = line[m[2]:m[3]]
		case m[4] >= 0:
			key = line[m[4]:m[5]]
		default:
			key = line[m[6]:m[7]]
		}
		start := m[1]
		end, multi, next := scanValue(line, start)
		cont = next
		if !addressable {
			continue
		}
		path := key
		if table != "" {
			path = table + "." + key
		}
		if _, dup := idx[path]; !dup {
			idx[path] = keyLoc{line: i, start: start, end: end, multiString: multi}
		}
	}
	return idx
}

// normalizeTable turns a header's inside ("llm . limits", `"a".b`) into the
// dotted path keys are looked up under.
func normalizeTable(inside string) string {
	parts := strings.Split(inside, ".")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && (p[0] == '"' || p[0] == '\'') && p[len(p)-1] == p[0] {
			p = p[1 : len(p)-1]
		}
		parts[i] = p
	}
	return strings.Join(parts, ".")
}

// continuation is a value that runs past its line: a multi-line string
// waiting for its closing delimiter, or an array/inline table waiting for
// its brackets to balance.
type continuation struct {
	delim string // `"""` or `'''`, for a multi-line string
	depth int    // open brackets, for an array
}

// feed consumes one more line of the continuation and reports whether the
// value ended on it.
func (c *continuation) feed(line string) bool {
	if c.delim != "" {
		return strings.Contains(line, c.delim)
	}
	_, depth := scanBrackets(line, 0, c.depth)
	c.depth = depth
	return depth <= 0
}

// scanValue finds the end of the value that starts at byte start of line:
// the byte after its last character on this line, whether it is a multi-line
// string, and the continuation when it runs on to later lines. A trailing
// comment and the spaces before it are not part of the value.
func scanValue(line string, start int) (end int, multi bool, next *continuation) {
	rest := line[start:]
	switch {
	case strings.HasPrefix(rest, `"""`) || strings.HasPrefix(rest, `'''`):
		delim := rest[:3]
		if i := strings.Index(rest[3:], delim); i >= 0 {
			return start + 3 + i + 3, true, nil
		}
		return len(strings.TrimRight(line, "\r\n")), true, &continuation{delim: delim}
	case len(rest) > 0 && rest[0] == '"':
		for i := 1; i < len(rest); i++ {
			switch rest[i] {
			case '\\':
				i++
			case '"':
				return start + i + 1, false, nil
			}
		}
	case len(rest) > 0 && rest[0] == '\'':
		if i := strings.IndexByte(rest[1:], '\''); i >= 0 {
			return start + 1 + i + 1, false, nil
		}
	case len(rest) > 0 && (rest[0] == '[' || rest[0] == '{'):
		e, depth := scanBrackets(line, start, 0)
		if depth > 0 {
			return e, false, &continuation{depth: depth}
		}
		return e, false, nil
	}
	// A bare value (number, bool, date): up to whitespace or a comment.
	i := 0
	for i < len(rest) && rest[i] != ' ' && rest[i] != '\t' && rest[i] != '#' && rest[i] != '\r' && rest[i] != '\n' {
		i++
	}
	return start + i, false, nil
}

// scanBrackets walks line from byte from with depth open brackets and
// returns the byte after the bracket that closed the outermost one (or the
// end of the line's content) and the depth left. Strings and comments are
// skipped, so a "]" inside quotes or after a "#" does not close anything.
func scanBrackets(line string, from, depth int) (end, left int) {
	i := from
	for i < len(line) {
		switch line[i] {
		case '#':
			return len(strings.TrimRight(line, "\r\n")), depth
		case '"':
			for i++; i < len(line) && line[i] != '"'; i++ {
				if line[i] == '\\' {
					i++
				}
			}
		case '\'':
			for i++; i < len(line) && line[i] != '\''; i++ {
			}
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth <= 0 {
				return i + 1, 0
			}
		}
		i++
	}
	return len(strings.TrimRight(line, "\r\n")), depth
}

// tomlString renders s as a TOML string. A literal string ('…') is kept
// literal when s can sit in one — no quote, no control character — and
// anything else is a basic string with the escapes TOML requires.
func tomlString(s string, literal bool) string {
	if literal && !strings.ContainsAny(s, "'") && !hasControl(s) {
		return "'" + s + "'"
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 || r == 0x7f || r == utf8.RuneError {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
