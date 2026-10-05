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
