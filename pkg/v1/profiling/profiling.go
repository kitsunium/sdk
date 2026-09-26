//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/profiling .

// Package profiling captures and reads the running process's own profiles
// (ADR 0121): its CPU over a bounded window, its live heap, and its
// goroutines — decoded, folded onto owners you name, and grouped.
//
//	p, err := profiling.CaptureCPU(ctx, 10*time.Second)       // one CPU profiler per process
//	f, err := profiling.Fold(p, profiling.FoldConfig{
//	    Attribute: func(s profiling.Sample) string {           // whose work was it?
//	        if v := s.Labels["component"]; len(v) > 0 {         // set with pprof.Do
//	            return v[0]
//	        }
//	        return ""
//	    },
//	})
//	for _, o := range f.Owners { fmt.Println(o.Owner, time.Duration(o.Value)) }
//
//	gs, err := profiling.Goroutines()
//	for _, g := range profiling.GroupGoroutines(gs, profiling.GroupConfig{Labels: []string{"component"}}) {
//	    fmt.Println(g.Count, g.State, g.Top)                    // 3 select app.(*Pool).wait
//	}
//
// # Captures
//
// [CaptureCPU] samples the CPU for a window of at most [MaxCPUWindow]. The
// runtime has ONE CPU profiler: while it runs — started by another capture or
// by anyone's pprof.StartCPUProfile, net/http/pprof's included — a capture is
// refused with [ProfilerBusy], not queued. A context that ends first returns
// [CaptureCanceled] and no profile. [CaptureHeap] collects garbage, then
// reads the live heap.
//
// # Profiles are decoded with the standard library
//
// [Parse] reads the pprof format — gzipped or not — written from
// profile.proto, so the SDK carries no protobuf dependency. It is bounded by
// [MaxProfileBytes] and refuses anything malformed with [ProfileMalformed]
// rather than guessing. A [Profile] is plain data: sample types, samples with
// their stacks — innermost frame first, an inlined call a frame of its own —
// their values and their labels.
//
// # The attribution is yours
//
// [Fold] adds up one sample type, charging each sample to the owner your
// Attribute function names: a CPU sample carries its goroutine's pprof labels;
// a heap sample carries none, so charge it by what is on its stack.
// [CanonicalName] spells a function the way go/types does not, so a frame
// meets the function a static analysis found. The result is in the sample
// type's own unit — nanoseconds, bytes — and adds up exactly: every owner's
// Value plus Unattributed is Total. The flame graph is pruned below
// FlameMinShare of the total.
//
// # What is estimated
//
// A heap profile is SAMPLED — about one allocation per 512 KiB is recorded and
// scaled back up — so eight megabytes held may read as six or ten; a CPU
// profile counts about a hundred samples a second. Both say where the cost is
// with confidence and how much only approximately: ask where the bytes are,
// not how many exactly.
//
// # Goroutines
//
// [Goroutines] reads the runtime's own dump: each goroutine's state — the
// runtime's word for what it waits on — how long it has been blocked, whether
// it is locked to its thread, its labels, its stack and the go statement that
// started it. [ParseGoroutines] reads a dump from anywhere — runtime.Stack, a
// crash, a SIGQUIT — and never fails. [GroupGoroutines] counts them by labels,
// state and top frame: the innermost frame that is not the runtime's
// machinery.
package profiling

import (
	"context"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcprof "github.com/kitsunium/sdk/internal/service/profiling"
)

// MaxCPUWindow is the longest window CaptureCPU samples.
const MaxCPUWindow time.Duration = svcprof.MaxCPUWindow

// MaxProfileBytes bounds what Parse reads, compressed and inflated alike.
const MaxProfileBytes int = svcprof.MaxProfileBytes

// FlameRoot is the name of a flame graph's root frame.
const FlameRoot string = svcprof.FlameRoot

// Fold's defaults, taken when a FoldConfig field is not positive.
const (
	DefaultTopFunctions  int     = svcprof.DefaultTopFunctions
	DefaultTopPerOwner   int     = svcprof.DefaultTopPerOwner
	DefaultFlameMinShare float64 = svcprof.DefaultFlameMinShare
	DefaultFlameMaxDepth int     = svcprof.DefaultFlameMaxDepth
)

