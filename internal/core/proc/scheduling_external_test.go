package proc_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Test_SchedPolicy_String pins the names a log line shows, and the numeric
// form of a value no policy has (4 is SCHED_ISO, never merged).
func Test_SchedPolicy_String(t *testing.T) {
	t.Parallel()
	tests := map[coreproc.SchedPolicy]string{
		coreproc.SchedOther: "other", coreproc.SchedFIFO: "fifo", coreproc.SchedRR: "rr",
		coreproc.SchedBatch: "batch", coreproc.SchedIdle: "idle", coreproc.SchedPolicy(4): "schedpolicy(4)",
	}
	for p, want := range tests {
		if got := p.String(); got != want {
			t.Errorf("SchedPolicy(%d).String() = %q, want %q", int(p), got, want)
		}
	}
}

// Test_IOClass_String pins the class names and the numeric fallback.
func Test_IOClass_String(t *testing.T) {
	t.Parallel()
	tests := map[coreproc.IOClass]string{
		coreproc.IOClassNone: "none", coreproc.IOClassRealTime: "realtime",
		coreproc.IOClassBestEffort: "besteffort", coreproc.IOClassIdle: "idle", coreproc.IOClass(9): "ioclass(9)",
	}
	for c, want := range tests {
		if got := c.String(); got != want {
			t.Errorf("IOClass(%d).String() = %q, want %q", int(c), got, want)
		}
	}
}
