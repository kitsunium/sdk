//go:build linux && (amd64 || arm64)

package exec

import (
	"encoding/binary"
	"errors"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// seccompInput is the part of struct seccomp_data the filter reads.
type seccompInput struct {
	arch uint32
	nr   uint32
	arg0 uint64
}

// runBPF interprets prog over in as the kernel's seccomp runs it, for the four
// instructions assembleSeccomp writes, and returns the program's verdict. It
// fails the test on any other instruction, a jump out of the program, or a
// program that ends without returning.
func runBPF(t *testing.T, prog []sockFilter, in seccompInput) uint32 {
	t.Helper()
	var data [24]byte
	binary.LittleEndian.PutUint32(data[seccompDataNr:], in.nr)
	binary.LittleEndian.PutUint32(data[seccompDataArch:], in.arch)
	binary.LittleEndian.PutUint64(data[seccompDataArg0Lo:], in.arg0)
	var a uint32
	for pc := 0; pc < len(prog); pc++ {
		ins := prog[pc]
		switch ins.code {
		case bpfLdWAbs:
			if int(ins.k)+4 > len(data) {
				t.Fatalf("pc %d: load past seccomp_data", pc)
			}
			a = binary.LittleEndian.Uint32(data[ins.k:])
		case bpfJeqK, bpfJgeK:
			taken := a == ins.k
			if ins.code == bpfJgeK {
				taken = a >= ins.k
			}
			if taken {
				pc += int(ins.jt)
			} else {
				pc += int(ins.jf)
			}
		case bpfRetK:
			return ins.k
		default:
			t.Fatalf("pc %d: instruction %#x is not one the assembler writes", pc, ins.code)
		}
	}
	t.Fatal("the program ended without returning")
	return 0
}

// Test_assembleSeccomp runs the assembled filter over the cases #728 names:
// another architecture's ABI and amd64's x32 ABI are killed whatever the
// lists say, the floor passes a filter that denies everything, the spawn's
// failure report passes on the handshake descriptor alone, a named call takes
// Action and every other one Default.
func Test_assembleSeccomp(t *testing.T) {
	t.Parallel()
	const hsfd = 5
	nr := func(name string) uint32 { return syscallNumbers()[name] }
	errno := seccompRetErrno | uint32(syscall.EACCES)
	denyAll := assembleSeccomp(&coreproc.SeccompValue{Default: coreproc.SeccompKill, Syscalls: []string{"write", "getpid"}, Action: coreproc.SeccompErrno, Errno: int(syscall.EACCES)}, hsfd)
	allowList := assembleSeccomp(&coreproc.SeccompValue{Default: coreproc.SeccompErrno, Syscalls: []string{"getpid", "getpid", "read"}, Action: coreproc.SeccompAllow}, hsfd)
	cases := []struct {
		name string
		prog []sockFilter
		in   seccompInput
		want uint32
	}{
		{"another architecture", denyAll, seccompInput{arch: sandboxAuditArch ^ 1, nr: nr("execve")}, seccompRetKillProcess},
		{"the floor's execve", denyAll, seccompInput{arch: sandboxAuditArch, nr: nr("execve")}, seccompRetAllow},
		{"the floor's exit_group", denyAll, seccompInput{arch: sandboxAuditArch, nr: nr("exit_group")}, seccompRetAllow},
		{"the floor's rt_sigreturn", denyAll, seccompInput{arch: sandboxAuditArch, nr: nr("rt_sigreturn")}, seccompRetAllow},
		{"the report on the handshake", denyAll, seccompInput{arch: sandboxAuditArch, nr: nr("write"), arg0: hsfd}, seccompRetAllow},
		{"a write elsewhere, listed", denyAll, seccompInput{arch: sandboxAuditArch, nr: nr("write"), arg0: 1}, errno},
		{"a write past 32 bits", denyAll, seccompInput{arch: sandboxAuditArch, nr: nr("write"), arg0: 1<<32 | hsfd}, errno},
		{"a listed call", denyAll, seccompInput{arch: sandboxAuditArch, nr: nr("getpid")}, errno},
		{"an unlisted call", denyAll, seccompInput{arch: sandboxAuditArch, nr: nr("read")}, seccompRetKillProcess},
		{"allow-list: listed", allowList, seccompInput{arch: sandboxAuditArch, nr: nr("read")}, seccompRetAllow},
		{"allow-list: unlisted, EPERM", allowList, seccompInput{arch: sandboxAuditArch, nr: nr("getppid")}, seccompRetErrno | uint32(syscall.EPERM)},
	}
	if sandboxX32Bit != 0 {
		cases = append(cases, struct {
			name string
			prog []sockFilter
			in   seccompInput
			want uint32
		}{"the x32 ABI", allowList, seccompInput{arch: sandboxAuditArch, nr: sandboxX32Bit | nr("read")}, seccompRetKillProcess})
	}
	for _, c := range cases {
		if got := runBPF(t, c.prog, c.in); got != c.want {
			t.Errorf("%s: the filter returns %#x, want %#x", c.name, got, c.want)
		}
	}
	if len(allowList) > sandboxMaxInsns {
		t.Fatalf("a %d-instruction filter is past BPF_MAXINSNS", len(allowList))
	}
}

// Test_assembleSeccompWholeTable pins that naming every call of the table
// still fits BPF_MAXINSNS and that every jump stays inside the program.
func Test_assembleSeccompWholeTable(t *testing.T) {
	t.Parallel()
	names := make([]string, 0, len(syscallNumbers()))
	for name := range syscallNumbers() {
		names = append(names, name)
	}
	prog := assembleSeccomp(&coreproc.SeccompValue{Default: coreproc.SeccompKill, Syscalls: names, Action: coreproc.SeccompAllow}, 3)
	if len(prog) > sandboxMaxInsns {
		t.Fatalf("the whole table assembles to %d instructions, past %d", len(prog), sandboxMaxInsns)
	}
	for pc, ins := range prog {
		if ins.code == bpfJeqK || ins.code == bpfJgeK {
			if pc+1+int(ins.jt) >= len(prog) || pc+1+int(ins.jf) >= len(prog) {
				t.Fatalf("pc %d jumps out of the program", pc)
			}
		}
	}
}

// Test_checkSeccomp pins the refusals before the fork: InvalidSpec for a filter
// that cannot be written or would be inert, SandboxUnavailable naming seccomp
// for a kernel without it — the typed ENOSYS of #728.
func Test_checkSeccomp(t *testing.T) {
	t.Parallel()
	ok := func() syscall.Errno { return 0 }
	enosys := func() syscall.Errno { return syscall.ENOSYS }
	valid := coreproc.SeccompValue{Default: coreproc.SeccompAllow, Syscalls: []string{"mkdirat"}, Action: coreproc.SeccompErrno}
	with := func(f func(*coreproc.SeccompValue)) *coreproc.SeccompValue {
		v := valid
		f(&v)
		return &v
	}
	invalid := map[string]*coreproc.SeccompValue{
		"Default equals Action": with(func(v *coreproc.SeccompValue) { v.Action = coreproc.SeccompAllow }),
		"no call named":         with(func(v *coreproc.SeccompValue) { v.Syscalls = nil }),
		"an unknown action":     with(func(v *coreproc.SeccompValue) { v.Action = 9 }),
		"an errno past 4095":    with(func(v *coreproc.SeccompValue) { v.Errno = 4096 }),
		"a negative errno":      with(func(v *coreproc.SeccompValue) { v.Errno = -1 }),
		"an unknown name":       with(func(v *coreproc.SeccompValue) { v.Syscalls = []string{"mkdirat", "no_such_call"} }),
		"the floor denied":      with(func(v *coreproc.SeccompValue) { v.Syscalls = []string{"execve"} }),
	}
	for name, sc := range invalid {
		if err := checkSeccomp(sc, ok); !errors.Is(err, coreproc.InvalidSpec) {
			t.Errorf("%s: checkSeccomp = %v, want InvalidSpec", name, err)
		}
	}
	if err := checkSeccomp(&valid, ok); err != nil {
		t.Errorf("a valid filter: checkSeccomp = %v", err)
	}
	if len(foreignSyscalls()) > 0 {
		if err := checkSeccomp(with(func(v *coreproc.SeccompValue) { v.Syscalls = foreignSyscalls()[:1] }), ok); err != nil {
			t.Errorf("another architecture's call %q: checkSeccomp = %v, want nil", foreignSyscalls()[0], err)
		}
	}
	err := checkSeccomp(&valid, enosys)
	if !errors.Is(err, coreproc.SandboxUnavailable) || !errors.Is(err, syscall.ENOSYS) {
		t.Fatalf("no seccomp: checkSeccomp = %v, want SandboxUnavailable wrapping ENOSYS", err)
	}
	if got := fieldOf(err, "feature"); got != "seccomp" {
		t.Errorf("the refusal names %q, want seccomp", got)
	}
}
