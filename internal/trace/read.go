package trace

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/llm"
)

// turnNameRE is the shape of a turn directory: NewID's output. Everything
// else under the traces dir — a human's notes, an editor's droppings — is
// invisible to this package: readers skip it and Prune never counts it, so
// the traces dir stays a place other files can safely share.
var turnNameRE = regexp.MustCompile(`^\d{8}T\d{6}Z-[0-9a-f]{4}$`)

// Summary is what a turn list needs: one row per turn, enough to pick one
// to open, none of the content.
type Summary struct {
	ID              string
	Verb            string
	Started         time.Time
	Rounds          int
	Reason          string // the done event's reason, "" while the turn has none
	InputTokens     int
	CachedTokens    int
	OutputTokens    int
	ReasoningTokens int
	ToolErrors      int
	WallMS          int64
	Bytes           int64 // the turn dir's size on disk
	HasUsage        bool
}

// Attempt is one request the loop sent: its wire shape (file, size, hash,
// message and tool counts), the response it got, and the tool calls that
// answer triggered. A retried round produces two Attempts — the failed one
// keeps its response so the retry's cause stays visible.
type Attempt struct {
	Round    int
	Attempt  int
	File     string
	Bytes    int
	SHA256   string
	Messages int
	ToolDefs int
	Response *Response
	Calls    []Tool
}

// Turn is one turn's whole story, as Load reconstructs it.
type Turn struct {
	ID       string
	Meta     Meta
	Started  time.Time
	Attempts []Attempt
	Elisions []Elision
	Retries  []RetryInfo
	Done     *Done
}

// Elision is one batch of content the loop dropped in a round.
type Elision struct {
	Round int
	Count int
	Bytes int
}

// RetryInfo is one failed attempt the loop re-sent.
type RetryInfo struct {
	Round   int
	Attempt int
	Reason  string
}

// rawEvent is the union of every event line: readers take the whole line as
// one struct and interpret by Kind, because a per-kind struct per line would
// mean a first unmarshal just to learn which struct to unmarshal into. Only
// the fields each kind actually writes are meaningful.
type rawEvent struct {
	Turn string    `json:"turn"`
	TS   time.Time `json:"ts"`
	Kind string    `json:"kind"`

	// turn
	Verb          string `json:"verb"`
	Session       string `json:"session"`
	Version       string `json:"lw_version"`
	Model         string `json:"gen_ai.request.model"`
	Server        string `json:"server.address"`
	Thinking      string `json:"thinking"`
	MaxRounds     int    `json:"max_rounds"`
	ContextTokens int    `json:"context_tokens"`

	// request, and the round/attempt every round-tagged kind carries
	Round    int    `json:"round"`
	Attempt  int    `json:"attempt"`
	File     string `json:"file"`
	Bytes    int    `json:"bytes"`
	SHA256   string `json:"sha256"`
	Messages int    `json:"messages"`
	ToolDefs int    `json:"tools"`

	// response
	Finish       string     `json:"finish"`
	FirstByteMS  int64      `json:"first_byte_ms"`
	FirstDeltaMS int64      `json:"first_delta_ms"`
	StreamMS     int64      `json:"stream_ms"`
	Reasoning    string     `json:"reasoning"`
	Text         string     `json:"text"`
	ToolCalls    []ToolCall `json:"tool_calls"`
	Usage        *usageJSON `json:"usage"`
	Cut          bool       `json:"cut"`
	Error        string     `json:"error"`

	// tool
	ID          string `json:"id"`
	Name        string `json:"name"`
	IsError     bool   `json:"is_error"`
	MS          int64  `json:"ms"`
	ResultBytes int    `json:"result_bytes"`

	// elide
	Count int `json:"count"`

	// done
	Reason string `json:"reason"`
	Rounds int    `json:"rounds"`
	WallMS int64  `json:"wall_ms"`
}

// usage converts the GenAI-named shape back to llm.Usage.
func (u *usageJSON) usage() *llm.Usage {
	if u == nil {
		return nil
	}
	return &llm.Usage{
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		CachedTokens:    u.CachedTokens,
		ReasoningTokens: u.ReasoningTokens,
	}
}

// turnIDs lists dir's turn directories, oldest first — the id is a UTC
// timestamp, so name order is time order. A missing dir has no turns, not
// an error: List and Size on a fresh vault must both be calm.
func turnIDs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && turnNameRE.MatchString(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids) // os.ReadDir already sorts; stated for the contract
	return ids, nil
}

