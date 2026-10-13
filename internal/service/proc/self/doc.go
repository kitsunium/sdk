// Package self is what the running process is doing right now: the Go
// runtime's own metrics and the kernel's account of the CPU time used.
//
// The rest of the proc domain acts on OTHER processes — it spawns, signals,
// reaps and limits children. This package only reads, and only the process it
// runs in. What the binary was built from is internal/service/proc/buildinfo's,
// a package of its own so that reading a build never imports runtime/metrics
// (ADR 0172). Nothing here can fail in a way a caller could act on, so nothing
// returns an error: an absent answer is a zero field or a false, and the doc
// comment of each field says which.
//
// Package self — the CPU time the process used, where the kernel's count is
// not at hand.
//
// Package self — the CPU time the process used, as the kernel counts it.
//
// Package self — the running process's runtime state, read from the Go
// runtime's own metrics and the kernel's CPU accounting.
package self
