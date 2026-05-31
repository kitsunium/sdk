// Package level — compile-time interface conformance assertions.
package level

// _ proves *Var satisfies the Leveler port at compile time; a drift in either
// signature breaks the build here rather than at a distant call site.
var _ Leveler = (*Var)(nil)
