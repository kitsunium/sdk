// Package profiling declares the values of the profiling domain (ADR 0121)
// and its codes: a decoded profile — ProfileValue, its samples, their frames
// and the sample types —, a profile folded onto owners — FoldedValue and its
// parts —, and a goroutine as the runtime's dump describes it, alone or
// grouped.
//
// The engine — capturing the CPU and the heap, decoding the pprof format,
// folding, reading a goroutine dump — is internal/service/observe/profiling,
// and its bounds and defaults are its own. There is no port here: there is
// one engine, the runtime's profilers, and the attribution a fold applies is a
// function the caller passes, not a contract (ADR 0121 §D1). ADR 0160 §1 gave
// the domain this package for its values and codes, and a port only where a
// second implementation or a test double needs one — nothing does.
package profiling
