// Package ask is the chat screen for talking to the curator agent, streaming
// its replies and tool activity into the pane. ask.go holds the ui.Pane
// itself (Model, its input box and scrollback rendering); stream.go holds
// the agent.Event pump (StreamMsg, Listen, EventMsg, StreamClosedMsg) and
// the scrollback state each event kind updates.
package ask
