//go:build linux && (amd64 || arm64)

package exec

import (
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Classic BPF, as seccomp runs it (linux/filter.h, linux/seccomp.h): the four
// instructions the filter is made of, the offsets of struct seccomp_data, and
// the return values of the three actions.
const (
	// bpfLdWAbs is BPF_LD|BPF_W|BPF_ABS: A = the 32-bit word at k.
	bpfLdWAbs uint16 = 0x20
	// bpfJeqK is BPF_JMP|BPF_JEQ|BPF_K: jump jt if A == k, else jf.
	bpfJeqK uint16 = 0x15
	// bpfJgeK is BPF_JMP|BPF_JGE|BPF_K: jump jt if A >= k, else jf.
	bpfJgeK uint16 = 0x35
	// bpfRetK is BPF_RET|BPF_K: return k.
	bpfRetK uint16 = 0x06
	// seccompDataNr is the offset of seccomp_data.nr.
	seccompDataNr uint32 = 0
	// seccompDataArch is the offset of seccomp_data.arch.
	seccompDataArch uint32 = 4
	// seccompDataArg0Lo is the offset of the low word of seccomp_data.args[0], little-endian.
	seccompDataArg0Lo uint32 = 16
	// seccompDataArg0Hi is the offset of its high word.
	seccompDataArg0Hi uint32 = 20
	// seccompRetKillProcess is SECCOMP_RET_KILL_PROCESS (Linux 4.14).
	seccompRetKillProcess uint32 = 0x80000000
	// seccompRetErrno is SECCOMP_RET_ERRNO; the errno is its low 16 bits.
	seccompRetErrno uint32 = 0x00050000
	// seccompRetAllow is SECCOMP_RET_ALLOW.
	seccompRetAllow uint32 = 0x7fff0000
	// seccompErrnoMax is the largest errno a filter may return (MAX_ERRNO).
	seccompErrnoMax int = 4095
	// seccompGetActionAvail is SECCOMP_GET_ACTION_AVAIL, the seccomp(2) operation that asks whether an action exists.
	seccompGetActionAvail uintptr = 2
	// reportSkip is how far the write check jumps when the call is no write: past its six instructions.
	reportSkip uint8 = 6
	// reportFdSkip is how far it jumps when the descriptor's low word is not the handshake's: to the reload of nr.
	reportFdSkip uint8 = 3
	// syscallBase is the base the table's numbers are written in.
	syscallBase int = 10
	// syscallBits is their width: a seccomp_data.nr.
	syscallBits int = 32
)

var (
	// seccompFloor are the calls every filter lets through: the stage's own
	// execve, its exit should that fail, and the return from a signal the
	// runtime may take between the filter and the execve.
	seccompFloor = [...]string{"execve", "exit", "exit_group", "rt_sigreturn"}

	// syscallNumbers is this architecture's table by name, parsed from
	// syscallTable the first time a filter needs it.
	syscallNumbers = sync.OnceValue(func() map[string]uint32 { return parseSyscallTable(syscallTable) })

	// foreignSyscalls is the other architecture's names this one lacks.
	foreignSyscalls = sync.OnceValue(func() []string { return strings.Fields(foreignTable) })
)

// parseSyscallTable reads "name number" lines into a map.
func parseSyscallTable(table string) map[string]uint32 {
	out := make(map[string]uint32, strings.Count(table, "\n"))
	//: each line.
	for line := range strings.Lines(table) {
		name, num, ok := strings.Cut(strings.TrimSpace(line), " ")
		n, err := strconv.ParseUint(num, syscallBase, syscallBits)
		//: a pair.
		if ok && err == nil {
			out[name] = uint32(n)
		}
	}
	//: the table.
	return out
}

// checkSeccomp refuses a filter that cannot be written or would be inert:
// InvalidSpec for an unknown action or name, an errno out of range, no call
// named, a Default equal to Action, or a floor call denied; SandboxUnavailable
// when the kernel has no seccomp or no SECCOMP_RET_KILL_PROCESS.
func checkSeccomp(sc *coreproc.SeccompValue, available func() syscall.Errno) error {
	//: no filter asked.
	if sc == nil {
		return nil
	}
	//: an action no kernel has, or one with nothing to distinguish.
	if sc.Default > coreproc.SeccompAllow || sc.Action > coreproc.SeccompAllow || sc.Default == sc.Action || len(sc.Syscalls) == 0 {
		return wrapInvalidSpec(errs.String("sandbox", "seccomp needs two distinct known actions and at least one call"))
	}
	//: SECCOMP_RET_ERRNO carries 1 to 4095.
	if sc.Errno < 0 || sc.Errno > seccompErrnoMax {
		return wrapInvalidSpec(errs.String("sandbox", "seccomp errno outside 0 to 4095"), errs.Int("errno", sc.Errno))
	}
	//: each name.
	for _, name := range sc.Syscalls {
		//: a name, known somewhere, not a floor call denied.
		if err := checkSyscallName(name, sc.Action); err != nil {
			return err
		}
	}
	//: the kernel's seccomp, with the kill action the arch check uses.
	if errno := available(); errno != 0 {
		return wrapSandboxUnavailable(errno, errs.String("feature", "seccomp"))
	}
	//: a filter the kernel will load.
	return nil
}

// checkSyscallName refuses a name no supported architecture knows, and a floor
// call given an action other than SeccompAllow.
func checkSyscallName(name string, action coreproc.SeccompAction) error {
	_, own := syscallNumbers()[name]
	//: a typo, not a call another architecture has.
	if !own && !slices.Contains(foreignSyscalls(), name) {
		return wrapInvalidSpec(errs.String("sandbox", "seccomp names an unknown system call"), errs.String("syscall", name))
	}
	//: the stage's own execve and exit cannot be filtered out.
	if action != coreproc.SeccompAllow && slices.Contains(seccompFloor[:], name) {
		return wrapInvalidSpec(errs.String("sandbox", "seccomp may not deny a call the spawn needs"), errs.String("syscall", name))
	}
	//: usable.
	return nil
}

// seccompAvailable asks the kernel whether seccomp exists and can kill a
// process: seccomp(SECCOMP_GET_ACTION_AVAIL, 0, &SECCOMP_RET_KILL_PROCESS).
func seccompAvailable() syscall.Errno {
	_, _, errno := syscall.Syscall(sysSeccomp, seccompGetActionAvail, 0, uintptr(unsafe.Pointer(new(seccompRetKillProcess))))
	//: ENOSYS without seccomp, EOPNOTSUPP before Linux 4.14.
	return errno
}

// seccompReturn is the filter's return value for action.
func seccompReturn(action coreproc.SeccompAction, errno int) uint32 {
	//: the three actions.
	switch action {
	//: through.
	case coreproc.SeccompAllow:
		return seccompRetAllow
	//: the call fails with errno, EPERM when unset.
	case coreproc.SeccompErrno:
		//: systemd's default SystemCallErrorNumber.
		if errno == 0 {
			errno = int(syscall.EPERM)
		}
		return seccompRetErrno | uint32(errno)
	//: the process dies.
	default:
		return seccompRetKillProcess
	}
}

// assembleSeccomp writes sc as a classic BPF program: the architecture check
// (another ABI is killed, and on amd64 the x32 one), the floor calls let
// through, a write to the handshake descriptor hsfd let through, then one
// comparison per named call taking Action, and Default for the rest. Every
// jump is 0 or a few instructions forward, far below the 255 a jump can reach.
// checkSeccomp has accepted sc.
func assembleSeccomp(sc *coreproc.SeccompValue, hsfd int) []sockFilter {
	numbers := syscallNumbers()
	kill := seccompRetKillProcess
	allow := seccompRetAllow
	prog := []sockFilter{
		{code: bpfLdWAbs, k: seccompDataArch},
		{code: bpfJeqK, jt: 1, k: sandboxAuditArch},
		{code: bpfRetK, k: kill},
		{code: bpfLdWAbs, k: seccompDataNr},
	}
	//: amd64's x32 ABI shares the architecture value: refused by its bit.
	if sandboxX32Bit != 0 {
		prog = append(prog, sockFilter{code: bpfJgeK, jf: 1, k: sandboxX32Bit}, sockFilter{code: bpfRetK, k: kill})
	}
	//: the floor.
	for _, name := range seccompFloor {
		prog = append(prog, sockFilter{code: bpfJeqK, jf: 1, k: numbers[name]}, sockFilter{code: bpfRetK, k: allow})
	}
	prog = append(prog,
		//: write(hsfd, …): the failure report of an execve the filter let through.
		sockFilter{code: bpfJeqK, jf: reportSkip, k: numbers["write"]},
		sockFilter{code: bpfLdWAbs, k: seccompDataArg0Lo},
		sockFilter{code: bpfJeqK, jf: reportFdSkip, k: uint32(hsfd)},
		sockFilter{code: bpfLdWAbs, k: seccompDataArg0Hi},
		sockFilter{code: bpfJeqK, jf: 1, k: 0},
		sockFilter{code: bpfRetK, k: allow},
		sockFilter{code: bpfLdWAbs, k: seccompDataNr},
	)
	listed := seccompReturn(sc.Action, sc.Errno)
	//: each named call this architecture has, once.
	for _, nr := range seccompNumbers(numbers, sc.Syscalls) {
		prog = append(prog, sockFilter{code: bpfJeqK, jf: 1, k: nr}, sockFilter{code: bpfRetK, k: listed})
	}
	//: every other call.
	return append(prog, sockFilter{code: bpfRetK, k: seccompReturn(sc.Default, sc.Errno)})
}

// seccompNumbers returns the numbers of names on this architecture, sorted and
// without repeats; a name it lacks, or a floor call, is left out.
func seccompNumbers(numbers map[string]uint32, names []string) []uint32 {
	out := make([]uint32, 0, len(names))
	//: each name.
	for _, name := range names {
		nr, ok := numbers[name]
		//: another architecture's call, or one the floor already decides.
		if !ok || slices.Contains(seccompFloor[:], name) {
			continue
		}
		out = append(out, nr)
	}
	slices.Sort(out)
	//: one comparison per number.
	return slices.Compact(out)
}
