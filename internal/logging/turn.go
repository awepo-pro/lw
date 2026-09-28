package logging

import "context"

// turnKey is the context key for the agent turn id (038). Unexported and of
// its own type, so no other package can collide with it.
type turnKey struct{}

// WithTurn returns ctx carrying the agent turn id (038, turn trace). The
// agent mints the id once per turn; every lw.log record written through a
// *Context slog call on that ctx carries it as turn=<id>, which is what
// joins lw.log to the turn's session records and its trace. The key lives
// here, in the leaf logging package, so the llm and agent packages (and
// their tests, which import logging) can read it without an import cycle.
func WithTurn(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, turnKey{}, id)
}

// TurnFrom returns the turn id WithTurn stored in ctx, or "" when there is
// none (a nil ctx included).
func TurnFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(turnKey{}).(string)
	return id
}
