// Package lint runs the fixed set of 11 vault health checks against a
// vault, index and graph, and reports findings the agent may read but never
// compute itself. It will hold lint.go, context.go and the 11 check_*.go
// files, one per check.
package lint
