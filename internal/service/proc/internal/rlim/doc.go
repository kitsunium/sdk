// Package rlim builds the kernel's resource-limit struct from a soft/hard pair:
// the one place the proc family turns a coreproc.LimitValue into the
// syscall.Rlimit that setrlimit(2) and prlimit64(2) read. exec's trampoline
// applies a limit in the child before exec, rlimit applies one to a running
// process, and both carried a copy of the constructor.
//
// It exists because the struct is not one type across the Unix kernels:
// FreeBSD and DragonFly declare Rlimit.Cur and Max as int64 (their rlim_t is
// __int64_t), every other Unix as uint64, which is coreproc.LimitValue's own
// width. Make is split by build tag so no caller ever names the field type,
// and LimitInfinity (^uint64(0)) lands as RLIM_INFINITY on both: verbatim where
// the fields are uint64, as int64(-1) where they are int64.
//
// Off Unix there is no rlimit struct, and the package holds nothing.
//
// Package rlim — the uint64 syscall.Rlimit constructor. On every Unix target
// except FreeBSD and DragonFly the kernel types Rlimit.Cur/Max as uint64,
// matching coreproc.LimitValue exactly.
//
// Package rlim — the int64 syscall.Rlimit constructor. FreeBSD and DragonFly
// type Rlimit.Cur/Max as int64 (their rlim_t is __int64_t), unlike every other
// Unix target.
package rlim
