//go:build windows

// Package cgroup — Windows control groups via Job Objects. A Win32 Job Object is
// the native, kernel-enforced equivalent of a cgroup v2 control group: it caps a
// set of assigned processes' memory / CPU / process-count and can terminate them
// atomically. We bind the kernel32 entry points directly (syscall.NewLazyDLL, no
// golang.org/x/sys per the dep-light invariant) with the ABI struct layouts
// hand-declared and cited from the Win32 headers (winnt.h / jobapi2.h), the same
// discipline the proc trampoline already uses.
//
// Mapping to the core/proc.Group port:
//   - Create        → CreateJobObjectW
//   - SetMemoryMax  → JOBOBJECT_EXTENDED_LIMIT_INFORMATION.ProcessMemoryLimit
//   - SetCPUMax     → JOBOBJECT_CPU_RATE_CONTROL_INFORMATION (hard cap)
//   - SetPidsMax    → JOBOBJECT_BASIC_LIMIT_INFORMATION.ActiveProcessLimit
//   - Add           → OpenProcess + AssignProcessToJobObject
//   - Kill          → TerminateJobObject
//   - Delete        → CloseHandle
//
// SetIOMax and Freeze/Thaw have no stable, generally-available Job Object
// equivalent, so they return the uniform UnsupportedPlatform sentinel.
package cgroup

