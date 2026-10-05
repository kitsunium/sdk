// Package profiling — hosts CanonicalName: one spelling for a function,
// whoever named it.
//
// Package profiling — hosts the captures: the process's CPU over a bounded
// window, and its live heap.
//
// Package profiling — hosts Fold: a profile's samples charged to owners, the
// costliest functions, and the flame graph, in the profile's own unit.
//
// Package profiling — hosts the goroutine view: the process's goroutines, each
// with its state, how long it has waited, its labels and its stack, parsed
// from the runtime's own dump.
//
// Package profiling — hosts GroupGoroutines: goroutines counted by what they
// work for, what they wait on and where.
//
// Package profiling — hosts the reading of goroutine labels in their two
// printed forms, and their matching onto a dump whose headers lack them.
//
// Package profiling — hosts Parse: a pprof profile's bytes, gzipped or not,
// decoded into a ProfileValue with its string, function and location tables
// resolved.
//
// Package profiling captures and reads the running process's own profiles
// (ADR 0121): its CPU over a bounded window and its live heap through
// runtime/pprof, decoded from the pprof wire format with the standard library
// alone; a fold that charges each sample to an owner the caller names and adds
// up per-owner costs, the costliest functions and a pruned flame graph; and
// its goroutines, parsed from the runtime's dump into their state, how long
// they have waited, their labels and their stacks, and grouped.
//
// # The attribution is the caller's
//
// A profile says which functions ran; what they ran FOR is the caller's
// knowledge. [Fold] takes an Attribute function: a framework reads the pprof
// label it put on the goroutine — a CPU sample carries its goroutine's labels
// — or, for the heap, whose samples carry none, looks for a function it owns on
// the stack. [CanonicalName] makes a frame's spelling meet the one a static
// analysis produced.
//
// # What is estimated
//
// A heap profile is SAMPLED — about one allocation per 512 KiB is recorded and
// scaled back up — and a CPU profile counts 100 samples a second: both say
// where the cost is with confidence and how much only approximately. A test,
// or a dashboard, should ask where the bytes are, not how many exactly.
//
// # One CPU profiler per process
//
// The runtime has one. [CaptureCPU] refuses a second capture with
// ProfilerBusy rather than queueing it, whoever started the first.
//
// # Values and codes are the core's
//
// What a capture returns — the profile, a fold, a goroutine — and every
// sentinel are declared in internal/core/observe/profiling (ADR 0160); this
// package is the engine and declares none of them.
//
// Package profiling — hosts the resolver: string indexes into strings,
// location ids into frames — each location's frames built once and shared by
// every sample that passes through it, and counted against MaxFrames when they
// are built and each time a stack copies them — and labels into maps.
//
// Package profiling — hosts the decoding of a profile's samples, locations and
// functions, and their resolution into frames once the string table is known.
//
// Package profiling — hosts the protocol-buffer wire reader the profile
// decoder stands on: varints, length-delimited fields, and the skip of a field
// the decoder does not read. Written from the encoding's specification so the
// SDK carries no protobuf dependency (the same choice as OTLP/JSON, ADR 0048).
package profiling
