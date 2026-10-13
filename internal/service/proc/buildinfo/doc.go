// Package buildinfo is what the running binary was built from, read from the
// build information the Go toolchain embedded in it.
//
// It only reads, and only the binary it runs in — or a debug.BuildInfo handed
// to ParseBuild. It is a package of its own, apart from
// internal/service/proc/self's runtime metrics, so that a program which only
// asks what it was built from never imports runtime/metrics, whose
// initialisation every process importing it pays at start (ADR 0172). Nothing
// here can fail in a way a caller could act on, so nothing returns an error: a
// binary built without module support reads as false.
package buildinfo