import (
	"os"
	"sync"
	"syscall"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// exitOSErr is sysexits.h EX_OSERR (71), restated to carry the cgroup sentinels'
// exit semantics when wrapping a Win32 GetLastError cause.
const exitOSErr int = 71

// kernel32 entry points. Bound lazily (resolved on first Call) — all are stable
// kernel32 exports present since Windows 2000 (CPU rate control since Windows 8).
var (
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")

	procCreateJobObjectW         = modKernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = modKernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = modKernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject       = modKernel32.NewProc("TerminateJobObject")
	procOpenProcess              = modKernel32.NewProc("OpenProcess")
	procGetCurrentProcess        = modKernel32.NewProc("GetCurrentProcess")
	procCloseHandle              = modKernel32.NewProc("CloseHandle")
)

// JobObjectInformationClass values (winnt.h JOBOBJECTINFOCLASS).
const (
	jobObjectExtendedLimitInformation  uintptr = 9
	jobObjectCPURateControlInformation uintptr = 15
)

// LimitFlags bits (winnt.h JOB_OBJECT_LIMIT_*).
const (
	jobLimitActiveProcess uint32 = 0x00000008
	jobLimitProcessMemory uint32 = 0x00000100
)

// CPU-rate ControlFlags bits (winnt.h JOB_OBJECT_CPU_RATE_CONTROL_*).
const (
	cpuRateControlEnable  uint32 = 0x00000001
	cpuRateControlHardCap uint32 = 0x00000002
	// cpuRateScale is the unit of CpuRate: 100% CPU == 10000 (1/100 of 1%).
	cpuRateScale uint32 = 10000
)

// OpenProcess access rights (winnt.h): set a quota on, and terminate, the target.
const (
	processSetQuota  uint32 = 0x0100
	processTerminate uint32 = 0x0001
)

// ioCounters is winnt.h IO_COUNTERS — six uint64 used only as padding inside the
// extended-limit struct; never read here.
type ioCounters struct {
	readOps, writeOps, otherOps    uint64
	readXfer, writeXfer, otherXfer uint64
}

// basicLimitInformation is winnt.h JOBOBJECT_BASIC_LIMIT_INFORMATION.
type basicLimitInformation struct {
	perProcessUserTimeLimit int64
	perJobUserTimeLimit     int64
	limitFlags              uint32
	minimumWorkingSetSize   uintptr
	maximumWorkingSetSize   uintptr
	activeProcessLimit      uint32
	affinity                uintptr
	priorityClass           uint32
	schedulingClass         uint32
}

// extendedLimitInformation is winnt.h JOBOBJECT_EXTENDED_LIMIT_INFORMATION: the
// basic limits plus the memory caps. We re-apply the whole struct on every Set*
// so memory and pids limits accumulate rather than overwrite one another.
type extendedLimitInformation struct {
	basicLimitInformation basicLimitInformation
	ioInfo                ioCounters
	processMemoryLimit    uintptr
	jobMemoryLimit        uintptr
	peakProcessMemoryUsed uintptr
	peakJobMemoryUsed     uintptr
}

// cpuRateControlInformation is winnt.h JOBOBJECT_CPU_RATE_CONTROL_INFORMATION.
// The second field is a union; we use only the CpuRate member (hard-cap mode).
type cpuRateControlInformation struct {
	controlFlags uint32
	cpuRate      uint32
}

// controlGroupWindows is the Windows core/proc.Group: a Job Object handle plus
// the accumulated extended-limit struct it re-applies on each Set*.
type controlGroupWindows struct {
	mu     sync.Mutex
	handle syscall.Handle
	ext    extendedLimitInformation
}

// available reports whether the platform offers control groups. Job Objects ship
// with every supported Windows release, so this is always true.
func available() bool {
	//: Job Objects are a baseline Win32 facility — always present.
	return true
}

// createGroup creates a new (anonymous) Job Object that confines the processes
// later attached via Add. The name/opts are cgroup-v2 path concepts with no Job
// Object analogue, so they are accepted for API parity and ignored.
func createGroup(_ string, _ ...Option) (g coreproc.Group, err error) {
	//: CreateJobObjectW(NULL attrs, NULL name) → a fresh anonymous job handle.
	h, _, errno := procCreateJobObjectW.Call(0, 0)
	//: a zero handle means the kernel refused to create the job.
	if h == 0 {
		//: surface the Win32 cause through the central CGROUP_CREATE_FAILED.
		return nil, cgroupErr(errno, coreproc.CodeCgroupCreateFailed, "CGROUP_CREATE_FAILED",
			"Could not create the control group",
			"service/proc/cgroup.Create: CreateJobObject failed")
	}
	//: hand back the live group; the caller Deletes it to release the handle.
	return &controlGroupWindows{handle: syscall.Handle(h)}, nil
}

// applyExtended re-applies the accumulated extended-limit struct to the job so a
// later Set* never clears an earlier one.
func (g *controlGroupWindows) applyExtended() error {
	//: SetInformationJobObject(JobObjectExtendedLimitInformation, &g.ext).
	r1, _, errno := procSetInformationJobObject.Call(uintptr(g.handle),
		jobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&g.ext)),
		unsafe.Sizeof(g.ext))
	//: a zero return means the limit was rejected or the controller is off.
	if r1 == 0 {
		//: surface the Win32 cause through the central CGROUP_WRITE_FAILED.
		return cgroupErr(errno, coreproc.CodeCgroupWriteFailed, "CGROUP_WRITE_FAILED",
			"Could not write the cgroup controller file",
			"service/proc/cgroup: SetInformationJobObject(extended) failed")
	}
	//: the accumulated limits are now in force for the job.
	return nil
}

// SetMemoryMax caps the per-process committed memory. A negative value clears the
// cap (the cgroup "max" semantics).
func (g *controlGroupWindows) SetMemoryMax(bytes int64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: a negative request means "no limit": drop the flag and the byte value.
	if bytes < 0 {
		//: clear the memory-limit bit; leave any other accumulated limit intact.
		g.ext.basicLimitInformation.limitFlags &^= jobLimitProcessMemory
		g.ext.processMemoryLimit = 0
		//: re-apply so the cleared cap takes effect.
		return g.applyExtended()
	}
	//: arm the per-process memory cap at the requested byte count.
	g.ext.basicLimitInformation.limitFlags |= jobLimitProcessMemory
	g.ext.processMemoryLimit = uintptr(bytes)
	//: push the updated struct to the kernel.
	return g.applyExtended()
}

