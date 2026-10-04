// Package level — compile-time interface conformance assertions.
//
// Package level — Leveler port (a live, mutable severity threshold).
package level

// _ proves *Var satisfies the Leveler port at compile time; a drift in either
// signature breaks the build here rather than at a distant call site.
var _ Leveler = (*Var)(nil)
