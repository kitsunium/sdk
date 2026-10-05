// Package exec — Unix best-effort post-start attributes: scheduling priority
// (Nice), OOM-killer bias (OOMScoreAdj). Each is applied after fork/exec and
// surfaces a typed error when the host refuses, rather than silently dropping a
// requested field.
//
// Package exec — Linux pre-exec cgroup v2 placement. When a Spec sets
// CgroupPath, the child must join that control group BEFORE it execs the target,
// so the controller limits (memory.max, pids.max, …) bind from the first
// instruction rather than after a post-spawn Group.Add(pid) race. The parent
// validates the path up front (validateCgroupPath); the trampoline then writes
// its own pid into <path>/cgroup.procs (applyCgroupPlacement) between the rlimit
// step and execve. cgroups are Linux-only, so this file is the only place that
// touches them; the !linux sibling rejects a non-empty path as UnsupportedPlatform.
//
// Package exec — cgroup placement on Unix platforms that are NOT Linux (the
// BSDs, Darwin). cgroup v2 is a Linux mechanism with no portable equivalent, so
// a non-empty Spec.CgroupPath is rejected up front with UnsupportedPlatform
// rather than silently ignored — silently dropping the placement would run the
// target unconfined, the exact failure the feature exists to prevent. The
// trampoline never reaches applyCgroupPlacement here because validateCgroupPath
// fails the spawn first; it is defined only to keep the unix build self-contained.
//
// Package exec — Windows cgroup-placement guard. A Spec.CgroupPath names a
// cgroup v2 directory, a Linux-only concept; Windows confines via Job Object
// assignment through the cgroup port (a handle, not a path), so a non-empty path
// at spawn has no meaning here and is rejected with the uniform sentinel.
//
// Package exec — Unix credential resolution: maps Spec.User/Group/Groups (names
// or numeric ids) to a syscall.Credential via os/user, the systemd
// User=/Group=/SupplementaryGroups= analogue.
//
// Package exec — the keystone spawn primitive: turns a coreproc.Spec into a
// running, supervised process via fork/exec with credentials, a private process
// group/session, and best-effort scheduling attributes.
//
// The platform-portable surface is Start; the concrete fork/exec lives in
// exec_unix.go (every Unix GOOS) with a degrading no-op in exec_other.go so the
// package compiles on every target. Errors are the central coreproc sentinels,
// wrapped (never re-Defined) with errs.Wrap.
//
// Package exec — the Unix spawn: validates the Spec, resolves credentials,
// builds the SysProcAttr (Setpgid/Setsid/Credential), forks/execs via
// os.StartProcess, then applies best-effort scheduling attributes. Returns a
// *Handle satisfying the coreproc.Process port.
//
// Package exec — the Windows spawn primitive. Start turns a Spec into a running,
// supervised process via os.StartProcess (CreateProcess under the hood) and
// returns a coreproc.Process handle. Stdio (inherit/null/capture) reuses the
// portable stdioState; Setpgid maps to a new console process group so a later
// CTRL_BREAK can target the child. The Unix-only Spec fields — rlimits, umask,
// nice/oom, credentials, ExtraFiles, a cgroup path — have no Windows equivalent
// in this layer (resource confinement is the Job Object backend's job, applied at
// the cgroup port), so they are rejected up front with the uniform sentinel
// rather than silently dropped.
//
// Package exec — the process-handle value: a Unix supervision handle
// implementing the coreproc.Process port over an *os.Process, with group-aware
// Signal/Stop and a once-only Wait that captures exit status plus wait4 rusage.
// The concrete type is unexported; Start returns it as the coreproc.Process
// interface.
//
// Package exec — the Windows process-handle value implementing the
// coreproc.Process port over an *os.Process. Windows has no Unix process groups
// or wait4 rusage, so SignalGroup degrades to the leader and the ExitValue
// carries the exit code + CPU times the os layer exposes (no terminating signal,
// no MaxRSS). The concrete type is unexported; Start returns it as the interface.
//
// Package exec — the parent<->trampoline handshake pipe. The re-exec trampoline
// (trampoline_unix.go) applies rlimits/umask in a forked child before exec'ing
// the real target; if that application fails, or the execve itself fails, the
// child exits with a distinct status — but Start could not otherwise tell that
// apart from a legitimate target exit of the same code. This pipe closes the gap:
// the child inherits the write end as handshakeFD and reports a one-byte status
// through it, so Start surfaces a typed RlimitFailed / SpawnFailed instead of a
// silent spawn. It is the same self-pipe + close-on-exec trick os/exec uses for
// its own errpipe: a clean execve closes the fd (the parent reads EOF = success),
// a failure writes a status byte first.
//
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
//
// Package exec — Unix umask / rlimit validation for the spawn. The Go runtime
// exposes no SysProcAttr hook to run setrlimit(2)/umask(2) in the child between
// fork and exec, so Start honours both via the re-exec trampoline
// (trampoline_unix.go): a Spec requesting a mapped Rlimit or a Umask is spawned
// through a self-invocation that applies the limits then execs the target. This
// validator therefore only rejects a Resource with no platform RLIMIT_* mapping
// (the SDK's "no silent field loss" rule) — every mappable field is honoured.
//
// Package exec — RLIMIT_AS mapping for the platforms whose stdlib syscall exports
// it (Linux, Darwin, FreeBSD, NetBSD, DragonFly). OpenBSD has no address-space
// rlimit — RLIMIT_AS is absent from its kernel ABI — so it provides the no-op in
// limittable_openbsd.go instead, leaving ResourceAS unmapped (UnknownResource).
//
// Package exec — OpenBSD has no address-space rlimit (RLIMIT_AS is absent from
// its kernel ABI), so it adds no resources beyond the common Unix set. ResourceAS
// therefore stays unmapped on OpenBSD and a Spec requesting it surfaces the typed
// UnknownResource — the honest "this platform cannot set that limit" answer,
// rather than a build failure on the missing constant.
//
// Package exec — Unix Resource→RLIMIT_* table. Only the constants the stdlib
// syscall package exports are mapped; RLIMIT_NPROC and RLIMIT_MEMLOCK live in
// golang.org/x/sys (banned here), so those resources stay unmapped and surface
// UnknownResource rather than a wrong limit. The address-space limit (RLIMIT_AS)
// is present on every supported target EXCEPT OpenBSD, so it is added by a
// build-tagged addPlatformLimits (limittable_as.go / limittable_openbsd.go)
// rather than referenced here — that keeps this file compiling on OpenBSD.
//
// Package exec — resolving a bare executable name through the PATH the child
// will run with.
//
// os.StartProcess does not search PATH: it hands its path to execve (or to
// CreateProcess) as written, so a bare "go" names a file in the current
// directory and fails with ENOENT. The port documents Spec.Path as "absolute
// or PATH-resolvable", which is what os/exec.Command gives a Go programmer, so
// the resolution happens here, once, before either spawn path — the direct
// fork/exec and the limits trampoline, which execs the target itself.
//
// Package exec — the Unix spellings of a PATH search: one candidate per
// directory, executable when any execute bit is set, variable names
// case-sensitive.
//
// Package exec — the Windows spellings of a PATH search: a name is tried with
// each PATHEXT extension unless it already carries one, a regular file is
// runnable by its extension, and variable names are case-insensitive ("Path"
// is the usual spelling there).
//
// Package exec — peak-RSS read on a 32-bit GOARCH, where Rusage.Maxrss is int32
// and is widened to the int64 the port expects (see maxrss_rss64_unix.go for the
// 64-bit counterpart).
//
// Package exec — peak-RSS read on a 64-bit GOARCH, where Rusage.Maxrss is
// already int64 and needs no widening cast (see maxrss_rss32_unix.go for the
// 32-bit counterpart).
//
// Package exec — Unix procfs writer: a tiny os.WriteFile shim used to set
// /proc/<pid>/oom_score_adj, isolated so the platform-specific path handling
// stays out of the attribute logic.
//
// Package exec — per-process stdio wiring. buildStdio turns a Spec's Stdio mode
// into the three *os.File the spawn passes as ProcAttr.Files, plus the
// parent-side lifecycle: the child-side ends to close once the child owns its
// dups (so EOF propagates on exit) and the copier goroutines that drain capture
// pipes into the caller's writers. StdioInherit (default) shares the parent's
// streams; StdioNull discards via the null device; StdioCapture connects pipes.
//
// Package exec — the re-exec trampoline that honours Spec.Rlimits and Spec.Umask
// pre-exec. The Go runtime exposes no SysProcAttr hook to run setrlimit(2)/
// umask(2) in the child between fork and exec, so when a Spec requests either,
// Start does NOT exec the target directly: it execs this very binary
// (os.Executable()) as a tiny trampoline, passing the real target + an encoded
// limits payload in a sentinel env var. The init() below detects that var,
// applies the limits in the fresh process (before main), then execve()s the real
// target — so the target starts already under its rlimits/umask, with no cgo and
// no dependency. The limits persist across the execve.
//
// Footgun: any binary that imports this package and is run with the sentinel env
// var set will re-exec. Start sets it only on the trampoline child and strips it
// before execve, so a normal run never has it; do not set it by hand.
//
// Package exec — central wrap helpers. Every syscall/stdlib cause is wrapped
// back onto a coreproc sentinel by restating that sentinel's exact
// Reason/Public/Private/ExitCode here once, so call sites stay terse and the
// magic exit-code literals live in a single named place.
//
// Package exec — darwin's word on whether this process's unreaped child has
// died: the probe zombieGroupRefused needs because darwin's kill(2) answers a
// group of zombies with EPERM.
//
// Package exec — the zombie probe where the kernel needs none.
package exec