// The codes a caller branches on with errs.HasCode.
const (
	CodeWindowInvalid     errs.Code = svcprof.CodeWindowInvalid
	CodeProfilerBusy      errs.Code = svcprof.CodeProfilerBusy
	CodeCaptureCanceled   errs.Code = svcprof.CodeCaptureCanceled
	CodeCaptureFailed     errs.Code = svcprof.CodeCaptureFailed
	CodeProfileMalformed  errs.Code = svcprof.CodeProfileMalformed
	CodeProfileTooLarge   errs.Code = svcprof.CodeProfileTooLarge
	CodeSampleTypeMissing errs.Code = svcprof.CodeSampleTypeMissing
)

var (
	// WindowInvalid: a CPU window not positive or over MaxCPUWindow (400).
	WindowInvalid = svcprof.WindowInvalid
	// ProfilerBusy: the process's one CPU profiler is running (409).
	ProfilerBusy = svcprof.ProfilerBusy
	// CaptureCanceled: the context ended before the window (503).
	CaptureCanceled = svcprof.CaptureCanceled
	// CaptureFailed: runtime/pprof could not write a profile.
	CaptureFailed = svcprof.CaptureFailed
	// ProfileMalformed: the bytes are not a well-formed pprof profile.
	ProfileMalformed = svcprof.ProfileMalformed
	// ProfileTooLarge: the profile exceeds MaxProfileBytes.
	ProfileTooLarge = svcprof.ProfileTooLarge
	// SampleTypeMissing: the profile does not measure that sample type.
	SampleTypeMissing = svcprof.SampleTypeMissing
)

// Profile is a decoded pprof profile.
type Profile = svcprof.ProfileValue

// SampleType names what a value measures and its unit.
type SampleType = svcprof.SampleTypeValue

// Sample is one sample: a stack, a value per sample type, and labels.
type Sample = svcprof.SampleValue

// Frame is one frame of a stack.
type Frame = svcprof.FrameValue

// FoldConfig says how Fold reads a profile; every zero field has a default.
type FoldConfig = svcprof.FoldConfig

// Folded is a profile folded, in the sample type's own unit.
type Folded = svcprof.FoldedValue

// OwnerCost is what one owner cost.
type OwnerCost = svcprof.OwnerCostValue

// FunctionCost is what one function cost.
type FunctionCost = svcprof.FunctionCostValue

// FlameNode is one frame of a flame graph.
type FlameNode = svcprof.FlameNodeValue

// Goroutine is one goroutine, as the runtime's dump describes it.
type Goroutine = svcprof.GoroutineValue

// GroupConfig says how GroupGoroutines groups.
type GroupConfig = svcprof.GroupConfig

// GoroutineGroup is goroutines sharing their labels, state and top frame.
type GoroutineGroup = svcprof.GoroutineGroupValue

// CaptureCPU samples the process's CPU for window and returns the profile.
func CaptureCPU(ctx context.Context, window time.Duration) (*Profile, error) {
	//: delegate verbatim to the service.
	return svcprof.CaptureCPU(ctx, window)
}

// CaptureHeap returns the live heap, after a garbage collection.
func CaptureHeap() (*Profile, error) {
	//: delegate verbatim to the service.
	return svcprof.CaptureHeap()
}

// Parse decodes a pprof profile, gzipped or not.
func Parse(data []byte) (*Profile, error) {
	//: delegate verbatim to the service.
	return svcprof.Parse(data)
}

// Fold charges every sample of p to an owner and adds it all up.
func Fold(p *Profile, cfg FoldConfig) (Folded, error) {
	//: delegate verbatim to the service.
	return svcprof.Fold(p, cfg)
}

// Goroutines returns every goroutine of the process.
func Goroutines() ([]Goroutine, error) {
	//: delegate verbatim to the service.
	return svcprof.Goroutines()
}

// ParseGoroutines reads a goroutine dump; it never fails.
func ParseGoroutines(dump []byte) []Goroutine {
	//: delegate verbatim to the service.
	return svcprof.ParseGoroutines(dump)
}

// GroupGoroutines groups goroutines by labels, state and top frame, largest
// first.
func GroupGoroutines(gs []Goroutine, cfg GroupConfig) []GoroutineGroup {
	//: delegate verbatim to the service.
	return svcprof.GroupGoroutines(gs, cfg)
}

// CanonicalName spells a function the same way whoever named it.
func CanonicalName(name string) string {
	//: delegate verbatim to the service.
	return svcprof.CanonicalName(name)
}
