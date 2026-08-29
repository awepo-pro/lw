// Package agent is the curator loop: dispatching tool calls returned by the
// LLM client, managing sessions and their append-only record log, building
// bounded context for each turn, and compacting history that grows past
// budget. It will hold agent.go, loop.go, session.go, context.go,
// compact.go and prompt.go.
package agent
