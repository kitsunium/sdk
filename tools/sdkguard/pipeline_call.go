// Package main — the log/slog entry points SDK001 watches.
package main

// pipelineCall names a log/slog entry point that builds a pipeline, with the
// sentence explaining what it costs.
//
// A slice rather than a map: the order is the order findings are reported in,
// and a map would make that depend on the runtime's iteration order.
type pipelineCall struct {
	// name is the function's identifier inside log/slog.
	name string
	// why states, in one sentence, what the call creates.
	why string
}
