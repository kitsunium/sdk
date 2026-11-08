// Package cgroup is the backend of the cgroup BPF programs over
// cilium/ebpf, in the third-party/ebpf vendor module (ADR 0157, ADR 0215):
// importing it — a blank import is enough — registers Backend with the
// engine, internal/service/proc/bpf, whose Probe and Attach functions, and
// pkg/v1/proc/bpf's, then load and attach programs instead of answering
// NOT_LINKED. A program that does not import it links nothing of
// cilium/ebpf.
//
// # The programs are built in Go
//
// Each program is a few instructions written with cilium/ebpf's asm package
// from the policy at hand — no C, no clang, no object file committed: a
// device policy is its rules compiled in a straight line, one comparison per
// field that is not a wildcard; a bind policy likewise, one program per
// family; an IP policy is two programs over two longest-prefix-match maps,
// one per family, and an array of counters. A rule that compares nothing
// ends the program, since the verifier refuses an unreachable instruction.
// Every return is a constant 0 or 1.
//
// # Attached beside the others, until Close
//
// A program is attached with BPF_PROG_ATTACH and BPF_F_ALLOW_MULTI, never
// with BPF_F_ALLOW_OVERRIDE: it runs beside the programs already attached to
// the group, and replaces none. The attachment is the kernel's, so it
// outlives the process that made it, until Close detaches it by its own
// descriptor or the group is removed. The group is opened first and refused,
// ATTACH_FAILED, when it is not on cgroup2fs or when it is its hierarchy's
// root — the machine's or a namespace's —, where a policy would bind
// everything.
//
// # Before 5.11
//
// A kernel before 5.11 charges BPF memory to RLIMIT_MEMLOCK. Before the
// first load, and only on such a kernel (uname's release), the limit is
// raised to infinity, once per process; without CAP_SYS_RESOURCE that is
// LOAD_REFUSED. cilium/ebpf's rlimit package is not imported: its init
// lowers the limit to zero and calls bpf() in every program that links it.
//
// # Off Linux amd64 and arm64
//
// The programs are still built, and tested, everywhere; loading them is
// UNSUPPORTED_PLATFORM, core/proc's, never a silent no-op.
package cgroup
