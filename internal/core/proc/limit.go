// Package proc — the LimitValue value type: a soft/hard setrlimit pair.
package proc

// LimitInfinity is the soft/hard value meaning "no limit" (RLIM_INFINITY). Use
// it in a LimitValue to lift a resource ceiling rather than setting a number.
const LimitInfinity uint64 = ^uint64(0)

// LimitValue is an immutable soft/hard resource-limit pair for setrlimit(2). The
// soft limit is the enforced value; the hard limit is the ceiling an
// unprivileged process may raise the soft limit to.
type LimitValue struct {
	// Soft is the enforced limit; it must not exceed Hard.
	Soft uint64
	// Hard is the ceiling for Soft; raising it typically requires privilege.
	Hard uint64
}
