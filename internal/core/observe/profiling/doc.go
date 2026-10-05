// Package profiling — range 0.3.89.*, allocated to the engine (ADR 0121
// service/observe/profiling block) and declared here since ADR 0160: the
// engine returns these and declares none.
//
// Package profiling — declares the sentinel *errs.Error outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// No refusal quotes the profile or the dump it refused: both describe the
// process's code, its files and what its goroutines were doing.
//
// Package profiling — a profile folded onto owners: what each owner and each
// function cost, and the flame graph, in the sample type's own unit. Folding
// is the engine's, internal/service/observe/profiling.Fold.
//
// Package profiling — the goroutine view: one goroutine as the runtime's dump
// describes it, and goroutines grouped. Reading the dump and grouping are the
// engine's, internal/service/observe/profiling.
//
// Package profiling — the values a decoded profile is made of: the profile,
// its samples, their stacks' frames and the sample types. Decoding them from
// the pprof format is the engine's, internal/service/observe/profiling.
//
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
