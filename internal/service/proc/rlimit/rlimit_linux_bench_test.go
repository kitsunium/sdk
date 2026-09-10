//go:build linux

// Package rlimit — the syscall half of the measurement. Apply is a table lookup
// plus one kernel call, so the only useful question is which of the two the
// caller is paying for, and whether the two kernel entry points the package
// routes between (setrlimit for self, prlimit64 for a foreign pid) differ enough
// to matter.
package rlimit_test

import (
	"os"
	osexec "os/exec"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/service/proc/rlimit"
)

// currentNoFile reads this process's live RLIMIT_NOFILE so the benchmarks can
// re-apply the value the process already has. Applying the current ceiling is a
// real setrlimit(2)/prlimit64(2) that changes nothing observable — the only way
// to measure the syscall thousands of times without walking a limit downwards.
func currentNoFile(b *testing.B) map[coreproc.Resource]coreproc.LimitValue {
	b.Helper()
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		b.Skipf("getrlimit(RLIMIT_NOFILE): %v", err)
	}
	// No conversion: this file is //go:build linux, where syscall.Rlimit's fields
	// are already uint64 (they are int64 only on FreeBSD/DragonFly).
	return map[coreproc.Resource]coreproc.LimitValue{
		coreproc.ResourceNoFile: {Soft: rl.Cur, Hard: rl.Max},
	}
}

// BenchmarkApply_Self is the setrlimit(2) path: pid 0 targets the caller.
func BenchmarkApply_Self(b *testing.B) {
	limits := currentNoFile(b)
	if err := rlimit.Apply(0, limits); err != nil {
		b.Skipf("Apply(self): %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = rlimit.Apply(0, limits)
	}
}

// BenchmarkApply_ForeignPID is the prlimit64(2) path. The man page grants it to
// an unprivileged caller whose uids match the target's, so this benchmark owns
// its target: it spawns a child, applies the child's own current NOFILE ceiling
// to it, and kills it afterwards. Where the kernel refuses (no CAP_SYS_RESOURCE
// and a mismatched identity, or a hardened container) the benchmark skips rather
// than publishing the cost of an EPERM.
func BenchmarkApply_ForeignPID(b *testing.B) {
	limits := currentNoFile(b)
	pid := startForeignChild(b)
	if err := rlimit.Apply(pid, limits); err != nil {
		b.Skipf("prlimit64 against an owned child is refused here: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = rlimit.Apply(pid, limits)
	}
}

// BenchmarkApply_Unmapped is the refusal: an unmapped Resource returns the bare
// sentinel before any syscall, which is what makes the syscall rows above
// attributable.
func BenchmarkApply_Unmapped(b *testing.B) {
	limits := map[coreproc.Resource]coreproc.LimitValue{
		coreproc.Resource(0): {Soft: 1, Hard: 1},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = rlimit.Apply(0, limits)
	}
}

// startForeignChild spawns a long-lived child owned by this process and
// registers its teardown, so the prlimit64 row addresses a real live pid.
func startForeignChild(b *testing.B) int {
	b.Helper()
	const sleeper string = "/bin/sleep"
	if _, err := os.Stat(sleeper); err != nil {
		b.Skipf("%s is not available on this host: %v", sleeper, err)
	}
	cmd := osexec.Command(sleeper, "3600")
	if err := cmd.Start(); err != nil {
		b.Skipf("cannot spawn %s: %v", sleeper, err)
	}
	b.Cleanup(func() {
		if err := cmd.Process.Kill(); err != nil {
			b.Logf("kill: %v", err)
		}
		if err := cmd.Wait(); err != nil {
			b.Logf("wait: %v", err)
		}
	})
	return cmd.Process.Pid
}
