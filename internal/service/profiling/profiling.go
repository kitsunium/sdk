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
// [ProfilerBusy] rather than queueing it, whoever started the first.
package profiling

import "time"

// MaxCPUWindow is the longest window CaptureCPU samples. A longer profile is
// not more precise, only larger and longer to hold the process's one CPU
// profiler: take several.
const MaxCPUWindow time.Duration = 5 * time.Minute

// MaxProfileBytes bounds what Parse reads: the input, and the input once
// decompressed. A CPU profile of the longest window CaptureCPU allows weighs a
// few megabytes; a gzip stream inflating past this is not a profile anyone
// should hold in memory.
const MaxProfileBytes int = 64 << 20

// MaxFrames bounds the frames Parse builds: each location's, once, and every
// sample's stack, which copies them. The bytes a profile weighs do not bound
// them: a sample names each location by its id, and a location stands for as
// many frames as it has inlined lines, so a few megabytes naming one deep
// location again and again would expand into gigabytes of frames. Four
// million frames — some 230 MB of them — hold a profile of tens of thousands
// of distinct stacks, as deep as the runtime records them.
const MaxFrames int = 1 << 22
