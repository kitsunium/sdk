//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/observe/profiling .

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
// refused with ProfilerBusy, not queued. A context that ends first returns
// CaptureCanceled and no profile. [CaptureHeap] collects garbage, then
// reads the live heap.
//
// # Profiles are decoded with the standard library
//
// [Parse] reads the pprof format — gzipped or not — written from
// profile.proto, so the SDK carries no protobuf dependency. It is bounded by
// [MaxProfileBytes] in what it reads and by [MaxFrames] in what it builds, and
// refuses anything malformed with ProfileMalformed rather than guessing. A
// gzipped profile is inflated by the SDK's own gzip transform scheme, over
// compress/gzip: importing this package links it, which registers "gzip",
// "flate" and "zlib" under the transform registry, as importing the codec
// package does. A [Profile] is plain data: sample types, samples with
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

	// registers "gzip": Parse, and so every capture, inflates the runtime's
	// gzipped profiles through the transform scheme it names.
	_ "github.com/kitsunium/sdk/internal/service/data/transform"
)
