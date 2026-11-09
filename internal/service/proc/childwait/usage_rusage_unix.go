//go:build unix && !illumos && !solaris

package childwait

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// UsageOf reads the page-fault, block-I/O and context-switch counters of a
// wait4 rusage into ExitValue.Usage. Linux, darwin and the BSDs fill them; a
// counter that reads negative is reported as zero, not wrapped.
func UsageOf(ru *syscall.Rusage) *coreproc.UsageValue {
	//: the six counters wait4 filled beside CPU time and peak RSS.
	return &coreproc.UsageValue{
		MinorFaults:      counter(ru.Minflt),
		MajorFaults:      counter(ru.Majflt),
		InBlock:          counter(ru.Inblock),
		OutBlock:         counter(ru.Oublock),
		VoluntaryCtxSw:   counter(ru.Nvcsw),
		InvoluntaryCtxSw: counter(ru.Nivcsw),
	}
}

// counter widens one rusage counter — int64 on a 64-bit target, int32 on
// 386, arm and mips — to the port's uint64. A counter is never negative; one
// that reads so is reported as zero rather than wrapped to a huge count.
func counter[T int32 | int64](v T) uint64 {
	//: a kernel counter is a count; a negative one is not a reading.
	if v < 0 {
		//: report nothing rather than a wrapped value.
		return 0
	}
	//: the non-negative count, widened.
	return uint64(v)
}
