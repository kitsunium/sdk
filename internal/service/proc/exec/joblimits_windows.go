//go:build windows

package exec

import (
	"errors"
	"os"
	"syscall"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// kernel32 Job Object and thread entry points, bound lazily.
var (
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")

	procCreateJobObjectW         = modKernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = modKernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = modKernel32.NewProc("AssignProcessToJobObject")
	procCloseHandle              = modKernel32.NewProc("CloseHandle")
	procCreateToolhelp32Snapshot = modKernel32.NewProc("CreateToolhelp32Snapshot")
	procThread32First            = modKernel32.NewProc("Thread32First")
	procThread32Next             = modKernel32.NewProc("Thread32Next")
	procOpenThread               = modKernel32.NewProc("OpenThread")
	procResumeThread             = modKernel32.NewProc("ResumeThread")
)

// Job Object info class + limit flags (winnt.h), the suspended creation flag
// (winbase.h), the thread snapshot class (tlhelp32.h) and the thread right
// ResumeThread requires (winnt.h).
const (
	jobObjectExtendedLimitInformation uintptr = 9
	jobLimitActiveProcess             uint32  = 0x00000008
	jobLimitProcessMemory             uint32  = 0x00000100
	createSuspended                   uint32  = 0x00000004
	th32csSnapThread                  uintptr = 0x00000004
	threadSuspendResume               uintptr = 0x0002
	resumeThreadFailed                uintptr = 0xFFFFFFFF
)

// threadEntry32 is tlhelp32.h THREADENTRY32.
type threadEntry32 struct {
	size           uint32
	usage          uint32
	threadID       uint32
	ownerProcessID uint32
	basePri        int32
	deltaPri       int32
	flags          uint32
}

// ioCounters is winnt.h IO_COUNTERS — padding inside the extended-limit struct.
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

// extendedLimitInformation is winnt.h JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
type extendedLimitInformation struct {
	basicLimitInformation basicLimitInformation
	ioInfo                ioCounters
	processMemoryLimit    uintptr
	jobMemoryLimit        uintptr
	peakProcessMemoryUsed uintptr
	peakJobMemoryUsed     uintptr
}

// jobLimit owns a Job Object handle that confines the spawned child.
type jobLimit struct {
	handle syscall.Handle
}

// newJobLimit builds a Job Object from spec's mappable rlimits, or returns nil
// when none are requested. An unmappable Resource is rejected with
// UnknownResource before any handle is created.
func newJobLimit(spec coreproc.Spec) (*jobLimit, error) {
	//: no rlimits → no job to create.
	if len(spec.Rlimits) == 0 {
		//: the common, unconfined spawn path.
		return nil, nil
	}
	ext, bErr := buildExtendedLimits(spec.Rlimits)
	//: an unmappable resource is a usage error surfaced before the spawn.
	if bErr != nil {
		//: propagate the typed UNKNOWN_RESOURCE verbatim.
		return nil, bErr
	}
	//: CreateJobObjectW(NULL, NULL) → a fresh anonymous job handle.
	h, _, errno := procCreateJobObjectW.Call(0, 0)
	//: a zero handle means the job could not be created.
	if h == 0 {
		//: surface the Win32 cause through the central RLIMIT_FAILED.
		return nil, wrapRlimit(errno)
	}
	job := &jobLimit{handle: syscall.Handle(h)}
	//: push the assembled limit struct to the kernel.
	r1, _, serrno := procSetInformationJobObject.Call(h, jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&ext)), unsafe.Sizeof(ext))
	//: a zero return means the limit was rejected.
	if r1 == 0 {
		//: release the job and surface the Win32 cause through RLIMIT_FAILED.
		job.close()
		//: propagate the typed RLIMIT_FAILED verbatim.
		return nil, wrapRlimit(serrno)
	}
	//: the live job carrying the requested limits.
	return job, nil
}

// buildExtendedLimits maps the requested rlimits onto the extended-limit struct,
// returning UnknownResource for the first resource with no Job Object analogue.
func buildExtendedLimits(limits map[coreproc.Resource]coreproc.LimitValue) (extendedLimitInformation, error) {
	var ext extendedLimitInformation
	//: map each requested resource onto its Job Object field.
	for res, lv := range limits {
		//: dispatch on the resource; an unmapped one fails the whole spawn.
		switch res {
		//: address-space / data caps map to the per-process memory limit.
		case coreproc.ResourceAS, coreproc.ResourceData:
			//: arm the memory cap at the hard ceiling.
			ext.basicLimitInformation.limitFlags |= jobLimitProcessMemory
			ext.processMemoryLimit = uintptr(lv.Hard)
		//: the process-count cap maps to the active-process limit.
		case coreproc.ResourceNProc:
			//: arm the active-process cap at the hard ceiling.
			ext.basicLimitInformation.limitFlags |= jobLimitActiveProcess
			ext.basicLimitInformation.activeProcessLimit = uint32(lv.Hard)
		//: every other resource has no Windows Job Object equivalent.
		default:
			//: the bare sentinel carries the no-cause UNKNOWN_RESOURCE error.
			return ext, coreproc.UnknownResource
		}
	}
	//: the assembled extended-limit struct for the requested resources.
	return ext, nil
}

