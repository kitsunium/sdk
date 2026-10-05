// Package self is what the running process can say about itself: what it was
// built from (build.go) and what it is doing right now (stats.go).
//
// The rest of the proc domain acts on OTHER processes — it spawns, signals,
// reaps and limits children. This package only reads, and only the process it
// runs in: the build information the Go toolchain embedded in the binary, the
// Go runtime's own metrics, and the kernel's account of the CPU time used.
// Nothing here can fail in a way a caller could act on, so nothing returns an
// error: an absent answer is a zero field or a false, and the doc comment of
// each field says which.
//
// Package self — the CPU time the process used, where the kernel's count is
// not at hand.
//
// Package self — the CPU time the process used, as the kernel counts it.
//
// Package self — the running process's runtime state, read from the Go
// runtime's own metrics and the kernel's CPU accounting.
package self
