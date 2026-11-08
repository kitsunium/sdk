package self

import (
	"syscall"
	"unsafe"
)

// processMemoryCounters is psapi.h PROCESS_MEMORY_COUNTERS.
type processMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

// The kernel32 entry points the kernel's view is read through.
var (
	modKernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procK32GetProcessMemoryInfo  = modKernel32.NewProc("K32GetProcessMemoryInfo")
	procGetProcessHandleCount    = modKernel32.NewProc("GetProcessHandleCount")
	procGetCurrentProcessForSelf = modKernel32.NewProc("GetCurrentProcess")
)

// kernelMemory fills the working set and its peak from
// K32GetProcessMemoryInfo, and the open HANDLES — not files alone — from
// GetProcessHandleCount.
func kernelMemory(stats *StatsValue) {
	self, _, _ := procGetCurrentProcessForSelf.Call()
	counters := processMemoryCounters{}
	counters.cb = uint32(unsafe.Sizeof(counters))
	//: the working set, the kernel's resident set.
	if r1, _, _ := procK32GetProcessMemoryInfo.Call(self, uintptr(unsafe.Pointer(&counters)), uintptr(counters.cb)); r1 != 0 {
		stats.RSSBytes = uint64(counters.workingSetSize)
		stats.PeakRSSBytes = uint64(counters.peakWorkingSetSize)
		stats.KernelMemory = true
	}
	var handles uint32
	//: the handle count.
	if r1, _, _ := procGetProcessHandleCount.Call(self, uintptr(unsafe.Pointer(&handles))); r1 != 0 {
		stats.OpenFiles = int(handles)
	}
}
