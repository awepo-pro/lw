// filter.go is the log screen's filter vocabulary: the five stage.Filter
// queries `f` cycles through (pinned item 4, backbone §5.7), their names,
// and the Note each shows on the Events panel (s2-screens.md T10: the
// filter shown as the panel's Note — "all", or the filter label).
package logview

import "github.com/awepo-pro/lw/internal/stage"

// filterKind is one of the five stage.Filter queries `f` cycles through
// (pinned item 4), in this fixed order.
type filterKind int

const (
	filterAll filterKind = iota
	filterAccepted
	filterRejected
	filterAgent
	filterHuman
	filterCount // sentinel: the number of states, for cycling with %
)

// label is filterKind's name, shown as the Events panel's Note (T10).
func (k filterKind) label() string {
	switch k {
	case filterAccepted:
		return "accepted"
	case filterRejected:
		return "rejected"
	case filterAgent:
		return "agent"
	case filterHuman:
		return "human"
	default:
		return "all"
	}
}

// query is the stage.Filter k selects with (pinned item 4, backbone §5.7).
// EvOpAccepted has no writer in v0.1 (backbone §5.7's own contract), so
// "accepted" resolves in practice to every commit_end; it stays in the
// Kinds list because a future writer of op_accepted must not need this
// screen edited to pick it up.
func (k filterKind) query() stage.Filter {
	switch k {
	case filterAccepted:
		return stage.Filter{Kinds: []stage.EventKind{stage.EvOpAccepted, stage.EvCommitEnd}}
	case filterRejected:
		return stage.Filter{Kinds: []stage.EventKind{stage.EvOpDropped, stage.EvHunkDropped, stage.EvChangesetRejected}}
	case filterAgent:
		return stage.Filter{ActorKind: "agent"}
	case filterHuman:
		return stage.Filter{ActorKind: "human"}
	default:
		return stage.Filter{} // "all": a zero Filter matches every event (backbone §5.7)
	}
}