// SetPidsMax caps the number of active processes in the job. A negative value
// clears the cap.
func (g *controlGroupWindows) SetPidsMax(n int64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: a negative request means "no limit": drop the active-process cap.
	if n < 0 {
		//: clear the active-process bit and count.
		g.ext.basicLimitInformation.limitFlags &^= jobLimitActiveProcess
		g.ext.basicLimitInformation.activeProcessLimit = 0
		//: re-apply the cleared cap.
		return g.applyExtended()
	}
	//: arm the active-process cap at n, clamped to the field's uint32 ceiling so a
	//: value beyond it caps at the maximum the Job Object can express rather than
	//: silently wrapping to a small (wrong) limit.
	g.ext.basicLimitInformation.limitFlags |= jobLimitActiveProcess
	//: clamp n to the uint32 max the ActiveProcessLimit field can hold.
	if n > int64(^uint32(0)) {
		//: cap at the largest representable limit instead of wrapping.
		g.ext.basicLimitInformation.activeProcessLimit = ^uint32(0)
	} else {
		//: n fits the field — use it verbatim.
		g.ext.basicLimitInformation.activeProcessLimit = uint32(n)
	}
	//: push the updated struct.
	return g.applyExtended()
}

// SetCPUMax caps CPU as a hard rate derived from quota/period (e.g. 50000/100000
// == 50%). A negative quota clears the cap. Period must be positive.
func (g *controlGroupWindows) SetCPUMax(quota, period int64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	info := cpuRateControlInformation{}
	//: a negative quota (or non-positive period) means "no limit": disable the
	//: rate control entirely (zero ControlFlags).
	if quota >= 0 && period > 0 {
		//: CpuRate is in 1/100 of 1%: rate = quota/period * 10000, hard-capped.
		info.controlFlags = cpuRateControlEnable | cpuRateControlHardCap
		info.cpuRate = uint32(quota * int64(cpuRateScale) / period)
	}
	//: SetInformationJobObject(JobObjectCpuRateControlInformation, &info).
	r1, _, errno := procSetInformationJobObject.Call(uintptr(g.handle),
		jobObjectCPURateControlInformation, uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info))
	//: a zero return means the kernel rejected the rate control.
	if r1 == 0 {
		//: surface the Win32 cause through the central CGROUP_WRITE_FAILED.
		return cgroupErr(errno, coreproc.CodeCgroupWriteFailed, "CGROUP_WRITE_FAILED",
			"Could not write the cgroup controller file",
			"service/proc/cgroup: SetInformationJobObject(cpu-rate) failed")
	}
	//: the CPU hard cap is in force.
	return nil
}

// SetIOMax has no stable Job Object equivalent (JOBOBJECT_IO_RATE_CONTROL is
// version-gated and storage-volume scoped), so it degrades to UnsupportedPlatform.
func (g *controlGroupWindows) SetIOMax(_ string) error {
	//: no generally-available per-job IO rate cap — honest typed degrade.
	return coreproc.UnsupportedPlatform
}

// Add attaches the process pid to the job so the limits bind it. pid 0 (or self)
// attaches the calling process via the current-process pseudo-handle.
func (g *controlGroupWindows) Add(pid int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: resolve a process handle: the current-process pseudo-handle for self,
	//: else OpenProcess with the rights AssignProcessToJobObject requires.
	h, closeIt, err := openTarget(pid)
	//: a failed open cannot be attached — surface the typed write failure.
	if err != nil {
		//: propagate the already-typed CGROUP_WRITE_FAILED.
		return err
	}
	//: release a real (non-pseudo) handle once the assign returns.
	if closeIt {
		//: close the OpenProcess handle after the assign.
		defer procCloseHandle.Call(h)
	}
	//: AssignProcessToJobObject(job, process) places pid under the job's limits.
	r1, _, errno := procAssignProcessToJobObject.Call(uintptr(g.handle), h)
	//: a zero return means the kernel refused the assignment.
	if r1 == 0 {
		//: surface the Win32 cause through the central CGROUP_WRITE_FAILED.
		return cgroupErr(errno, coreproc.CodeCgroupWriteFailed, "CGROUP_WRITE_FAILED",
			"Could not write the cgroup controller file",
			"service/proc/cgroup.Add: AssignProcessToJobObject failed")
	}
	//: pid is now a member of the job.
	return nil
}

