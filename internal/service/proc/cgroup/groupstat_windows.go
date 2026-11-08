package cgroup

import (
	"encoding/binary"
	"errors"
	"syscall"
	"time"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The JOBOBJECTINFOCLASS values Stat and Members query (winnt.h).
const (
	// jobObjectBasicAccountingInformation is JobObjectBasicAccountingInformation.
	jobObjectBasicAccountingInformation uintptr = 1
	// jobObjectBasicProcessIDList is JobObjectBasicProcessIdList.
	jobObjectBasicProcessIDList uintptr = 3
	// fileTimeUnit is the 100 ns tick a job's times are counted in.
	fileTimeUnit time.Duration = 100
	// errorMoreData is ERROR_MORE_DATA: the pid list outgrew the buffer.
	errorMoreData syscall.Errno = 234
	// pidListHeader is the two DWORDs before the ULONG_PTR pid array.
	pidListHeader int = 8
	// pidListStart is the first buffer's room, in pids; it doubles on demand.
	pidListStart int = 64
	// pidWord32 is a ULONG_PTR's width on a 32-bit Windows.
	pidWord32 int = 4
	// exitIOErr is sysexits.h EX_IOERR (74), the exit status CGROUP_READ_FAILED
	// carries; restated to wrap a cause without re-Defining the central code.
	exitIOErr int = 74
)

// procQueryInformationJobObject reads a job's accounting and process list.
var procQueryInformationJobObject = modKernel32.NewProc("QueryInformationJobObject")

// basicAccountingInformation is winnt.h JOBOBJECT_BASIC_ACCOUNTING_INFORMATION.
type basicAccountingInformation struct {
	totalUserTime             int64
	totalKernelTime           int64
	thisPeriodTotalUserTime   int64
	thisPeriodTotalKernelTime int64
	totalPageFaultCount       uint32
	totalProcesses            uint32
	activeProcesses           uint32
	totalTerminatedProcesses  uint32
}

// Stat reads the job's accounting: user and kernel time, the active process
// count and the peak job memory. A job counts no throttling, no current
// memory, no events and no per-device I/O: the value is Partial.
func (g *controlGroupWindows) Stat() (coreproc.GroupStatValue, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var acct basicAccountingInformation
	//: the accounting the job keeps.
	if err := g.query(jobObjectBasicAccountingInformation, unsafe.Pointer(&acct), unsafe.Sizeof(acct)); err != nil {
		return coreproc.GroupStatValue{}, err
	}
	var ext extendedLimitInformation
	//: the peak, beside the limits.
	if err := g.query(jobObjectExtendedLimitInformation, unsafe.Pointer(&ext), unsafe.Sizeof(ext)); err != nil {
		return coreproc.GroupStatValue{}, err
	}
	user := time.Duration(acct.totalUserTime) * fileTimeUnit
	kernel := time.Duration(acct.totalKernelTime) * fileTimeUnit
	//: what a job counts, partial by nature.
	return coreproc.GroupStatValue{
		CPUUsage: user + kernel, CPUUser: user, CPUSystem: kernel,
		PidsCurrent: int64(acct.activeProcesses), MemoryPeak: int64(ext.peakJobMemoryUsed),
		Partial: true,
	}, nil
}

// Members lists the job's process ids.
func (g *controlGroupWindows) Members() ([]int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	pointer := int(unsafe.Sizeof(uintptr(0)))
	//: grow until the list fits.
	for room := pidListStart; ; room *= 2 {
		buf := make([]byte, pidListHeader+room*pointer)
		err := g.query(jobObjectBasicProcessIDList, unsafe.Pointer(&buf[0]), uintptr(len(buf)))
		//: more processes than room.
		if errors.Is(err, errorMoreData) {
			continue
		}
		if err != nil {
			return nil, err
		}
		listed := int(binary.LittleEndian.Uint32(buf[4:]))
		pids := make([]int, 0, listed)
		//: one ULONG_PTR per process.
		for i := range listed {
			pids = append(pids, pidAt(buf[pidListHeader+i*pointer:], pointer))
		}
		return pids, nil
	}
}

// pidAt reads one ULONG_PTR process id, little-endian at the pointer's width.
func pidAt(b []byte, pointer int) int {
	//: a 32-bit Windows.
	if pointer == pidWord32 {
		return int(binary.LittleEndian.Uint32(b))
	}
	//: a 64-bit one.
	return int(binary.LittleEndian.Uint64(b))
}

// query calls QueryInformationJobObject for one class into out.
func (g *controlGroupWindows) query(class uintptr, out unsafe.Pointer, size uintptr) error {
	r1, _, errno := procQueryInformationJobObject.Call(uintptr(g.handle), class, uintptr(out), size, 0)
	//: a zero return is a refusal, the cause in errno.
	if r1 == 0 {
		return errs.Wrap(errno, errs.WrapParams{
			Code:     coreproc.CodeCgroupReadFailed,
			Reason:   "CGROUP_READ_FAILED",
			Public:   "Could not read the control group's counters",
			Private:  "service/proc/cgroup: QueryInformationJobObject failed",
			ExitCode: exitIOErr,
		})
	}
	//: filled.
	return nil
}
