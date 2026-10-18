package serverengine

import (
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
)

// The process sample is the SDK's reading of this process.
func TestTheProcessSample(t *testing.T) {
	runtime.GC()
	p := sampleProcess()
	if p.PID != os.Getpid() || p.Goroutines < 1 || p.HeapBytes == 0 || p.MemoryBytes < p.HeapBytes || p.GCCycles == 0 || p.LastGC == nil {
		t.Errorf("sample %+v", p)
	}
	// The uptime and a pause's p99 may read 0 where the timer is coarse
	// (Windows: ~0.5-15 ms) or every pause is below the histogram's first
	// bucket: only a negative one is wrong.
	if p.UptimeMs < 0 || p.At.IsZero() || p.At.Location() != time.UTC || p.GCPauseP99Ms < 0 {
		t.Errorf("sample %+v", p)
	}
}

// Enable plugs the sampler in beside the HTTP engine: a serving app's runtime
// graph carries the process (ADR 0172).
func TestEnablePlugsTheSampler(t *testing.T) {
	Enable()
	sample := plug.SampleProcess.Load()
	if sample == nil {
		t.Fatal("no sampler after Enable")
	}
	if p := (*sample)(); p == nil || p.PID != os.Getpid() {
		t.Errorf("the plugged sampler: %+v", p)
	}
}