// openTarget returns a process handle for pid (the current-process pseudo-handle
// for self, which must not be closed) plus whether the caller must CloseHandle.
func openTarget(pid int) (h uintptr, mustClose bool, err error) {
	//: pid 0 or the caller's own pid uses the pseudo-handle (never closed).
	if pid == 0 || pid == os.Getpid() {
		//: GetCurrentProcess returns the (HANDLE)-1 pseudo-handle for self.
		cur, _, _ := procGetCurrentProcess.Call()
		//: pseudo-handle, no close required.
		return cur, false, nil
	}
	//: a foreign pid needs an explicit handle with set-quota + terminate rights.
	hp, _, errno := procOpenProcess.Call(uintptr(processSetQuota|processTerminate), 0, uintptr(pid))
	//: a zero handle means the process is gone or access was denied.
	if hp == 0 {
		//: surface the Win32 cause through the central CGROUP_WRITE_FAILED.
		return 0, false, cgroupErr(errno, coreproc.CodeCgroupWriteFailed, "CGROUP_WRITE_FAILED",
			"Could not write the cgroup controller file",
			"service/proc/cgroup.Add: OpenProcess failed")
	}
	//: a real handle the caller must close after assignment.
	return hp, true, nil
}

// Kill atomically terminates every process in the job (the cgroup.kill analogue).
func (g *controlGroupWindows) Kill() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: TerminateJobObject(job, exit=1) kills all assigned processes at once.
	r1, _, errno := procTerminateJobObject.Call(uintptr(g.handle), 1)
	//: a zero return means the kernel could not terminate the job.
	if r1 == 0 {
		//: surface the Win32 cause through the central CGROUP_WRITE_FAILED.
		return cgroupErr(errno, coreproc.CodeCgroupWriteFailed, "CGROUP_WRITE_FAILED",
			"Could not write the cgroup controller file",
			"service/proc/cgroup.Kill: TerminateJobObject failed")
	}
	//: every member has been terminated.
	return nil
}

// Freeze has no stable Job Object equivalent (JobObjectFreezeInformation is an
// undocumented NT class), so it degrades to UnsupportedPlatform.
func (g *controlGroupWindows) Freeze() error {
	//: no documented per-job freeze on Windows — honest typed degrade.
	return coreproc.UnsupportedPlatform
}

// Thaw mirrors Freeze: no stable Job Object equivalent.
func (g *controlGroupWindows) Thaw() error {
	//: no documented per-job thaw on Windows — honest typed degrade.
	return coreproc.UnsupportedPlatform
}

// Delete releases the job handle. With no kill-on-close flag set, any still-live
// members keep running but leave the job (their limits lift) — matching the
// cgroup rmdir contract that callers move processes out first.
func (g *controlGroupWindows) Delete() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: CloseHandle releases the job object.
	r1, _, errno := procCloseHandle.Call(uintptr(g.handle))
	//: a zero return means the handle could not be closed.
	if r1 == 0 {
		//: surface the Win32 cause through the central CGROUP_DELETE_FAILED.
		return cgroupErr(errno, coreproc.CodeCgroupDeleteFailed, "CGROUP_DELETE_FAILED",
			"Could not delete the control group",
			"service/proc/cgroup.Delete: CloseHandle failed")
	}
	//: the job object is released.
	return nil
}

// cgroupErr wraps a Win32 cause in a central cgroup sentinel, restating the
// sentinel's code/reason/public verbatim with a Windows-accurate private detail.
func cgroupErr(cause error, code errs.Code, reason, public, private string) error {
	//: restate the central sentinel's fields; the code is never re-Defined here.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     code,
		Reason:   reason,
		Public:   public,
		Private:  private,
		ExitCode: exitOSErr,
	})
}
