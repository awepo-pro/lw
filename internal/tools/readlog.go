package tools

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// unreadSource is one source ingested in this registry whose chunks were not
// all read: its staged path, the chunks raw.get never served (ascending) and
// the total it has.
type unreadSource struct {
	Path   string
	Chunks []int
	N      int
}

// readLog is the 040 read log: which sources this registry ingested, and
// which of their chunks raw.get has served since. stage.close reads it to
// tell a model that skimmed a source from one that read it.
//
// Why it exists: a live ingest read one or two chunks a round, skimmed
// near-cap sources and wrote pages from chunks it never opened, and nothing
// in the tool layer noticed — stage.ingest_source reported "N chunk(s)" and
// stage.close only summarized. The prompt asks for full reading; this makes
// the ask checkable.
//
// One *readLog is made per Registry (NewRegistry) and shared by raw.get,
// stage.ingest_source and stage.close, so its state is per registry
// instance — the same lifetime 043's shrink arms have — and a second
// registry over the same engine, a joined changeset's earlier process,
// starts blind. That is deliberate: only sources ingested through THIS
// registry are checked, because only for those does the log know the reads
// it did not see are reads that did not happen.
//
// Tools may run concurrently within a round, so every method takes mu; the
// log is -race clean. The methods are nil-safe so a Deps built by hand
// (Deps{}) needs no log: a nil log records and reports nothing.
type readLog struct {
	mu       sync.Mutex
	ingested map[string]int          // staged path → chunk count raw.get reports for it
	read     map[string]map[int]bool // staged path → chunks served so far
	refused  string                  // the unread set the last close refusal named; "" when none is outstanding
}

func newReadLog() *readLog {
	return &readLog{ingested: map[string]int{}, read: map[string]map[int]bool{}}
}

// noteIngest records that this registry staged path, whose body raw.get
// serves in n chunks. A path ingested again is a new body — the changeset
// dropped the old op and staged another at the same path — so reads of the
// earlier one are forgotten rather than counted toward it.
func (l *readLog) noteIngest(path string, n int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ingested[path] = n
	l.read[path] = map[int]bool{}
}

// noteRead records that raw.get served chunk of path. A source this registry
// never ingested is not tracked: the log has no obligation to hold for it
// (a committed source, or one an earlier process staged), so the read is
// dropped rather than stored for nothing.
func (l *readLog) noteRead(path string, chunk int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if set, ok := l.read[path]; ok {
		set[chunk] = true
	}
}

// unread lists the sources with chunks still unread, sorted by path, each
// chunk list ascending. live narrows the sources to the ones the changeset
// being closed still holds (nil keeps every ingested source): see
// closeVerdict for why the close needs that.
func (l *readLog) unread(live func(path string) bool) []unreadSource {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.unreadLocked(live)
}

func (l *readLog) unreadLocked(live func(path string) bool) []unreadSource {
	paths := make([]string, 0, len(l.ingested))
	for p := range l.ingested {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var out []unreadSource
	for _, p := range paths {
		if live != nil && !live(p) {
			continue
		}
		n := l.ingested[p]
		var missing []int
		for c := 1; c <= n; c++ {
			if !l.read[p][c] {
				missing = append(missing, c)
			}
		}
		if len(missing) > 0 {
			out = append(out, unreadSource{Path: p, Chunks: missing, N: n})
		}
	}
	return out
}

// closeVerdict decides whether stage.close refuses, atomically with the
// refusal it remembers, and returns the unread sources either way (the
// caller names them in the refusal or in the summary line).
//
// The rule: a close is refused when the unread set is non-empty AND is not
// the set the last refusal already named. So the first close over a given
// set is refused once — the model is told exactly what it skipped and can
// read it — and a second close over the same set goes through: a guard that
// could never be overridden would trap a model whose source genuinely cannot
// be read (and a turn that burns its rounds against a wall is the failure
// 040 exists to stop). Reads that change the set are a new set, so the model
// is told again what is still missing; an empty set forgets the refusal, so
// a later identical set is fresh.
//
// live is the changeset's still-live ingest_source paths. A registry
// outlives a changeset (the TUI's does, and an MCP server's), and the log
// keeps what it saw; a source whose op was dropped or committed is no longer
// part of the changeset being closed, so its unread chunks must not hold
// this close to a source nobody will write pages from.
func (l *readLog) closeVerdict(live func(path string) bool) (unread []unreadSource, refuse bool) {
	if l == nil {
		return nil, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	unread = l.unreadLocked(live)
	if len(unread) == 0 {
		l.refused = ""
		return nil, false
	}
	key := unreadClauses(unread)
	if key == l.refused {
		return unread, false
	}
	l.refused = key
	return unread, true
}

// unreadClauses renders the unread set as the refusal names it —
// "<src> chunks <a, b> of <n> unread", sources joined by "; ". It is also
// the key a refusal is remembered under: it carries the path, the chunks and
// the total, everything that makes one set differ from another.
func unreadClauses(unread []unreadSource) string {
	parts := make([]string, len(unread))
	for i, u := range unread {
		parts[i] = fmt.Sprintf("%s chunks %s of %d unread", u.Path, joinChunks(u.Chunks), u.N)
	}
	return strings.Join(parts, "; ")
}

// unreadRefusal is the IsError text of a refused stage.close (040): what was
// skipped, how to read it (in one round — the other half of 040 is fewer
// rounds), and that a repeat closes anyway. The wording is a frozen
// contract; the trace and the model both quote it.
func unreadRefusal(unread []unreadSource) string {
	return "stage.close: not every chunk of the sources ingested in this changeset was read — " +
		unreadClauses(unread) +
		". Read them with raw.get (request several chunks in one round), or call stage.close again to close anyway."
}

// unreadLine is the final line a stage.close that went through over an
// unread set appends to its summary, so the trace and the review show that
// the changeset was closed over chunks nobody read.
func unreadLine(unread []unreadSource) string {
	parts := make([]string, len(unread))
	for i, u := range unread {
		parts[i] = u.Path + " " + joinChunks(u.Chunks)
	}
	return "unread chunks: " + strings.Join(parts, "; ")
}

// joinChunks renders chunk numbers as "2, 3".
func joinChunks(chunks []int) string {
	s := make([]string, len(chunks))
	for i, c := range chunks {
		s[i] = strconv.Itoa(c)
	}
	return strings.Join(s, ", ")
}
