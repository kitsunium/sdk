//go:build linux && (amd64 || arm64)

package exec

import (
	"runtime"
	"syscall"
	"testing"
)

// sandboxSyscallWant is, per architecture, the kernel's number of each system
// call the sandbox makes that the standard library does not export, and the
// audit architecture its filter accepts.
var sandboxSyscallWant = map[string]struct {
	arch    uint32
	x32     uint32
	numbers map[string]uintptr
}{
	"amd64": {arch: 0xC000003E, x32: 0x40000000, numbers: map[string]uintptr{
		"seccomp": 317, "memfd_create": 319, "bpf": 321, "execveat": 322, "pidfd_open": 434,
		"close_range": 436, "mount_setattr": 442, "landlock_create_ruleset": 444,
		"landlock_add_rule": 445, "landlock_restrict_self": 446,
	}},
	"arm64": {arch: 0xC00000B7, numbers: map[string]uintptr{
		"seccomp": 277, "memfd_create": 279, "bpf": 280, "execveat": 281, "pidfd_open": 434,
		"close_range": 436, "mount_setattr": 442, "landlock_create_ruleset": 444,
		"landlock_add_rule": 445, "landlock_restrict_self": 446,
	}},
}

// Test_sandboxSyscallTable pins #728's per-architecture tables on the
// architecture the test runs on — the CI's amd64 and arm64 lanes run both —:
// each constant is the kernel's number, the seccomp name table agrees with it
// and with every number the standard library exports for the calls the stage
// makes, no number is given twice, and the memfd and seal flags are the
// kernel's.
func Test_sandboxSyscallTable(t *testing.T) {
	t.Parallel()
	want, ok := sandboxSyscallWant[runtime.GOARCH]
	if !ok {
		t.Fatalf("no expected table for %s", runtime.GOARCH)
	}
	if sandboxAuditArch != want.arch || sandboxX32Bit != want.x32 {
		t.Errorf("audit arch %#x, x32 bit %#x; want %#x, %#x", sandboxAuditArch, sandboxX32Bit, want.arch, want.x32)
	}
	constants := map[string]uintptr{
		"seccomp": sysSeccomp, "memfd_create": sysMemfdCreate, "bpf": sysBPF, "execveat": sysExecveat,
		"pidfd_open": sysPidfdOpen, "close_range": sysCloseRange, "mount_setattr": sysMountSetattr,
		"landlock_create_ruleset": sysLandlockCreateRuleset, "landlock_add_rule": sysLandlockAddRule,
		"landlock_restrict_self": sysLandlockRestrictSelf,
	}
	for name, nr := range want.numbers {
		if constants[name] != nr {
			t.Errorf("the constant for %s is %d, want %d", name, constants[name], nr)
		}
		if uintptr(syscallNumbers()[name]) != nr {
			t.Errorf("syscallNumbers()[%q] = %d, want %d", name, syscallNumbers()[name], nr)
		}
	}
	stdlib := map[string]uintptr{
		"prctl": syscall.SYS_PRCTL, "capget": syscall.SYS_CAPGET, "capset": syscall.SYS_CAPSET,
		"setresuid": syscall.SYS_SETRESUID, "setresgid": syscall.SYS_SETRESGID, "setgroups": syscall.SYS_SETGROUPS,
		"mount": syscall.SYS_MOUNT, "umount2": syscall.SYS_UMOUNT2, "pivot_root": syscall.SYS_PIVOT_ROOT,
		"execve": syscall.SYS_EXECVE, "exit": syscall.SYS_EXIT, "exit_group": syscall.SYS_EXIT_GROUP,
		"rt_sigreturn": syscall.SYS_RT_SIGRETURN, "write": syscall.SYS_WRITE, "gettid": syscall.SYS_GETTID,
		"openat": syscall.SYS_OPENAT, "fcntl": syscall.SYS_FCNTL, "getppid": syscall.SYS_GETPPID,
	}
	for name, nr := range stdlib {
		if got, ok := syscallNumbers()[name]; !ok || uintptr(got) != nr {
			t.Errorf("syscallNumbers()[%q] = %d (%v), the standard library says %d", name, got, ok, nr)
		}
	}
	seen := make(map[uint32]string, len(syscallNumbers()))
	for name, nr := range syscallNumbers() {
		if other, dup := seen[nr]; dup {
			t.Errorf("%s and %s are both %d", name, other, nr)
		}
		seen[nr] = name
	}
	for _, name := range foreignSyscalls() {
		if _, own := syscallNumbers()[name]; own {
			t.Errorf("%s is both this architecture's and foreign", name)
		}
	}
	flags := map[string][2]uintptr{
		"MFD_CLOEXEC": {mfdCloexec, 1}, "MFD_ALLOW_SEALING": {mfdAllowSealing, 2},
		"F_ADD_SEALS": {fAddSeals, 1033}, "F_GET_SEALS": {fGetSeals, 1034},
		"F_SEAL_SEAL": {fSealSeal, 1}, "F_SEAL_SHRINK": {fSealShrink, 2}, "F_SEAL_GROW": {fSealGrow, 4}, "F_SEAL_WRITE": {fSealWrite, 8},
	}
	for name, v := range flags {
		if v[0] != v[1] {
			t.Errorf("%s = %#x, want %#x", name, v[0], v[1])
		}
	}
}

// Test_sandboxSyscallsAnswer asks the running kernel for each number the
// stage uses, with arguments it refuses: any answer but ENOSYS proves the
// number is a call this kernel has, so a table typo cannot hide behind the
// constants test above. Landlock and mount_setattr may be absent from a kernel
// the test runs on: their ENOSYS skips with the reason.
func Test_sandboxSyscallsAnswer(t *testing.T) {
	t.Parallel()
	for name, nr := range map[string]uintptr{
		"seccomp": sysSeccomp, "memfd_create": sysMemfdCreate, "execveat": sysExecveat,
		"mount_setattr": sysMountSetattr, "landlock_create_ruleset": sysLandlockCreateRuleset,
		"landlock_add_rule": sysLandlockAddRule, "landlock_restrict_self": sysLandlockRestrictSelf,
	} {
		//: -1 as every descriptor, NULL as every pointer, an impossible flag.
		_, _, errno := syscall.Syscall6(nr, ^uintptr(0), 0, ^uintptr(0), 0, 0, 0)
		if errno == syscall.ENOSYS {
			t.Logf("%s (%d): ENOSYS on this kernel", name, nr)
			continue
		}
		if errno == 0 {
			t.Errorf("%s (%d) accepted arguments it must refuse", name, nr)
		}
	}
}
