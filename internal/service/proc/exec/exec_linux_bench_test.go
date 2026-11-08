//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package exec

import (
	"os"
	"sync/atomic"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// BenchmarkHandle_SignalHandle_Live is #928's row beside Handle_Signal_Live:
// the same SIGCONT, sent through the child's pidfd with pidfd_send_signal
// rather than through os.Process.
func BenchmarkHandle_SignalHandle_Live(b *testing.B) {
	p := startSleeper(b)
	ph := p.(coreproc.ProcessHandle)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = ph.SignalHandle(coreproc.Signal(syscall.SIGCONT))
	}
	b.StopTimer()
	stopSleeper(b, p)
}

// BenchmarkSeal_CloseRange is #1089's cost on a kernel with
// close_range(CLOSE_RANGE_CLOEXEC): one syscall per spawn.
func BenchmarkSeal_CloseRange(b *testing.B) {
	var missing atomic.Bool
	b.ReportAllocs()
	for b.Loop() {
		errSink = sealWith(markRangeCloexec, &missing)
	}
	if missing.Load() {
		b.Skip("this kernel has no close_range with CLOSE_RANGE_CLOEXEC")
	}
}

// BenchmarkSeal_Walk is the same seal on a kernel without it: the walk of
// /proc/self/fd, one fcntl per open descriptor.
func BenchmarkSeal_Walk(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		errSink = sealByWalk(fdDir)
	}
	_ = os.Getpid()
}

// BenchmarkStart_BinTrue_CgroupFD is #929's row beside Start_BinTrue_Inherit and
// Start_BinTrue_Trampoline: the same /bin/true launch for a Spec that names a
// CgroupPath and nothing else, which the child is cloned into — clone3 with
// CLONE_INTO_CGROUP, one open of the group's directory — instead of re-execing
// the supervisor to write its pid into cgroup.procs. The criterion is that it is
// within 1.2x of Inherit. It needs a cgroup delegated to the benchmarking user
// (a subtree of this process's own group), and skips without one or on a kernel
// below 5.7, so no row is ever a trampoline's in disguise.
func BenchmarkStart_BinTrue_CgroupFD(b *testing.B) {
	requireBinary(b, benchTrue)
	if !cloneIntoCgroupProbe() {
		b.Skip("kernel below 5.7: the trampoline places the child")
	}
	spec := coreproc.Spec{Path: benchTrue, Args: []string{benchTrue}, Setpgid: true, CgroupPath: delegatedSubtree(b)}
	if needsTrampoline(spec) {
		b.Fatal("a CgroupPath alone still routes through the trampoline")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		p, err := Start(b.Context(), spec)
		if err != nil {
			b.Fatalf("Start: %v", err)
		}
		exit, wErr := p.Wait()
		exitSink, errSink = exit, wErr
	}
}
