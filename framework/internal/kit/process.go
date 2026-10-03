// Package kit — the process sample: what the running process uses.
package kit

import (
	"runtime"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/proc/process"
)

// sampleProcess is the SDK's snapshot of the running process (process.Self)
// as the model says it. Latency percentiles come from the runtime's
// histograms, which count since the process started. The time is the wall
// clock's: a process sample is about the machine, not the product's clock.
func sampleProcess() *model.Process {
	st := process.Self()
	p := &model.Process{
		PID:               st.PID,
		Go:                runtime.Version(),
		OS:                runtime.GOOS,
		Arch:              runtime.GOARCH,
		MaxProcs:          st.MaxProcs,
		CPUs:              st.CPUs,
		Goroutines:        st.Goroutines,
		HeapBytes:         st.HeapBytes,
		HeapObjects:       st.HeapObjects,
		HeapGoalBytes:     st.HeapGoalBytes,
		MemoryBytes:       st.MemoryBytes,
		TotalAllocBytes:   st.TotalAllocBytes,
		GCCycles:          st.GCCycles,
		GCPauseP99Ms:      millis(st.GCPauses.Quantile(0.99)),
		SchedLatencyP99Ms: millis(st.SchedLatencies.Quantile(0.99)),
		CPUSeconds:        round2(st.CPUTime.Seconds()),
		UptimeMs:          millis(st.Uptime),
		At:                st.At.UTC(),
	}
	if !st.LastGC.IsZero() {
		p.LastGC = new(st.LastGC.UTC())
	}
	return p
}

// millis is d in milliseconds, to two decimals.
func millis(d time.Duration) float64 { return round2(float64(d) / float64(time.Millisecond)) }
