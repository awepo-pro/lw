// Package ask is the chat screen for talking to the curator agent, streaming
// its replies and tool activity into the pane. ask.go holds the ui.Pane
// itself (Model, construction, key handling and the D10 suggested prompts);
// view.go is the pane frame (transcript + message panels);
// transcript.go draws the transcript's contents (empty state, conversation
// turn shape, tool rows); inline.go is the inline renderer and its
// styled-cell wrap; state.go folds agent events into the scrollback;
// stream.go is the event pump and turn start; session.go is the
// session/changeset lifecycle; file.go is the file key (conversation
// carry-over, the recorded answer, ctrl+s, 009).
package ask
