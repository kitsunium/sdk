//go:build unix

package exec

import (
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/service/proc/childwait"
)

// coreBit is the WCOREDUMP flag of a wait status word: 0200 on Linux, darwin,
// the BSDs, illumos and Solaris alike, beside the signal number in the low
// seven bits.
const coreBit syscall.WaitStatus = 0x80

// rusageReported reports whether this kernel fills the rusage counters wait4
// returns: illumos and Solaris leave them at zero, as they leave ru_maxrss.
func rusageReported() bool {
	//: every unix kernel but the two that leave the counters unfilled.
	return runtime.GOOS != "illumos" && runtime.GOOS != "solaris"
}

// Test_exitValue_usage pins the six counters #1090 found dropped: page faults,
// block I/O and context switches are carried from wait4's rusage into
// ExitValue.Usage, and Known says whether the platform filled them.
func Test_exitValue_usage(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		ru   syscall.Rusage
		want coreproc.UsageValue
	}
	tests := []tc{
		{"an idle process", syscall.Rusage{}, coreproc.UsageValue{}},
		{
			"a process that faulted, read, wrote and switched",
			syscall.Rusage{Minflt: 16384, Majflt: 3, Inblock: 8, Oublock: 120, Nvcsw: 40, Nivcsw: 7},
			coreproc.UsageValue{MinorFaults: 16384, MajorFaults: 3, InBlock: 8, OutBlock: 120, VoluntaryCtxSw: 40, InvoluntaryCtxSw: 7},
		},
		{"a negative counter reads as none", syscall.Rusage{Minflt: -1}, coreproc.UsageValue{}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ru := c.ru

		out := exitValue(syscall.WaitStatus(0), &ru)

		//: a platform that does not report gives no value at all.
		if !rusageReported() {
			if out.Usage != (coreproc.UsageValue{}) {
				t.Errorf("Usage = %+v, want none, not Known", out.Usage)
			}
			return
		}
		c.want.Known = true
		if out.Usage != c.want {
			t.Errorf("Usage = %+v, want %+v", out.Usage, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_exitValue_coreDumped pins the core-dump bit of the status word: a
// signalled death that dumped core says so, one that did not — the OOM
// killer's SIGKILL, Stop's escalation — does not, and a normal exit never
// does. exitValue is the one translation for a status the handle's own wait
// collected and for one a sweep handed over, so this table covers both paths.
func Test_exitValue_coreDumped(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		ws         syscall.WaitStatus
		wantSignal bool
		wantCore   bool
	}
	tests := []tc{
		{"a SIGSEGV that dumped core", syscall.WaitStatus(syscall.SIGSEGV) | coreBit, true, true},
		{"a SIGSEGV with RLIMIT_CORE at zero", syscall.WaitStatus(syscall.SIGSEGV), true, false},
		{"a SIGKILL", syscall.WaitStatus(syscall.SIGKILL), true, false},
		{"a normal exit with status 3", syscall.WaitStatus(3 << 8), false, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := exitValue(c.ws, nil)
		if got.Signaled != c.wantSignal {
			t.Fatalf("Signaled = %v, want %v", got.Signaled, c.wantSignal)
		}
		if got.CoreDumped != c.wantCore {
			t.Errorf("CoreDumped = %v, want %v", got.CoreDumped, c.wantCore)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_exitValue_isTheSweepsExit holds the two readings of one wait4 status to
// each other: the handle's own (exitValue, over the status its wait or a claim
// collected) and the reaper's (childwait.StatusValue.Exit, which WithOnExit
// reports for an orphan). A status a sweep collected must read the same either
// way — the core-dump bit and the six rusage counters included (#1090, #1136).
func Test_exitValue_isTheSweepsExit(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		ws   syscall.WaitStatus
		ru   syscall.Rusage
	}
	busy := syscall.Rusage{
		Utime: syscall.Timeval{Sec: 1, Usec: 500}, Stime: syscall.Timeval{Usec: 250},
		Maxrss: 2048, Minflt: 16384, Majflt: 3, Inblock: 8, Oublock: 120, Nvcsw: 40, Nivcsw: 7,
	}
	tests := []tc{
		{"a SIGSEGV that dumped core", syscall.WaitStatus(syscall.SIGSEGV) | coreBit, busy},
		{"a SIGKILL", syscall.WaitStatus(syscall.SIGKILL), busy},
		{"a normal exit with status 3", syscall.WaitStatus(3 << 8), busy},
		{"an idle exit", syscall.WaitStatus(0), syscall.Rusage{}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ru := c.ru
		own := exitValue(c.ws, &ru)
		status := childwait.StatusValue{WaitStatus: c.ws, Rusage: c.ru}
		swept := status.Exit()
		//: Usage is a value: the whole ExitValue compares, counters and Known included.
		if own != swept {
			t.Errorf("own wait %+v, sweep %+v", own, swept)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// corePatternWritesAFile reports whether a core dump on this host lands as a
// file in the dying process's directory — the only setup under which a test
// can make the kernel dump core and know it succeeded. A piped pattern hands
// the image to a collector that may refuse it, and every kernel but Linux
// writes it somewhere a test does not own.
func corePatternWritesAFile() bool {
	//: only Linux names its pattern in procfs.
	if runtime.GOOS != "linux" {
		//: no pattern to read.
		return false
	}
	pattern, err := os.ReadFile("/proc/sys/kernel/core_pattern")
	//: an unreadable pattern is an unknown destination.
	if err != nil {
		//: cannot tell.
		return false
	}
	p := strings.TrimSpace(string(pattern))
	//: a relative file name lands in the child's directory, the test's TempDir.
	return p != "" && !strings.HasPrefix(p, "|") && !strings.HasPrefix(p, "/")
}

// corePatternPipes reports whether the kernel hands a core dump to a program
// (core_pattern starts with "|", as apport and systemd-coredump do): core(5)
// says RLIMIT_CORE is not enforced for a dump piped to a program, so a limit
// of zero does not stop WCOREDUMP there.
func corePatternPipes() bool {
	//: only Linux names its pattern in procfs.
	if runtime.GOOS != "linux" {
		//: no pattern to read.
		return false
	}
	pattern, err := os.ReadFile("/proc/sys/kernel/core_pattern")
	//: an unreadable pattern is an unknown destination, treated as not piping.
	if err != nil {
		//: cannot tell.
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(string(pattern)), "|")
}

// segvSpec returns a spec whose shell kills itself with SIGSEGV in dir, its
// RLIMIT_CORE set to core (soft and hard), so the kernel dumps core exactly
// when core is not zero, a pattern piping to a program aside (corePatternPipes).
func segvSpec(dir string, core uint64) coreproc.Spec {
	//: kill -SEGV $$ is a SIGSEGV death whatever sent it: the default action dumps.
	return coreproc.Spec{
		Path:    shellPath,
		Args:    []string{"sh", "-c", "kill -SEGV $$; sleep 5"},
		Dir:     dir,
		Rlimits: map[coreproc.Resource]coreproc.LimitValue{coreproc.ResourceCore: {Soft: core, Hard: core}},
	}
}

// startDirect spawns spec through Start and returns its handle.
func startDirect(t *testing.T, spec coreproc.Spec) *handle {
	t.Helper()
	p, err := Start(t.Context(), spec)
	if err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	return p.(*handle)
}

// TestWait_coreDumpedAndUsageAgreeOnBothPaths spawns a child that dies of
// SIGSEGV, once with RLIMIT_CORE at the hard limit and once at zero, and reads
// its exit both through the handle's own wait and through a reaper sweep that
// collects it first (ADR 0093). The two paths must report the same CoreDumped
// and a Usage: the sweep's StatusValue carries the whole rusage, and
// #1090 found three of its sixteen fields kept. It runs alone — a sweep
// collects any child of the test process, and the parallel tests of this
// package start theirs only once every sequential test has finished.
func TestWait_coreDumpedAndUsageAgreeOnBothPaths(t *testing.T) {
	var hard syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &hard); err != nil {
		t.Fatalf("Getrlimit(RLIMIT_CORE) = %v", err)
	}
	type tc struct {
		name  string
		core  uint64
		swept bool
	}
	tests := []tc{
		{"own wait, core allowed", uint64(hard.Max), false},
		{"sweep, core allowed", uint64(hard.Max), true},
		{"own wait, RLIMIT_CORE zero", 0, false},
		{"sweep, RLIMIT_CORE zero", 0, true},
	}
	dumps := corePatternWritesAFile() && hard.Max != 0
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h := startDirect(t, segvSpec(t.TempDir(), c.core))
		//: the sweep path: a reaper collects the child before its own wait.
		if c.swept {
			sweepUntilCollected(t, h.pid)
		}
		got, err := h.Wait()
		if err != nil {
			t.Fatalf("Wait = %v, want nil", err)
		}
		if !got.Signaled || got.Signal != coreproc.Signal(syscall.SIGSEGV) {
			t.Fatalf("exit = %+v, want a death by SIGSEGV", got)
		}
		if got.Usage.Known != rusageReported() {
			t.Errorf("Usage = %+v, want a value exactly where the kernel reports one", got.Usage)
		}
		//: RLIMIT_CORE at zero stops a dump to a file; a pattern that pipes to a
		//: program is not bound by it (core(5)), where the kernel still dumps.
		if c.core == 0 && !corePatternPipes() && got.CoreDumped {
			t.Errorf("CoreDumped = true with RLIMIT_CORE at zero")
		}
		//: an allowed dump is asserted where its destination is the child's dir.
		if c.core != 0 && dumps && !got.CoreDumped {
			t.Errorf("CoreDumped = false for a SIGSEGV with RLIMIT_CORE at %d", c.core)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// sweepUntilCollected drives childwait.ReapAny, as a running reaper does,
// until it has collected pid.
func sweepUntilCollected(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := childwait.ReapAny()
		if err != nil {
			t.Fatalf("ReapAny = %v while waiting for %d", err, pid)
		}
		if got == pid {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the sweep never collected %d", pid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// heldBytes is what TestWait_minorFaultsOfAChildHolding64MiB's child holds.
const heldBytes int = 64 << 20

// hugePageBytes is the transparent huge page of x86-64 and arm64 Linux: a
// fault on an anonymous region under THP maps 2 MiB at once.
const hugePageBytes int = 2 << 20

// faultUnit is the most memory one minor fault can map on this host: a huge
// page where Linux maps anonymous memory with THP always on, the base page
// everywhere else (16 KiB on darwin arm64, 4 KiB on most others).
func faultUnit() int {
	setting, err := os.ReadFile("/sys/kernel/mm/transparent_hugepage/enabled")
	//: THP set to always maps a fresh anonymous region in huge pages.
	if err == nil && strings.Contains(string(setting), "[always]") {
		//: one fault per 2 MiB at best.
		return hugePageBytes
	}
	//: one fault per base page.
	return os.Getpagesize()
}

// TestWait_minorFaultsOfAChildHolding64MiB reads the page faults of a shell
// that holds 64 MiB in a variable: one fresh page per fault at the most, so
// Usage.MinorFaults reaches 64 MiB over the largest page a fault can map —
// 16 384 with 4 KiB pages, fewer where pages are larger or THP maps 2 MiB at
// once — wherever the kernel reports the counter.
func TestWait_minorFaultsOfAChildHolding64MiB(t *testing.T) {
	t.Parallel()
	//: the counter is not reported where the rusage is left unfilled.
	if !rusageReported() {
		t.Skip("illumos and Solaris leave the rusage counters unfilled")
	}
	pages := uint64(heldBytes / faultUnit())
	script := `x=$(/bin/dd if=/dev/zero bs=1048576 count=64 2>/dev/null | /usr/bin/tr '\000' a); test "${#x}" -eq 67108864`
	h := startDirect(t, coreproc.Spec{Path: shellPath, Args: []string{"sh", "-c", script}})
	got, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait = %v, want nil", err)
	}
	if got.Code != 0 {
		t.Fatalf("the child exited %d: it did not hold the 64 MiB", got.Code)
	}
	if !got.Usage.Known || got.Usage.MinorFaults < pages {
		t.Errorf("Usage = %+v, want MinorFaults >= %d", got.Usage, pages)
	}
}