// assign attaches p to the job so the limits bind it, through the handle
// CreateProcess returned (PROCESS_ALL_ACCESS, so the set-quota and terminate
// rights AssignProcessToJobObject requires are held, and no pid is reopened).
//
// p must have been created suspended. Assigning a child that already runs
// raced its exit: a `cmd /c exit 0` could be gone before the assignment, and
// AssignProcessToJobObject refuses a process that has exited with
// ERROR_ACCESS_DENIED — 9 of 2 000 spawns on windows-latest (#304).
func (j *jobLimit) assign(p *os.Process) error {
	var r1 uintptr
	var aerrno error
	//: the handle stays valid until the callback returns, even if p exits.
	hErr := p.WithHandle(func(handle uintptr) {
		//: AssignProcessToJobObject(job, child) binds the child to the limits.
		r1, _, aerrno = procAssignProcessToJobObject.Call(uintptr(j.handle), handle)
	})
	//: no handle to assign through: the process was already released.
	if hErr != nil {
		//: surface it through the central RLIMIT_FAILED.
		return wrapRlimit(hErr, errs.Int("pid", p.Pid))
	}
	//: a zero return means the assignment was refused.
	if r1 == 0 {
		//: surface the assign failure through the central RLIMIT_FAILED.
		return wrapRlimit(aerrno, errs.Int("pid", p.Pid))
	}
	//: the child is now confined by the job's limits.
	return nil
}

// resumeSuspended resumes the threads of pid, a process created suspended:
// os.StartProcess closes the primary thread's handle, so the thread is found
// again in a thread snapshot. A process created suspended has run no code and
// started no thread of its own, so the thread found is its primary thread.
func resumeSuspended(pid int) error {
	snap, _, serrno := procCreateToolhelp32Snapshot.Call(th32csSnapThread, 0)
	//: INVALID_HANDLE_VALUE means no snapshot was taken.
	if syscall.Handle(snap) == syscall.InvalidHandle {
		//: surface the snapshot failure through the central RLIMIT_FAILED.
		return wrapRlimit(serrno, errs.Int("pid", pid))
	}
	//: release the snapshot once the walk is over — in a closure, as a deferred
	//: call's arguments are evaluated at once and would close it now.
	defer func() { swallowErr2(procCloseHandle.Call(snap)) }()
	entry := threadEntry32{size: uint32(unsafe.Sizeof(threadEntry32{}))}
	resumed := 0
	ok, _, werrno := procThread32First.Call(snap, uintptr(unsafe.Pointer(&entry)))
	//: walk every thread of the system, resuming the child's.
	for ok != 0 {
		//: only the child's threads are resumed.
		if entry.ownerProcessID == uint32(pid) {
			//: resume this thread of the child.
			if rErr := resumeThread(entry.threadID, pid); rErr != nil {
				//: the child stays suspended; the caller kills it.
				return rErr
			}
			resumed++
		}
		ok, _, werrno = procThread32Next.Call(snap, uintptr(unsafe.Pointer(&entry)))
	}
	//: the walk must end on ERROR_NO_MORE_FILES, never on a fault.
	if !errors.Is(werrno, syscall.ERROR_NO_MORE_FILES) {
		//: surface the walk failure through the central RLIMIT_FAILED.
		return wrapRlimit(werrno, errs.Int("pid", pid))
	}
	//: a child with no thread in the snapshot cannot be resumed.
	if resumed == 0 {
		//: surface the missing thread through the central RLIMIT_FAILED.
		return wrapRlimit(syscall.ERROR_NOT_FOUND, errs.Int("pid", pid))
	}
	//: the child runs, confined.
	return nil
}

// resumeThread opens thread tid with THREAD_SUSPEND_RESUME and resumes it once.
func resumeThread(tid uint32, pid int) error {
	th, _, oerrno := procOpenThread.Call(threadSuspendResume, 0, uintptr(tid))
	//: a zero handle means the thread could not be opened.
	if th == 0 {
		//: surface the open failure through the central RLIMIT_FAILED.
		return wrapRlimit(oerrno, errs.Int("pid", pid))
	}
	//: release the thread handle once it is resumed (in a closure, as above).
	defer func() { swallowErr2(procCloseHandle.Call(th)) }()
	prev, _, rerrno := procResumeThread.Call(th)
	//: (DWORD)-1 means the thread was not resumed.
	if prev == resumeThreadFailed {
		//: surface the resume failure through the central RLIMIT_FAILED.
		return wrapRlimit(rerrno, errs.Int("pid", pid))
	}
	//: the thread's suspend count is one lower.
	return nil
}

// close releases the job handle. Without a kill-on-close flag the assigned child
// keeps running but leaves the job (limits lift) — invoked once the child exits.
func (j *jobLimit) close() {
	//: a nil job (no rlimits) has nothing to release.
	if j == nil {
		//: nothing to do on the unconfined spawn path.
		return
	}
	//: CloseHandle releases the job object.
	swallowErr2(procCloseHandle.Call(uintptr(j.handle)))
}

// swallowErr2 discards the (r1, r2, err) of a best-effort kernel32 Call during
// cleanup, recording the discard so the error audit treats it as deliberate.
func swallowErr2(_, _ uintptr, _ error) {
	//: a cleanup CloseHandle fault is non-actionable — the process exits regardless.
}
