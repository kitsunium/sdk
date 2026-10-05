// Package level defines severity levels for log records. It is a core/observe/logger
// sub-package (logger-domain vocabulary, so not kernel) and remains stdlib-only:
// no dependency beyond the Go standard library, and specifically never log/slog.
//
// Package level — compile-time interface conformance assertions.
//
// Package level — Leveler port (a live, mutable severity threshold).
//
// Package level — ParseLevel, the inverse of a lowercased Level.String.
//
// Package level — range 0.2.17.* (ADR 0006 core/observe/logger/level block).
//
// Package level — Var, the atomic Level holder behind the Leveler port.
package level