// dirSize sums the sizes of the regular files under dir. An entry (or dir
// itself) that vanished mid-walk is not an error: another lw process's prune
// may remove a turn while we size it, and losing this turn's trace to a
// peer's cleanup race would break tracing being observation only.
func dirSize(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		n += info.Size()
		return nil
	})
	return n, err
}

// parseEvents reads a turn's events.ndjson back. A torn final line — a
// crash mid-write — is data lw wrote while dying, not an error in the
// trace, so any line that does not unmarshal is dropped, wherever it sits.
func parseEvents(turnDir string) ([]rawEvent, error) {
	b, err := os.ReadFile(filepath.Join(turnDir, eventsFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var evs []rawEvent
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		var ev rawEvent
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Kind == "" {
			continue
		}
		evs = append(evs, ev)
	}
	return evs, nil
}

// List summarizes dir's turns, newest first — the order a human (and `lw
// trace list`) looks at them. A turn whose events cannot be read — a chmod
// 000 dir, a lost events.ndjson — is skipped with one WARN, not dropped from
// the whole listing: one bad turn must not blind `lw trace` to the rest
// (038 T2b). An error comes back only when dir itself cannot be read; a
// missing dir lists zero turns.
func List(dir string) ([]Summary, error) {
	ids, err := turnIDs(dir)
	if err != nil {
		return nil, err
	}
	var out []Summary
	for i := len(ids) - 1; i >= 0; i-- {
		s, err := summarize(filepath.Join(dir, ids[i]), ids[i])
		if err != nil {
			slog.Warn("trace unreadable", "turn", ids[i], "err", err)
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// summarize folds one turn's events into a Summary. Token fields sum every
// response's usage, because a turn's real cost is the whole loop, retries
// included.
func summarize(turnDir, id string) (Summary, error) {
	s := Summary{ID: id}
	evs, err := parseEvents(turnDir)
	if err != nil {
		return s, err
	}
	for _, ev := range evs {
		switch ev.Kind {
		case kindTurn:
			s.Verb, s.Started = ev.Verb, ev.TS
		case kindResponse:
			if u := ev.Usage.usage(); u != nil {
				s.InputTokens += u.InputTokens
				s.CachedTokens += u.CachedTokens
				s.OutputTokens += u.OutputTokens
				s.ReasoningTokens += u.ReasoningTokens
				s.HasUsage = true
			}
		case kindTool:
			if ev.IsError {
				s.ToolErrors++
			}
		case kindDone:
			s.Reason, s.WallMS = ev.Reason, ev.WallMS
		}
		if ev.Round > s.Rounds {
			s.Rounds = ev.Round
		}
	}
	size, err := dirSize(turnDir)
	if err != nil {
		return s, err
	}
	s.Bytes = size
	return s, nil
}

// Load reads one turn back in full.
func Load(dir, id string) (*Turn, error) {
	if !turnNameRE.MatchString(id) {
		return nil, fmt.Errorf("trace: no turn matches %q", id)
	}
	turnDir := filepath.Join(dir, id)
	if _, err := os.Stat(turnDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("trace: no turn matches %q", id)
		}
		return nil, err
	}
	evs, err := parseEvents(turnDir)
	if err != nil {
		return nil, err
	}
	t := &Turn{ID: id}
	for i := range evs {
		ev := evs[i]
		switch ev.Kind {
		case kindTurn:
			t.Meta = Meta{
				Verb: ev.Verb, Session: ev.Session, Version: ev.Version,
				Model: ev.Model, Server: ev.Server, Thinking: ev.Thinking,
				MaxRounds: ev.MaxRounds, ContextTokens: ev.ContextTokens,
			}
			t.Started = ev.TS
		case kindRequest:
			t.Attempts = append(t.Attempts, Attempt{
				Round: ev.Round, Attempt: ev.Attempt, File: ev.File,
				Bytes: ev.Bytes, SHA256: ev.SHA256,
				Messages: ev.Messages, ToolDefs: ev.ToolDefs,
			})
		case kindResponse:
			if a := latestAttempt(t.Attempts, ev.Round, ev.Attempt); a != nil {
				a.Response = &Response{
					Round: ev.Round, Attempt: ev.Attempt, Finish: ev.Finish,
					FirstByteMS: ev.FirstByteMS, FirstDeltaMS: ev.FirstDeltaMS, StreamMS: ev.StreamMS,
					Reasoning: ev.Reasoning, Text: ev.Text, ToolCalls: ev.ToolCalls,
					Usage: ev.Usage.usage(), Cut: ev.Cut, Error: ev.Error,
				}
			}
		case kindTool:
			// A tool belongs to the attempt whose response asked for it:
			// the latest attempt of its round (the retry that survived).
			if a := latestRound(t.Attempts, ev.Round); a != nil {
				a.Calls = append(a.Calls, Tool{
					Round: ev.Round, ID: ev.ID, Name: ev.Name, IsError: ev.IsError,
					MS: ev.MS, ResultBytes: ev.ResultBytes,
				})
			}
		case kindElide:
			t.Elisions = append(t.Elisions, Elision{Round: ev.Round, Count: ev.Count, Bytes: ev.Bytes})
		case kindRetry:
			t.Retries = append(t.Retries, RetryInfo{Round: ev.Round, Attempt: ev.Attempt, Reason: ev.Reason})
		case kindDone:
			t.Done = &Done{Reason: ev.Reason, Rounds: ev.Rounds, Error: ev.Error, WallMS: ev.WallMS}
		}
	}
	return t, nil
}

// latestAttempt returns the newest attempt matching round and attempt,
// falling back to the round's newest attempt so a response still lands
// somewhere visible when its own attempt line was lost to a crash.
func latestAttempt(attempts []Attempt, round, attempt int) *Attempt {
	for i := len(attempts) - 1; i >= 0; i-- {
		if attempts[i].Round == round && attempts[i].Attempt == attempt {
			return &attempts[i]
		}
	}
	return latestRound(attempts, round)
}

// latestRound returns the newest attempt of round, or nil.
func latestRound(attempts []Attempt, round int) *Attempt {
	for i := len(attempts) - 1; i >= 0; i-- {
		if attempts[i].Round == round {
			return &attempts[i]
		}
	}
	return nil
}

// Body returns the exact request bytes a turn sent in one attempt — the
// gunzipped content of the request file, byte-identical to what lw POSTed.
func Body(dir, id string, round, attempt int) ([]byte, error) {
	t, err := Load(dir, id)
	if err != nil {
		return nil, err
	}
	for i := range t.Attempts {
		a := &t.Attempts[i]
		if a.Round == round && a.Attempt == attempt {
			// The file name comes from the trace itself, not from lw: a
			// hand-edited or damaged trace must not steer an open outside
			// the turn's dir the way the id's own regexp check does.
			if a.File == "" || a.File == "." || a.File == ".." || filepath.Base(a.File) != a.File {
				return nil, fmt.Errorf("trace: turn %s names a bad request file %q", id, a.File)
			}
			f, err := os.Open(filepath.Join(dir, id, a.File))
			if err != nil {
				return nil, err
			}
			defer f.Close()
			z, err := gzip.NewReader(f)
			if err != nil {
				return nil, err
			}
			defer z.Close()
			return io.ReadAll(z)
		}
	}
	return nil, fmt.Errorf("trace: turn %s has no request for round %d attempt %d", id, round, attempt)
}

// Resolve turns a human's reference into a turn id: "last", a full id, or a
// unique prefix of one. Anything ambiguous or unknown is an error whose
// text says what to do next, because this is what a person types and gets
// back (038 T2, W7).
func Resolve(dir, ref string) (string, error) {
	ids, err := turnIDs(dir)
	if err != nil {
		return "", err
	}
	if ref == "last" {
		if len(ids) == 0 {
			return "", errors.New("no traces yet")
		}
		return ids[len(ids)-1], nil
	}
	for _, id := range ids {
		if id == ref {
			return id, nil
		}
	}
	var matches []string
	for _, id := range ids {
		if strings.HasPrefix(id, ref) {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no turn matches %q", ref)
	default:
		return "", fmt.Errorf("%q matches %d turns; give more of the id", ref, len(matches))
	}
}

// Size reports how much the traces dir holds: how many turns, how many
// bytes — the fact `lw trace`'s footer shows next to the keep budget. Like
// List it skips a turn it cannot read, with the same one-line WARN (038
// T2b), and errs only when dir itself cannot be read.
func Size(dir string) (turns int, bytes int64, err error) {
	ids, err := turnIDs(dir)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range ids {
		n, err := dirSize(filepath.Join(dir, id))
		if err != nil {
			slog.Warn("trace unreadable", "turn", id, "err", err)
			continue
		}
		turns++
		bytes += n
	}
	return turns, bytes, nil
}
