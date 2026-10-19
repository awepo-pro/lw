package tools

import (
	"encoding/json"
	"strings"
)

// LiveSource is a raw source the open changeset stages (a live OpIngestSource)
// or recompiles (a 055 target): its vault path and the chunk count raw.get
// serves for it. cmd/lw derives the list from the changeset and the vault; this
// package takes it as given, so Replay never touches the engine.
type LiveSource struct {
	Path   string
	Chunks int
}

// PastCall is one tool record from the changeset's session, oldest first: the
// tool's canonical name, the arguments as they were sent, and the result's
// text. It mirrors the fields of an agent.Record that matter here — internal/
// agent imports this package, so cmd/lw does the conversion.
type PastCall struct{ Tool, Args, Result string }

// Replay rebuilds, on a fresh registry, the in-process state a long-lived one
// would hold after the calls in past, so a registry that serves one tool call
// (063: `lw tool`, one process per call) enforces the same 040 and 043 guards
// as the in-process ingest turn did. Call it once, before the one Call.
//
// 040 read log. Every live source is noted at its true chunk count. Then, in
// order: a raw.get that served a chunk (RawGetServed) marks (source, chunk)
// read — chunk from its arguments, 0 meaning 1, as the handler treats it; a
// stage.close that was refused (UnreadRefusalKey) restores the refusal key; a
// stage.close that went through over an empty unread set clears it, and one
// that went through over a non-empty set leaves it, as closeVerdict does. A
// stage.ingest_source record that staged a path in live starts that path over
// (reads and refusal forgotten, count taken from live), because a path
// ingested again is a new body; a record naming a path not in live is ignored,
// so a source whose op was dropped does not come back from its old record. A
// refused raw.get is not a read.
//
// 043 shrink arms. In order, per (path, section) from a stage.patch_page
// record's arguments: a shrink refusal (ShrinkRefusal, either form) arms the
// key, a patch that staged disarms it, any other refusal changes nothing — the
// rules the tool itself applies. This is an explicit amendment of 043's "an
// arm must not survive a restart", scoped to Replay (A-063-1): without it an
// allow_shrink call from a fresh process could never be confirmed. A registry
// that never calls Replay behaves as 043 specified.
//
// Replay never calls the engine and writes nothing; it only fills this
// registry's own in-memory state. A record it cannot read — arguments that are
// not JSON, a tool it does not track, a result of no known shape — is skipped,
// never an error: the history is the agent's, and it may hold anything. Live
// sources are held live at every later close (readLog.hold) so a recompile
// target, which no ingest_source op names, is not pruned as dropped.
func (r *Registry) Replay(live []LiveSource, past []PastCall) {
	reads := r.deps.reads
	chunks := make(map[string]int, len(live))
	for _, s := range live {
		reads.noteIngest(s.Path, s.Chunks)
		reads.hold(s.Path)
		chunks[s.Path] = s.Chunks
	}
	for _, c := range past {
		switch recordedToolName(c.Tool) {
		case "stage.ingest_source":
			r.replayIngest(c, chunks)
		case "raw.get":
			r.replayRawGet(c)
		case "stage.close":
			r.replayClose(c)
		case "stage.patch_page":
			r.replayPatch(c)
		}
	}
}

// recordedToolName is the canonical dotted name of a recorded tool. A session
// record carries the canonical spelling (the loop writes it), so that is taken
// as it stands; a name without a dot is a wire spelling and goes through
// CanonicalName. CanonicalName alone is not safe on a canonical name: it maps
// every "_" to ".", and "stage.patch_page" comes back as "stage.patch.page".
func recordedToolName(name string) string {
	if strings.Contains(name, ".") {
		return name
	}
	return CanonicalName(name)
}

// replayIngest starts over the read log of a source a recorded
// stage.ingest_source staged again (C-063-5). A path ingested again is a new
// body — the changeset dropped the old op and staged another at the same path —
// so noteIngest forgets the reads, and the refusal, made of the old one
// (A-040-4); without this the reads that came before the re-ingest in the
// history would count toward a body they never covered. Only a path in live is
// noted, and at live's count: live already holds every source the changeset
// stages, so a record whose op was dropped and never staged again names a path
// that is not in it and stays gone.
func (r *Registry) replayIngest(c PastCall, live map[string]int) {
	path, ok := stagedSourcePath(c.Result)
	if !ok {
		return
	}
	if n, held := live[path]; held {
		r.deps.reads.noteIngest(path, n)
	}
}

// replayRawGet marks the chunk a recorded raw.get served as read.
func (r *Registry) replayRawGet(c PastCall) {
	if !RawGetServed(c.Result) {
		return
	}
	var a rawGetArgs
	if err := decodeArgs(json.RawMessage(c.Args), &a); err != nil {
		return
	}
	chunk := a.Chunk
	if chunk == 0 {
		chunk = 1
	}
	r.deps.reads.noteRead(strings.TrimSpace(a.Source), chunk)
}

// replayClose restores what a recorded stage.close left in the read log's
// refusal memory.
func (r *Registry) replayClose(c PastCall) {
	if key, ok := UnreadRefusalKey(c.Result); ok {
		r.deps.reads.setRefused(key)
		return
	}
	if through, overUnread := closeWentThrough(c.Result); through && !overUnread {
		r.deps.reads.setRefused("")
	}
}

// replayPatch applies a recorded stage.patch_page to the shrink arms.
func (r *Registry) replayPatch(c PastCall) {
	arms := r.deps.arms
	if arms == nil {
		return
	}
	var a stagePatchPageArgs
	if err := decodeArgs(json.RawMessage(c.Args), &a); err != nil {
		return
	}
	switch key := shrinkArmKey(a.Path, a.Section); {
	case ShrinkRefusal(c.Result):
		arms.arm(key)
	case patchStaged(c.Result):
		arms.disarm(key)
	}
}
