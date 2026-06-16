//go:build windows

// Package exec — spawn-time resource confinement via a Win32 Job Object: the
// Windows realisation of a Spec's rlimits. A child requesting a mappable Resource
// is assigned, right after spawn, to a Job Object carrying the limit, so the
// rlimit "grafts onto" the spawn (the cgroup port is the standalone equivalent).
// kernel32 is bound directly (syscall.NewLazyDLL, ABI cited, no x/sys).
//
// Mappable: ResourceAS/ResourceData → ProcessMemoryLimit; ResourceNProc →
// ActiveProcessLimit. Every other Resource (NoFile/Core/FSize/Stack/MemLock) has
// no Job Object analogue and is rejected with UnknownResource before the spawn,
// honouring the no-silent-field-loss rule.
package exec

import (
	"syscall"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// kernel32 Job Object entry points, bound lazily.
var (
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")

	procCreateJobObjectW         = modKernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = modKernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = modKernel32.NewProc("AssignProcessToJobObject")
	procOpenProcess              = modKernel32.NewProc("OpenProcess")
	procCloseHandle              = modKernel32.NewProc("CloseHandle")
)

// Job Object info class + limit flags (winnt.h) and OpenProcess rights.
const (
	jobObjectExtendedLimitInformation uintptr = 9
	jobLimitActiveProcess             uint32  = 0x00000008
	jobLimitProcessMemory             uint32  = 0x00000100
	processSetQuota                   uintptr = 0x0100
	processTerminate                  uintptr = 0x0001
)

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

// assign attaches pid to the job so the limits bind it. It opens the target with
// the set-quota + terminate rights AssignProcessToJobObject requires.
func (j *jobLimit) assign(pid int) error {
	//: open the freshly spawned child with the rights the assign needs.
	hp, _, errno := procOpenProcess.Call(processSetQuota|processTerminate, 0, uintptr(pid))
	//: a zero handle means the child is already gone or access was denied.
	if hp == 0 {
		//: surface the open failure through the central RLIMIT_FAILED.
		return wrapRlimit(errno, errs.Int("pid", pid))
	}
	//: release the process handle once the assign returns.
	defer procCloseHandle.Call(hp)
	//: AssignProcessToJobObject(job, child) binds the child to the limits.
	r1, _, aerrno := procAssignProcessToJobObject.Call(uintptr(j.handle), hp)
	//: a zero return means the assignment was refused.
	if r1 == 0 {
		//: surface the assign failure through the central RLIMIT_FAILED.
		return wrapRlimit(aerrno, errs.Int("pid", pid))
	}
	//: the child is now confined by the job's limits.
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
