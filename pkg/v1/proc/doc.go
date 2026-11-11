// Package proc is the capability-preflight facade for the process-supervision
// domain. Some proc primitives have no native, secure mechanic on every OS
// (cgroup is Linux, Windows and FreeBSD only, rlimit/sd_notify are Unix-only,
// the OOM bias Linux-only, …); the SDK
// honours those gaps by returning the typed [UnsupportedPlatform] error and
// NEVER panicking on its own. This package lets a CONSUMER instead choose to
// fail fast at its own startup — the idiomatic Go MustX pattern
// ([regexp.MustCompile] / [text/template.Must]): the library offers the panic,
// it never imposes it.
//
// # Why preflight instead of recover
//
// A process supervisor (a PID1) cannot rely on a recover() in its hot path: an
// unrecovered panic in pid 1 orphans every supervised service, and a panic that
// crosses a CGO/FFI boundary is undefined behaviour. The safe pattern is to
// reject an unsupported configuration up front — assert the required
// capabilities once, at startup, then route only to capabilities that
// [Supported] reports present.
//
//	func main() {
//		// crash-at-launch, clearly, if this host cannot supervise at all:
//		proc.MustSupport(proc.CapProcessSpawn, proc.CapReaper)
//		os.Exit(run())
//	}
//
//	// elsewhere, branch instead of crash for an optional capability:
//	if proc.Supported(proc.CapCgroup) {
//		// confine via cgroup / Job Object
//	}
//
// # Capability × platform matrix
//
// Every row is the engines' own answer: a capability marked ❌ is refused with
// [UnsupportedPlatform] by its engine, and one marked ✅ is not, which a test of
// this package holds on every platform the SDK's runtime lanes run.
//
//	Capability           linux darwin windows freebsd openbsd netbsd dragonfly illumos solaris
//	CapProcessSpawn        ✅     ✅     ✅      ✅      ✅      ✅      ✅        ✅      ✅
//	CapSignalRelay         ✅     ✅     ✅      ✅      ✅      ✅      ✅        ✅      ✅
//	CapReaper              ✅     ✅     ✅¹     ✅      ✅      ✅      ✅        ✅⁴     ✅⁴
//	CapCgroup              ✅     ❌     ✅²     ✅²     ❌      ❌      ❌        ❌      ❌
//	CapRlimit              ✅     ✅     ❌³     ✅      ✅      ✅      ✅        ✅      ✅
//	CapUmaskNice           ✅     ✅     ❌      ✅      ✅      ✅      ✅        ✅      ✅
//	CapOOMScoreAdj         ✅     ❌     ❌      ❌      ❌      ❌      ❌        ❌      ❌
//	CapSdNotify            ✅     ✅     ❌      ✅      ✅      ✅      ✅        ✅      ✅
//	CapSocketActivation    ✅     ✅     ❌      ✅      ✅      ✅      ✅        ✅      ✅
//	CapDieWithParent       ✅     ❌     ✅      ✅      ❌      ❌      ❌        ❌      ❌
//	CapScheduling          ✅     ❌     ❌⁵     ✅⁶     ❌      ❌      ❌        ❌      ❌
//	CapPTY                 ✅     ✅     ❌      ✅      ✅      ❌      ❌        ❌      ❌
//	CapProcessHandle       ✅     ❌     ✅      ❌      ❌      ❌      ❌        ❌      ❌
//	CapSandbox             ✅⁷    ❌     ❌      ❌      ❌      ❌      ❌        ❌      ❌
//
// ¹ Windows has no zombies; the reaper is a correct no-op there. ² Windows
// cgroup is the Job Object backend; FreeBSD's is the rctl(8) backend, which
// needs a kernel carrying RACCT (kern.racct.enable=1): without it
// cgroup.Available reports false and cgroup.Create returns
// [UnsupportedPlatform]. ³ the Unix setrlimit-on-self model has no
// Windows analogue; Windows resource limits are applied through the cgroup
// (Job Object) container or at spawn via the process Spec, not a standalone rlimit.
// ⁴ illumos and Solaris have no reparent-here facility, like darwin, OpenBSD
// and NetBSD: SetChildSubreaper returns [UnsupportedPlatform]. Their reaper
// also sweeps once a second, because the Go runtime forks every child there
// with FORK_NOSIGCHLD and its exit posts no SIGCHLD. Both columns are proven
// on OmniOS r151054 and Oracle Solaris 11.4 (amd64) by the SDK's
// cross-platform runtime lane.
// ⁵ Windows maps Spec.Nice onto a priority class, but has no scheduling
// policy or I/O class. ⁶ FreeBSD applies SchedFIFO, SchedRR and SchedIdle
// through rtprio(2); it has no batch policy and no per-process I/O class.
// ⁷ on amd64 and arm64, the architectures whose system call tables the sandbox's
// seccomp filter is written against; Linux on any other GOARCH refuses it.
//
// [Supported] consults runtime.GOOS only — CapSandbox's row is written for the
// GOARCH the binary is built for — it is the platform-level matrix, not a runtime
// probe. A capability that is native on the GOOS may still be unavailable at
// runtime (e.g. cgroup present but not delegated); use the per-facade probe for
// that (e.g. cgroup.Available()).
package proc
